# 5. Running unattended

So far you've started work by hand, and `start` stopped as soon as the work was waiting on CI.
Something has to come back to it, and find new tickets without being told. This lesson runs ynf
three ways that do: a sweep, a long-running worker that also takes webhooks, and a CI job.

You need the terminal from lesson 1, with `YNF_CONFIG` set.

Always pass `--lane gofmt` to `sweep` and `serve` in this track. The paused lanes still notice their
tickets, and an item waiting in a paused lane never settles.

## One sweep

```bash
ynf sweep --lane gofmt
```

A sweep does two things. It runs each lane's search and starts tracking any ticket it hasn't seen,
then steps every item whose timer is due. The log shows both:

```text
level=INFO msg=tracking item=item/github.com/<you>/ynf-sandbox/issues/3 lane=gofmt
level=INFO msg=decided item=item/github.com/<you>/ynf-sandbox/issues/3 event=ynf.ticket.matched state=ready reason="eligible for lane gofmt"
...
level=INFO msg=decided item=item/github.com/<you>/ynf-sandbox/issues/3 event=ynf.action.done state=proposed reason="proposed as #14"
level=INFO msg=decided item=item/github.com/<you>/ynf-sandbox/issues/5 event=ynf.timer.due state=in_review reason="#13: CI green"
```

Then it prints every item:

```text
ITEM                              LANE   STATE      PR   REASON
github.com/<you>/ynf-sandbox#3    gofmt  proposed   #14  proposed as #14
github.com/<you>/ynf-sandbox#5    gofmt  in_review  #13  #13: CI green
```

### What just happened

The search found the issue labelled `ynf:fmt`, which ynf hadn't seen, and worked it exactly as
`start` did in lesson 3: a run, a pull request, `proposed`. The item from lesson 3 had a timer: a
proposed item asks to be checked again after `poll.ci`, fifteen seconds in your config. Its CI had
finished green, so it moved to `in_review`, and the issue's label changed to `ynf:in-review`. (If
its CI is still running, it stays `proposed`, with the reason `#13: CI pending`.)

An item **settles** when nothing more happens without a person: in review, escalated, quarantined,
ignored, done or closed. `proposed` isn't settled: CI is still to come.

## Until everything settles

```bash
ynf sweep --until-settled --timeout 15m --lane gofmt
```

This repeats the sweep every fifteen seconds until every item has settled, then prints the table:

```text
ITEM                              LANE   STATE      PR   REASON
github.com/<you>/ynf-sandbox#3    gofmt  in_review  #14  #14: CI green
github.com/<you>/ynf-sandbox#5    gofmt  in_review  #13  #13: CI green
```

If the timeout passes first, it prints the table anyway and exits 31. That's the form for a
one-off job: run until there's nothing left to do, then stop.

## A long-running worker

`ynf serve` sweeps every minute until you stop it. With `--listen`, it also takes GitHub webhooks,
so it hears about changes as they happen instead of on the next minute, and an instruction endpoint,
`POST /start`, for other systems to start work. Both need secrets:

```bash
openssl rand -hex 16 > ~/ynf-tutorial/webhook-secret
openssl rand -hex 16 > ~/ynf-tutorial/start-token
export YNF_WEBHOOK_SECRET=$(cat ~/ynf-tutorial/webhook-secret)
export YNF_START_TOKEN=$(cat ~/ynf-tutorial/start-token)
ynf serve --listen 127.0.0.1:8080 --lane gofmt
```

Expected, among the first lines:

```text
level=INFO msg="receiving GitHub webhooks" addr=127.0.0.1:8080 path=/webhook/github
```

`serve` won't listen without a webhook secret. Without a start token, `POST /start` simply doesn't
exist. Leave it running, and open a second terminal with the two `export` lines from lesson 1 and
these:

```bash
export YNF_WEBHOOK_SECRET=$(cat ~/ynf-tutorial/webhook-secret)
export YNF_START_TOKEN=$(cat ~/ynf-tutorial/start-token)
curl -s localhost:8080/healthz
```

Expected:

```text
ok
```

### Start work over HTTP

`POST /start` takes the same instruction as `ynf start --detach`: it records the work, and the
worker's loop picks it up. This one has no ticket at all, just a prompt, with the label the lane
reads (lesson 7 says more about prompts):

```bash
curl -s -X POST localhost:8080/start \
  -H "Authorization: Bearer $YNF_START_TOKEN" \
  -d '{"prompt": "Run gofmt over internal/format", "labels": ["pkg:internal/format"], "repo": "<you>/ynf-sandbox", "lane": "gofmt"}'
```

Expected, with status 202:

```json
{"item":"item/adhoc/01m43h2c8yq7v5x3f9kt0w6b1r","lane":"gofmt","ref":"adhoc/01m43h2c8yq7v5x3f9kt0w6b1r"}
```

Within a minute, the first terminal logs the item being decided, run and proposed. Without the
token, the answer is `401` and nothing is recorded. An instruction ynf won't take, such as a lane
that doesn't exist, is refused with `422` and the reason, also before anything is recorded.

