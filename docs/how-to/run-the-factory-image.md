# Run the factory image

Run ynf as a job, on a job runner or in CI, from the factory image: ynh's image with ynf and ynm
added. Each run starts ynh as a process beside ynf, as another user, using the harness installed
in the image. The job runner provides the containment.

## Get an image with your harness in it

The image ynf publishes, `ghcr.io/eyelock/ynf-factory:<version>`, has no harness of its own.
Build your harness onto it with ynh, which gives the factory flavour of your harness image:

```bash
ynh image <harness> --base ghcr.io/eyelock/ynf-factory:<version> --entrypoint agent
```

To try unreleased versions, `make factory-image YNH_SRC=<ynh checkout> YNM_SRC=<ynm checkout>`
builds `ynf-factory:dev` from your checkouts.

## Configure ynf for it

The config the job gives ynf says every run is inline:

```yaml
version: 1
factory: { repo: example-org/factory }
executor: inline
work_dir: /work
```

`executor: inline` declares that the operator provides the containment. Never set it outside
such a container: there, ynf would start agents with no containment at all.

## Run it as a job

```bash
docker run --rm --user root \
  --cap-drop ALL --cap-add SETUID --cap-add SETGID --cap-add CHOWN \
  --security-opt no-new-privileges \
  -e GITHUB_TOKEN -e ANTHROPIC_API_KEY \
  -v "$PWD/config.yaml:/etc/ynf/config.yaml:ro" -v ynf-work:/work \
  --entrypoint ynf <your harness image> \
  --config /etc/ynf/config.yaml start example-org/payments#42
```

ynf runs as root with only the capabilities it needs to hand a run's folders to the `ynh` user and
start the run as that user, so the run can't read ynf's environment or token. Give the job a
network policy that allows the forge, the model API and the hosts the lane allows: ynf logs the
hosts each lane expects, but here the job runner enforces them.

Use `ynf handle` instead of `ynf start` when a CI trigger starts the job, and `ynf serve` for a
long-running worker.

## Check it before relying on it

Run these the same way, in place of `start`:

```bash
ynf doctor     # config, store, lanes, forges, trackers, and the ynh and ynm in the image
ynf harness    # each lane, the harness installed in the image, and whether it fits the lane
```

`make -C sandbox e2e-factory` is the acceptance test of exactly this.
