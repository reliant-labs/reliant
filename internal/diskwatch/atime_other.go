// Copyright (c) 2025 Reliant Labs
//go:build !linux && !darwin

package diskwatch

import (
	"io/fs"
	"time"
)

// accessTime is unknown here; last use falls back to mtime.
func accessTime(fs.FileInfo) (time.Time, bool) { return time.Time{}, false }
