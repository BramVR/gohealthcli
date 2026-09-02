---
status: "accepted"
summary: "Build one-page raw Rollup reads with the same Provider request and range-window code as sync."
read_when:
  - "Changing raw Rollup reads, request bodies, paging, or range limits."
  - "Changing Rollup request builders or Provider range-window policy."
---
# Reuse Provider Rollup builders for one raw request

## Problem

Raw Rollup exploration must produce the same first request as sync without importing Sync Run parsing, pagination, chunking, or archive writes. Rollup requests are POSTs, so a plan must also describe a sanitized request body. Provider range limits complicate the boundary: sync may split a range into several requests, while raw must reject that range and issue at most one request.

## Usage

Users select the endpoint family explicitly:

```bash
gohealthcli raw data-type steps daily-rollup --from 2026-01-01 --to 2026-01-10 --plan --json
gohealthcli raw data-type total-calories rollup --from 2026-01-01T00:00:00Z --to 2026-01-10T00:00:00Z --window 1h --output ./calories.json
```

The command layer still uses the established raw description and execution path:

```go
description, err := googlehealth.DescribeRawRequest(options)
request, err := googlehealth.BuildRawRequest(options)
```

Sync continues to ask ingestion for its first request:

```go
description, err := ingestion.DescribePlan(request)
```

Both paths reach the same Provider-owned Rollup builders and range-window functions.

## Shape

`RawRequestOptions` carries the explicit operation and physical `Window` input. The target parser checks the canonical Data Type catalog for `dailyRollUp` or `rollUp` support. Physical windows must match one of that catalog row's supported granularities.

`DescribeRawRequest` resolves the range with the shared resolver, asks the same range-window function used by sync to compute Provider requests, and requires exactly one result. It then calls the existing daily or physical Rollup builder. Normal raw execution sends that returned request once. It does not call ingestion execution or parse the response.

`RawRequestDescription` carries the sanitized POST body alongside the sanitized URL. Sanitization redacts a supplied page token while retaining the requested range, window, and page size. The CLI plan owns the stable JSON, plain, and human result shapes.

This keeps the interface small. Callers supply command inputs and receive one complete request description. The Provider package hides catalog lookup, duration checks, range limits, range and body encoding, scopes, paging placement, and token redaction.

## Synthesis decision

Extending the Provider-owned raw description is the base because it preserves the existing raw execution and plan seams. The useful part of the ingestion alternative, its range-window policy, stays shared through the existing low-level functions. Ingestion itself remains out of raw.

## Tradeoffs accepted

- We accept additive Rollup fields on `RawRequestOptions` in exchange for one request-description API across all raw operations.
- We accept a sanitized body copy in the description in exchange for complete secret-free POST plans.
- We reject ranges that sync would split in exchange for raw's one-request contract.
- We restrict raw physical windows to catalog granularities in exchange for catalog-backed completion and local validation.

## Alternatives considered

Routing raw through `Ingestion.DescribePlan` would reuse the first request but expose Sync Run window counts, attachment policy, and later ingestion behavior to a command that must never enter that lifecycle. Callers would still need a second API for explicit raw paging.

A second raw-only Rollup builder would keep the raw code path isolated but duplicate request bodies, range limits, scopes, and catalog checks. It creates the drift this issue is meant to prevent.

## Open questions and risks

No owner decision remains. Tests must pin POST body redaction, raw and sync first-request equality, one-request range refusal, and the absence of Health Archive effects.

## Next implementation step

Add CLI contract tests for exact bytes, zero-effect planning, safe output, local failures, completion, and compatibility with existing raw operations.
