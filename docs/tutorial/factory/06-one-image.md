# 6. One image

Everything so far ran on your laptop: ynf as a process, each agent run in a container ynf started.
A job runner, a CI system or a pool of workers wants something else: one image it can run as a
job. That's ynf's **factory image**: ynh's image, with ynf and ynm added. Your harness is built on
it, and each run happens inside the same container as ynf, beside it, as another user. This lesson
builds that image, runs the factory from it as a job, and checks that the joins from lessons 1 to 4
still hold.

The agent run near the end costs about $0.15 to $0.25 on your key.

## Prerequisites

The block from [the track's README](README.md#every-lesson-starts-here), and your model key. A
fresh sandbox, so the lint ticket is open again:

```bash
export YNF_SRC=<your ynf checkout>
set -a; . "$YNF_SRC/sandbox/sandbox.env"; set +a
export REPO=$SANDBOX_OWNER/${SANDBOX_NAME:-ynf-sandbox}
export TUTORIAL=$HOME/ynf-factory-tutorial
export YNF_CONFIG=$TUTORIAL/config.yaml
export YNM_HOME=$TUTORIAL/ynm-home
cd "$TUTORIAL"
export ANTHROPIC_API_KEY=<your key>
make -C "$YNF_SRC/sandbox" reset
```

## The factory image

```bash
make -C "$YNF_SRC" factory-image
```

Expected, after a few minutes the first time:

```text
built ynf-factory:dev:
  ynf <version>
  ynh 0.9.0
  ynm <version>
```

The versions of ynh and ynm come from `images/factory/versions.env` in your ynf checkout: the
pair this ynf was tested with. The ynm release is downloaded with `gh`, so for now this needs read
access to `eyelock/ynm`. Once ynf is released, the same image is published as
`ghcr.io/eyelock/ynf-factory:<version>` and you can pull it instead.

The factory image has no harness of its own. It's a base, as ynh's own image was for lesson 1's
agent image.

## Your harness on it

Build the sandbox's harness onto it in two steps, as `make -C sandbox e2e-factory` does. First the
tools the sensors need, Go and golangci-lint, on the factory image instead of ynh's; then the
harness itself, with `ynh image`, which installs it and makes ynh's agent run the entrypoint:

```bash
ctx=$(mktemp -d) && cp "$YNF_SRC/sandbox/images/agent/Dockerfile" "$ctx/" && mkdir -p "$ctx/ynh"
docker build -q --build-arg YNH_BASE=ynf-factory:dev -t ynf-sandbox-agent:factory "$ctx"
git -C "$TUTORIAL/ynf-sandbox" pull -q
ynh image ynf-sandbox --from "$TUTORIAL/ynf-sandbox" --base ynf-sandbox-agent:factory \
  --entrypoint agent --tag ynf-sandbox-harness:factory
```

`ynf-sandbox-harness:factory` is the **factory flavour** of your harness: the same harness lesson 1
built for one run, now in an image that also carries ynf and ynm.

## Run it as a job

The job's config says one new thing: `executor: inline`. It declares that whoever runs this
container has contained it, so ynf starts each run as a process beside itself instead of in a
container of its own. State and work go on a volume, so one job's results are there for the next:

```bash
cat > inline.yaml <<EOF
version: 1
factory: { repo: $SANDBOX_OWNER/${SANDBOX_FACTORY:-ynf-sandbox-factory} }
executor: inline
store: sqlite:///work/ynf.db
work_dir: /work
memory: { provider: none }
EOF
docker volume create ynf-factory-work
```

Memory is off here. The ynm in the image would write to a personal store inside the container,
which goes when the job ends. A real factory's jobs write to a shared ynm over HTTP, as in
lesson 5.

The sandbox's tracker stand-in is a test program, not part of the image, so build it for Linux and
mount it in, with its one ticket:

```bash
ARCH=$(docker image inspect --format '{{.Architecture}}' ynf-sandbox-harness:factory)
(cd "$YNF_SRC/sandbox/e2e" && GOOS=linux GOARCH=$ARCH CGO_ENABLED=0 go build -o "$TUTORIAL/ynf-sandbox-tracker-linux" ./tracker)
cat > tickets.json <<EOF
{"SBX-1": {"title": "internal/format is not gofmt-clean, from the tracker",
           "body": "Run gofmt over internal/format.", "labels": ["pkg:internal/format"],
           "status": "open", "repo": "github.com/$REPO"}}
EOF
```

A real tracker's MCP server would be installed in the harness image instead.

Now the job itself, as a shell function so each command below reads like the ones before:

```bash
ynfjob() {
  docker run --rm --user root --cap-drop ALL --cap-add SETUID --cap-add SETGID --cap-add CHOWN \
    --security-opt no-new-privileges -e GITHUB_TOKEN=$(gh auth token) -e ANTHROPIC_API_KEY \
    -e YNF_SANDBOX_TRACKER_DATA=/etc/ynf/tickets.json \
    -v "$TUTORIAL/inline.yaml:/etc/ynf/config.yaml:ro" -v "$TUTORIAL/tickets.json:/etc/ynf/tickets.json" \
    -v "$TUTORIAL/ynf-sandbox-tracker-linux:/usr/local/bin/ynf-sandbox-tracker:ro" \
    -v ynf-factory-work:/work \
    --entrypoint ynf ynf-sandbox-harness:factory --config /etc/ynf/config.yaml "$@"
}
```

Every flag is part of the containment. ynf runs as root with every capability dropped except the
three it needs to hand a run's folders to another user and start the run as that user:
`SETUID`, `SETGID` and `CHOWN`. `no-new-privileges` stops anything it starts from gaining more.
The run itself happens as the `ynh` user, which can't read ynf's environment, so it never sees the
GitHub token. The network is the job runner's: on a real runner, a policy that allows GitHub,
the model API and the lane's hosts.

## Check it

```bash
ynfjob doctor
```

Expected:

```text
ok    config                   /etc/ynf/config.yaml
ok    store                    sqlite:///work/ynf.db
ok    factory                  <you>/ynf-sandbox-factory at <sha> (.agents/factory)
ok    repos                    <you>/ynf-sandbox
ok    lanes <you>/ynf-sandbox .agents/factory on main at <sha>: deps, doc-drift, fix-ci, gofmt, lint-paydown, reclaim
ok    forge default            github.com: reached <you>/ynf-sandbox
ok    tracker tracker          tracker.ynf-sandbox.invalid: its server has the tools it is configured to call
ok    git                      git version <version>
--    docker                   not needed: no lane runs in docker here
ok    ynh                      0.9.0
ok    ynm                      @ynm/cli/<version> linux-<arch> node-<version>
```

No Docker inside, and none needed: every lane runs inline. Now the join from lesson 1, from the
inside:

```bash
ynfjob harness
```

Expected, for each ynh lane:

```text
  ok    lint-paydown: originate lane, ynh on inline
        harness ., installed beside ynf, in this image
        installed as local/ynf-sandbox
        budgets: 12 turns, 1500000 tokens, 30m wall
        sensors: docs, lint, test
        focuses: docs, fix-ci, tidy
```

On your laptop, ynf couldn't read a harness that was still in the repository. Here it's installed,
so ynf asks the image's ynh for it, reads its budgets, sensors and focuses, and holds every lane to
them before anything runs.

## Start work

```bash
L=$(gh issue list -R $REPO --search 'Unchecked errors in internal/store in:title' --json number -q '.[0].number')
ynfjob start $REPO#$L --lane lint-paydown
ynfjob start tracker/SBX-1 --repo $REPO --lane gofmt
```

Expected: both end with a draft pull request, as on your laptop. The agent run's log has two lines
lesson 2's didn't:

```text
level=INFO msg="egress is the job runner's" item=item/github.com/<you>/ynf-sandbox/issues/<n> lane=lint-paydown expects=proxy.golang.org,sum.golang.org,storage.googleapis.com
level=INFO msg="run started" item=... lane=lint-paydown runner=ynh executor=inline image="" base=main attempt=1
```

ynf no longer runs an egress proxy: the job runner's network policy is the boundary, so ynf says
which hosts the lane expects, to make a mismatch visible. `image=""` because the run is in this
image, using the harness installed here.

The state is on the volume, so a later job sees what this one did:

```bash
ynfjob items ls
ynfjob stats --lane lint-paydown
ynfjob replay $REPO#$L
```

Expected: both items `proposed`, the lint run in the by-model table, and every decision replaying
the same. CI is checked by whichever job runs next: in a deployment, `ynf handle` on CI's events,
or a scheduled `ynf sweep`.

## The automated version

The sandbox's acceptance test does all of this, from building the factory flavour to checking each
fixture's outcome, with ynf inside the image:

```bash
make -C "$YNF_SRC/sandbox" e2e-factory
```

It resets the sandbox first, and runs the command lanes by default (`FACTORY_LANES=gofmt,deps`),
so it costs nothing unless you add an agent lane.

## Clean up

```bash
docker volume rm ynf-factory-work
```

## What just happened

- **The factory image** put the three tools together: ynh's image, ynf at this checkout's version,
  and ynm at the version they were tested with.
- **ynh** built your harness onto it, installed, so the image carries the harness, the tools its
  sensors need, and the tools that run it.
- **ynf**, as root with three capabilities, read the installed harness, held each lane to it, and
  ran each run beside itself as the `ynh` user: no Docker, no egress proxy, the job runner's
  containment instead.

The joins are the ones from lessons 1 to 4. Only where each tool runs has changed.

This is the end of the track. [Track 1](../ynf/README.md) and this one cover ynf from a single
command lane to a factory in one image. ynf's [how-to guides](../../how-to/README.md) and
[reference](../../reference/README.md) take it from here.
