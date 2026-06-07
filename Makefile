.PHONY: docs-site docs-site-clean docs-commands

docs-site:
	@node scripts/build-docs-site.mjs

docs-site-clean:
	@rm -rf dist/docs-site

docs-commands:
	@go run ./cmd/gohealthcli schema --json | node scripts/gen-command-reference.mjs
