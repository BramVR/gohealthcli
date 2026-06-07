.PHONY: docs-site docs-site-clean

docs-site:
	@node scripts/build-docs-site.mjs

docs-site-clean:
	@rm -rf dist/docs-site
