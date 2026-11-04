// Copyright (c) 2025 Reliant Labs

//go:build unix

package daemonpolicy

import (
	"os"
	"syscall"
)

// mayHaveOtherNames reports whether a file has more than one hard link.
func mayHaveOtherNames(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return !ok || st.Nlink > 1
}
