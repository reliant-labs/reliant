//go:build windows

package gocacheprog

import "os"

// checkOwner has no mode bits or uids to read on Windows.
func checkOwner(string, os.FileInfo) error { return nil }
