//go:build unix

package storage

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"golang.org/x/sys/unix"
)

// treeDir is a directory a tree removal holds open. Every operation on its
// entries is relative to that descriptor (fstatat, openat, unlinkat), never
// to a path the kernel resolves again, so an entry swapped for a symlink
// after it was examined cannot redirect the removal: openat with O_NOFOLLOW
// refuses it, and unlinkat removes the link itself, never its target.
type treeDir struct {
	f  *os.File
	fd int
	// path names the directory in errors and for the test seam; it is never
	// resolved.
	path string
}

// treeStat is what a removal learns about an entry without following it.
type treeStat struct {
	dir      bool
	perm     fs.FileMode
	dev, ino uint64
}

// errNotDir means an entry examined as a directory no longer is one: it was
// swapped for a symlink or a file.
var errNotDir = errors.New("no longer a directory")

// errTreeChanged means an entry was replaced by another directory between
// being examined and being opened.
var errTreeChanged = errors.New("replaced while being removed")

// openTreeParent opens the directory holding path, the anchor every later
// operation is relative to, and returns path's name in it. The parent is the
// root the caller chose (a cache, TMPDIR), so it is resolved normally; the
// entry itself is not.
func openTreeParent(path string) (*treeDir, string, error) {
	path = filepath.Clean(path)
	parent, name := filepath.Dir(path), filepath.Base(path)
	if name == "." || name == ".." || name == string(filepath.Separator) {
		return nil, "", &fs.PathError{Op: "remove", Path: path, Err: fs.ErrInvalid}
	}
	fd, err := ignoringEINTR(func() (int, error) {
		return unix.Open(parent, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	})
	if err != nil {
		return nil, "", &fs.PathError{Op: "open", Path: parent, Err: err}
	}
	return &treeDir{f: os.NewFile(uintptr(fd), parent), fd: fd, path: parent}, name, nil
}

func (d *treeDir) close() { _ = d.f.Close() }

func (d *treeDir) join(name string) string { return filepath.Join(d.path, name) }

// lstat examines name without following it.
func (d *treeDir) lstat(name string) (treeStat, error) {
	var st unix.Stat_t
	if err := unix.Fstatat(d.fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return treeStat{}, &fs.PathError{Op: "fstatat", Path: d.join(name), Err: err}
	}
	return treeStatOf(&st), nil
}

func treeStatOf(st *unix.Stat_t) treeStat {
	return treeStat{
		dir:  uint32(st.Mode)&unix.S_IFMT == unix.S_IFDIR, //nolint:unconvert // uint16 on darwin
		perm: fs.FileMode(st.Mode) & fs.ModePerm,
		dev:  uint64(st.Dev), //nolint:unconvert // int32 on darwin
		ino:  uint64(st.Ino), //nolint:unconvert // uint32 on some BSDs
	}
}

// openDir opens the directory entry name that lstat reported as want. A
// symlink or file in its place is errNotDir; another directory in its place
// is errTreeChanged. A directory without owner rwx gets it through its own
// descriptor — a Go module cache writes 0555 directories, whose children
// cannot otherwise be unlinked.
func (d *treeDir) openDir(name string, want treeStat) (*treeDir, error) {
	path := d.join(name)
	fd, err := ignoringEINTR(func() (int, error) {
		return unix.Openat(d.fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	})
	if err != nil {
		// O_NOFOLLOW on a symlink is ELOOP (EMLINK on FreeBSD); O_DIRECTORY
		// on anything else is ENOTDIR.
		if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.EMLINK) || errors.Is(err, unix.ENOTDIR) {
			err = fmt.Errorf("%w: %w", errNotDir, err)
		}
		return nil, &fs.PathError{Op: "openat", Path: path, Err: err}
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		_ = unix.Close(fd)
		return nil, &fs.PathError{Op: "fstat", Path: path, Err: err}
	}
	if got := treeStatOf(&st); got.dev != want.dev || got.ino != want.ino {
		_ = unix.Close(fd)
		return nil, &fs.PathError{Op: "openat", Path: path, Err: errTreeChanged}
	}
	if want.perm&0o700 != 0o700 {
		_ = unix.Fchmod(fd, uint32(want.perm|0o700))
	}
	return &treeDir{f: os.NewFile(uintptr(fd), path), fd: fd, path: path}, nil
}

// names lists the directory's entries, sorted.
func (d *treeDir) names() ([]string, error) {
	names, err := d.f.Readdirnames(-1)
	sort.Strings(names)
	return names, err
}

// remove unlinks name from the directory: the entry itself, whatever it is.
func (d *treeDir) remove(name string, dir bool) error {
	flags := 0
	if dir {
		flags = unix.AT_REMOVEDIR
	}
	if _, err := ignoringEINTR(func() (int, error) { return 0, unix.Unlinkat(d.fd, name, flags) }); err != nil {
		return &fs.PathError{Op: "unlinkat", Path: d.join(name), Err: err}
	}
	return nil
}

func ignoringEINTR(fn func() (int, error)) (int, error) {
	for {
		n, err := fn()
		if !errors.Is(err, unix.EINTR) {
			return n, err
		}
	}
}
