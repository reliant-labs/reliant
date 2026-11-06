//go:build linux

package worktreereclaim

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// retirementTree pins an inspected checkout's directory, never a pathname
// prefix that an external rename/symlink can redirect. This is containment and
// change detection, not a write freeze for arbitrary external processes.
type retirementTree struct {
	root    *os.File
	parent  *os.File
	name    string
	entries map[string]retirementEntry
	bytes   int64
	newest  time.Time
	// subtree marks a regenerable directory inside a live checkout (a
	// node_modules, build output) rather than a whole checkout: it holds no
	// git pointer, a .git anywhere in it is a nested repository and a hold,
	// and remove takes the directory itself last.
	subtree bool
}

// maxRetirementEntries bounds one inventory. A checkout with its installs
// is well over 100k entries (reliant's frontends/web/node_modules alone is
// ~108k), so the 50k strip-walk budget this once shared refused exactly the
// worktrees that hold the disk. It bounds memory, not safety: every entry is
// still inspected, and hitting it holds the tree.
const maxRetirementEntries = 500_000

type retirementEntry struct {
	stat unix.Stat_t
}

func retirementOpen(dir int, name string, directory bool) (*os.File, error) {
	flags := uint64(unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NONBLOCK | unix.O_NOFOLLOW)
	if directory {
		flags |= unix.O_DIRECTORY
	}
	fd, err := unix.Openat2(dir, name, &unix.OpenHow{Flags: flags, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_XDEV})
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}
func retirementRoot(path string) (*retirementTree, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	// NO_XDEV starts at the checkout's pinned parent: a worktree volume itself
	// can be mounted, but neither a mounted checkout nor any descendant can be.
	fd, err := unix.Openat2(unix.AT_FDCWD, filepath.Dir(abs), &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_CLOEXEC | unix.O_DIRECTORY | unix.O_NOFOLLOW, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return nil, err
	}
	parent := os.NewFile(uintptr(fd), filepath.Dir(abs))
	root, err := retirementOpen(fd, filepath.Base(abs), true)
	if err != nil {
		parent.Close()
		return nil, err
	}
	return &retirementTree{root: root, parent: parent, name: filepath.Base(abs)}, nil
}
func (t *retirementTree) close()                      { t.root.Close(); t.parent.Close() }
func (t *retirementTree) inspectionFiles() []*os.File { return []*os.File{t.root, t.parent} }
func retirementSame(a, b unix.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Mode == b.Mode && a.Size == b.Size && a.Mtim == b.Mtim && a.Ctim == b.Ctim && a.Nlink == b.Nlink
}
func retirementIdentity(a, b unix.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Mode == b.Mode
}

// installRecord is what a package manager wrote into the node_modules it
// populated, naming the packages it put there. It is the proof that a
// directory in node_modules is an install's output, reproduced from the
// lockfile, rather than something a person put there: a package.json alone
// proves nothing (anyone can keep a hand-made package, with its data/ or
// .env, in node_modules). No record, or a record that does not name the
// directory, keeps the never-list's hold.
type installRecord struct {
	// npm: the package paths in node_modules/.package-lock.json, npm's
	// hidden lockfile ("node_modules/pkg", "node_modules/a/node_modules/b"),
	// without workspace links.
	npm map[string]bool
	// pnpm: node_modules/.modules.yaml and node_modules/.pnpm/lock.yaml are
	// both regular files, so .pnpm/ is pnpm's virtual store, which nothing
	// but pnpm writes.
	pnpm bool
}

// maxInstallRecordBytes bounds the hidden lockfile read; a larger one is no
// record (the tree holds).
const maxInstallRecordBytes = 64 << 20

// readInstallRecord reads the install record of the node_modules directory
// dir. Anything unreadable, oversized, malformed or not a regular file is no
// record. The record files are themselves inventoried, so validate catches
// one rewritten after inspection.
func readInstallRecord(dir *os.File) installRecord {
	var rec installRecord
	if b, ok := readRecordFile(dir, ".package-lock.json", true); ok {
		var lock struct {
			Packages map[string]struct {
				Link bool `json:"link"`
			} `json:"packages"`
		}
		if json.Unmarshal(b, &lock) == nil {
			rec.npm = map[string]bool{}
			for p, e := range lock.Packages {
				if !e.Link {
					rec.npm[p] = true
				}
			}
		}
	}
	_, modules := readRecordFile(dir, ".modules.yaml", false)
	_, lock := readRecordFile(dir, ".pnpm/lock.yaml", false)
	rec.pnpm = modules && lock
	return rec
}

