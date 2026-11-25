//go:build !linux

package cli

import "io/fs"

// statChangeTime reads no inode change time off Linux; 0 leaves the
// executable-identity cache keyed on size and modification time alone.
func statChangeTime(fs.FileInfo) int64 { return 0 }
