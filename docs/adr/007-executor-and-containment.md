# ADR-007: Executor and containment

Status: draft (2026-10-03)
Satisfies: FR-7, FR-11, FR-13, FR-14, NFR-4, NFR-5, NFR-6

## Context

ynh is explicit that it "declares, executes and packages. It does not enforce containment", and
that an unattended loop without a container and an egress policy "is an unattended agent with
your credentials on an open network". ynf is the operator that ynh hands containment to.

Ticket and issue text is a prompt-injection vector into the agent. Whatever the agent can reach,
an attacker who can write a ticket can try to reach.

## Decision

**An executor port** starts and supervises one run of the lane's runner (ADR-012), usually
`ynh agent run`:

```go
type Executor interface {
    Start(ctx context.Context, spec RunSpec) (Run, error)
}
type Run interface {
    Events() <-chan TrajectoryEvent      // the --emit-jsonl stream
    Control(msg ControlMsg) error        // ynh's stdin NDJSON channel
    Wait() (RunResult, error)            // the --format json object
}
```

| Executor | Runs the runner as | Host (ADR-009) |
|---|---|---|
| `process` | a child process on the host | a developer's machine, for attended work and shadow mode only |
| `docker` | a container beside ynf, from the lane's image | a developer's machine, a worker in a pool |
| `inline` | a child process inside the container ynf itself runs in, which is the containment | a job runner, a CI job |
| `ecs`, `k8s-job` | a remote task; ynf tails its event stream and relays control messages | later, a hosted service |

**Where the image comes from.** A lane names a published harness image (`run.image`, pinned by
digest), and ynf pulls it. Only when a lane names a harness folder in the repository and nothing
published does ynf build one, with `ynh image <harness> --entrypoint agent`, keyed on the harness
folder's contents and the base image's id, so it is built again only when one of those changes.
Building is a convenience for development and the sandbox; an instance can turn it off
(`images: { build: false }`), so a deployed factory only runs pinned, published images. The same
harness is published in two flavours that differ only in their base (ADR-009): on ynh's image for
developers, and on ynf's factory image for a job runner. ynf reads what the image's harness
declares from the image itself (ADR-012).

Every run gets its own checkout, step directory and **tool caches**. The checkout is a local
clone of ynf's mirror, not a `git worktree`: its `.git` is a real folder inside it, so the one
mount gives the run a working repository (`git diff`, `origin/main`) and nothing of ynf's, none of
the mirror's other branches. A cache shared between
checkouts is a correctness problem, not only a containment one. golangci-lint's analysis cache
attributes a finding to whichever copy of identical code it saw first, so a sensor scoped with
`--new-from-merge-base` in a second checkout silently drops it. Go's test cache replays a flaky
test's one pass forever, so the run can never look stuck. Contained executors start each run with
empty caches; the `process` executor points `XDG_CACHE_HOME` and the common tool cache variables
into the step directory. Separately, a sensor that judges a run should not read results from a
cache at all (`go test -count=1`). Checkout paths are resolved to their real path before use: on
macOS the temp directory is under `/var`, a symlink to `/private/var`, and when git and a linter
disagree about a file's path, `--new-from-merge-base` matches nothing and the sensor passes.

With the ynh runner, ynf passes `--emit-jsonl <step dir>/trajectory.jsonl`, so ynh writes a
checkpoint (ynh only checkpoints when given a real emit path).

**The agent never holds forge or tracker write credentials.** The worker receives only what the
harness's `env_passthrough` declares: model credentials and, where needed, read-only tokens. When
the run ends, ynf itself reads the checkout (`base_commit` and `changed_files` from the result),
commits with ynf's trailers (ADR-010), pushes, opens or updates the pull request, comments and
labels, using the forge and tracker credentials only ynf holds (ADR-003). A prompt-injected agent
can at worst produce a bad diff, which a human reviews.

With `docker`, the worker is a separate container and never sees ynf's environment. With
`inline`, ynf and the worker share one container, so the worker runs as a different user that
cannot read ynf's environment, files or process, and receives only its passthrough variables.

