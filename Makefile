# Build and test leanfeed.
#   make            run all tests, then build bin/leanfeed
#   make build      build bin/leanfeed
#   make test       run go vet, then all tests with the race detector, including the slow checks
#   make test-short run the tests without the slow checks and the browser tests
#   make test-ui    run the UI flow tests and the browser tests (needs Chrome; see flake.nix)
#   make lint       run golangci-lint, which also reports formatting (gofumpt, goimports)
#   make fmt        apply the formatting make lint checks for
#   make vuln       check the dependencies for known vulnerabilities with govulncheck
#   make clean      remove bin/, including the installed lint and vulnerability tools

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BIN     := bin/leanfeed

# Pinned so a local run matches CI, which installs the same versions; see
# .github/workflows/ci.yml.
GOLANGCI_LINT_VERSION := v2.12.2
GOVULNCHECK_VERSION   := v1.6.0
TOOLS_DIR             := $(CURDIR)/bin

# golangci-lint refuses to lint a module whose Go version is newer than the
# one it was built with. Building it with our own toolchain avoids that.
TOOLCHAIN := $(shell go env GOVERSION)

.PHONY: all build test test-short test-ui lint fmt vuln clean

all: test build

# CGO_ENABLED=0 gives a static binary; templates and assets are embedded.
build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o $(BIN) ./cmd/leanfeed

# go test runs only a subset of vet's checks, so the explicit pass is not redundant.
test:
	go vet ./...
	go test -race ./...

test-short:
	go test -short ./...

test-ui:
	go test -count=1 -run 'Flow|Browser' -v ./internal/web

$(TOOLS_DIR)/golangci-lint:
	GOTOOLCHAIN=$(TOOLCHAIN) GOBIN=$(TOOLS_DIR) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

$(TOOLS_DIR)/govulncheck:
	GOTOOLCHAIN=$(TOOLCHAIN) GOBIN=$(TOOLS_DIR) go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)

lint: $(TOOLS_DIR)/golangci-lint
	$(TOOLS_DIR)/golangci-lint run

fmt: $(TOOLS_DIR)/golangci-lint
	$(TOOLS_DIR)/golangci-lint fmt

vuln: $(TOOLS_DIR)/govulncheck
	$(TOOLS_DIR)/govulncheck ./...

clean:
	rm -rf bin
