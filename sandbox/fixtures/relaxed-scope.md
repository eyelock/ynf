Tidy `internal/format`.

There is no ticket: this is ad hoc work, started from a prompt, with its label `pkg:internal/format`
given with `--label`. The `relaxed` lane it is started on scopes the lint sensor to `true`, which
the harness's `golangci-lint run ./...` does not allow, so ynf refuses the run before it starts.
