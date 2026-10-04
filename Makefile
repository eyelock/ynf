# ynf: your named factory.
#   make deps     check the prerequisites (Go, golangci-lint) and download the modules
#   make check    format, vet, lint, tests with the race detector, and the coverage gate
#   make build    bin/ynf
#   make install  bin/ynf and its linux builds into $(INSTALL_DIR) (~/.ynf/bin, as ynh and ynm
#                 use ~/.ynh/bin and ~/.ynm/bin); put it on your PATH
#   make e2e      the factory acceptance test against the live sandbox (sandbox/Makefile)
#   make factory-image  ynf's factory image (ADR-009): ynh's image with ynf and ynm, at the
#                 versions in images/factory/versions.env, and this checkout's ynf. To try dev
#                 builds: YNH_SRC=<ynh checkout> builds its ynh in; YNM_SRC=<ynm checkout> builds
#                 its ynm (pnpm). FACTORY_IMAGE names it (default ynf-factory:dev).

VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS     := -s -w -X github.com/eyelock/ynf.Version=$(VERSION)
INSTALL_DIR ?= $(HOME)/.ynf/bin
COVERAGE    ?= 80

include images/factory/versions.env
FACTORY_IMAGE ?= ynf-factory:dev
YNH_SRC       ?=
YNM_SRC       ?=

.PHONY: deps check build install test cover lint vet fmt fmt-check e2e calibrate clean factory-image

check: fmt-check vet lint cover

deps:
	@command -v go >/dev/null 2>&1 || { echo "Installing Go..."; brew install go; }
	@command -v golangci-lint >/dev/null 2>&1 || { echo "Installing golangci-lint..."; brew install golangci-lint; }
	go mod download
	cd sandbox/e2e && go mod download
	@echo "All prerequisites installed."

# bin/ynf for this machine, plus static linux builds the docker executor runs as its egress proxy.
build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/ynf ./cmd/ynf
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/ynf-linux-arm64 ./cmd/ynf
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/ynf-linux-amd64 ./cmd/ynf

install: build
	@mkdir -p $(INSTALL_DIR)
	@# Copy to a temporary name, then rename over the old binary. Overwriting a binary in place
	@# keeps the same file, and on macOS the kernel's cached code-signature check for it can go
	@# stale: the next launch is killed with SIGKILL (exit 137) and no message. A rename puts a
	@# new file in place.
	@for f in ynf ynf-linux-arm64 ynf-linux-amd64; do \
	  cp bin/$$f $(INSTALL_DIR)/.$$f.tmp && mv -f $(INSTALL_DIR)/.$$f.tmp $(INSTALL_DIR)/$$f; \
	done
	@echo "installed ynf $(VERSION) to $(INSTALL_DIR)"

test:
	go test -race -count=1 ./...

cover:
	@scripts/coverage.sh $(COVERAGE)

lint:
	golangci-lint run ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

fmt-check:
	@out=$$(gofmt -l . | grep -v '^sandbox/seed/' || true); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

e2e:
	$(MAKE) -C sandbox e2e

calibrate:
	$(MAKE) -C sandbox calibrate

clean:
	rm -rf bin

# The factory image, for docker's architecture. Each tool is a release unless a checkout is given.
factory-image:
	@set -e; arch=$$(docker version --format '{{.Server.Arch}}'); ctx=$$(mktemp -d); \
	trap 'rm -rf "$$ctx"' EXIT; \
	cp images/factory/Dockerfile "$$ctx/"; mkdir -p "$$ctx/ynh" "$$ctx/ynm"; \
	echo "ynf $(VERSION) from this checkout"; \
	CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o "$$ctx/ynf-linux-$$arch" ./cmd/ynf; \
	if [ -n "$(YNH_SRC)" ]; then \
	  echo "ynh from $(YNH_SRC), over ghcr.io/eyelock/ynh:$(YNH_VERSION)"; \
	  (cd "$(YNH_SRC)" && CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -trimpath -o "$$ctx/ynh/ynh" ./cmd/ynh); \
	else echo "ynh $(YNH_VERSION)"; fi; \
	if [ -n "$(YNM_SRC)" ]; then \
	  echo "ynm from $(YNM_SRC)"; \
	  (cd "$(YNM_SRC)" && pnpm -s build >/dev/null && node scripts/release/build-slim.mjs dev >/dev/null); \
	  tar -xzf "$(YNM_SRC)/dist-release/ynm_dev_slim.tar.gz" -C "$$ctx/ynm"; \
	else \
	  echo "ynm $(YNM_VERSION)"; \
	  gh release download v$(YNM_VERSION) -R eyelock/ynm -p 'ynm_$(YNM_VERSION)_slim.tar.gz' -D "$$ctx"; \
	  tar -xzf "$$ctx/ynm_$(YNM_VERSION)_slim.tar.gz" -C "$$ctx/ynm"; \
	fi; \
	docker build -q --build-arg YNH_BASE=ghcr.io/eyelock/ynh:$(YNH_VERSION) --build-arg YNF_VERSION=$(VERSION) -t $(FACTORY_IMAGE) "$$ctx" >/dev/null; \
	echo "built $(FACTORY_IMAGE):"; \
	docker run --rm --entrypoint sh $(FACTORY_IMAGE) -c 'printf "  ynf %s\n  ynh %s\n  ynm %s\n" "$$(ynf version)" "$$(ynh version)" "$$(ynm --version 2>/dev/null | tail -1)"'
