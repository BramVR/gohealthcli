.PHONY: build build-info fmt fmt-check lint docs-site docs-site-clean docs-commands docs-check docs-export-datasets

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

# fmt rewrites the tree to canonical gofmt form; fmt-check is the
# read-only guard the ci workflow runs on every push/PR so formatting
# drift cannot accumulate on main again.
fmt:
	@gofmt -w .

fmt-check:
	@out="$$(gofmt -l .)" || { echo "gofmt failed"; exit 1; }; \
	if [ -n "$$out" ]; then \
		echo "gofmt drift in:"; echo "$$out"; \
		echo "run 'make fmt' to fix"; exit 1; \
	fi

# lint runs golangci-lint with the checked-in .golangci.yml (issue #279).
# CI pins the same version via golangci/golangci-lint-action in
# .github/workflows/ci.yml; keep GOLANGCI_LINT_VERSION in sync with that
# workflow. Install the pinned version locally with:
#   go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2
# The version probe below only warns (no hard fail) so a newer local
# build stays usable; CI remains the authoritative pinned run.
GOLANGCI_LINT_VERSION := v2.12.2
lint:
	@local_version="v$$(golangci-lint version --short 2>/dev/null || echo unknown)"; \
	if [ "$$local_version" != "$(GOLANGCI_LINT_VERSION)" ]; then \
		echo "warning: local golangci-lint $$local_version != pinned $(GOLANGCI_LINT_VERSION)"; \
	fi; \
	golangci-lint run

docs-site:
	@node scripts/build-docs-site.mjs

docs-site-clean:
	@rm -rf dist/docs-site

docs-commands:
	@go run ./cmd/gohealthcli schema --json | node scripts/gen-command-reference.mjs

# docs-check is the ADR-0008 drift guard (issue #74): regenerate the
# command-reference pages into a temp directory and byte-compare them
# against the committed docs/commands.md + docs/commands/*.md. Exits
# non-zero with a unified diff naming the drifted file; the fix is
# `make docs-commands` plus committing the result. CI runs this after
# the build so flag changes cannot land without a docs regen.
docs-check:
	@go run ./cmd/gohealthcli schema --json | node scripts/check-command-reference.mjs

# docs-export-datasets rewrites README.md's "Normalized export
# datasets" bullet block from exportDatasetCatalogSingleton.Names().
# PRD #144 slice 4 (issue #165). The drift guard in
# cmd/gohealthcli/docs_export_datasets_test.go runs the same splice
# in-test and fails CI when the committed README does not match a
# fresh regeneration, so re-running this target is the canonical fix
# for that test failure.
docs-export-datasets:
	@go run ./cmd/gohealthcli docs-export-datasets --readme README.md
