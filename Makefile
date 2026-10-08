# Build and test leanfeed.
#   make            run all tests, then build bin/leanfeed
#   make build      build bin/leanfeed
#   make test       run go vet, then all tests with the race detector, including the slow checks
#   make test-short run the tests without the slow checks and the browser tests
#   make test-ui    run the UI flow tests and the browser tests (needs Chrome; see flake.nix)
#   make clean      remove bin/

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BIN     := bin/leanfeed

.PHONY: all build test test-short test-ui clean

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

clean:
	rm -rf bin
