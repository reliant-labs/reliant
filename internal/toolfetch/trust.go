package toolfetch

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"sync"
)

// ErrUntrustedCache means a directory on the tool cache's path can be written
// by someone other than this user, so no binary under it can be trusted and
// reinstalling into it would not help. The error names the directory and the
// fix (tighten its mode, or point FORGE_TOOLS_CACHE somewhere private).
var ErrUntrustedCache = errors.New("tool cache is writable by another user")

// modesEnforced reports whether owner and mode bits are the access control
// on a host running hostOS (Unix), as opposed to ACLs (Windows). On Windows
// os.Stat reports synthetic 0666/0777 modes and no owner, so the ownership
// and mode rules would refuse every cache; there forge reads each
// component's owner and DACL instead (acl.go) and refuses the cache unless
// they prove it private.
func modesEnforced(hostOS string) bool { return hostOS != "windows" }

// enforcesModes is modesEnforced for the host this Fetcher runs on.
func (f *Fetcher) enforcesModes() bool { return modesEnforced(f.hostOS) }

// checkOwned is the rule for everything forge itself created in the cache:
// the release kind directory, an entry, and the files in it. Not a link,
// owned by this user, and writable by nobody else.
func (f *Fetcher) checkOwned(path string, info fs.FileInfo) error {
	if info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink", path)
	}
	if !f.enforcesModes() {
		return f.checkACL(path, aclOwned)
	}
	uid, _, ok := fileOwner(info)
	if !ok {
		return nil
	}
	if int(uid) != f.owner {
		return fmt.Errorf("%s is owned by uid %d, not this user (uid %d)", path, uid, f.owner)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s is group- or world-writable (%v)", path, info.Mode().Perm())
	}
	return nil
}

// checkAncestor is the rule for each directory above the release kind
// directory, up to the filesystem root. Someone who can write one of them can
// rename the cache away and put their own in its place, so each must be owned
// by this user or root, and writable by nobody else — except:
//
//   - a sticky directory (/tmp): others may create entries in it, but cannot
//     rename or remove ours;
//   - a group-writable directory this user owns whose group is the user's own
//     private group (the user-private-group convention: group == username,
//     the primary group, umask 002 — so ~/.cache is 0775 on many Linux
//     machines). That group has no other member by convention; a machine
//     that adds members to a user's private group has given them the user's
//     files anyway.
func (f *Fetcher) checkAncestor(path string, info fs.FileInfo) error {
	if info.Mode()&fs.ModeSymlink != 0 {
		// The path was resolved before the walk, so a link here is the
		// path changing underneath us.
		return fmt.Errorf("%s became a symlink", path)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	if !f.enforcesModes() {
		return f.checkACL(path, aclAncestor)
	}
	uid, gid, ok := fileOwner(info)
	if !ok {
		return nil
	}
	if uid != 0 && int(uid) != f.owner {
		return fmt.Errorf("%s is owned by uid %d, neither this user (uid %d) nor root", path, uid, f.owner)
	}
	perm := info.Mode()
	sticky := perm&fs.ModeSticky != 0
	if perm.Perm()&0o002 != 0 && !sticky {
		return fmt.Errorf("%s is world-writable without the sticky bit (%v)", path, perm.Perm())
	}
	if perm.Perm()&0o020 != 0 && !sticky && (int(uid) != f.owner || !isPrivateGroup(gid)) {
		return fmt.Errorf("%s is group-writable (%v) by a group other than this user's private group", path, perm.Perm())
	}
	return nil
}

// CheckCache reports whether f would trust dir as its cache, applying the
// rules Ensure and Find apply, without creating or installing anything. It
// returns nil, an error wrapping ErrUntrustedCache that names the path at
// fault, or the error resolving dir.
//
// Tests that install into a fresh temporary directory call it first: on a
// host whose own ancestry of that directory forge refuses (a Linux user
// namespace, where / belongs to the unmapped overflow uid; a /tmp without
// the sticky bit) nothing can be installed there, and the test skips
// naming the host's path instead of failing.
func (f *Fetcher) CheckCache(dir string) error {
	_, err := f.trustedKindDir(dir)
	return err
}

// trustedKindDir resolves the release kind directory to its real path and
// verifies the whole chain above it. The returned path contains no link, so
// every entry path built on it is checked component by component.
func (f *Fetcher) trustedKindDir(dir string) (string, error) {
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	if real, err = filepath.Abs(real); err != nil {
		return "", err
	}
	info, err := os.Lstat(real)
	if err != nil {
		return "", err
	}
	if err := f.checkOwned(real, info); err != nil {
		return "", untrusted(err, real)
	}
	for p := filepath.Dir(real); ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil {
			return "", err
		}
		if err := f.checkAncestor(p, info); err != nil {
			return "", untrusted(err, real)
		}
		if filepath.Dir(p) == p {
			return real, nil
		}
	}
}

func untrusted(err error, kindDir string) error {
	return fmt.Errorf("%w: %v\n  forge will not run a tool from %s: anyone who can write that path can substitute the binary.\n"+
		"  Fix: remove other users' write access to the path named above (`chmod go-w` on Unix; on Windows `icacls`, keeping only you, SYSTEM and Administrators), or set %s to a directory only you can write",
		ErrUntrustedCache, err, kindDir, envToolsCache)
}

// envToolsCache is internal/toolcache.EnvRoot, named here for the message.
const envToolsCache = "FORGE_TOOLS_CACHE"

// isPrivateGroup reports whether gid is the current user's primary group and
// carries the user's name: the user-private-group convention.
func isPrivateGroup(gid uint32) bool {
	pg := currentPrivateGroup()
	return pg.ok && pg.gid == gid
}

type privateGroupInfo struct {
	gid uint32
	ok  bool
}

// currentPrivateGroup is the current user's primary group when its name is
// the user's name, looked up once per process.
var currentPrivateGroup = sync.OnceValue(func() privateGroupInfo {
	u, err := user.Current()
	if err != nil {
		return privateGroupInfo{}
	}
	gid, err := strconv.ParseUint(u.Gid, 10, 32)
	if err != nil {
		return privateGroupInfo{}
	}
	g, err := user.LookupGroupId(u.Gid)
	if err != nil || g.Name != u.Username {
		return privateGroupInfo{}
	}
	return privateGroupInfo{gid: uint32(gid), ok: true}
})
