---
status: "accepted"
summary: "Build raw request plans from one Provider-owned description shared with execution."
read_when:
  - "Changing raw request construction, planning, or output."
  - "Changing Provider request headers or URL sanitization."
---
# Build raw plans from Provider request descriptions

## Problem

`raw --plan` must report the exact request that a normal raw read would build while performing no Provider, credential, or Health Archive access. The existing Provider module already owns target aliases, the identity endpoint catalog, the Data Type catalog, range shapes, range resolution, and URL construction. Rebuilding any of those rules in the command would make planning drift from execution.

## Usage

Users plan either identity or Data Type reads through the existing command:

```bash
gohealthcli raw endpoint getIdentity --plan --json
gohealthcli raw data-type heart-rate --from yesterday --to today --plan --plain
```

The command layer makes one Provider call to describe the request:

```go
description, err := googlehealth.DescribeRawRequest(options)
result := newRawPlanResult(description)
```

Normal reads keep their existing entry point and byte-for-byte stdout behavior:

```go
request, err := googlehealth.BuildRawRequest(options)
body, err := runtime.fetchRawProvider(ctx, request, accessToken)
```

## Shape

The Provider package owns this description:

```go
type RawRequestDescription struct {
    Request          RawRequest
    Range            *ResolvedRange
    PageSize         int64
    PageTokenProvided bool
    Headers          map[string]string
}

func DescribeRawRequest(RawRequestOptions) (RawRequestDescription, error)
func BuildRawRequest(RawRequestOptions) (RawRequest, error)
func SanitizeRawRequestURL(RawRequest) (string, error)
```

`DescribeRawRequest` parses and validates the target, resolves the range once, calls the production request builder, derives the non-secret headers used by execution, and sanitizes paging material for display. `BuildRawRequest` returns the description's request, so a normal read and its plan cannot select different methods or URLs. The command owns only flag-mode rules, optional non-secret timezone config lookup, plan result types, effect constants, and output formatting.

This interface hides the Provider catalog, filter grammar, URL query layout, header policy, and sanitization behind one call. Callers still supply user inputs and the captured clock because those are command facts. Planning never accepts a base URL, token, Connection, archive handle, or cursor.

## Synthesis decision

The Provider-owned description is the base. It keeps the existing production builder as the single execution path and adds the resolved facts that a plan needs without parsing the finished URL in the command. The separate command-owned result type keeps stable CLI JSON fields out of the Provider transport types.

## Tradeoffs accepted

- We accept a second Provider entry point in exchange for keeping `BuildRawRequest` compatible and making it delegate to the same description.
- We accept a raw-specific plan result instead of reusing the sync plan result because raw paging inputs and its all-false effects differ from predicted Sync Run effects.
- We report only whether a page token was supplied and redact its URL value in exchange for keeping plans secret-free.

## Alternatives considered

Command-owned projection would call `BuildRawRequest`, parse its URL, and rerun range and header rules. It exposes Provider query layout and timing rules to the command, so it is a shallow interface and loses on drift risk.

Expanding `RawRequest` with resolved range, paging, sanitized URL, and plan effects would give execution callers metadata they do not use. It leaks CLI reporting into the Provider transport and makes unrelated ingestion builders populate raw-command fields.

## Open questions and risks

No owner decision remains. The main implementation risk is accidentally reading config for identity plans or Data Type plans with an explicit timezone. Forbidden-adapter and missing-path tests must pin those paths before implementation.

## Next implementation step

Add red tests for identity and Data Type plans that bind every external adapter to fail, then implement the Provider description and the smallest JSON plan path.
