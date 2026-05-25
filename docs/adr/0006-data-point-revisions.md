---
status: "accepted"
summary: "Keep one canonical Data Point row and preserve previous raw versions as Data Point Revisions."
read_when:
  - "Implementing Data Point upsert behavior."
  - "Handling upstream corrections."
  - "Changing raw record audit semantics."
---
# Data Point Revisions

When an upstream correction changes a Data Point, `gohealthcli` keeps one canonical Data Point row for current queries and preserves the previous raw JSON as a Data Point Revision. This avoids duplicate current query rows while keeping an audit trail for provider corrections and local migration/debugging work.
