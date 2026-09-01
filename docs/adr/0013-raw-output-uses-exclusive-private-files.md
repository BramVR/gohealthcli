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

Before the Provider read, the command asks the runtime to prepare the output:

```go
output, err := runtime.prepareRawOutput(outputPath)
```

Preparation validates the destination, pins its resolved parent, rejects an
actual Windows network parent, and creates the private staging file. A Provider
failure aborts that capability. A successful read completes it with the exact
body:

```go
byteCount, err := output.Complete(body)
```

## Shape

The runtime adapter exposes one preparation operation:

```go
prepareRawOutput func(path string) (preparedRawOutput, error)
```

The returned capability exposes only `Complete(payload)` and `Abort(cause)`.
Its production implementation owns destination inspection, a private sibling
staging file, owner-only POSIX mode, the exact-byte write, close handling,
no-replace publication, and cleanup. The command never receives an OS file or
coordinates those stages.

Existing regular files, directories, symbolic links, and other destination
types are rejected before the Provider read. After a successful read, the
prepared capability already owns a random staging file relative to the pinned
parent handle. On Linux and macOS, that file is mode `0600`. Completion writes,
confirms mode `0600`, and closes the staging file before the final path exists.

The parent handle requests search and child-creation rights, not directory
listing rights. A write-and-search-only drop-box directory therefore remains a
valid destination on Linux and macOS when the effective user owns it and group
and other users cannot write there. This parent requirement prevents another identity from replacing the
named Unix staging entry before publication while allowing ordinary `0755`
directories. macOS also reads extended security from the pinned directory
descriptor and rejects every extended ACL before creating the staging file, so
ACL inheritance cannot grant access outside the mode bits. Windows likewise
omits list-directory access while retaining the rights needed for child
creation and rename; its handle-relative publication does not rename a
replaceable staging pathname.

Publication uses an atomic no-replace rename relative to the pinned parent:
`renameat2` with `RENAME_NOREPLACE` on Linux, `renameatx_np` with `RENAME_EXCL`
on macOS, and handle-relative `FileRenameInformation` on local Windows volumes.
A destination created after preflight wins; the staged response is removed and
the concurrent file stays untouched. A failed write, short write, permission
change, close, or publish never creates the final destination. Errors name the
operation and path, never the payload.

Cleanup is relative to the same pinned parent on Linux and macOS. Windows keeps
a duplicate handle to the staging file and marks that handle for deletion.
Renaming or replacing an ancestor path after the staging file opens therefore
cannot redirect publication or cleanup to another file.

Other build targets fail before publication because this contract needs an
atomic no-replace rename. A link-then-unlink fallback is not equivalent: an
unlink failure would report failure after the destination already existed.

The Windows staging file is a direct child of the resolved requested parent, so
it inherits the same parent ACL that the final path would receive. Rename
preserves that descriptor. The portable output helper cannot promise a Unix
mode or replace Windows ACL
inheritance. Users must choose a directory whose ACL already grants access only
to the intended identities. The docs state this limit instead of claiming that
`0600` changes a Windows ACL.

Windows checks the resolved parent path from the pinned directory handle. UNC
paths, mapped network drives, and local-looking reparse paths that resolve to a
network share are rejected before the Provider read. The SMB protocol requires
a zero rename root handle, which cannot preserve the pinned-parent publication
contract if an ancestor path changes.

Windows also rejects final names whose Win32 and native interpretations can
differ: trailing dots or spaces, reserved device stems, alternate streams,
control characters, and Win32-invalid punctuation. Preflight and native
publication therefore address the same leaf.

The interface is deep enough for this command. One opaque capability hides every
file safety decision, while the caller retains only completion, abort, and the
user-visible byte count. It deliberately does not accept overwrite, append,
format, or permission options.

## Synthesis decision

The initial synthesis selected direct exclusive creation with path cleanup. Two
review cycles found the same structural flaw: a cleanup check and remove cannot
atomically prove that the path still names the file this process opened. A
concurrent replacement could be deleted, while a failed identity inspection
could leave an empty final path. That repeated friction triggered the Scrap
phase.

The revised design promotes same-directory private staging to the base. The
runtime adapter prepares and pins the output before Provider access, and its
opaque capability publishes only a complete closed file through platform
no-replace rename. This removes final-path cleanup and prevents a second
pathname resolution from redirecting Windows output to a network share.

## Tradeoffs accepted

- We accept small platform files for no-replace rename in exchange for one
  consistent final-path contract on Linux, macOS, and Windows.
- We accept a random private sibling file during the write in exchange for
  keeping the requested destination absent until every byte is closed.
- We require effective-user ownership and reject group- or other-writable
  parents on Linux and macOS because those platforms publish the named staging
  entry, not the held file descriptor.
- We accept platform-specific parent handles in exchange for making later
  publication and cleanup independent of ancestor pathname replacement.
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
