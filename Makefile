# ynf: your named factory.
#   make check    format, vet, lint, tests with the race detector, and the coverage gate
#   make build    bin/ynf
#   make install  bin/ynf into $(INSTALL_DIR)
#   make e2e      the factory acceptance test against the live sandbox (sandbox/Makefile)

VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS     := -s -w -X github.com/eyelock/ynf.Version=$(VERSION)
INSTALL_DIR ?= $(HOME)/.local/bin
COVERAGE    ?= 80

.PHONY: check build install test cover lint vet fmt fmt-check e2e calibrate clean

check: fmt-check vet lint cover

# bin/ynf for this machine, plus static linux builds the docker executor runs as its egress proxy.
build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/ynf ./cmd/ynf
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/ynf-linux-arm64 ./cmd/ynf
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/ynf-linux-amd64 ./cmd/ynf

install: build
	@mkdir -p $(INSTALL_DIR)
	cp bin/ynf bin/ynf-linux-arm64 bin/ynf-linux-amd64 $(INSTALL_DIR)/
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
