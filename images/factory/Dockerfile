# ynf's factory image (ADR-009): ynh's image (ynh, git, Node and the vendor CLIs) with ynf and ynm,
# so one image can run a factory job. Harness images built on it with `ynh image <harness> --base`
# are the factory flavour: run one as a job with `ynf start` or `ynf handle`, and ynf runs ynh
# inline beside it, as another user (ADR-007).
#
# Build it with `make factory-image`, which supplies the context: ynf-linux-<arch> (static linux
# builds, one per architecture built), ynm/
# (ynm's Node build, which runs on the Node ynh's image ships), and ynh/ (a ynh build to use
# instead of the base's, for trying ynh before it is released; empty keeps the base's).
ARG YNH_BASE=ghcr.io/eyelock/ynh:0.9.0
FROM ${YNH_BASE}
ARG TARGETARCH
USER root
COPY ynf-linux-${TARGETARCH} /usr/local/bin/ynf
COPY ynm/ /usr/local/lib/ynm/
COPY ynh/ /tmp/ynh/
RUN ln -s /usr/local/lib/ynm/bin/ynm /usr/local/bin/ynm \
 && if [ -f /tmp/ynh/ynh ]; then install -m 0755 /tmp/ynh/ynh /usr/local/bin/ynh; fi \
 && rm -rf /tmp/ynh \
 # ynf and the run are different users sharing a repository mirror by design (ADR-007).
 && git config --system safe.directory '*'
ARG YNF_VERSION=dev
LABEL dev.ynf.version=${YNF_VERSION}
# The harness images built on this one set their own user and entrypoint (ynh agent run); a
# factory job runs them as root with `--entrypoint ynf`, and ynf starts each run as ynh.
USER ynh
