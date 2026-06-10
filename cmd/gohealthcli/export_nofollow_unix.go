//go:build unix

package main

import "syscall"

// exportOpenNoFollow makes os.OpenFile refuse to follow a symbolic link at the
// final path component, so a symlinked --output cannot be truncated through its
// target even if it is swapped in after restrictExistingExportOutput's Lstat
// check (a TOCTOU race). Zero on platforms without O_NOFOLLOW.
const exportOpenNoFollow = syscall.O_NOFOLLOW
