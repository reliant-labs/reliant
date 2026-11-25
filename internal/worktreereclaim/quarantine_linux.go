//go:build linux

package worktreereclaim

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"golang.org/x/sys/unix"
)

// parkedTree is what park leaves behind: the parent descriptor the tree does
// not hold (its original parent while parked, the trash once put back), the
// tree's original name and path, and the trash with the parked name, where
// its ParkedOriginSuffix marker lives.
type parkedTree struct {
	other, trash *os.File
	name, path   string
	parkedName   string
}

func (p *parkedTree) close()            { p.other.Close(); p.trash.Close() }
func (p *parkedTree) files() []*os.File { return []*os.File{p.other, p.trash} }

// forget removes the marker once nothing is parked any more.
func (p *parkedTree) forget() {
	_ = unix.Unlinkat(int(p.trash.Fd()), p.parkedName+ParkedOriginSuffix, 0)
}

// park renames the inventoried directory, by its pinned parent descriptor,
// into trash under a fresh name, records its original path beside it (the
// ParkedOriginSuffix marker, written first), and re-anchors the tree there.
// Nothing at the original name is replaced, and an entry that is not the
// inventoried directory is renamed straight back.
func (t *retirementTree) park(trash string) (*parkedTree, error) {
	if err := os.MkdirAll(trash, 0o700); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(trash)
	if err != nil {
		return nil, err
	}
	open := func() (*os.File, error) {
		fd, err := unix.Openat2(unix.AT_FDCWD, abs, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_CLOEXEC | unix.O_DIRECTORY | unix.O_NOFOLLOW, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
		if err != nil {
			return nil, err
		}
		return os.NewFile(uintptr(fd), abs), nil
	}
	dir, err := open()
	if err != nil {
		return nil, err
	}
	markers, err := open()
	if err != nil {
		dir.Close()
		return nil, err
	}
	fail := func(err error) (*parkedTree, error) {
		dir.Close()
		markers.Close()
		return nil, err
	}
	fd := int(dir.Fd())
	name := t.name + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	// The marker goes first, so a daemon stopped at any point after the
	// rename leaves a parked tree that names where it belongs.
	if err := writeMarker(fd, name+ParkedOriginSuffix, t.path); err != nil {
		return fail(err)
	}
	if err := unix.Renameat2(int(t.parent.Fd()), t.name, fd, name, unix.RENAME_NOREPLACE); err != nil {
		_ = unix.Unlinkat(fd, name+ParkedOriginSuffix, 0)
		return fail(err)
	}
	var moved, root unix.Stat_t
	err = unix.Fstatat(fd, name, &moved, unix.AT_SYMLINK_NOFOLLOW)
	if err == nil {
		err = unix.Fstat(int(t.root.Fd()), &root)
	}
	if err == nil && (!retirementIdentity(moved, t.entries[""].stat) || !retirementIdentity(root, moved)) {
		err = errors.New("the directory was replaced before it was parked")
	}
	if err != nil {
		if rerr := unix.Renameat2(fd, name, int(t.parent.Fd()), t.name, unix.RENAME_NOREPLACE); rerr != nil {
			return fail(fmt.Errorf("%w; it stays at %s: %v", err, filepath.Join(abs, name), rerr))
		}
		_ = unix.Unlinkat(fd, name+ParkedOriginSuffix, 0)
		return fail(err)
	}
	// The rename is the one change to the directory itself (its ctime, and
	// its ".." when the parent changed); everything under it is unchanged and
	// validated against the inventory as before.
	e := t.entries[""]
	e.stat = root
	t.entries[""] = e
	p := &parkedTree{other: t.parent, trash: markers, name: t.name, path: t.path, parkedName: name}
	t.parent, t.name, t.path = dir, name, filepath.Join(abs, name)
	return p, nil
}

// writeMarker creates name in dir holding the original path, durably.
func writeMarker(dir int, name, original string) error {
	fd, err := unix.Openat(dir, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), name)
	_, err = f.WriteString(original)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = unix.Unlinkat(dir, name, 0)
	}
	return err
}

// unpark renames what is left of a parked tree back to its original name,
// which must still be free.
func (t *retirementTree) unpark(p *parkedTree) error {
	if err := unix.Renameat2(int(t.parent.Fd()), t.name, int(p.other.Fd()), p.name, unix.RENAME_NOREPLACE); err != nil {
		return err
	}
	t.parent, p.other = p.other, t.parent
	t.name, t.path = p.name, p.path
	return nil
}

// renameNoReplace renames from to to, failing when to exists.
func renameNoReplace(from, to string) error {
	return unix.Renameat2(unix.AT_FDCWD, from, unix.AT_FDCWD, to, unix.RENAME_NOREPLACE)
}
