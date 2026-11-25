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
// not hold (its original parent while parked, the trash once put back) and
// the tree's original name and path.
type parkedTree struct {
	other      *os.File
	name, path string
}

func (p *parkedTree) close()            { p.other.Close() }
func (p *parkedTree) files() []*os.File { return []*os.File{p.other} }

// park renames the inventoried directory, by its pinned parent descriptor,
// into trash under a fresh name, and re-anchors the tree there. Nothing at
// the original name is replaced, and an entry that is not the inventoried
// directory is renamed straight back.
func (t *retirementTree) park(trash string) (*parkedTree, error) {
	if err := os.MkdirAll(trash, 0o700); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(trash)
	if err != nil {
		return nil, err
	}
	fd, err := unix.Openat2(unix.AT_FDCWD, abs, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_CLOEXEC | unix.O_DIRECTORY | unix.O_NOFOLLOW, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return nil, err
	}
	dir := os.NewFile(uintptr(fd), abs)
	name := t.name + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if err := unix.Renameat2(int(t.parent.Fd()), t.name, fd, name, unix.RENAME_NOREPLACE); err != nil {
		dir.Close()
		return nil, err
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
			err = fmt.Errorf("%w; it stays at %s: %v", err, filepath.Join(abs, name), rerr)
		}
		dir.Close()
		return nil, err
	}
	// The rename is the one change to the directory itself (its ctime, and
	// its ".." when the parent changed); everything under it is unchanged and
	// validated against the inventory as before.
	e := t.entries[""]
	e.stat = root
	t.entries[""] = e
	p := &parkedTree{other: t.parent, name: t.name, path: t.path}
	t.parent, t.name, t.path = dir, name, filepath.Join(abs, name)
	return p, nil
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
