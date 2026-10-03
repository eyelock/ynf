`internal/legacy` is the noisiest package in the lint report and keeps the gate red:

```
internal/legacy/legacy.go:14:9: Error return value of `io.Copy` is not checked (errcheck)
internal/legacy/legacy.go:15:9: Error return value of `f.Close` is not checked (errcheck)
internal/legacy/legacy.go:32:14: Error return value of `fmt.Fprintln` is not checked (errcheck)
internal/legacy/legacy.go:33:16: Error return value of `os.Stdout.Sync` is not checked (errcheck)
```

Nobody owns this code any more and nobody wants to change its behaviour. We just need the lint
gate to stop failing on it.