// readRecordFile opens name beneath dir without following any symlink and
// reports whether it is a regular file, reading it when read is set.
func readRecordFile(dir *os.File, name string, read bool) ([]byte, bool) {
	f, err := retirementOpen(int(dir.Fd()), name, false)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Size > maxInstallRecordBytes {
		return nil, false
	}
	if !read {
		return nil, true
	}
	b, err := io.ReadAll(io.LimitReader(f, maxInstallRecordBytes+1))
	if err != nil || len(b) > maxInstallRecordBytes {
		return nil, false
	}
	return b, true
}

// segments is rel (tree-relative; "" is the tree's root) as path segments
// from the tree's parent when the tree is a node_modules shed on its own, so
// both inspections see the same "node_modules/..." path.
func (t *retirementTree) segments(rel string) []string {
	parts := splitSegments(rel)
	if t.subtree && t.name == "node_modules" {
		parts = append([]string{t.name}, parts...)
	}
	return parts
}

// installRoot reports whether the directory at rel is the outermost
// node_modules on its path: the one a package manager writes its record into.
func (t *retirementTree) installRoot(rel string) bool {
	parts := t.segments(rel)
	return len(parts) > 0 && slices.Index(parts, "node_modules") == len(parts)-1
}

// installedPackage reports whether the directory at rel is a package that
// the install recorded in rec put there: it holds a package.json file, and
// rec names it — npm's hidden lockfile lists its path, or it is a package in
// pnpm's virtual store (.pnpm/<name>@<version>/node_modules/<name>).
// Everything inside one is the install's output, reproduced from the
// lockfile, so a name on the never-list there (`data`, `.env`, `*.db`,
// `secrets`) is the package's own content and not the user's data. Packages
// ship such names routinely: @protobufjs/utf8 has tests/data and
// tsconfig-paths lib/__tests__/data, and one of them is in nearly every
// frontend's tree, so the never-list held every node_modules shed and every
// whole retirement of a checkout with an install (2026-11-06: zero sheds in
// 3,730 live-row reconciles on one machine with 108 GiB of node_modules). A
// never-listed name anywhere else in node_modules (its own data/, a directory
// no record names, a package.json-bearing directory a person made) still
// holds.
func (t *retirementTree) installedPackage(rel string, children []os.DirEntry, rec *installRecord) bool {
	if rec == nil {
		return false
	}
	parts := t.segments(rel)
	n := len(parts)
	var name string
	switch {
	case n >= 2 && parts[n-2] == "node_modules" && !strings.HasPrefix(parts[n-1], "@"):
		name = parts[n-1]
	case n >= 3 && parts[n-3] == "node_modules" && strings.HasPrefix(parts[n-2], "@"):
		name = parts[n-2] + "/" + parts[n-1]
	default:
		return false
	}
	manifest := false
	for _, c := range children {
		if c.Name() == "package.json" && c.Type().IsRegular() {
			manifest = true
			break
		}
	}
	if !manifest {
		return false
	}
	root := slices.Index(parts, "node_modules")
	under := parts[root:]
	if rec.npm[strings.Join(under, "/")] {
		return true
	}
	// pnpm: node_modules/.pnpm/<id>/node_modules/<name>, where <id> is the
	// package's own name ("/" as "+") at a version.
	return rec.pnpm && len(under) == 4+len(strings.Split(name, "/")) && under[1] == ".pnpm" && under[3] == "node_modules" &&
		strings.HasPrefix(under[2], strings.ReplaceAll(name, "/", "+")+"@")
}

