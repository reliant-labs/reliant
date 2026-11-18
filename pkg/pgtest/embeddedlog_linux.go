//go:build linux

package pgtest

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ownOpenPaths lists the files this process has a descriptor on, from
// /proc/self/fd. A descriptor whose file was unlinked reads back with a
// " (deleted)" suffix and is skipped: it can no longer be claimed or removed.
func ownOpenPaths() ([]string, error) {
	const fdDir = "/proc/self/fd"
	entries, err := os.ReadDir(fdDir)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, e := range entries {
		target, err := os.Readlink(filepath.Join(fdDir, e.Name()))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue // the descriptor ReadDir itself held, or one closed since
			}
			return nil, err
		}
		if strings.HasSuffix(target, " (deleted)") || !filepath.IsAbs(target) {
			continue
		}
		paths = append(paths, target)
	}
	return paths, nil
}
