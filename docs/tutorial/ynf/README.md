# Tutorial: ynf on its own

Seven lessons that teach ynf, the outer loop, with nothing else involved. You build a sandbox of
your own on GitHub, point ynf at it, and watch tickets become draft pull requests. Then you look at
what ynf decided and why, run it unattended, break it on purpose, and bring in tickets from a
tracker that isn't GitHub.

Every lane this track runs is a command lane: `gofmt`, run in a container. There is no model, no API
key and no agent, and runs take seconds. ynf doesn't need ynh, ynm or ynr to do any of this: it detects
them and never requires them (ADR-012). [The factory track](../factory/README.md) adds them, once you know
how the loop behaves.

Each lesson builds on the one before. Every step is a command followed by what you should see.
Timestamps, ids, issue and pull request numbers will differ from the ones shown.

## Lessons

| Lesson | What you will learn |
|---|---|
| [1. A sandbox and what ynf can see](01-a-sandbox-and-what-ynf-can-see.md) | Build your sandbox, write a config, and check it with `doctor`, `forges`, `trackers` and `harness` |
| [2. Lanes](02-lanes.md) | Searches, guards and labels; the configuration repository under the repository's own lanes; `lanes show` and where each value came from |
| [3. A ticket becomes a pull request](03-a-ticket-becomes-a-pull-request.md) | `ynf start`, the log as it runs, the draft pull request and its trailers, labels moving on the issue |
| [4. What it decided and why](04-what-it-decided-and-why.md) | `items log`, `replay`, and replaying under a lane you changed |
| [5. Running unattended](05-running-unattended.md) | `sweep --until-settled`, `serve` with webhooks and `POST /start`, `handle` in GitHub Actions |
| [6. When things go wrong](06-when-things-go-wrong.md) | Kill ynf mid-run and watch it restart, not resume; `retry` and `release`; `pause` and `resume`; `stats` |
| [7. Tickets from elsewhere](07-tickets-from-elsewhere.md) | A tracker that isn't GitHub, read with `ticket` and started with `--repo`; work from a prompt with `--label` |

## What you need

- **Go 1.26, git and Docker running.** The sandbox's lanes run in a container.
- **Read access to `github.com/eyelock/ynr`,** while that repository is private. Building ynf from
  source needs it; lesson 1 says where to read how.
- **Terraform 1.10 or later,** which builds the sandbox.
- **The GitHub CLI, signed in,** with the `repo` and `delete_repo` scopes:
  `gh auth refresh -s delete_repo`. ynf uses its token when `GITHUB_TOKEN` isn't set.
- **About an hour.** Most of it is waiting for the sandbox's CI.

No AWS account, no API key, no ynh, no ynm and no ynr.

## Afterwards

The sandbox's repositories are yours. `make -C sandbox reset` rebuilds them from scratch, and
`make -C sandbox down` deletes them. The config and state you create live in one folder,
`~/ynf-tutorial`, which you can delete.
