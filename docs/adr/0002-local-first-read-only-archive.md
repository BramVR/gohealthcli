---
status: "accepted"
summary: "Begin as a local-first read-only SQLite archive with raw JSON preservation."
read_when:
  - "Adding write/delete behavior."
  - "Changing archive storage or privacy model."
---
# Local-First Read-Only Archive

`gohealthcli` starts as a local-first read-only archive with SQLite storage, raw JSON preservation, and scriptable terminal output. This mirrors the proven `gobankcli` operating model and avoids the higher safety burden of writing or deleting sensitive health records while the Google Health API is still settling.
