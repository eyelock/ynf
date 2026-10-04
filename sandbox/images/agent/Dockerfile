# The sandbox's agent base: ynh's image (ynh, git and the vendor CLIs) plus the tools the sandbox
# harness's sensors run, golangci-lint and the Go toolchain. `ynh image --base` builds each harness
# image on it.
ARG YNH_BASE=ghcr.io/eyelock/ynh:0.9.0
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
ENV PATH=/usr/local/go/bin:$PATH \
    GOTOOLCHAIN=local
USER ynh
