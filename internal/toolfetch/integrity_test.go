package toolfetch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// installed installs the fake tool into a fresh cache and returns the
// fetcher, the release server and the installed entry.
func installed(t *testing.T) (*Fetcher, *release, Tool, Installed) {
	t.Helper()
	archive := tarGz(t, "faketool", fakeBinary)
	tool := fakeTool(archive, nil)
	rel := newRelease(t)
	rel.serve(tool, "linux", "amd64", archive)
	f := rel.fetcher(t, "linux")
	in, err := f.Ensure(context.Background(), tool)
	if err != nil {
		t.Fatal(err)
	}
	return f, rel, tool, in
}

// assertRefusedThenReinstalled: Find refuses the tampered entry, and Ensure
// downloads again and serves a clean, trusted entry at the same path.
func assertRefusedThenReinstalled(t *testing.T, f *Fetcher, rel *release, tool Tool, before Installed) {
	t.Helper()
	if _, ok := f.Find(tool); ok {
		t.Fatal("Find trusted a tampered entry")
	}
	fixed, err := f.Ensure(context.Background(), tool)
	if err != nil {
		t.Fatal(err)
	}
	if rel.hits.Load() != 2 {
		t.Fatalf("Ensure made %d requests; want a reinstall (2)", rel.hits.Load())
	}
	got, err := os.ReadFile(fixed.Path)
	if err != nil || string(got) != fakeBinary || fixed.Source != SourceRelease || fixed.Path != before.Path {
		t.Fatalf("repair = %q, %+v, %v", got, fixed, err)
	}
	info, err := os.Lstat(fixed.Path)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("repaired binary is not a regular file: %v, %v", info, err)
	}
	if modesEnforced(runtime.GOOS) && info.Mode().Perm() != 0o555 {
		t.Errorf("repaired binary mode %v, want 0555", info.Mode().Perm())
	}
	if modesEnforced(runtime.GOOS) {
		if di, _ := os.Lstat(filepath.Dir(fixed.Path)); di.Mode().Perm()&0o022 != 0 {
			t.Errorf("repaired entry mode %v is writable by others", di.Mode().Perm())
		}
	}
}

func TestCacheRejectsSubstitution(t *testing.T) {
	t.Parallel()
	t.Run("binary bytes", func(t *testing.T) {
		t.Parallel()
		f, rel, tool, in := installed(t)
		if err := os.Remove(in.Path); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(in.Path, []byte("substituted"), 0o555); err != nil {
			t.Fatal(err)
		}
		assertRefusedThenReinstalled(t, f, rel, tool, in)
	})
	t.Run("source", func(t *testing.T) {
		t.Parallel()
		f, rel, tool, in := installed(t)
		in.Source = SourceGoInstall
		in.GoPackage = tool.GoPackage + "@" + tool.Version
		if err := writeManifest(filepath.Dir(in.Path), in); err != nil {
			t.Fatal(err)
		}
		assertRefusedThenReinstalled(t, f, rel, tool, in)
	})
	// R3.B1: whoever can write the binary can write the manifest beside it,
	// so the manifest's own hash of the substitute must vouch for nothing.
	t.Run("binary and manifest together", func(t *testing.T) {
		t.Parallel()
		f, rel, tool, in := installed(t)
		evil := []byte("substituted")
		if err := os.Remove(in.Path); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(in.Path, evil, 0o555); err != nil {
			t.Fatal(err)
		}
		in.BinarySHA256 = sum(evil)
		if err := writeManifest(filepath.Dir(in.Path), in); err != nil {
			t.Fatal(err)
		}
		assertRefusedThenReinstalled(t, f, rel, tool, in)
	})
}

