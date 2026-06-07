---
status: "accepted"
summary: "Deepen Provider, Connection, Health Archive, and export modules before expanding Data Types."
read_when:
  - "Refactoring command, Provider, Connection, Health Archive, or export code."
  - "Adding Data Types, Rollups, Provider fixtures, or normalized export datasets."
  - "Replacing package globals with runtime adapters."
---
# Deepen Provider, Connection, Archive, and Export Modules

Before expanding Data Type coverage, deepen the modules that carry Google Health API drift, Connection access, Health Archive lifecycle, normalized export datasets, and runtime adapters.

`Sync Run` stays the archive attempt/coordinator: selected Data Types, requested range, endpoint family used, counts, status, and errors. A Google Health ingestion module owns endpoint family selection, request building, pagination, Data Point/Rollup parsing, Provider error normalization, and Provider fixture tests. `raw` remains endpoint-shaped Provider exploration and does not use Sync Run ingestion behavior.

Connection access owns the shared path from config and current Connection to a usable access token: token metadata validation, scope checks, Credential Store runtime validation, access-token loading, common Provider 401 handling, and identity mismatch wording. `connect` remains separate because it creates or refreshes a Connection after OAuth.

Health Archive lifecycle owns create/open/migrate/inspect/read-only-vs-write setup. Reader, writer, and Connection role adapters remain separate, but they should not repeat lifecycle invariants.

Normalized export dataset definitions own view SQL, field order, sort order, and value kinds. Health Archive lifecycle applies those definitions during migrations; export writers only format rows.

Runtime dependencies should become explicit runtime adapters after the module splits make real seams visible. Use production and test adapters where both exist; do not add hypothetical seams for their own sake.
