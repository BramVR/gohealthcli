---
status: "accepted"
summary: "Stage raw Provider responses privately, then publish with no-replace rename."
read_when:
  - "Changing raw file output, destination replacement, or output cleanup."
  - "Changing local output permissions or final-component symlink handling."
---
# Publish raw responses from private staging files

## Problem

`raw` already returns the Provider body as exact bytes. `raw --output PATH` must
put those same bytes in a new local file without parsing them, printing a
preview, following a final-component symbolic link, or replacing anything.
The output contains sensitive health data. POSIX permissions, Windows limits,
short writes, close failures, and terminal status failures all need one stated
contract.

## Usage

The caller names a new path:

```bash
gohealthcli raw endpoint getIdentity --output ./identity-response.json
gohealthcli raw data-type steps --from yesterday --to today --output ./steps-response.json
```

On success, the destination contains the exact Provider bytes. Stdout stays
empty. Stderr reports only the quoted path and byte count. Without `--output`,
the same response bytes still go directly to stdout.

The command layer makes one file call after a successful Provider read:

```go
byteCount, err := runtime.writeRawOutput(outputPath, body)
```

## Shape

The runtime adapter exposes one operation:

```go
writeRawOutput func(path string, payload []byte) (byteCount int, err error)
```

Its production implementation owns destination inspection, a private sibling
staging file, owner-only POSIX mode, the exact-byte write, close handling,
no-replace publication, and cleanup. The command never receives a file handle
or coordinates those stages.

Existing regular files, directories, symbolic links, and other destination
types are rejected before the Provider read. After a successful read, the
writer checks again and creates a random sibling staging file. On Linux and
macOS, that file is mode `0600`. The writer writes, confirms mode `0600`, and
closes the staging file before the final path exists.

Publication uses an atomic no-replace rename supported by each primary
platform: `renameat2` with `RENAME_NOREPLACE` on Linux, `renameatx_np` with
`RENAME_EXCL` on macOS, and `MoveFileEx` without replacement on Windows. A
destination created after preflight wins; the staged response is removed and
the concurrent file stays untouched. A failed write, short write, permission
change, close, or publish never creates the final destination. Errors name the
operation and path, never the payload.

Other build targets fail before publication because this contract needs an
atomic no-replace rename. A link-then-unlink fallback is not equivalent: an
unlink failure would report failure after the destination already existed.

The Windows staging file is a direct child of the requested parent, so it
inherits the same parent ACL that the final path would receive. Rename preserves
that descriptor. The
portable output helper cannot promise a Unix mode or replace Windows ACL
inheritance. Users must choose a directory whose ACL already grants access only
to the intended identities. The docs state this limit instead of claiming that
`0600` changes a Windows ACL.

The interface is deep enough for this command. One call hides every file safety
decision, while the caller retains only the user-visible path and byte count.
It deliberately does not accept overwrite, append, format, or permission
options.

## Synthesis decision

The initial synthesis selected direct exclusive creation with path cleanup. Two
review cycles found the same structural flaw: a cleanup check and remove cannot
atomically prove that the path still names the file this process opened. A
concurrent replacement could be deleted, while a failed identity inspection
could leave an empty final path. That repeated friction triggered the Scrap
phase.

The revised design promotes same-directory private staging to the base. The
runtime adapter remains the public shape, but its private implementation now
publishes only a complete closed file through platform no-replace rename. This
removes final-path cleanup rather than adding another path identity check.

## Tradeoffs accepted

- We accept small platform files for no-replace rename in exchange for one
  consistent final-path contract on Linux, macOS, and Windows.
- We accept a random private sibling file during the write in exchange for
  keeping the requested destination absent until every byte is closed.
- We accept inherited Windows ACLs in exchange for staying within the existing
  cross-platform output model and making its limit explicit.
- We keep a complete destination when only the later stderr status write fails.
  The Provider bytes reached their requested file, so deleting them would turn
  a terminal problem into data loss.

## Alternatives considered

Direct exclusive creation hides fewer implementation steps, but a failed write
must then delete the final path. Portable pathname APIs cannot make the
identity check and removal one atomic operation. This shape was implemented,
reviewed, and scrapped because it could delete a concurrent replacement.

Opening and cleaning the final path directly in `raw.go` is even shallower. It
would make the command coordinate path checks, permissions, writes, close, and
cleanup while retaining the same race.

## Open questions and risks

No owner decision remains. Native Windows CI must keep proving no-replace
publication, invalid-parent, staging cleanup, and reparse behavior. Windows ACL
inheritance remains a documented platform limit.

## Next implementation step

Add red command and file-operation tests, then fill the runtime operation
without changing Provider request or Health Archive paths.