**Approval prompts are switched off only inside containment, or by the person at the terminal.**
A worker with no one to approve its edits changes nothing. ynh passes no permission flag unless
asked; a lane asks with `run.ynh.auto_approve: edits | all`, which ynf passes to
`ynh agent run --auto-approve` only for a contained run, and only after asking the agent image's
own ynh, which runs the agent, for its capabilities (0.9.0 or later). An older image is refused
before anything runs. Outside containment a lane's setting is ignored, with a warning: a
repository's policy must never switch off the prompts on someone's own machine. There, only the
person starting the work can, explicitly, with `ynf start … --auto-approve edits`. `edits`
approves file edits and still refuses commands, with ynh's sensors checking the work between
turns; lanes use the narrowest level that works. The vendor-specific part, which mode each vendor
CLI needs and when the vendor refuses it, is ynh's.

**A diff gate before every push.** ynf refuses to push, and escalates, when the diff:

- touches paths outside the lane's `allowed_paths` (all paths by default)
- touches sensor configuration, baselines or calibration references
- touches CI workflow files, CODEOWNERS, or ynf's resolved repository folder
  (`.agents/factory/` or a fallback, ADR-009), which holds the lane policy itself

This works with any runner, and with ynh it catches what the `tamper` outcome cannot: changes
outside the files ynh's baseline knows about.

**Containment is mandatory for unattended work.** Work whose trigger is not a person at a terminal
must use `docker`, `inline`, `ecs` or `k8s-job`. `process` is accepted only for attended work:
`ynf start` run from a terminal, `--interactive`, and `ynf shadow`. `inline` is accepted only
where the instance is configured as running inside containment the operator owns (a job runner's
container with its network restricted); ynf cannot verify that from inside, so it is an explicit
operator setting, never a default. ynh's `--sandbox srt` stacks on top where the backend supports
it. A lane that asks for containment the host cannot provide fails to load; it never degrades to
uncontained.

**Egress is the lane's alone.** A harness declares nothing about the network: it does what it
asks for, and is written as if it could reach anything. The lane decides what a run may reach in
this setting:

```yaml
run:
  egress:
    allow:
      - proxy.golang.org
      - sum.golang.org
      - registry.npmjs.org
      - "*.githubusercontent.com"
```

A contained executor denies everything else. The model provider's endpoint is allowed implicitly,
derived from the run's backend rather than declared by the harness (default, 2026-10-03). Under
`inline`, egress is enforced by the job runner's network policy, which the operator sets to the
lane's list; ynf records the list it expected.

Running a harness in a lane is where a mismatch shows up, so it has to show up legibly rather than
as a timeout three tools deep. Egress goes through an allow-list proxy in the executor that logs
every denial. With docker, a lane that allows no hosts runs with `--network none`; one that
allows hosts runs on a per-run `--internal` network, which has no route out, beside a proxy
container (ynf itself, `ynf egress-proxy`) that is the only thing attached to both that network
and the outside. The proxy is reached through the standard `HTTP(S)_PROXY` variables; a tool that
ignores them simply cannot connect. Every job container also drops all capabilities, cannot gain
privileges, and is named, so a run whose lease is lost is removed rather than left running. A run with denials records them in its step log, and each denied host becomes a
failure signature, `sig/egress/denied/<host>` (ADR-008). A lane that keeps hitting the same
denial says exactly which line to add, or which harness behaviour to question.

