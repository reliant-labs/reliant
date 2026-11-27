// Copyright (c) 2025 Reliant Labs
package daemonpolicy

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func ghConfigTestBase(t *testing.T) string {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	return base
}

// The empty gh config directory is one per user, reused by every call and
// every process: it was a fresh /tmp/reliant-no-gh-* per process that nothing
// removed.
func TestEmptyGHConfig_IsOnePerUserDirectory(t *testing.T) {
	base := ghConfigTestBase(t)
	dir := emptyGHConfigIn(base)
	if !trustedGHConfigParent(base) {
		if dir != neutralGHConfigDir {
			t.Fatalf("untrusted parent gave %q, want neutral path", dir)
		}
		return
	}
	if dir != filepath.Join(base, "no-gh") {
		t.Fatalf("emptyGHConfigIn = %q, want %s", dir, filepath.Join(base, "no-gh"))
	}
	if again := emptyGHConfigIn(base); again != dir {
		t.Errorf("a second call made %q, want the same %s", again, dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Errorf("%s is not an empty directory: %v %v", dir, entries, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(dir)
		if err != nil || info.Mode().Perm() != 0o500 {
			t.Errorf("%s mode = %v (%v), want 0500 so no login can be written into it", dir, info.Mode(), err)
		}
	}
}

// A shared directory outlives the process that checked it, so it is checked
// on every call: one a login was written into is never handed out, and one
// whose mode was opened up is closed again.
func TestEmptyGHConfig_RefusesAPlantedLoginAndRestoresTheMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory modes are a read-only attribute on Windows")
	}
	base := ghConfigTestBase(t)
	if !trustedGHConfigParent(base) {
		t.Skip("host temp directory has an untrusted ancestor; cannot exercise reuse of a trusted directory")
	}
	dir := emptyGHConfigIn(base)
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := emptyGHConfigIn(base); got != dir {
		t.Fatalf("an opened-up empty directory gave %q, want %s", got, dir)
	}
	if info, _ := os.Stat(dir); info.Mode().Perm() != 0o500 {
		t.Errorf("mode = %v, want 0500 restored", info.Mode())
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hosts.yml"), []byte("PAT"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := emptyGHConfigIn(base); got != neutralGHConfigDir {
		t.Errorf("a directory holding a login gave %q, want %s", got, neutralGHConfigDir)
	}
}

func TestEmptyGHConfig_RefusesUnsafeParents(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix ownership and permission checks")
	}
	for _, mode := range []os.FileMode{0o777, 0o770, 0o755} {
		t.Run(fmt.Sprintf("base-%o", mode), func(t *testing.T) {
			base := ghConfigTestBase(t)
			if err := os.Chmod(base, mode); err != nil {
				t.Fatal(err)
			}
			if got := emptyGHConfigIn(base); got != neutralGHConfigDir {
				t.Fatalf("unsafe base gave %q, want neutral path", got)
			}
		})
	}
	t.Run("writable-ancestor", func(t *testing.T) {
		parent := ghConfigTestBase(t)
		base := filepath.Join(parent, "private")
		if err := os.Mkdir(base, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(parent, 0o777); err != nil {
			t.Fatal(err)
		}
		if got := emptyGHConfigIn(base); got != neutralGHConfigDir {
			t.Fatalf("replaceable ancestor gave %q, want neutral path", got)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		parent := ghConfigTestBase(t)
		link := filepath.Join(parent, "link")
		if err := os.Symlink(ghConfigTestBase(t), link); err != nil {
			t.Fatal(err)
		}
		if got := emptyGHConfigIn(link); got != neutralGHConfigDir {
			t.Fatalf("symlink base gave %q, want neutral path", got)
		}
	})
}

const ghConfigHelperEnv = "RELIANT_DAEMONPOLICY_GHCONFIG_HELPER"

// TestEmptyGHConfigHelper is not a test: it is the process
// TestCredentialsNone_LeavesNothingInTMPDIR launches.
func TestEmptyGHConfigHelper(t *testing.T) {
	if os.Getenv(ghConfigHelperEnv) == "" {
		t.Skip("helper process only")
	}
	for range 2 {
		env := envMap(ChildEnv(context.Background(), nil, CredentialsNone))
		fmt.Println("GH_CONFIG_DIR=" + env["GH_CONFIG_DIR"])
	}
}

// A process that builds a credentials-none environment leaves nothing in
// $TMPDIR: each one used to leave an empty reliant-no-gh-* there.
func TestCredentialsNone_LeavesNothingInTMPDIR(t *testing.T) {
	if os.Getenv(ghConfigHelperEnv) != "" {
		t.Skip("running as the helper")
	}
	tmp, home := t.TempDir(), t.TempDir()
	cache := filepath.Join(home, "cache")
	want := filepath.Join(cache, "reliant", "no-gh")
	if runtime.GOOS == "darwin" {
		want = filepath.Join(home, "Library", "Caches", "reliant", "no-gh")
	}
	for range 2 { // the second process reuses the first one's directory
		cmd := exec.Command(os.Args[0], "-test.run=^TestEmptyGHConfigHelper$", "-test.v")
		cmd.Env = append(os.Environ(),
			ghConfigHelperEnv+"=1",
			"TMPDIR="+tmp, "TMP="+tmp, "TEMP="+tmp,
			"HOME="+home, "XDG_CACHE_HOME="+cache, "LocalAppData="+cache)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("helper: %v\n%s", err, out)
		}
		// On a host with an untrusted ancestor (or without Unix ownership
		// checks), the helper must fail closed rather than reuse the cache.
		if !trustedGHConfigParent(filepath.Dir(want)) {
			want = neutralGHConfigDir
		}
		if strings.Count(string(out), "GH_CONFIG_DIR="+want+"\n") != 2 {
			t.Errorf("helper did not use %s for both calls:\n%s", want, out)
		}
	}
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	if len(left) != 0 {
		t.Errorf("building a credentials-none environment left %v in $TMPDIR", left)
	}
}
