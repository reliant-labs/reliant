// Copyright (c) 2026 Reliant Labs
//go:build linux

package shell

import (
	"os"

	"golang.org/x/sys/unix"

	"github.com/reliant-labs/reliant/internal/logging"
)

// punchAlign is the granularity a hole is punched at: a file system block.
const punchAlign = 4096

// punchRead releases [from, upto) of an output file from disk, rounded down
// to a block, and returns the new released mark. The child appends with
// O_APPEND, so offsets go on growing; the file stays as long as it was, but
// what was read occupies no disk. A dev server logging for days would
// otherwise fill the disk with output the journal already holds the end of.
// A file system that cannot punch holes keeps the file whole.
func punchRead(f *os.File, from, upto int64) int64 {
	upto &^= punchAlign - 1
	if upto <= from {
		return from
	}
	err := unix.Fallocate(int(f.Fd()), unix.FALLOC_FL_PUNCH_HOLE|unix.FALLOC_FL_KEEP_SIZE, from, upto-from)
	if err != nil {
		logging.Debug("Background process output file: cannot release what was read", "path", f.Name(), "error", err)
		// Not retried for this stretch: mark it done.
	}
	return upto
}
