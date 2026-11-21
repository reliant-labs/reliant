//go:build !unix

package repowalk

import (
	"io/fs"
	"os"
)

// readDirNoLog falls back to the logged os.ReadDir where a directory cannot
// be opened outside package os (Windows). The walk is still correct; the
// cached result is only keyed on more than it needs to be, which costs a
// re-run, never a stale replay.
func readDirNoLog(dir string) ([]fs.DirEntry, error) {
	return os.ReadDir(dir)
}
