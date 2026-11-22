package storage

import (
	"io/fs"
	"os"
	"testing"
)

// TestMain sizes Go cache entries by length for this package's tests: their
// budgets and reports are arithmetic over entries of a few hundred bytes,
// which the filesystem rounds up to a block of a size that varies by
// filesystem. TestGoCacheEntriesAreSizedByAllocatedBlocks covers the real
// accounting (entryDiskBytes = allocatedBytes) directly.
func TestMain(m *testing.M) {
	entryDiskBytes = func(info fs.FileInfo) int64 { return info.Size() }
	os.Exit(m.Run())
}
