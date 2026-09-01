//go:build !unix

package main

// outputOpenNoFollow is zero on platforms without O_NOFOLLOW (e.g. Windows),
// where the os.Lstat pre-check in validateOutputPathNoFollow is the symlink
// guard. See export_nofollow_unix.go.
const outputOpenNoFollow = 0
