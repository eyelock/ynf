# The sandbox's agent base: ynh's image (ynh, git and the vendor CLIs) plus the tools the sandbox
# harness's sensors run, golangci-lint and the Go toolchain. `ynh image --base` builds each harness
# image on it.
ARG YNH_BASE=ghcr.io/eyelock/ynh:0.14.0
# The golangci-lint release is the repository's one pin, .golangci-lint-version: the sandbox
# Makefile passes it as this build arg, so a bump there reaches this image too.
FROM golang:1.27-alpine AS go
ARG GOLANGCI_LINT_VERSION
RUN test -n "$GOLANGCI_LINT_VERSION" || { echo "build with --build-arg GOLANGCI_LINT_VERSION=\$(cat .golangci-lint-version)"; exit 1; }
RUN wget -qO- https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh | sh -s -- -b /out "$GOLANGCI_LINT_VERSION"

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
