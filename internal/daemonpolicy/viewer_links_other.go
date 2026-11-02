// Copyright (c) 2025 Reliant Labs

//go:build !unix

package daemonpolicy

import "os"

// mayHaveOtherNames reports whether a file may have more than one hard link.
// The link count is not in os.FileInfo here, so every file may.
func mayHaveOtherNames(os.FileInfo) bool { return true }
