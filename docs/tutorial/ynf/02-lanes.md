# 2. Lanes

A lane is a rule for one kind of work: where its tickets come from, whether to take one on, how to
do the work, and what to do with the result. This lesson reads the sandbox's lanes, then shows how
the configuration repository's lanes and the repository's own fit together.

You need the terminal from lesson 1, with `YNF_CONFIG` set.

## Read a lane

The sandbox's lanes are a file in the repository itself, `.agents/factory/lanes.yaml`. Fetch it:

```bash
gh api repos/<you>/ynf-sandbox/contents/.agents/factory/lanes.yaml \
  -H 'Accept: application/vnd.github.raw' > ~/ynf-tutorial/lanes.yaml
```

Open it and find the `gofmt` lane:

```yaml
  gofmt:
    kind: originate
    intake:
      - github.search: 'repo:<you>/ynf-sandbox is:issue is:open label:"ynf:fmt"'
        every: 5m
    run:
      runner: command
      image: golang:1.26-alpine
      command:
        argv: ['gofmt', '-w', './{label.pkg}']
      egress:
        allow: []
    when:
      converged: open_pr
      ci_failed: escalate
    pr:
      allowed_paths: ['**/*.go']
    labels:
      on_claim:    { add: [ynf:working], remove: [ynf:fmt] }
      on_propose:  { add: [ynf:proposed], remove: [ynf:working] }
      on_review:   { add: [ynf:in-review], remove: [ynf:proposed] }
      on_escalate: { add: [ynf:needs-human], remove: [ynf:working, ynf:proposed] }
```

Read it top to bottom:

- **`kind: originate`:** the lane starts new work from a ticket. The other kind, `adopt`, takes
  over a pull request someone else opened, like `fix-ci` does.
- **`intake`:** where tickets come from. This is a GitHub search, run every five minutes when ynf
  is running unattended: open issues labelled `ynf:fmt`.
- **`run`:** how the work is done. A `command` runner runs `gofmt -w` in a `golang:1.26-alpine`
  container. `{label.pkg}` comes from the ticket's `pkg:` label, so a ticket labelled
  `pkg:internal/format` formats `./internal/format`. `egress.allow: []` means the container
  reaches nothing on the network.
- **`when`:** what to do with each outcome. A run that converges, meaning it finished and changed
  something, becomes a pull request. If the pull request's CI then fails, ynf escalates to a
  person instead of trying again.
- **`pr.allowed_paths`:** the only files a change may touch. A run that changes anything else is
  refused before it's committed.
- **`labels`:** what ynf writes on the ticket as it works, so people can see where it is. The
  trigger label goes as soon as the ticket is taken on, so no search finds it twice.

The top of the file has `defaults` that every lane gets unless it says otherwise: the `docker`
executor, three attempts, thirty days' retention, and three hosts Go needs to download modules.

## Guards

`gofmt` takes every ticket its search finds. A lane can be choosier with a guard, a condition in
CEL over facts ynf has just read. `fix-ci` has one:

```yaml
    guards:
      eligible: 'facts.pr.checks.exists(c, c.required && c.conclusion == "failure") && !facts.pr.draft && !facts.pr.fork'
```

It only adopts a pull request whose required checks have failed, which isn't a draft, and which
doesn't come from a fork. Facts are read fresh before every decision, never taken from a webhook or
a cache, so a guard always judges the ticket as it is now.

## Two layers

Now look at the bottom of the file:

```yaml
  deps:
    # The deps lane comes from the configuration repository, <you>/ynf-sandbox-factory, where
    # it is on. This repository turns it off with one key: its lanes win, key by key (ADR-006).
    enabled: false
```

That's not a whole lane. The `deps` lane is defined in the configuration repository, as a default
for every repository the factory enrols:

```bash
gh api repos/<you>/ynf-sandbox-factory/contents/.agents/factory/lanes.yaml \
  -H 'Accept: application/vnd.github.raw'
```

Expected, after its comments:

