---
status: "accepted"
summary: "Build raw reconcile pages with the same Provider request builder and page-size policy as sync."
read_when:
  - "Changing raw reconcile reads or source-family filtering."
  - "Changing Data Point request construction or paging policy."
---
# Reuse the Data Point read builder for raw reconcile

## Problem

`raw data-type <data-type> reconcile` must issue one Provider-shaped page without importing Sync Run behavior. Its method, URL, filter, range shape, scopes, source-family mapping, page size, and page token must still match sync. Duplicating those rules in the command would let raw and sync drift.

## Usage

Users can inspect or fetch one catalog-supported reconcile page:

```bash
gohealthcli raw data-type steps reconcile --source-family wearable --from yesterday --to today --plan --json
gohealthcli raw data-type steps reconcile --source-family wearable --from yesterday --to today --output ./reconcile-page.json
```

The command describes or builds one request through the Provider package:

```go
description, err := googlehealth.DescribeRawRequest(options)
request, err := googlehealth.BuildRawRequest(options)
```

Sync asks its ingestion planner for the first request. Both paths end at the same internal builder:

```go
buildGoogleHealthDataPointReadRawRequest(dataType, from, to, sourceFamily, pageSize, pageToken)
```

## Shape

`RawRequestOptions` carries the explicit `reconcile` operation, source-family keyword, resolved range inputs, and optional paging inputs. The Provider target parser checks the canonical endpoint catalog and derives the reconcile range shape. Raw reconcile defaults to the same catalog-aware Data Point page size as sync; an explicit raw page size and page token pass through the same builder for one-page exploration.

The shared builder owns endpoint selection, filter grammar, source-family resource mapping, URL encoding, scopes, and request metadata. The raw command owns flag provenance, zero-effect planning output, exact-byte stdout, and safe file output. It never calls ingestion execution, parses a response, opens a Sync Run, advances a Sync Cursor, or creates a sidecar.

This interface is deep enough because two public planning paths hide the complete Provider request grammar behind one internal builder. Raw callers provide only user inputs; they do not coordinate Provider construction steps.

## Synthesis decision

Extend the existing Provider-owned raw description and rename the sync-specific builder as the shared Data Point read builder. Keep ingestion execution out of raw. This preserves the established raw planning and output paths while making request equality structural.

## Tradeoffs accepted

- We accept one explicit raw operation token in exchange for keeping implicit list and `get` behavior compatible.
- We accept a raw-only page-size override in exchange for one-page paging exploration; the default remains sync's page size.
- We accept source-family flag provenance in `RawRequestOptions` in exchange for rejecting missing, empty, and misplaced flags before credential access.

## Alternatives considered

Routing raw through `Ingestion.DescribePlan` would reuse the first request, but it also exposes Sync Run concepts such as range-window counts and conditional Attachment work. Raw callers would need to understand sync policy, so the interface is shallower.

A second reconcile builder dedicated to raw would keep command code small but duplicate the Provider filter and URL grammar. It hides little and creates the exact drift this issue forbids.

## Open questions and risks

No owner decision remains. Native Windows CI must keep proving that reconcile uses the existing safe raw output path.

## Next implementation step

Keep request-equality, one-page, planning, safe-output, and no-archive tests at the public Provider and CLI seams.
