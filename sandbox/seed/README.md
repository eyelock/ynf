# ynf-sandbox

A disposable repository for testing [ynf](https://github.com/eyelock/ynf). Every problem in it
is planted. It is created, and recreated from scratch, by Terraform in ynf's `sandbox/` folder;
anything changed here by hand is lost on the next reset.

## greet

```bash
go run ./cmd/greet --name ynf
```

| Flag | Meaning |
|---|---|
| `--name` | who to greet (default `world`) |
| `--loud` | print the greeting in capitals |
