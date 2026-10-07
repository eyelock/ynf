# ynf

**Your named factory.** The outer loop around agent runs: ynf watches tickets and pull requests,
decides deterministically what the next turn should be, runs it in a container, proposes the
change as a draft pull request, and follows it through CI and review. A human always merges.

ynf is the runtime layer of [ynh](https://github.com/eyelock/ynh)'s factory pattern, and works
with ynh and [ynm](https://github.com/eyelock/ynm) when they are installed, without needing either.
A lane that runs an agent unsupervised (`run.ynh.auto_approve`) needs ynh 0.9.0 or later in its
agent image.

```bash
brew install eyelock/tap/ynf
ynf --config config.yaml sweep --until-settled
```

Or from source: `make build`, then `bin/ynf`.

- [Run ynf locally](docs/how-to/run-ynf-locally.md)
- [The design](docs/README.md): architecture decisions and explanation
- [The sandbox](sandbox/README.md): a disposable repository with planted problems and expected
  outcomes, and `make e2e`, the factory's acceptance test

## Developing

```bash
make check    # gofmt, vet, golangci-lint, race tests, and an 80% per-package coverage gate
make e2e      # rebuild the sandbox and run ynf against it end to end
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for the branch and pull request workflow. Report
vulnerabilities as [SECURITY.md](SECURITY.md) describes. Everyone taking part follows the
[Code of Conduct](CODE_OF_CONDUCT.md).

## Licence

[MIT](LICENSE), copyright (c) 2026 David Collie.
