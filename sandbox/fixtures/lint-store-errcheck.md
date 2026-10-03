`golangci-lint` reports unchecked errors in `internal/store`:

```
internal/store/file.go:12:15: Error return value of `f.Close` is not checked (errcheck)
internal/store/file.go:13:9: Error return value of `f.Write` is not checked (errcheck)
internal/store/file.go:19:11: Error return value of `os.Remove` is not checked (errcheck)
```

`Save` can report success after a failed write, which is the one that matters.
