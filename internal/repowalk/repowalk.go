// Package repowalk walks this repository's tree for the tests that scan it
// (the guard tests: goexec's toolchain guards, trimpathguard, vacuousguard),
// in a way Go's test cache can replay.
//
// THE PROBLEM. `go test` keys a cached result on every file and directory the
// test opened or stat'ed inside the module. For a directory it LISTED, the key
// is the directory's own stat plus the stat of every entry in it, so a test
// that walks the repository root with filepath.WalkDir keys on the mtime of
// every top-level entry — .git, .forge, tmp, bin included. Those change all
// the time without any tracked file changing: git rewrites .git/index on a
// status refresh, `forge ci verify-generated` and `forge generate` record
// their cache in .forge/, a build writes bin/. Every such write made the
// scanning packages re-run on an unchanged tree, so a second `task ci` on the
// same commit re-paid them, and a CI runner (whose fresh .git has the
// checkout's mtime) never replayed them at all.
//
// WHAT THIS DOES INSTEAD. WalkDir lists the root WITHOUT the read reaching the
// test log, and keys on the root's own stat instead (os.Stat, which is
// logged). A directory's mtime changes exactly when an entry is created,
// removed or renamed in it, so a new or deleted top-level entry still
// invalidates the result. Each top-level entry the callback descends into is
// walked with the ordinary, logged calls, so a change anywhere below it does
// too; an entry the callback skips (fs.SkipDir) is never read, and so never
// keys the result. What the callback learns of a top-level entry without
// opening it is its name and type, which only a rename can change.
//
// The callback must therefore READ (open or stat) whatever it decides on,
// beyond a top-level entry's name and type. DirEntry.Info does stat, so it is
// part of the key.
//
// On Windows the root listing cannot be read outside package os, and WalkDir
// is plain filepath.WalkDir: correct, keyed on more than it needs.
package repowalk

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// WalkDir is filepath.WalkDir(root, fn) with root's listing kept out of the
// test cache's key (see the package doc). The callback sees the same calls in
// the same lexical order: root, then each top-level entry, then its subtree.
func WalkDir(root string, fn fs.WalkDirFunc) error {
	info, err := os.Lstat(root)
	if err != nil {
		err = fn(root, nil, err)
		if errors.Is(err, filepath.SkipDir) || errors.Is(err, filepath.SkipAll) {
			return nil
		}
		return err
	}
	if err := fn(root, fs.FileInfoToDirEntry(info), nil); err != nil {
		if errors.Is(err, filepath.SkipDir) || errors.Is(err, filepath.SkipAll) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return nil
	}
	entries, err := readDirNoLog(root)
	if err != nil {
		// As WalkDir does for an unreadable directory: a second call with
		// the error, which the callback may turn into SkipDir.
		if err = fn(root, fs.FileInfoToDirEntry(info), err); err != nil &&
			!errors.Is(err, filepath.SkipDir) && !errors.Is(err, filepath.SkipAll) {
			return err
		}
		return nil
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, d := range entries {
		top := filepath.Join(root, d.Name())
		err := fn(top, d, nil)
		switch {
		case errors.Is(err, filepath.SkipAll):
			return nil
		case errors.Is(err, filepath.SkipDir):
			if !d.IsDir() {
				return nil // SkipDir on a file skips the rest of its directory
			}
			continue
		case err != nil:
			return err
		}
		if !d.IsDir() {
			continue
		}
		// filepath.WalkDir swallows SkipAll (it returns nil), so the descent
		// says whether the callback asked to stop the whole walk.
		stopped := false
		err = filepath.WalkDir(top, func(p string, d fs.DirEntry, err error) error {
			if p == top && err == nil {
				return nil // the callback has seen it: this call is the descent
			}
			err = fn(p, d, err)
			if errors.Is(err, filepath.SkipAll) {
				stopped = true
			}
			return err
		})
		if err != nil {
			return err
		}
		if stopped {
			return nil
		}
	}
	return nil
}
