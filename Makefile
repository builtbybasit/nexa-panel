.PHONY: build release release-linux test fmt vet web-install web-dev web-build check

build:
	go build -o bin/nexa ./cmd/nexa

release: web-build
	mkdir -p dist
	go build -tags embed -trimpath -o dist/nexa ./cmd/nexa

release-linux:
	bash scripts/build-linux-release.sh amd64

test:
	go test ./...

fmt:
	gofmt -w $$(find cmd internal -name '*.go' -type f)

vet:
	go vet ./...

web-install:
	cd web && bun install

web-dev:
	cd web && bun run dev

web-build:
	cd web && bun run build

check: test vet web-build
