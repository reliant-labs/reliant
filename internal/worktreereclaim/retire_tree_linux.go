//go:build linux

package worktreereclaim

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
}
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
func (t *retirementTree) inventory(ctx context.Context) (map[string]retirementEntry, int64, time.Time, error) {
	entries := map[string]retirementEntry{}
	var bytes int64
	var newest time.Time
	var visit func(*os.File, string) error
	visit = func(dir *os.File, rel string) error {
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
		for _, child := range children {
			if err := ctx.Err(); err != nil {
				return err
			}
			if len(entries) >= maxStripWalk {
				return errors.New("retirement inventory budget exceeded")
			}
			name := child.Name()
			path := name
			if rel != "" {
				path = rel + "/" + name
			}
			if neverListed(path) {
				return fmt.Errorf("protected descendant: %s", path)
			}
			if name == ".git" && rel != "" {
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
					err = visit(childDir, path)
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
				if s.Nlink != 1 {
					return errors.New("hard-linked file has external ownership")
				}
				entries[path] = retirementEntry{stat: s}
				bytes += s.Size
			case unix.S_IFLNK:
				// Symlinks are never followed, even if their destination is in-tree.
				// A conservative hold is simpler than inferring external ownership.
				return errors.New("symlink in retirement tree")
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
	if err := visit(t.root, ""); err != nil {
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
		if err == nil && ((isDir && !retirementIdentity(e.stat, st)) || (!isDir && !retirementSame(e.stat, st))) {
			err = errors.New("entry changed at deletion boundary")
		}
		if err == nil {
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
	var named unix.Stat_t
	if err := unix.Fstatat(int(t.parent.Fd()), t.name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if !retirementIdentity(named, t.entries[""].stat) {
		return errors.New("checkout name replaced during retirement")
	}
	return unix.Unlinkat(int(t.parent.Fd()), t.name, unix.AT_REMOVEDIR)
}
