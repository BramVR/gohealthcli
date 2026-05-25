---
status: "accepted"
summary: "Treat the First Release as narrow but foundation-grade instead of a disposable MVP."
read_when:
  - "Scoping First Release behavior."
  - "Considering shortcuts in credential, archive, provider, or command design."
---
# Foundation-Grade First Release

`gohealthcli` should ship a narrow First Release rather than a disposable MVP. The first surface can have limited commands, Data Types, and normalized exports, but credential storage, schema migrations, archive identity, raw JSON preservation, and command contracts should be designed as durable foundations because health data is sensitive and local archives are hard to rebuild casually.