// The threat model: a cache entry another user could write is refused and
// reinstalled, and so is a link anywhere in it.
func TestCacheRefusesEntriesOthersCouldWrite(t *testing.T) {
	t.Parallel()
	t.Run("group-writable entry", func(t *testing.T) {
		t.Parallel()
		if !modesEnforced(runtime.GOOS) {
			t.Skip("Windows protects the cache by ACL, not mode bits")
		}
		f, rel, tool, in := installed(t)
		if err := os.Chmod(filepath.Dir(in.Path), 0o775); err != nil {
			t.Fatal(err)
		}
		assertRefusedThenReinstalled(t, f, rel, tool, in)
	})
	t.Run("group-writable binary", func(t *testing.T) {
		t.Parallel()
		if !modesEnforced(runtime.GOOS) {
			t.Skip("Windows protects the cache by ACL, not mode bits")
		}
		f, rel, tool, in := installed(t)
		if err := os.Chmod(in.Path, 0o575); err != nil {
			t.Fatal(err)
		}
		assertRefusedThenReinstalled(t, f, rel, tool, in)
	})
	t.Run("owner-writable binary", func(t *testing.T) {
		t.Parallel()
		if !modesEnforced(runtime.GOOS) {
			t.Skip("Windows keeps 0755")
		}
		f, rel, tool, in := installed(t)
		if err := os.Chmod(in.Path, 0o755); err != nil {
			t.Fatal(err)
		}
		assertRefusedThenReinstalled(t, f, rel, tool, in)
	})
	t.Run("symlinked binary", func(t *testing.T) {
		t.Parallel()
		f, rel, tool, in := installed(t)
		// The link points at identical, correctly-moded bytes: only the
		// link itself is wrong, because its target can be anywhere.
		elsewhere := filepath.Join(t.TempDir(), "faketool")
		if err := os.WriteFile(elsewhere, []byte(fakeBinary), 0o555); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(in.Path); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(elsewhere, in.Path); err != nil {
			t.Skipf("cannot create a symlink here: %v", err)
		}
		assertRefusedThenReinstalled(t, f, rel, tool, in)
	})
	t.Run("symlinked entry", func(t *testing.T) {
		t.Parallel()
		f, rel, tool, in := installed(t)
		entry := filepath.Dir(in.Path)
		moved := filepath.Join(t.TempDir(), "entry")
		if err := os.Rename(entry, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(moved, entry); err != nil {
			t.Skipf("cannot create a symlink here: %v", err)
		}
		assertRefusedThenReinstalled(t, f, rel, tool, in)
	})
}

// A cache that belongs to another user is refused outright: nothing under it
// is trusted, and nothing is installed into it.
func TestCacheRefusesAForeignOwner(t *testing.T) {
	t.Parallel()
	if !modesEnforced(runtime.GOOS) {
		t.Skip("Windows protects the cache by ACL; forge does not read owners there")
	}
	f, rel, tool, _ := installed(t)
	// Creating files owned by another uid needs root; instead this Fetcher
	// is another user looking at the same cache.
	other := *f
	other.owner = f.owner + 1
	if _, ok := other.Find(tool); ok {
		t.Fatal("Find trusted a cache owned by another user")
	}
	_, err := other.Ensure(context.Background(), tool)
	if !errors.Is(err, ErrUntrustedCache) || !strings.Contains(err.Error(), "owned by uid") {
		t.Fatalf("err = %v, want ErrUntrustedCache naming the owner", err)
	}
	if rel.hits.Load() != 1 {
		t.Error("an untrusted cache was installed into")
	}
}

// Every directory above the cache matters: one others can write lets them
// rename the cache away. A sticky world-writable directory (/tmp) does not.
func TestCacheRefusesAWritableAncestor(t *testing.T) {
	t.Parallel()
	if !modesEnforced(runtime.GOOS) {
		t.Skip("Windows protects the cache by ACL, not mode bits")
	}
	archive := tarGz(t, "faketool", fakeBinary)
	tool := fakeTool(archive, nil)
	rel := newRelease(t)
	rel.serve(tool, "linux", "amd64", archive)
	for _, c := range []struct {
		mode os.FileMode
		ok   bool
	}{{0o777, false}, {0o777 | os.ModeSticky, true}, {0o755, true}} {
		parent := t.TempDir()
		if err := os.Chmod(parent, c.mode); err != nil {
			t.Fatal(err)
		}
		f := rel.fetcher(t, "linux")
		f.CacheDir = filepath.Join(parent, "release")
		_, err := f.Ensure(context.Background(), tool)
		if c.ok && err != nil {
			t.Errorf("parent %v: %v", c.mode, err)
		}
		if !c.ok && (!errors.Is(err, ErrUntrustedCache) || !strings.Contains(err.Error(), parent)) {
			t.Errorf("parent %v: err = %v, want ErrUntrustedCache naming %s", c.mode, err, parent)
		}
		_ = os.Chmod(parent, 0o755)
	}
}

// The cache path may itself be reached through a link (macOS /var, a
// relocated home): it is resolved once, then checked.
func TestCacheRootThroughASymlink(t *testing.T) {
	t.Parallel()
	archive := tarGz(t, "faketool", fakeBinary)
	tool := fakeTool(archive, nil)
	rel := newRelease(t)
	rel.serve(tool, "linux", "amd64", archive)
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "cache")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}
	f := rel.fetcher(t, "linux")
	f.CacheDir = link
	in, err := f.Ensure(context.Background(), tool)
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := filepath.EvalSymlinks(real); !strings.HasPrefix(in.Path, want) {
		t.Errorf("path %s is not under the resolved cache %s", in.Path, want)
	}
}

