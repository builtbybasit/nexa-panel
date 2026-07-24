.DEFAULT_GOAL := build

GO ?= go
BUN ?= bun
PYTHON ?= python3
STATICCHECK_VERSION ?= v0.7.0
DEADCODE_VERSION ?= v0.48.0
GOVULNCHECK_VERSION ?= v1.6.0

.PHONY: build release release-linux test test-race test-embed fmt fmt-check mod-check vet \
	go-staticcheck go-deadcode go-vulncheck \
	web-install web-dev web-test web-typecheck web-build web-deadcode web-audit openapi-lint \
	openapi-gen openapi-gen-check \
	scripts-check check ci \
	test-db-acceptance test-node-lifecycle

build:
	$(GO) build -o bin/nexa ./cmd/nexa

release: check
	mkdir -p dist
	$(GO) build -tags embed -trimpath -o dist/nexa ./cmd/nexa

release-linux:
	bash scripts/build-linux-release.sh amd64

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

# The embedded-frontend build tag swaps in a different asset handler, so its
# tests only compile once web-build has produced the dist tree.
test-embed: web-build
	$(GO) test -tags embed ./...

# Destructive real-engine suites. They are NOT part of `check`/`ci`: each one
# destroys a real database and needs a Docker daemon, so `go test ./...` skips
# them until NEXA_MYSQL_INTEGRATION / NEXA_POSTGRES_INTEGRATION are set. This
# target and the CI job of the same name are the only things that set them.
#   make test-db-acceptance SUITE=mysql
test-db-acceptance:
	GO="$(GO)" bash scripts/test-db-acceptance.sh $(SUITE)

# Executed host lifecycle scenarios against the disposable systemd node. Needs
# the nexa-node image (docker build -t nexa-node .) and a Linux release binary.
#   make test-node-lifecycle BINARY=dist/nexa-linux-arm64
test-node-lifecycle:
	bash scripts/test-node-lifecycle.sh $(BINARY)

fmt:
	GO="$(GO)" bash scripts/check-go-format.sh --write

fmt-check:
	GO="$(GO)" bash scripts/check-go-format.sh

mod-check:
	$(GO) mod tidy -diff

vet:
	$(GO) vet ./...

# Internal packages intentionally avoid repetitive package-doc boilerplate,
# and several validation errors are complete user-facing sentences. Every
# other Staticcheck check is enforced for both development and production tags.
go-staticcheck:
	$(GO) run honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION) -checks=all,-ST1000,-ST1005 ./...
	$(GO) run honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION) -checks=all,-ST1000,-ST1005 -tags embed ./...

go-deadcode:
	$(GO) run golang.org/x/tools/cmd/deadcode@$(DEADCODE_VERSION) -tags embed ./...

go-vulncheck:
	$(GO) run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

web-install:
	cd web && $(BUN) install --frozen-lockfile

web-dev:
	cd web && $(BUN) run dev

web-test: web-install
	cd web && $(BUN) run test

web-typecheck: web-install
	cd web && $(BUN) run typecheck

# Node sizes its default old-space heap from the machine's total RAM, so the
# production bundle built on a 16 GB workstation gets ~4 GB and the same build on
# a 7 GB CI runner gets ~2 GB — where it died with "Ineffective mark-compacts
# near heap limit". Pinning the ceiling makes the build depend on the code rather
# than on who is running it.
web-build: web-install
	cd web && NODE_OPTIONS=--max-old-space-size=4096 $(BUN) run build

web-deadcode: web-install
	cd web && $(BUN) run deadcode

# bun audit is a live call to the advisory registry. A registry outage (5xx/429)
# must not block a tagged release when the code is unchanged, so the gate is run
# through a wrapper that fails on a reported advisory but downgrades a
# transport/service failure to a warning. See scripts/web-audit.sh.
web-audit: web-install
	BUN="$(BUN)" bash scripts/web-audit.sh

openapi-lint: web-install
	cd web && $(BUN) run openapi:lint

# Regenerate the embedded OpenAPI contract and its Go models. Depends on the
# vendored redocly (web-install) for bundling; the generator is pinned via the
# go.mod tool directive.
openapi-gen: web-install
	bash scripts/openapi-generate.sh

# Fail if the committed OpenAPI artifacts drift from the spec under openapi/.
openapi-gen-check: openapi-gen
	@git diff --exit-code -- internal/platform/httpapi/apispec/openapi.gen.json internal/platform/httpapi/apispec/models.gen.go \
		|| { echo "openapi artifacts are stale; run 'make openapi-gen' and commit the result" >&2; exit 1; }

scripts-check:
	bash -n scripts/*.sh
	PYTHONPYCACHEPREFIX="$${TMPDIR:-/tmp}/nexa-panel-pycache" $(PYTHON) -m py_compile scripts/*.py
	@if command -v shellcheck >/dev/null 2>&1; then shellcheck scripts/*.sh; \
	elif [ "$${SHELLCHECK_REQUIRED:-0}" = 1 ]; then echo "shellcheck is required for CI/release validation" >&2; exit 1; \
	else echo "shellcheck not installed; syntax check only"; fi

# The cheap Go gates fail fast first, then the frontend runs: web-build has to
# produce internal/platform/webui/dist before any -tags embed target, because
# the embedded assets are gitignored and //go:embed refuses to compile without
# them. The embed-tagged Go gates therefore come last.
check: fmt-check mod-check vet test scripts-check \
	web-test web-typecheck web-build web-deadcode web-audit openapi-lint \
	go-staticcheck go-deadcode go-vulncheck test-embed

# CI also compiles the production-only embedded frontend path after every
# source, contract, and packaging check has passed.
ci: check test-race
	mkdir -p dist
	$(GO) build -tags embed -trimpath -o dist/nexa-ci ./cmd/nexa
