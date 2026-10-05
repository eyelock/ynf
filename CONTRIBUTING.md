# Contributing to ynf

## Build and check

```bash
make deps      # Go and golangci-lint, and the modules
make check     # format, vet, lint, race tests, and every package at 80% coverage or more
make install   # into ~/.ynf/bin
make help      # every target
```

ynf builds on ynr's spool exporter, a Go module in ynr's repository, which is private while ynr is.
Building ynf from source needs read access to `github.com/eyelock/ynr`: the Makefile sets
`GOPRIVATE=github.com/eyelock/ynr`, and git must be able to fetch it (a signed-in `gh`, an SSH key
or a token in a `url.<base>.insteadOf` setting). CI uses the `YNR_READ_PACKAGES` secret for the
same. Released binaries are unaffected.

`make check` is what CI runs on every pull request. `make e2e` runs the factory against a live
sandbox of your own ([Test against the sandbox](docs/how-to/test-against-the-sandbox.md)).

## Branches and pull requests

ynf uses Gitflow, the same model as [ynh](https://github.com/eyelock/ynh) and
[ynm](https://github.com/eyelock/ynm). Nothing is committed to `main` or `develop` directly: every
change goes through a branch and a pull request.

| Work | Branch from | Pull request into |
|---|---|---|
| Feature, fix, docs, CI | `develop` | `develop` |
| Release | `develop`, as `release/vX.Y.Z` | `main`, then back-merged into `develop` |
| Hotfix | the release tag, as `hotfix/<what>` | `main`, then back-merged into `develop` |

Branch names use a slash: `feat/…`, `fix/…`, `docs/…`, `ci/…`, `chore/…`, `infra/…`,
`hotfix/…`. `develop` is the default branch. Feature pull requests are squash-merged, titled and
described by the pull request, so its `Closes #n` lines close issues; release and hotfix pull
requests into `main` use a true merge, so the back-merge into `develop` is clean. `main` accepts
pull requests only from `develop`, `release/*` or `hotfix/*` (the "Verify PR source branch" check
enforces it), and release tags are cut from `main`.

```bash
git switch develop && git pull
git switch -c feat/my-change
# ...work, commit...
git fetch origin develop && git merge origin/develop
make check
gh pr create --base develop
```

A pull request stacked on another targets that branch. Merge the base first; when a base is
squash-merged, replay the stacked branch's own commits onto `develop` before merging it.

Releasing: [Cut a release](docs/how-to/cut-a-release.md).