```yaml
version: 1
lanes:
  deps:
    # Dependency bumps fail ynh's factory-pattern test on blast radius.
    kind: originate
    intake:
      - github.search: 'repo:<you>/ynf-sandbox is:issue is:open label:"ynf:deps"'
        every: 1h
    run:
      runner: ynh
      ynh: { harness: ., focus: tidy }
    when:
      converged: open_pr
```

ynf lays the repository's lanes over the configuration repository's, key by key, and the
repository wins. So the sandbox gets the whole `deps` lane from the factory, and switches it off
with one key of its own.

## See where each value came from

`lanes show` gives the merged lane, and the source of every value in it:

```bash
ynf lanes show --repo <you>/ynf-sandbox deps
```

Expected, among the output (the lanes are printed as YAML; `--format json` prints the same as JSON):

```yaml
sources:
  deps:
    enabled: repo@43e2224
    intake: config@998570c
    kind: config@998570c
    run.runner: config@998570c
    run.ynh.focus: config@998570c
    run.ynh.harness: config@998570c
    when.converged: config@998570c
```

`config@998570c` means the configuration repository set it, at that commit; `repo@43e2224` means
the repository did. The same command for `gofmt` shows every value from `repo@…`, since the
factory doesn't define that lane at all.

### What just happened

ynf read both repositories at their current commits, merged them, and kept track of which layer
set each value. Every decision it makes records both commits, so you can always say which
configuration was in force when it decided something. A factory's lanes are changed the way code
is: by a commit, reviewed in a pull request.

## Validate a lanes file

Lanes are checked against a schema whenever ynf reads them. You can check a file yourself first:

```bash
ynf lanes validate --file ~/ynf-tutorial/lanes.yaml
```

Expected, with exit code 30:

```text
ynf: lanes.yaml does not match the schema:
jsonschema validation failed with 'https://eyelock.github.io/ynf/schema/lanes.schema.json#'
- at '/lanes/deps': missing properties 'kind', 'intake', 'run', 'when'
```

That's the layering again. On its own, the sandbox's file isn't a complete set of lanes: its `deps`
entry only switches off a lane the configuration repository defines. ynf validates the merged
result when it reads both, which is what `doctor` and `lanes show` just did. Give `validate` the
repository and it does the same, so you can check a change to a repository's lanes before you
push it:

```bash
ynf lanes validate --repo <you>/ynf-sandbox --file ~/ynf-tutorial/lanes.yaml
```

Expected:

```text
/Users/you/ynf-tutorial/lanes.yaml: valid, 1 lanes (deps)
merged over config@998570c of <you>/ynf-sandbox-factory
```

The file alone failed; merged over the configuration repository's lanes it is valid. Errors in
the merged result are reported the same way as above, naming the key that is wrong.

Lesson 4 replays a ticket under a changed lane, so write the `gofmt` lane into a file of its own
now. It is a whole lane, so it validates on its own too:

```bash
cat > ~/ynf-tutorial/gofmt.yaml <<'EOF'
version: 1
lanes:
  gofmt:
    kind: originate
    intake:
      - github.search: 'repo:<you>/ynf-sandbox is:issue is:open label:"ynf:fmt"'
        every: 5m
    run:
      runner: command
      image: golang:1.26-alpine
      command:
        argv: ['gofmt', '-w', './{label.pkg}']
    when:
      converged: open_pr
      ci_failed: escalate
    pr:
      allowed_paths: ['**/*.go']
EOF
ynf lanes validate --file ~/ynf-tutorial/gofmt.yaml
```

Expected:

```text
/Users/you/ynf-tutorial/gofmt.yaml: valid, 1 lanes (gofmt)
```

## What you know now

- A lane says where tickets come from (**intake**), whether to take one (**guards**), how to do it
  (**run**), what to do with each outcome (**when**), what a change may touch (**pr**) and what to
  tell people (**labels**).
- The **configuration repository's** lanes lie under each repository's own, and the repository
  wins, key by key.
- `ynf lanes show` says which layer set each value, at which commit.

Next: [3. A ticket becomes a pull request](03-a-ticket-becomes-a-pull-request.md).
