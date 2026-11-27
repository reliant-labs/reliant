//go:build unix

package toolfetch

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// statInfo is a directory's fs.FileInfo with a chosen owner and mode, so the
// ancestor rules are proven without a host that has such directories.
type statInfo struct {
	mode     fs.FileMode
	uid, gid uint32
}

func (s statInfo) Name() string       { return "d" }
func (s statInfo) Size() int64        { return 0 }
func (s statInfo) Mode() fs.FileMode  { return s.mode }
func (s statInfo) ModTime() time.Time { return time.Time{} }
func (s statInfo) IsDir() bool        { return s.mode.IsDir() }
func (s statInfo) Sys() any           { return &syscall.Stat_t{Uid: s.uid, Gid: s.gid} }

// The ancestor rules, independent of the host's own directories. The
// installing tests skip on a host whose temporary directory fails these
// rules (trustedTempDir); this test is what keeps that skip from hiding a
// rule that refuses root or this user.
func TestCheckAncestorRules(t *testing.T) {
	t.Parallel()
	const self, overflow = 1001, 65534
	f := New()
	f.owner, f.hostOS = self, "linux"
	// No gid here is this process's private group, so the user-private-group
	// exception never applies.
	const gid = 4242424
	for _, c := range []struct {
		name string
		info statInfo
		want string // "" = trusted
	}{
		{"root 0755", statInfo{fs.ModeDir | 0o755, 0, gid}, ""},
		{"this user 0755", statInfo{fs.ModeDir | 0o755, self, gid}, ""},
		{"root sticky 1777 (/tmp)", statInfo{fs.ModeDir | fs.ModeSticky | 0o777, 0, gid}, ""},
		{"unmapped overflow uid (a user namespace's /)", statInfo{fs.ModeDir | 0o755, overflow, gid}, "owned by uid 65534, neither this user (uid 1001) nor root"},
		{"another user", statInfo{fs.ModeDir | 0o755, self + 1, gid}, "owned by uid 1002"},
		{"world-writable, not sticky", statInfo{fs.ModeDir | 0o777, 0, gid}, "world-writable without the sticky bit"},
		{"group-writable by root's group", statInfo{fs.ModeDir | 0o775, 0, gid}, "group-writable"},
		{"not a directory", statInfo{0o644, 0, gid}, "is not a directory"},
	} {
		err := f.checkAncestor("/x", c.info)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: refused: %v", c.name, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: err = %v, want one containing %q", c.name, err, c.want)
		}
	}
}

// CheckCache applies Ensure's rules: a cache others can write is refused
// with ErrUntrustedCache, wherever the host's own directories stand (the
// cache itself is checked before any ancestor).
func TestCheckCacheRefusesAWritableCache(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "release")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	err := New().CheckCache(dir)
	if !errors.Is(err, ErrUntrustedCache) || !strings.Contains(err.Error(), "group- or world-writable") {
		t.Fatalf("err = %v, want ErrUntrustedCache for a world-writable cache", err)
	}
	if err := New().CheckCache(filepath.Join(dir, "missing")); err == nil || errors.Is(err, ErrUntrustedCache) {
		t.Fatalf("err = %v, want the resolution error, not ErrUntrustedCache", err)
	}
}
