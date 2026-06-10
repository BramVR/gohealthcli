//go:build !unix

package main

// exportOpenNoFollow is zero on platforms without O_NOFOLLOW (e.g. Windows),
// where the os.Lstat pre-check in restrictExistingExportOutput is the symlink
// guard. See export_nofollow_unix.go.
const exportOpenNoFollow = 0
