# The factory: ynh, ynm and ynf together

[Track 1](../ynf/README.md) taught ynf on its own: lanes that run commands, items that become pull
requests, decisions you can replay. This track adds the other two tools by hand, one join at a
time, until the sandbox runs as a factory: ynf decides, ynh runs an agent with its harness and
sensors, and ynm remembers what keeps going wrong.

Each lesson is about where one tool hands over to the next. Each shows the configuration that
makes the join, what you see when it works, and ends with **What just happened**, naming which tool
did what.

| Lesson | The join |
|---|---|
| [1. A harness lane](01-a-harness-lane.md) | ynf reads ynh's harness: a lane may tighten its budgets and scope its sensors, never loosen or redefine them |
| [2. A contained agent run](02-a-contained-agent-run.md) | ynf runs ynh in a container: the agent image, the egress proxy, `auto_approve`, the model key, and the result ynh hands back |
| [3. Which model, at what cost](03-which-model-at-what-cost.md) | What ynh reports becomes ynf's run records: `ynf stats` by model and effort |
| [4. When it keeps failing](04-when-it-keeps-failing.md) | ynf writes each failure to ynm; ynm's dream turns repeats into a reflection |
| [5. Shared memory](05-shared-memory.md) | ynf writes to a hosted ynm over HTTP, the store a pool of workers shares |
| [6. One image](06-one-image.md) | All three in one image, run as a job, every run beside ynf |

## Before you start

- **Track 1 done.** This track assumes its basics: the sandbox, `ynf start`, `items log`,
  `replay`, labels and lanes.
- **ynh and ynm installed**, as well as ynf:

  ```bash
  brew install eyelock/tap/ynh eyelock/tap/ynm
  ynh version
  ynm --version
  ```

  Each tool has its own tutorials for using it alone: ynh's
  [first harness](https://github.com/eyelock/ynh/blob/develop/docs/tutorial/first-harness.md) and
  [agent loop](https://github.com/eyelock/ynh/blob/develop/docs/tutorial/agent-loop.md), and ynm's
  [tutorial series](https://github.com/eyelock/ynm/blob/main/docs/tutorial/README.md). You don't
  need them first, but they explain each tool's side of the joins here in more depth.
- **Docker**, running.
- **Your sandbox**, from track 1: `sandbox/sandbox.env` names you as `SANDBOX_OWNER`, and
  `make -C sandbox up` has created `<you>/ynf-sandbox` and `<you>/ynf-sandbox-factory`.
- **An Anthropic API key**, in `ANTHROPIC_API_KEY`, from lesson 2 on.

## What it costs

Lessons 2, 3, 4 and 6 run a real agent, which spends real money on your key. In the runs these
lessons were written from, an agent run that fixed a lint finding cost **$0.14 to $0.25**. A run
that fails can cost more: it may use its whole turn budget, and a lane may retry it. Every other
step runs no model and costs nothing.

## Every lesson starts here

Each lesson begins with the same block, so it can be run on its own. It points at your ynf
checkout, reads your sandbox's names, and keeps this track's state in its own folder:

```bash
export YNF_SRC=<your ynf checkout>
set -a; . "$YNF_SRC/sandbox/sandbox.env"; set +a
export REPO=$SANDBOX_OWNER/${SANDBOX_NAME:-ynf-sandbox}
export TUTORIAL=$HOME/ynf-factory-tutorial
export YNF_CONFIG=$TUTORIAL/config.yaml
export YNM_HOME=$TUTORIAL/ynm-home
cd "$TUTORIAL" 2>/dev/null || true
```

`YNM_HOME` gives ynm a store of its own for this track, so nothing here touches your personal
memory. Lesson 1 creates the folder and the config.
