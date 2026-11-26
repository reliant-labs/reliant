// Copyright (c) 2025 Reliant Labs
//go:build windows

package worktreereclaim

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// openSourceCopy opens the regular file at rel in the source checkout,
// refusing a symlink or junction at every component below the checkout's root,
// and checks the open is the file it stat'ed. Windows has no openat, so the
// components are checked by path; the retirement inventory, which deletes, is
// Linux-only and opens through the unix variant.
func openSourceCopy(source, rel string) (*os.File, error) {
	parts := splitSegments(rel)
	if len(parts) == 0 {
		return nil, errors.New("empty source path")
	}
	p := source
	var last os.FileInfo
	for _, name := range parts {
		if name == "." || name == ".." {
			return nil, fmt.Errorf("source path component %q", name)
		}
		p = filepath.Join(p, name)
		fi, err := os.Lstat(p)
		if err != nil {
			return nil, err
		}
		if fi.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			return nil, errors.New("source path crosses a link")
		}
		last = fi
	}
	if !last.Mode().IsRegular() {
		return nil, errors.New("source copy is not a regular file")
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	if opened, err := f.Stat(); err != nil || !os.SameFile(opened, last) {
		f.Close()
		return nil, errors.New("source copy replaced while opening")
	}
	return f, nil
}