**A run's telemetry goes to a spool folder, not over the network.** When ynf has a spool root
(ADR-009), each run gets `runs/<run id>/` under it and starts with `YNR_SPOOL` set to that folder
and with `TRACEPARENT`, so its telemetry is written to disk beside the run and read by `ynr serve`
on the host (ynr ADR-004). Under `docker`, the run's container has that folder mounted, at the fixed
path `/run/ynr/spool`, and nothing else of the spool: not the root, not `manifests/`, not
`factory/`, not another run's folder. The run needs no network path to a collector, so egress is
what it was. Under `inline`, the run user is handed its own folder for the run and takes nothing
else of the spool; the root must let it reach its folder (mode `0711`, so it cannot list its
neighbours). The manifest naming the run's lane, harness, focus, item and step is written to
`manifests/<run id>.json`, which the run cannot reach, before the run starts: ynr stamps those
names from it, so a run cannot claim another factory. When the run writes as a user other than the
folder's owner, the manifest also names that user as `uid`, and ynr reads the files that user owns
in that run's folder. ynf finds the uid from the executor, never from the run: under `docker` with
an image that keeps its own user (a harness image built by ynh, or a command lane with
`run.command.image_user`) from the image's configured user, resolved through the image's
`/etc/passwd` when it is a name, which ynf copies out of a container it creates and never starts;
under `inline` from the run user. A run that writes as the folder's owner has no `uid`. When the collector is on, a run starts
without the operator's `OTEL_EXPORTER_OTLP_*`, whichever executor it uses.

**A run that fills its folder must not stall ynf.** Telemetry is never allowed to fail a step, so
what a run writes into its folder is bounded, and a spool that cannot be written costs the run its
telemetry and nothing else: the step goes on, and ynf says so in its log. The bound is
`telemetry.run_quota` (64 MiB by default), and ynf gives it as strongly as the host allows.

Where the host lets ynf make one, each run's folder is a size-limited volume of its own, sized by
the quota: a tmpfs on Linux when ynf has `CAP_SYS_ADMIN`, a sparse disk image on macOS, which needs
no privilege. An executor that cannot use one says so, and its runs keep a plain folder. A write past the limit fails inside the run, with no space left, and ynf has nothing
to remove. ynr reads the folder as it would any other: it checks each file against its own
folder's device, so a volume of its own is accepted, and it still refuses a file with more than one
link. Under `docker` only that volume is mounted into the container. The volume goes at the run's
end, whether the step succeeded or failed, was interrupted or panicked, and only after its files
are shipped or captured: with `ynr serve` running, ynf waits up to ten seconds for it to ship and
delete the run's closed files; whatever is left, which includes a file still open, is copied into
the run's capture (ADR-010); then the volume is unmounted and its folder removed. The job's end
unmounts any volume whose run never reached its end. A ynf killed outright cannot do that, so the
next factory job (`sweep`, `serve`, `handle` or `shadow run`) does it for the dead one at its
start, before it makes volumes of its own: it copies what is left in each such volume into the run
capture, within the capture limits, unmounts it, and removes the folder. It logs what it cleaned
once per job, and a volume it cannot clean is logged and left for the next job; none of it fails
the job.

Telling a dead ynf's volume from a live run's is the part that must not go wrong, since a live
run's volume taken away costs the run its files. When ynf makes a run's volume it writes a lease,
`manifests/<run id>.owner`, beside the manifest in the folder a run cannot reach: the host's name,
ynf's PID and that process's start time. A volume is stale only when its lease is for this host
and no process has that PID with that start time. The PID alone would not do, since the system
reuses PIDs and a dead ynf's may belong to another process by the next job; with the start time, a
reused PID reads as dead. Another ynf running on the same host and sharing the spool root has a
lease that reads as alive, so its volumes are left, as are this process's own, and a host that
cannot say whether a process is running counts it as running. A lease from another host is left,
since a mount is the host's. A run folder with no lease, such as one made by a ynf that wrote none,
is left too: ynf cleans up only what it can show it made.

Only ynf's own volumes are found. On Linux that is a tmpfs of source `ynf-run` mounted at the
run's folder under `runs/`, as the kernel's mount table lists it. On macOS it is the disk image
ynf made for the run, found by the image path the lease records, in a folder of ynf's own naming;
the other disk images attached to the machine are never matched, and never detached.

Where a volume cannot be made, ynf holds the folder to the quota by measuring it every quarter of a
second while the run lasts and removing the largest files, never following a link, until it is back
under. That bounds a flood to what a run writes in a quarter of a second above the quota. It is not
a hard limit, and ynf says so in its log, once per job, naming why there is no volume. Which a run
gets:

