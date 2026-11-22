package derivecontract

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// The migration writes only inside the package it was pointed at, and that
// package only inside the project. Both are checked on resolved paths: a
// package directory, contract.go or deps.go that is a symlink out of the
// project would otherwise have the rewrite land somewhere nobody asked for.

// resolve is path made absolute and its symlinks evaluated.
func resolve(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// within reports whether path is base or under it; both resolved.
func within(base, path string) bool {
	rel, err := filepath.Rel(base, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// escapes names each file of paths (in the resolved package dir) that
// resolves outside it: an existing file through its symlinks, a dangling
// symlink always, and a new file through its parent directory.
func escapes(dir string, paths []string) []string {
	var out []string
	for _, path := range paths {
		fi, err := os.Lstat(path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			parent, perr := resolve(filepath.Dir(path))
			if perr != nil || !within(dir, parent) {
				out = append(out, fmt.Sprintf("%s would be created in %s, outside the package directory", filepath.Base(path), parent))
			}
			continue
		case err != nil:
			out = append(out, fmt.Sprintf("%s cannot be inspected: %v", filepath.Base(path), err))
			continue
		}
		if fi.Mode()&fs.ModeSymlink == 0 {
			continue
		}
		target, rerr := filepath.EvalSymlinks(path)
		if rerr != nil {
			out = append(out, fmt.Sprintf("%s is a symlink that does not resolve (%v); the migration writes through it, so fix or remove it first", filepath.Base(path), rerr))
			continue
		}
		if !within(dir, target) {
			out = append(out, fmt.Sprintf("%s is a symlink to %s, outside the package directory; the migration rewrites files only inside the package", filepath.Base(path), target))
		}
	}
	return out
}
