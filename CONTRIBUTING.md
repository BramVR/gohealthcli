# Contributing

`gohealthcli` is a local-first Health Archive CLI. Treat changes as
health-data-adjacent even when the patch is documentation-only.

## Work From Issues

Issues and PRDs live in GitHub Issues for `BramVR/gohealthcli`.

- Use [docs/agents/issue-tracker.md](docs/agents/issue-tracker.md) for the issue workflow.
- Use [docs/agents/triage-labels.md](docs/agents/triage-labels.md) for canonical labels.
- Keep issue and PR language aligned with [CONTEXT.md](CONTEXT.md).

Prefer small pull requests that close one ready issue. If a change needs a new
decision, write that decision down before broad implementation work.

## Pull Requests

`main` is maintained through protected-branch pull requests.

- Branch from the latest `main`.
- Keep runtime, dependency, CI, docs, and governance changes separated when practical.
- Fill out the pull request template, including proof and confidentiality review.
- Wait for required CI to pass before merge.
- CODEOWNERS identifies Bram as repository-wide maintainer owner.

## Local Proof

Run the closest relevant gate before opening or updating a pull request.

```bash
make fmt-check
make lint
go test ./...
make docs-check
make docs-site
```

For docs-only or governance-only changes, still run the docs/site checks and
the full CI-equivalent gate when practical.

## Docs And Releases

User-visible behavior changes need matching documentation. Command or flag
changes also need regenerated command-reference docs with `make docs-commands`.

Release execution follows [docs/release.md](docs/release.md). Release proof
must confirm GitHub Release artifacts, Homebrew tap handoff where applicable,
and installed binary behavior.

## Public Artifacts

Before posting a pull request, comment, release note, log, or screenshot, check
that it contains no secrets, tokens, OAuth client JSON, personal Health Archive
data, raw provider payloads, private filesystem paths, or identifying account
details. Mark the PR checklist only after that review passes.