| Executor and host | Per-run folder | A full spool |
|---|---|---|
| `docker` on Docker Desktop for Mac | its own folder, plain: the quota watcher is the bound. A disk image attached at the run's folder is read by `ynr serve` on the host, but Docker Desktop's daemon cannot bind it into a container (`error while creating mount source path ... file exists`), so ynf does not make one | there is no tmpfs on the host, so a RAM disk or a volume of its own for the spool root does it |
| `docker` on Linux, ynf with `CAP_SYS_ADMIN` (root, or a job with that capability) | a tmpfs of the quota's size at the run's folder: a hard quota | a run cannot outgrow its own volume |
| `docker` on Linux, ynf unprivileged | its own folder is the only part of the spool the run can write; the quota watcher is the bound | bounded by the filesystem the spool is on: put the root on a tmpfs or a volume of its own and a run that outruns the watcher fills that, never ynf's state |
| `inline` in a job container | the run user's folder only. The container has no `CAP_SYS_ADMIN`, so no volume: the quota watcher is the bound. A job that does grant it gets a tmpfs | the job's spool volume, such as an `emptyDir` with a size limit, or a tmpfs |
| `process`, on a laptop | the run is not contained and can write wherever the user can. On macOS, and on Linux with `CAP_SYS_ADMIN`, its spool folder is a volume as above, a hard quota; elsewhere the quota watcher is the bound | the laptop's own disk, unless the root is on a filesystem of its own |
| a hosted CI runner | as `docker` or `inline`, whichever the job uses; an unprivileged runner gets the quota watcher | `/dev/shm` or another tmpfs, tolerated filling: the job loses telemetry, not work |

What each gives besides the quota is that ynf's state, its store and its work folder are never on
the spool's filesystem, when the operator follows the advice, and that a full spool never fails a
step: ynf's own writer drops what it cannot write and counts it, the manifest and the folder are
made or skipped with a logged warning, and the run starts either way. `ynr serve` stops reading a
run folder that exceeds its own budget and records a provenance warning, which is the reader's
backstop (ynr ADR-003).

A run whose container keeps an image's own user (a harness image built by ynh) writes files that
user owns. With its uid in the manifest, `ynr serve` reads them; a uid that could not be found out
(an image that cannot be inspected, a user missing from its `/etc/passwd`) is logged and left out,
and ynr then refuses what that user writes, which stays in the folder and is swept into the run
capture instead (ADR-010).

**No mode opens egress**, shadow mode included. Shadow runs the real agent on real ticket text,
the input most likely to carry an injected instruction, so it gets the lane's policy like any
other run. Its denials are logged the same way, which is how a lane's `allow` list is discovered
safely. Yield measured under open egress would also be the yield of a different system.

**Adopted items get extra guards:** commits are added, never force-pushed; the step aborts and
re-queues if the head moved since it was probed; drafts and fork pull requests are skipped by
default; the pull request author stays the owner of record.

## Alternatives

- **Let the agent push, open pull requests and comment through an MCP server.** Simpler, and the
  common pattern, but it hands write credentials to the component that reads untrusted text.
- **Shadow mode with open but logged egress, to propose an `allow` list.** Rejected: it opens an
  exfiltration path exactly where the input is least vetted.
- **`unsafe_uncontained: true` as a lane escape hatch.** Rejected: ynh's own rule is that a containment control that cannot be applied is an error, not a warning.

## Consequences

- A developer can run everything without Docker for attended work; unattended lanes on a laptop
  need Docker (or a compatible runtime).
- A deployed factory with `images.build: false` runs only what has been published and pinned.
- Harnesses that today rely on the agent running `gh pr create` need a focus that stops at the
  diff when run under ynf.

## Open questions

- What the job runner can do per job: run more than one container (so `docker` works inside a
  job and `inline` is unnecessary), and restrict a job's egress to a list.
- Whether separate users inside one container are enough to keep ynf's credentials from the
  worker under `inline`, or whether a job should only ever run the worker, with ynf elsewhere.

## History

- 2026-10-03: drafted.
