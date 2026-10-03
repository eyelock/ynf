`clock.Stamp` drops the error from `os.WriteFile`:

```
internal/clock/clock.go:11:14: Error return value of `os.WriteFile` is not checked (errcheck)
```

Callers should find out when the stamp was not written.