func TestUnsafeMetadataRejectedBeforeIO(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"name", "version", "os", "arch", "asset"} {
		for _, bad := range []string{"..", "../escape", `..\escape`, "/absolute", `C:drive`, "trailing.", "nul\x00"} {
			t.Run(field+"/"+bad, func(t *testing.T) {
				t.Parallel()
				root := t.TempDir()
				f := testFetcher(filepath.Join(root, "cache"), "", "linux")
				f.LookPath = func(string) (string, error) { t.Fatal("unexpected IO"); return "", nil }
				tool := Tool{Name: "faketool", Version: "v1", Checksums: map[string]string{}}
				switch field {
				case "name":
					tool.Name = bad
				case "version":
					tool.Version = bad
				case "os":
					f.GOOS = bad
				case "arch":
					f.GOARCH = bad
				case "asset":
					tool.Checksums[bad] = "bad"
				}
				if _, err := f.Ensure(context.Background(), tool); err == nil {
					t.Fatal("Ensure accepted unsafe metadata")
				}
				if _, ok := f.Find(tool); ok {
					t.Fatal("Find accepted unsafe metadata")
				}
				entries, err := os.ReadDir(root)
				if err != nil || len(entries) != 0 {
					t.Fatalf("cache touched: %v, %v", entries, err)
				}
			})
		}
	}
}

// The current user's SID in the fake Windows ACLs below.
const testUserSID = "S-1-5-21-1-2-3-1001"

// privateSD is what a per-user profile directory carries: owned by the
// user, full control to the user, SYSTEM and Administrators only.
func privateSD() winSecurity {
	return winSecurity{Owner: testUserSID, ACEs: []winACE{
		{Type: aceTypeAllowed, Mask: fileAllAccess, SID: testUserSID},
		{Type: aceTypeAllowed, Mask: fileAllAccess, SID: sidSystem},
		{Type: aceTypeAllowed, Mask: fileAllAccess, SID: sidAdmins},
	}}
}

// withGrant is sd plus an allow ACE for sid.
func withGrant(sd winSecurity, sid string, mask uint32) winSecurity {
	sd.ACEs = append(append([]winACE(nil), sd.ACEs...), winACE{Type: aceTypeAllowed, Mask: mask, SID: sid})
	return sd
}

// windowsHost is a Fetcher acting as a Windows host whose ACL reader
// returns override(path) when it reports true, else privateSD. The mode
// bits and owner uid are deliberately hostile (0777, another uid): on
// Windows they mean nothing and must decide nothing.
func windowsHost(t *testing.T, rel *release, parent string, override func(path string) (winSecurity, bool)) *Fetcher {
	t.Helper()
	f := rel.fetcher(t, "linux")
	f.hostOS = "windows"
	f.owner++
	f.CacheDir = filepath.Join(parent, "release")
	f.acl = aclSource{user: testUserSID, read: func(path string) (winSecurity, error) {
		if sd, ok := override(path); ok {
			return sd, nil
		}
		return privateSD(), nil
	}}
	return f
}

