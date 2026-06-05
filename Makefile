SHELL := bash
.SHELLFLAGS := -eu -o pipefail -c

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
	-X 'main.version=$(VERSION)' \
	-X 'main.commit=$(COMMIT)' \
	-X 'main.date=$(DATE)'

export CGO_ENABLED := 0

.PHONY: build test lint vuln coverage gates release-snapshot clean

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o xray ./cmd/xray

test:
	go test -race ./...

lint:
	golangci-lint run

vuln:
	govulncheck ./...

coverage:
	go test ./... -coverprofile=coverage.out -covermode=atomic
	go-test-coverage --config=.testcoverage.yml

# Run every gate the CI pipeline runs. Use before pushing to main.
gates: lint vuln coverage

release-snapshot:
	goreleaser release --snapshot --clean

clean:
	rm -rf xray dist/ coverage.out