### Send a webhook

GitHub signs each webhook with the shared secret, and ynf checks the signature before reading
anything else. Send one yourself: a `pull_request` event about the pull request the sweep opened,
`#14` in the table above. Use your number:

```bash
cat > ~/ynf-tutorial/pr.json <<'EOF'
{"repository": {"full_name": "<you>/ynf-sandbox", "html_url": "https://github.com/<you>/ynf-sandbox"},
 "pull_request": {"number": 14}}
EOF
SIG=$(openssl dgst -sha256 -hmac "$YNF_WEBHOOK_SECRET" ~/ynf-tutorial/pr.json | sed 's/^.* //')
curl -s -X POST localhost:8080/webhook/github \
  -H "X-GitHub-Event: pull_request" -H "X-GitHub-Delivery: tutorial-1" \
  -H "X-Hub-Signature-256: sha256=$SIG" --data-binary "@$HOME/ynf-tutorial/pr.json"
```

Expected:

```text
queued
```

And in the first terminal, at once rather than on the next minute:

```text
level=INFO msg=decided item=item/github.com/<you>/ynf-sandbox/issues/3 event=ynf.forge.changed state=in_review reason="#14: in review"
```

Nothing had changed on the pull request, so the item stays in review; the point is that ynf looked
the moment it was told, instead of on its next timer.

Send exactly the same request again. The answer is `duplicate`: each delivery id is handled once,
since GitHub retries deliveries. Change one character of `pr.json` without signing it again, and
the answer is `401`, `webhook signature does not match`.

### What just happened

The webhook named a pull request; ynf found the item it belongs to and stepped it at once. It
didn't believe anything else the payload said. A webhook is only a hint that something changed:
ynf read the pull request's state fresh from GitHub before deciding, as it does for every decision.
That's why a forged, stale or out-of-order webhook can't make ynf do the wrong thing. At worst it
makes ynf look sooner.

Stop `serve` with Ctrl-C. It prints the table on the way out.

## In CI, one event at a time

A third way needs no server at all. A GitHub Actions workflow runs on the repository's own events,
and `ynf handle` handles the one that triggered it. Try it here with the payload you just wrote:

```bash
ynf handle --github-event ~/ynf-tutorial/pr.json --github-event-name pull_request
```

Expected, after the log:

```text
pull_request on <you>/ynf-sandbox: issues [], pull requests [14]
```

`handle` steps the items the event touches, and sweeps the repository for new tickets. It has no
`--lane`, so it sweeps every lane, including the paused ones. Run `ynf items ls` and you'll see
items for the lint and docs tickets, and the slow one from lesson 6, all waiting in `ready`:

```text
github.com/<you>/ynf-sandbox#12   lint-paydown  ready      lane lint-paydown is paused (the ynf track runs no agents); waiting
```

That's pausing doing its job: the tickets are noticed, and nothing starts. The `deps` ticket
appears as `ignored`, since its lane is switched off.

A workflow that does this on GitHub looks like this. You don't need to set it up for this track:

```yaml
# .github/workflows/ynf.yml in the repository ynf works on
name: ynf
on:
  issues: { types: [opened, labeled, reopened] }
  pull_request: { types: [synchronize, closed, reopened] }
  check_suite: { types: [completed] }
  schedule: [{ cron: '*/15 * * * *' }]
concurrency: ynf
jobs:
  handle:
    runs-on: ubuntu-latest
    steps:
      # Install ynf: brew install eyelock/tap/ynf once it is released.
      - run: ynf --config .ynf/config.yaml handle
        env:
          GITHUB_TOKEN: ${{ secrets.YNF_TOKEN }}
```

`handle` reads the event from `GITHUB_EVENT_PATH` and `GITHUB_EVENT_NAME`, which Actions sets. A
`schedule` or `workflow_dispatch` event runs a full sweep instead, which catches anything a missed
event would have. Two things differ from your laptop:

- **State must outlive the job.** Each job starts on a fresh machine, so the config's `store` is an
  S3 bucket (`s3://bucket/prefix?region=…`) rather than a file.
- **ynf needs its own token.** Pushes and pull requests made with a workflow's built-in token don't
  trigger other workflows, so the pull requests ynf opened wouldn't get CI. `YNF_TOKEN` is a token
  that can push branches and open pull requests.

[Run the factory image](../../how-to/run-the-factory-image.md) shows the deployed version: ynf in
an image, run as a job.

## What you know now

- A **sweep** runs the lanes' searches and steps every item that's due. `--until-settled` repeats
  it until only people can move things on.
- **`serve`** is a long-running worker. With `--listen` it takes signed GitHub webhooks, each
  handled once and only as a hint, and `POST /start` instructions with a bearer token.
- **`handle`** is the same in CI: one event per job, with state somewhere that outlives the job.
- However work arrives, it becomes the same item, decided from fresh facts.

Next: [6. When things go wrong](06-when-things-go-wrong.md).
