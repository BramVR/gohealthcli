.PHONY: build build-info docs-site docs-site-clean docs-commands

# build embeds the three Version-module identifiers (version/commit/built)
# at link time via -ldflags. Defaults are "dev" so an unstamped `go build`
# still produces a usable (if unstamped) binary; this target is the
# canonical production path. PRD #143 slice 5 (issue #174).
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse HEAD 2>/dev/null || echo dev)
BUILT   ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.built=$(BUILT)

build:
	@mkdir -p dist
	@go build -ldflags "$(LDFLAGS)" -o dist/gohealthcli ./cmd/gohealthcli

# build-info is a developer smoke target: print the values the next `make
# build` would embed. Use to debug stamping issues without round-tripping
# through the binary.
build-info:
	@echo "VERSION=$(VERSION)"
	@echo "COMMIT=$(COMMIT)"
	@echo "BUILT=$(BUILT)"

docs-site:
	@node scripts/build-docs-site.mjs

docs-site-clean:
	@rm -rf dist/docs-site

docs-commands:
	@go run ./cmd/gohealthcli schema --json | node scripts/gen-command-reference.mjs