// R3.B1: on a Windows host the cache is held to its ACLs. Owner and mode
// bits decide nothing; another principal's write access anywhere refuses
// the cache; a reader that cannot answer fails closed. Exercised on every OS
// through the Fetcher's hostOS and ACL seams; the real ACL reader is tested
// on Windows (acl_windows_test.go).
func TestWindowsHostHoldsTheCacheToItsACL(t *testing.T) {
	t.Parallel()
	archive := tarGz(t, "faketool", fakeBinary)
	tool := fakeTool(archive, nil)
	none := func(string) (winSecurity, bool) { return winSecurity{}, false }
	hostileModes := func(t *testing.T) string {
		parent := t.TempDir()
		if err := os.Chmod(parent, 0o777); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })
		return parent
	}

	t.Run("a private ACL is trusted whatever the mode bits", func(t *testing.T) {
		t.Parallel()
		rel := newRelease(t)
		rel.serve(tool, "linux", "amd64", archive)
		f := windowsHost(t, rel, hostileModes(t), none)
		in, err := f.Ensure(context.Background(), tool)
		if err != nil {
			t.Fatalf("a private Windows cache was refused: %v", err)
		}
		// 0755, not 0555, which would set the read-only attribute. A real
		// Windows stat reports only that attribute (0444 vs 0666).
		info, _ := os.Lstat(in.Path)
		if runtime.GOOS == "windows" && info.Mode().Perm()&0o200 == 0 {
			t.Errorf("Windows binary is read-only (%v)", info.Mode().Perm())
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o755 {
			t.Errorf("Windows-host binary mode %v, want 0755", info.Mode().Perm())
		}
		for _, p := range []string{filepath.Dir(in.Path), filepath.Join(filepath.Dir(in.Path), manifestName), in.Path} {
			if err := os.Chmod(p, 0o777); err != nil {
				t.Fatal(err)
			}
		}
		if _, ok := f.Find(tool); !ok || rel.hits.Load() != 1 {
			t.Fatalf("mode bits decided a Windows hit (found %v, %d downloads)", ok, rel.hits.Load())
		}
	})

	t.Run("no ACL reader fails closed", func(t *testing.T) {
		t.Parallel()
		rel := newRelease(t)
		f := windowsHost(t, rel, t.TempDir(), none)
		f.acl = aclSource{}
		if _, err := f.Ensure(context.Background(), tool); !errors.Is(err, ErrUntrustedCache) || rel.hits.Load() != 0 {
			t.Fatalf("err = %v (%d downloads), want ErrUntrustedCache before any download", err, rel.hits.Load())
		}
	})

	for _, c := range []struct {
		name string
		at   string // "kind" (the cache), or "parent" (an ancestor)
		sd   winSecurity
		ok   bool
	}{
		{"cache grants Everyone write", "kind", withGrant(privateSD(), "S-1-1-0", fileWriteData), false},
		{"cache grants Users GENERIC_WRITE", "kind", withGrant(privateSD(), "S-1-5-32-545", genericWrite), false},
		{"cache owned by another user", "kind", winSecurity{Owner: "S-1-5-21-9-9-9-500", ACEs: privateSD().ACEs}, false},
		{"cache with a NULL DACL", "kind", winSecurity{Owner: testUserSID, NullDACL: true}, false},
		{"ancestor grants delete-child", "parent", withGrant(privateSD(), "S-1-5-11", fileDeleteChild), false},
		{"ancestor grants WRITE_DAC", "parent", withGrant(privateSD(), "S-1-1-0", writeDAC), false},
		{"ancestor lets users add folders (C:\\ default)", "parent", withGrant(privateSD(), "S-1-5-11", fileAppendData), true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			rel := newRelease(t)
			rel.serve(tool, "linux", "amd64", archive)
			parent := canonical(t, t.TempDir())
			target := filepath.Join(parent, "release")
			if c.at == "parent" {
				target = parent
			}
			f := windowsHost(t, rel, parent, func(p string) (winSecurity, bool) { return c.sd, p == target })
			_, err := f.Ensure(context.Background(), tool)
			if c.ok && err != nil {
				t.Fatalf("refused: %v", err)
			}
			if !c.ok && (!errors.Is(err, ErrUntrustedCache) || !strings.Contains(err.Error(), target) || rel.hits.Load() != 0) {
				t.Fatalf("err = %v (%d downloads), want ErrUntrustedCache naming %s before any download", err, rel.hits.Load(), target)
			}
		})
	}

	t.Run("an entry another user could write is refused", func(t *testing.T) {
		t.Parallel()
		rel := newRelease(t)
		rel.serve(tool, "linux", "amd64", archive)
		var binary string
		f := windowsHost(t, rel, t.TempDir(), func(p string) (winSecurity, bool) {
			return withGrant(privateSD(), "S-1-1-0", fileWriteData), binary != "" && p == binary
		})
		in, err := f.Ensure(context.Background(), tool)
		if err != nil {
			t.Fatal(err)
		}
		binary = in.Path
		if _, ok := f.Find(tool); ok {
			t.Fatal("a Windows host trusted a binary Everyone can write")
		}
	})

	t.Run("links and substituted bytes are still refused", func(t *testing.T) {
		t.Parallel()
		rel := newRelease(t)
		rel.serve(tool, "linux", "amd64", archive)
		f := windowsHost(t, rel, t.TempDir(), none)
		in, err := f.Ensure(context.Background(), tool)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(in.Path); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(in.Path, []byte("substituted"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, ok := f.Find(tool); ok {
			t.Fatal("a Windows host trusted a substituted binary")
		}
		elsewhere := filepath.Join(t.TempDir(), "faketool")
		if err := os.WriteFile(elsewhere, []byte(fakeBinary), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(in.Path); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(elsewhere, in.Path); err != nil {
			t.Skipf("cannot create a symlink here: %v", err)
		}
		if _, ok := f.Find(tool); ok {
			t.Fatal("a Windows host trusted a symlinked binary")
		}
	})
}
