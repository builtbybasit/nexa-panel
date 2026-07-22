.DEFAULT_GOAL := build

GO ?= go
BUN ?= bun
STATICCHECK_VERSION ?= v0.7.0
DEADCODE_VERSION ?= v0.48.0
GOVULNCHECK_VERSION ?= v1.6.0

.PHONY: build release release-linux test test-race test-embed fmt fmt-check mod-check vet \
	go-staticcheck go-deadcode go-vulncheck \
	web-install web-dev web-test web-typecheck web-build web-deadcode web-audit openapi-lint \
	scripts-check check ci

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

web-build: web-install
	cd web && $(BUN) run build

web-deadcode: web-install
	cd web && $(BUN) run deadcode

web-audit: web-install
	cd web && $(BUN) audit

openapi-lint: web-install
	cd web && $(BUN) run openapi:lint

scripts-check:
	bash -n scripts/*.sh
	@if command -v shellcheck >/dev/null 2>&1; then shellcheck scripts/*.sh; else echo "shellcheck not installed; syntax check only"; fi

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
