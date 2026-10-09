// Copyright (c) 2025 Reliant Labs

//go:build windows

package rootwatch

import "os"

// identityOf has no device/inode pair to offer on Windows, so the watcher
// records nothing there and stays inert.
func identityOf(os.FileInfo) (Identity, bool) {
	return Identity{}, false
}