func (t *retirementTree) inventory(ctx context.Context) (map[string]retirementEntry, int64, time.Time, error) {
	entries := map[string]retirementEntry{}
	var bytes int64
	var newest time.Time
	var visit func(*os.File, string, bool, *installRecord) error
	// inPackage: dir is inside an installed package (installedPackage); rec:
	// the install record of the node_modules dir is in, if any.
	visit = func(dir *os.File, rel string, inPackage bool, rec *installRecord) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		var st unix.Stat_t
		if err := unix.Fstat(int(dir.Fd()), &st); err != nil {
			return err
		}
		entries[rel] = retirementEntry{stat: st}
		when := time.Unix(st.Mtim.Sec, st.Mtim.Nsec)
		if when.After(newest) {
			newest = when
		}
		// Open a new description so enumeration offset never leaks across passes.
		reader, err := retirementOpen(int(dir.Fd()), ".", true)
		if err != nil {
			return err
		}
		children, err := reader.ReadDir(maxStripWalk + 1)
		reader.Close()
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if len(children) > maxStripWalk {
			return errors.New("retirement directory budget exceeded")
		}
		if t.installRoot(rel) {
			r := readInstallRecord(dir)
			rec = &r
		}
		inPackage = inPackage || t.installedPackage(rel, children, rec)
		for _, child := range children {
			if err := ctx.Err(); err != nil {
				return err
			}
			if len(entries) >= maxRetirementEntries {
				return errors.New("retirement inventory budget exceeded")
			}
			name := child.Name()
			path := name
			if rel != "" {
				path = rel + "/" + name
			}
			if !inPackage && neverListed(path) {
				return fmt.Errorf("protected descendant: %s", path)
			}
			if name == ".git" && (rel != "" || t.subtree) {
				return errors.New("nested repository in retirement tree")
			}
			var s unix.Stat_t
			if err := unix.Fstatat(int(dir.Fd()), name, &s, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				return err
			}
			if s.Dev != st.Dev {
				return errors.New("retirement crosses filesystem")
			}
			switch s.Mode & unix.S_IFMT {
			case unix.S_IFDIR:
				if strings.Count(path, "/") >= 64 {
					return errors.New("retirement depth budget exceeded")
				}
				childDir, err := retirementOpen(int(dir.Fd()), name, true)
				if err != nil {
					return err
				}
				var opened unix.Stat_t
				err = unix.Fstat(int(childDir.Fd()), &opened)
				if err == nil && !retirementSame(s, opened) {
					err = errors.New("directory identity changed")
				}
				if err == nil {
					err = visit(childDir, path, inPackage, rec)
				}
				childDir.Close()
				if err != nil {
					return err
				}
			case unix.S_IFREG:
				f, err := retirementOpen(int(dir.Fd()), name, false)
				if err != nil {
					return err
				}
				var opened unix.Stat_t
				err = unix.Fstat(int(f.Fd()), &opened)
				f.Close()
				if err != nil {
					return err
				}
				if !retirementSame(s, opened) {
					return errors.New("file identity changed")
				}
				// A file with other links (pnpm hard-links its store into every
				// node_modules) loses only this name: unlinkat never frees
				// content another link still names.
				entries[path] = retirementEntry{stat: s}
				if s.Nlink == 1 {
					bytes += s.Size
				}
			case unix.S_IFLNK:
				// Symlinks are never followed: remove unlinks the link itself,
				// which touches nothing it points at. Every node_modules has them
				// (.bin, pnpm's whole layout), so refusing them held every
				// worktree that had run an install.
				entries[path] = retirementEntry{stat: s}
			default:
				return errors.New("special file in retirement tree")
			}
			if name != ".git" {
				when := time.Unix(s.Mtim.Sec, s.Mtim.Nsec)
				if when.After(newest) {
					newest = when
				}
			}
		}
		return nil
	}
	if err := visit(t.root, "", false, nil); err != nil {
		return nil, 0, time.Time{}, err
	}
	return entries, bytes, newest, nil
}
func retirementMatchesPath(t *retirementTree, path string) (bool, error) {
	other, err := retirementRoot(path)
	if err != nil {
		return false, err
	}
	defer other.close()
	var a, b unix.Stat_t
	if err := unix.Fstat(int(t.root.Fd()), &a); err != nil {
		return false, err
	}
	if err := unix.Fstat(int(other.root.Fd()), &b); err != nil {
		return false, err
	}
	return retirementIdentity(a, b), nil
}

