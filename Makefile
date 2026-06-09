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

.PHONY: build test lint vuln coverage gates prose sweep mutation-audit release-snapshot clean

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

prose:
	vale README.md CHANGELOG.md CONTRIBUTING.md SECURITY.md CLAUDE.md \
	    docs/spec.md docs/schema.md docs/security.md docs/threat-model.md \
	    docs/style-guide.md docs/adr/

# Run every gate the CI pipeline runs. Use before pushing to main.
gates: lint vuln coverage

# Once-per-quarter code-quality sweep — not in CI (see ADR 029).
# Install:
#   go install golang.org/x/tools/cmd/deadcode@latest
#   go install go.uber.org/nilaway/cmd/nilaway@latest
# (gocritic ships with golangci-lint; gopls already on PATH for IDEs.)
sweep:
	@echo "== deadcode =="
	@deadcode ./... || true
	@echo
	@echo "== nilaway =="
	@nilaway -include-pkgs=github.com/kmcd/xray ./... || true
	@echo
	@echo "== gocritic =="
	@golangci-lint run --default=none --enable=gocritic --enable-only || true

# Once-per-release-touching-connector mutation audit — not in CI (see ADR 029).
# Scoped to packages with provenance-write coverage. sentry + circleci
# excluded until VCR cassettes land (#66 follow-ups). Config in .gremlins.yaml.
# Install: go install github.com/go-gremlins/gremlins/cmd/gremlins@latest
mutation-audit:
	@echo "== connector =="; gremlins unleash ./internal/connector/ || true
	@echo "== github =="; gremlins unleash ./internal/connectors/github/ || true
	@echo "== githubactions =="; gremlins unleash ./internal/connectors/githubactions/ || true
	@echo "== bugsnag =="; gremlins unleash ./internal/connectors/bugsnag/ || true
	@echo "== honeycomb =="; gremlins unleash ./internal/connectors/honeycomb/ || true

release-snapshot:
	goreleaser release --snapshot --clean

clean:
	rm -rf xray dist/ coverage.out
