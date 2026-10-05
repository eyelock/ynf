# The sandbox's agent base: ynh's image (ynh, git and the vendor CLIs) plus the tools the sandbox
# harness's sensors run, golangci-lint and the Go toolchain. `ynh image --base` builds each harness
# image on it.
ARG YNH_BASE=ghcr.io/eyelock/ynh:0.10.0
FROM golang:1.26-alpine AS go
RUN wget -qO- https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh | sh -s -- -b /out v2.12.2

FROM ${YNH_BASE}
USER root
COPY --from=go /usr/local/go /usr/local/go
COPY --from=go /out/golangci-lint /usr/local/bin/golangci-lint
# make agent-image YNH_SRC=<ynh checkout> supplies a develop build in ynh/, to try ynh before it
# is released; an empty folder keeps the base's.
COPY ynh/ /tmp/ynh/
RUN if [ -f /tmp/ynh/ynh ]; then install -m 0755 /tmp/ynh/ynh /usr/local/bin/ynh; fi && rm -rf /tmp/ynh
# golangci-lint's cache lives outside the checkout, so the diff gate never sees it, and is open to
# whichever user the container runs as. A scope may only narrow a sensor's command (ADR-006), so
# the lane cannot carry the location in its command; the image provides it, and every container
# starts with this folder empty.
RUN mkdir -p /var/cache/golangci-lint && chmod 1777 /var/cache/golangci-lint
ENV PATH=/usr/local/go/bin:$PATH \
    GOTOOLCHAIN=local \
    GOLANGCI_LINT_CACHE=/var/cache/golangci-lint
USER ynh