func inspectRetirementTree(ctx context.Context, path string) (*retirementTree, error) {
	t, err := retirementRoot(path)
	if err != nil {
		return nil, err
	}
	t.entries, t.bytes, t.newest, err = t.inventory(ctx)
	if err != nil {
		t.close()
		return nil, err
	}
	return t, nil
}
func (t *retirementTree) validate(ctx context.Context) error {
	var named unix.Stat_t
	if err := unix.Fstatat(int(t.parent.Fd()), t.name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if !retirementIdentity(named, t.entries[""].stat) {
		return errors.New("checkout pathname was replaced")
	}
	entries, _, _, err := t.inventory(ctx)
	if err != nil {
		return err
	}
	if len(entries) != len(t.entries) {
		return errors.New("retirement contents changed")
	}
	for p, e := range t.entries {
		if got, ok := entries[p]; !ok || !retirementSame(e.stat, got.stat) {
			return fmt.Errorf("retirement entry changed: %s", p)
		}
	}
	return nil
}

// remove only touches the inventoried directory via anchored descriptors. Any
// unexpected name/type/mount/identity is a hold, never a RemoveAll fallback.
// Caller must preserve its journal on partial progress; deletion isn't atomic.
func (t *retirementTree) remove(ctx context.Context, check func() error) error {
	if err := t.validate(ctx); err != nil {
		return err
	}
	paths := make([]string, 0, len(t.entries))
	for p := range t.entries {
		if p != "" && p != ".git" {
			paths = append(paths, p)
		}
	}
	sort.Slice(paths, func(i, j int) bool {
		di, dj := strings.Count(paths[i], "/"), strings.Count(paths[j], "/")
		if di != dj {
			return di > dj
		}
		return paths[i] < paths[j]
	})
	for _, p := range paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := check(); err != nil {
			return err
		}
		dir, err := retirementOpen(int(t.root.Fd()), filepath.ToSlash(filepath.Dir(p)), true)
		if err != nil {
			return err
		}
		parentRel := filepath.ToSlash(filepath.Dir(p))
		if parentRel == "." {
			parentRel = ""
		}
		var parentStat unix.Stat_t
		if err := unix.Fstat(int(dir.Fd()), &parentStat); err != nil || !retirementIdentity(parentStat, t.entries[parentRel].stat) {
			dir.Close()
			return errors.New("ancestor directory changed at deletion boundary")
		}
		base := filepath.Base(p)
		e := t.entries[p]
		var st unix.Stat_t
		err = unix.Fstatat(int(dir.Fd()), base, &st, unix.AT_SYMLINK_NOFOLLOW)
		isDir := e.stat.Mode&unix.S_IFMT == unix.S_IFDIR
		isLink := e.stat.Mode&unix.S_IFMT == unix.S_IFLNK
		if err == nil && ((isDir && !retirementIdentity(e.stat, st)) || (!isDir && !retirementSame(e.stat, st))) {
			err = errors.New("entry changed at deletion boundary")
		}
		if err == nil && !isLink {
			// Openat2 supplies NO_XDEV for directories including same-device binds.
			f, openErr := retirementOpen(int(dir.Fd()), base, isDir)
			if openErr != nil {
				err = openErr
			} else {
				var opened unix.Stat_t
				err = unix.Fstat(int(f.Fd()), &opened)
				f.Close()
				if err == nil && !retirementIdentity(st, opened) {
					err = errors.New("entry replaced while opening")
				}
			}
		}
		if err == nil {
			flags := 0
			if isDir {
				flags = unix.AT_REMOVEDIR
			}
			err = unix.Unlinkat(int(dir.Fd()), base, flags)
		}
		dir.Close()
		if err != nil {
			return err
		}
	}
	if err := check(); err != nil {
		return err
	}
	if t.subtree {
		return t.removeRoot()
	}
	// Keep .git until all other entries have been removed, so partial failures
	// remain registered/recoverable. Never remove an unexpected final entry.
	reader, err := retirementOpen(int(t.root.Fd()), ".", true)
	if err != nil {
		return err
	}
	names, err := reader.Readdirnames(2)
	reader.Close()
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if len(names) != 1 || names[0] != ".git" {
		return errors.New("unexpected entries remain after retirement")
	}
	var pointer unix.Stat_t
	if err := unix.Fstatat(int(t.root.Fd()), ".git", &pointer, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if !retirementSame(pointer, t.entries[".git"].stat) {
		return errors.New("git pointer changed")
	}
	if err := unix.Unlinkat(int(t.root.Fd()), ".git", 0); err != nil {
		return err
	}
	return t.removeRoot()
}

// removeRoot removes the now-empty inventoried directory itself, only while
// its name still names the directory that was inspected.
func (t *retirementTree) removeRoot() error {
	var named unix.Stat_t
	if err := unix.Fstatat(int(t.parent.Fd()), t.name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if !retirementIdentity(named, t.entries[""].stat) {
		return errors.New("checkout name replaced during retirement")
	}
	return unix.Unlinkat(int(t.parent.Fd()), t.name, unix.AT_REMOVEDIR)
}

// inspectRegenerableTree inventories a regenerable directory inside a live
// checkout (node_modules, build output, a tool cache) for shedding. It is the
// retirement inventory with the same holds — a protected descendant, a nested
// repository, a mount, a special file — except that the directory has no git
// pointer of its own and remove takes the directory itself.
func inspectRegenerableTree(ctx context.Context, path string) (*retirementTree, error) {
	t, err := retirementRoot(path)
	if err != nil {
		return nil, err
	}
	t.subtree = true
	t.entries, t.bytes, t.newest, err = t.inventory(ctx)
	if err != nil {
		t.close()
		return nil, err
	}
	return t, nil
}
