# 5. Shared memory

So far ynf has written to your own ynm store, through the ynm CLI. That's right for one developer
on one laptop. A factory that runs as a pool of workers, or in CI, has many writers at once and no
laptop, and the pattern it learns from belongs to the team, not to whoever ran it. That is what
ynm's hosted server is for. This lesson runs one locally, points ynf at it over HTTP, and sees a
failure land in the shared store.

It runs no model.

## Prerequisites

The block from [the track's README](README.md#every-lesson-starts-here), and `curl`:

```bash
export YNF_SRC=<your ynf checkout>
set -a; . "$YNF_SRC/sandbox/sandbox.env"; set +a
export REPO=$SANDBOX_OWNER/${SANDBOX_NAME:-ynf-sandbox}
export TUTORIAL=$HOME/ynf-factory-tutorial
export YNF_CONFIG=$TUTORIAL/config.yaml
export YNM_HOME=$TUTORIAL/ynm-home
cd "$TUTORIAL"
export NS=factory/github.com/$REPO
```

## A hosted store

ynm's server fronts one bare repository. Create one and start the server on it, with a static
token, which is fine for trying it out:

```bash
ynm init --bare "$TUTORIAL/shared.git"
ynm init --cwd "$TUTORIAL/shared.git"
ynm serve --http --port 3999 --token demo --no-personal --cwd "$TUTORIAL/shared.git" > "$TUTORIAL/serve.log" 2>&1 &
echo $! > "$TUTORIAL/serve.pid"
until curl -sf http://localhost:3999/health > /dev/null; do sleep 0.5; done
cat "$TUTORIAL/serve.log"
```

Expected:

```text
ynm-mcp listening on http://localhost:3999/mcp (health: http://localhost:3999/health)
ynm-mcp auth: bearer
```

`--no-personal` is how a shared server runs: it keeps nothing at the personal level, so everything
written to it is shared with everyone who uses it. ynm's
[hosted tutorial](https://github.com/eyelock/ynm/blob/main/docs/tutorial/10-hosted.md) covers the
server itself.

## Point ynf at it

Change the config's `memory` block from the CLI to the server:

```bash
cat > config.yaml <<EOF
version: 1
factory: { repo: $SANDBOX_OWNER/${SANDBOX_FACTORY:-ynf-sandbox-factory} }
memory:
  provider: ynm
  transport: http
  endpoint: http://localhost:3999/mcp
  token_env: YNF_YNM_TOKEN
EOF
```

ynf reads the token from the variable `token_env` names, never from the file. Try it without:

```bash
ynf items ls
```

Expected, with exit code 30:

```text
ynf: memory.transport http needs a token: set memory.token_env and that variable
```

ynf refuses to start rather than run without the memory it was told to use. Set the token:

```bash
export YNF_YNM_TOKEN=demo
ynf items ls
```

Expected: the items from lessons 1 to 4.

The config doesn't say `level`, and doesn't need to. Over HTTP ynf writes at `distributed` unless
told otherwise, because a hosted store keeps nothing personal. Asking for `level: personal` here
would have every write refused, and ynf would log each refusal as a `memory remember` warning.

## A failure, shared

Make one more gofmt run fail, as in lesson 4:

```bash
ynf start --prompt "Format internal/nowhere, shared" --label pkg:internal/nowhere --repo $REPO --lane gofmt
```

Expected: `adhoc/<id>: escalated in lane gofmt (outcome.error)`.

ynf wrote the occurrence with ynm's `memory_remember` tool, over MCP, with the token as a bearer
token. Ask the server for it the same way, with `memory_recall`:

```bash
curl -s -X POST http://localhost:3999/mcp \
  -H 'Authorization: Bearer demo' -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"memory_recall","arguments":{"namespace":"'"$NS"'","tags":["ynf.failure.v1"]}}}' \
  | grep '^data:' | cut -c7- | python3 -m json.tool | head -30
```

Expected: a result with one hit, the occurrence you just made: subject `sig/outcome/error`, level
`distributed`, the `ynf.failure.v1` tag, and the summary
`sig/outcome/error on adhoc/<id> (gofmt), occurrence 1, run <run id>`, in the namespace `$NS`.
Lesson 4's memories aren't there: they're in your own store, which this server has never seen.

The server kept the record as ynf sent it. `memory_recall` leaves out the structured part; the
store's raw records have it:

```bash
ynm export --cwd "$TUTORIAL/shared.git" | python3 -c '
import json, sys
for line in sys.stdin:
    r = json.loads(line)
    print(r["namespace"], r["level"], r["dataSchema"], r["provenance"])
    print(r["data"])'
```

Expected: the namespace `$NS`, level `distributed`, the schema `ynf.failure.v1`, and `data` with
the signature, the lane, the item and the occurrence count. The `provenance` has `source` naming
the step that wrote it and `actor` naming the writer: here `user:` and the name ynm runs as, because a
static token names no one, so the server records itself. ynm files a write under the caller only
when it names no namespace, and ynf always names one, so the token does not move the record.

## In production: a machine token

A static token is one shared secret: everyone who has it is the same writer. A real shared server
signs people and machines in through an identity provider, and ynm records each write's author in
its audit log. A factory's workers aren't people, so they use the **client-credentials grant**: an
Auth0 machine-to-machine application, a Keycloak service account or similar, whose token carries
`memory:read` and `memory:write`. Its subject is the writer ynm records, in place of the server's own name that the static token
gave.

Nothing in ynf's config changes but the endpoint. The token arrives in the variable `token_env`
names, put there by whatever starts the worker: your CI's secret store, or the job runner's. Part 2
of ynm's hosted tutorial sets up the server side, and
[Operate a hosted store](https://github.com/eyelock/ynm/blob/main/docs/how-to/operate-a-hosted-store.md)
runs it for real.

## Clean up

Stop the server, and put the config back to your own store for the next lesson:

```bash
kill "$(cat "$TUTORIAL/serve.pid")"
cat > config.yaml <<EOF
version: 1
factory: { repo: $SANDBOX_OWNER/${SANDBOX_FACTORY:-ynf-sandbox-factory} }
memory: { provider: ynm }
EOF
unset YNF_YNM_TOKEN
```

## What just happened

- **ynm** ran as a server in front of a shared store, answering MCP over HTTP to a bearer token.
- **ynf**, configured with `transport: http`, opened an MCP session to it, and wrote the occurrence
  with `memory_remember` at the `distributed` level: the same record lesson 4 wrote through the CLI.
- **You** read it back the way any agent or tool would, with `memory_recall` and the same token.

For one developer, the CLI and a personal store; for a pool or CI, one server and a machine token.
ynf's records are the same either way.

Next: [6. One image](06-one-image.md).
