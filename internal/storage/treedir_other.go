//go:build !unix

package storage

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// treeDir is the path-based fallback of the descriptor-relative tree removal
// in treedir_unix.go, for platforms without openat/unlinkat. It never follows
// a symlink it has examined (os.Lstat, os.Remove), but cannot close the window
// between examining an entry and acting on it.
type treeDir struct{ path string }

type treeStat struct {
	dir  bool
	perm fs.FileMode
}

var errNotDir = errors.New("no longer a directory")

func openTreeParent(path string) (*treeDir, string, error) {
	path = filepath.Clean(path)
	name := filepath.Base(path)
	if name == "." || name == ".." || name == string(filepath.Separator) {
		return nil, "", &fs.PathError{Op: "remove", Path: path, Err: fs.ErrInvalid}
	}
	return &treeDir{path: filepath.Dir(path)}, name, nil
}

func (d *treeDir) close() {}

func (d *treeDir) join(name string) string { return filepath.Join(d.path, name) }

func (d *treeDir) lstat(name string) (treeStat, error) {
	info, err := os.Lstat(d.join(name))
	if err != nil {
		return treeStat{}, err
	}
	return treeStat{dir: info.IsDir(), perm: info.Mode().Perm()}, nil
}

func (d *treeDir) openDir(name string, want treeStat) (*treeDir, error) {
	path := d.join(name)
	if want.perm&0o700 != 0o700 {
		_ = os.Chmod(path, want.perm|0o700)
	}
	return &treeDir{path: path}, nil
}

func (d *treeDir) names() ([]string, error) {
	entries, err := os.ReadDir(d.path)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names, err
}

func (d *treeDir) remove(name string, _ bool) error {
	path := d.join(name)
	err := os.Remove(path)
	if err != nil && errors.Is(err, fs.ErrPermission) {
		// A read-only file on Windows.
		_ = os.Chmod(path, 0o700)
		err = os.Remove(path)
	}
	return err
}
