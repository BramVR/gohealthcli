---
status: "accepted"
summary: "Use one root CONTEXT.md until the project has multiple bounded contexts."
read_when:
  - "Adding CONTEXT-MAP.md or splitting domain docs."
  - "Running grill-with-docs against the project."
---
# Single Root Context

Use one root `CONTEXT.md` for now because the project has one cohesive domain: archiving personal health data from upstream providers. A `CONTEXT-MAP.md` would add structure before there are multiple bounded contexts to separate, so it should wait until the CLI grows distinct provider, archive, or analytics domains that need their own language.
