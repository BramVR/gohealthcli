---
status: "accepted"
summary: "Write raw Provider responses through one exclusive, private file operation."
read_when:
  - "Changing raw file output, destination replacement, or output cleanup."
  - "Changing local output permissions or final-component symlink handling."
---
# Write raw responses through exclusive private files

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

Its production implementation owns destination inspection, exclusive creation,
final-component no-follow behavior, owner-only POSIX mode, the exact-byte
write, close handling, and cleanup. The command never receives a file handle or
coordinates those stages.

The destination is opened with create-new semantics. Existing regular files,
directories, symbolic links, and other destination types are rejected. The
same final-component no-follow flag used by export closes the POSIX check and
open race. A failed write, short write, permission change, or close removes the
file created by this invocation before returning the error. Errors name the
operation and path, never the payload.

On POSIX, creation requests mode `0600` and descriptor-based chmod confirms it.
On Windows, create-new semantics prevent overwrite and reject a pre-existing
reparse destination. The file inherits the parent directory ACL because the
portable output helper cannot promise a Unix mode or replace Windows ACL
inheritance. Users who need an owner-only Windows ACL must choose a directory
whose ACL already grants access only to the intended identities. The docs state
this limit instead of claiming that `0600` changes a Windows ACL.

The interface is deep enough for this command. One call hides every file safety
decision, while the caller retains only the user-visible path and byte count.
It deliberately does not accept overwrite, append, format, or permission
options.

## Synthesis decision

The selected design writes the final path through exclusive creation and cleans
it up on any file failure. It keeps the existing export path inspection and
no-follow rules. The runtime adapter supplies the deterministic failure seam
needed for command tests without exposing filesystem stages to the command.

The rejected staged-file design contributed one rule: the destination must
never survive a reported file-write failure. Its temporary-file installation
shape was not retained because portable atomic no-replace rename semantics and
temporary-file cleanup would add separate platform machinery for a response
that is already fully buffered in memory.

## Tradeoffs accepted

- We accept that the final path exists while its single write is in progress in
  exchange for portable exclusive creation and bounded cleanup.
- We accept inherited Windows ACLs in exchange for staying within the existing
  cross-platform output model and making its limit explicit.
- We keep a complete destination when only the later stderr status write fails.
  The Provider bytes reached their requested file, so deleting them would turn
  a terminal problem into data loss.

## Alternatives considered

A same-directory staging file followed by a no-replace install keeps the final
name absent until all bytes are closed. It loses because portable no-replace
installation needs different primitives on POSIX and Windows, and a failed
temporary-file cleanup can leave a second sensitive copy. That complexity would
sit behind a small interface, but it is larger than the current failure
contract requires.

Opening the final path directly in `raw.go` has fewer lines. It loses because
the command would coordinate path checks, file permissions, writes, close, and
cleanup. Callers would need to understand the security rules, making the module
shallow and repeating export's policy.

## Open questions and risks

No owner decision remains. Native Windows CI must keep proving create-new,
invalid-parent, cleanup, and reparse behavior. Windows ACL inheritance remains
a documented platform limit.

## Next implementation step

Add red command and file-operation tests, then fill the runtime operation
without changing Provider request or Health Archive paths.
