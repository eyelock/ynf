The append-edit lane's command edits `internal/greet/greet_test.go`, a test file that already exists. The lane's
`append_only_paths` refuse that, so this item always escalates with the diff gate's reason; it needs no model.
