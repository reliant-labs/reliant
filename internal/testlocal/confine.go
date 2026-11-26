package testlocal

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// A run directory is not always forge's own writing: CI downloads one as an
// artifact of code nobody has reviewed yet, and reads it in a job that may
// hold credentials. capture.json names the files the lane's results and
// coverage are read from, so every one of those names is confined to the
// lane directory: an absolute path, a `..` that climbs out, or a symbolic
// link that points out of it is refused, never read and never uploaded.
// The reads go through an [os.Root] opened on the lane directory, which
// resolves every component (symbolic links included) inside it, so there is
// no window between a check and the open for a link to be swapped in.

// ErrOutsideLane is the error for a path capture.json names that is not
// inside the lane directory.
var ErrOutsideLane = errors.New("is not inside the lane directory")

// lanePath is name, a path capture.json names, as a path relative to the
// lane directory. A trailing slash (a directory input) is dropped.
func lanePath(name string) (string, error) {
	rel := filepath.FromSlash(strings.TrimSuffix(name, "/"))
	// IsLocal refuses "", absolute paths, volume names and any `..` that
	// leaves the directory; on Windows, reserved names too.
	if !filepath.IsLocal(rel) {
		return "", fmt.Errorf("path %q %w", name, ErrOutsideLane)
	}
	return rel, nil
}

// confinedErr names the path in an error from the lane's [os.Root], and
// marks an escape as [ErrOutsideLane].
func confinedErr(name string, err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) && strings.Contains(pe.Err.Error(), "escapes") {
		return fmt.Errorf("path %q %w (it resolves outside it)", name, ErrOutsideLane)
	}
	return err
}

// openRoot opens the lane directory as an [os.Root].
func (l *Lane) openRoot() (*os.Root, error) {
	return os.OpenRoot(l.Dir)
}

// Stat stats a file capture.json names, inside the lane directory.
func (l *Lane) Stat(name string) (fs.FileInfo, error) {
	rel, err := lanePath(name)
	if err != nil {
		return nil, err
	}
	root, err := l.openRoot()
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	fi, err := root.Stat(rel)
	if err != nil {
		return nil, confinedErr(name, err)
	}
	return fi, nil
}

// Open opens a regular file capture.json names, read through the lane
// directory so that nothing outside it can be reached.
func (l *Lane) Open(name string) (*os.File, error) {
	rel, err := lanePath(name)
	if err != nil {
		return nil, err
	}
	root, err := l.openRoot()
	if err != nil {
		return nil, err
	}
	// A file opened through a Root stays open after the Root is closed.
	defer func() { _ = root.Close() }()
	f, err := root.Open(rel)
	if err != nil {
		return nil, confinedErr(name, err)
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("path %q is not a regular file", name)
	}
	return f, nil
}

// copyCovdata copies the covdata files of the directory capture.json names
// (`covmeta.*` and `covcounters.*`, what [testreport.ConvertCovdata] reads)
// into a new temporary directory, through the lane directory, so that the
// `go tool covdata` it runs reads only files inside the lane. The caller
// removes the directory.
func (l *Lane) copyCovdata(name string) (string, error) {
	rel, err := lanePath(name)
	if err != nil {
		return "", err
	}
	root, err := l.openRoot()
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	sub, err := root.OpenRoot(rel)
	if err != nil {
		return "", confinedErr(name, err)
	}
	defer func() { _ = sub.Close() }()
	d, err := sub.Open(".")
	if err != nil {
		return "", err
	}
	entries, err := d.ReadDir(-1)
	_ = d.Close()
	if err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp("", "forge-lane-covdata-")
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		n := e.Name()
		if !strings.HasPrefix(n, "covmeta.") && !strings.HasPrefix(n, "covcounters.") {
			continue
		}
		// Only regular files: a symbolic link is not covdata forge wrote,
		// wherever it points.
		if !e.Type().IsRegular() {
			continue
		}
		if err := copyFromRoot(sub, n, filepath.Join(tmp, n)); err != nil {
			_ = os.RemoveAll(tmp)
			return "", confinedErr(name+"/"+n, err)
		}
	}
	return tmp, nil
}

func copyFromRoot(root *os.Root, name, dst string) error {
	in, err := root.Open(name)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	if fi, err := in.Stat(); err != nil {
		return err
	} else if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", name)
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// regularNotLink reports whether p is a regular file and not a symbolic
// link. The manifests of a run (run.json, capture.json) must be.
func regularNotLink(p string) (bool, error) {
	fi, err := os.Lstat(p)
	if err != nil {
		return false, err
	}
	return fi.Mode().IsRegular(), nil
}
