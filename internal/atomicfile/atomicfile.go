// Package atomicfile is the one way forge writes a file it generates or
// scaffolds.
//
// os.WriteFile truncates the target and then writes it. A process that dies
// between the two — a sandbox restart in the middle of `forge generate` — leaves
// the file empty or partial, and a generated file that is empty is not "stale":
// it is a compile error in every package that imports it. WriteFile instead
// writes a sibling temp file, fsyncs it, and renames it over the target, so the
// target is always either the complete old bytes or the complete new bytes.
//
// internal/writeguard fails the build on a direct os.WriteFile under internal/,
// so a new generator cannot reintroduce the truncating write.
package atomicfile

import (
	"errors"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// tempPrefix names the sibling temp file. It is dot-prefixed so an interrupted
// write never shows up as a tracked source file.
const tempPrefix = ".forge-tmp-"

// Seams for the tests: the rename and the fsync are the two steps a crash
// consequence depends on.
var (
	renameFile = replaceFile
	syncFile   = func(f *os.File) error { return f.Sync() }
)

// scratchRoots are directories whose contents are thrown away by the process
// that made them, so a crash there loses nothing and fsync buys nothing.
var (
	scratchMu    sync.RWMutex
	scratchRoots = map[string]int{}
)

// MarkScratch declares dir a throwaway tree: WriteFile still writes
// atomically under it (readers never see a partial file) but skips the fsync,
// which on a busy disk dominates a render of hundreds of files. The returned
// function ends the declaration. Never mark a directory whose contents outlive
// the process.
func MarkScratch(dir string) (unmark func()) {
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	scratchMu.Lock()
	scratchRoots[dir]++
	scratchMu.Unlock()
	return func() {
		scratchMu.Lock()
		defer scratchMu.Unlock()
		if scratchRoots[dir]--; scratchRoots[dir] <= 0 {
			delete(scratchRoots, dir)
		}
	}
}

func isScratch(path string) bool {
	scratchMu.RLock()
	defer scratchMu.RUnlock()
	if len(scratchRoots) == 0 {
		return false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	for root := range scratchRoots {
		if abs == root || strings.HasPrefix(abs, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// WriteFile writes data to path atomically. It is a drop-in for os.WriteFile:
// a new file gets perm (subject to the umask); an existing regular file keeps
// its current permission bits and perm is ignored; a symlink is written
// through to its target. A path that exists but is not a regular file (a
// device, a FIFO) cannot be replaced by rename and is written in place.
//
// On any error the target is untouched and the temp file is removed.
func WriteFile(path string, data []byte, perm fs.FileMode) error {
	target, fi, err := resolveTarget(path)
	if err != nil {
		return err
	}
	existing := fi != nil
	if existing {
		if !fi.Mode().IsRegular() {
			return os.WriteFile(target, data, perm)
		}
		perm = fi.Mode().Perm()
	}

	tmp, err := createTemp(filepath.Dir(target), perm)
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	// A new file takes perm through the umask, exactly as os.WriteFile does.
	// An existing file's mode is copied verbatim, so the umask must not narrow it.
	if existing {
		if err := tmp.Chmod(perm); err != nil && !errors.Is(err, errors.ErrUnsupported) {
			_ = tmp.Close()
			return err
		}
	}
	if isScratch(target) {
		// no fsync: see MarkScratch
	} else if err := syncFile(tmp); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := renameFile(tmpName, target); err != nil {
		return err
	}
	committed = true
	return nil
}

// Replace renames oldPath over newPath, for a file the caller finished
// writing some other way (a `go install` into a staging dir). It is the
// rename WriteFile commits with: on Windows, where a replace is refused while
// any handle — a reader, a virus scanner — has newPath open, it retries for a
// moment, and a refusal that outlasts that leaves newPath's old content.
func Replace(oldPath, newPath string) error { return replaceFile(oldPath, newPath) }

// resolveTarget is the file a write to path lands in, with its current info.
// A symlink resolves to its target, so the link survives the rename. A nil
// FileInfo means nothing is there yet (or a dangling link); any other Lstat
// failure is returned.
func resolveTarget(path string) (string, fs.FileInfo, error) {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return path, nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	if fi.Mode()&fs.ModeSymlink == 0 {
		return path, fi, nil
	}
	target := path
	if resolved, rerr := filepath.EvalSymlinks(path); rerr == nil {
		target = resolved
	}
	if fi, err = os.Stat(target); err != nil {
		return target, nil, nil
	}
	return target, fi, nil
}

func createTemp(dir string, perm fs.FileMode) (*os.File, error) {
	for range 100 {
		name := filepath.Join(dir, tempPrefix+strconv.FormatUint(rand.Uint64(), 36))
		f, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, perm)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		return f, err
	}
	return nil, &fs.PathError{Op: "createtemp", Path: filepath.Join(dir, tempPrefix+"*"), Err: fs.ErrExist}
}
