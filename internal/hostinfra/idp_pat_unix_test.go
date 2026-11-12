//go:build !windows

package hostinfra

// POSIX permission bits: Windows has no group/other mode to tighten, so these
// assertions build only where they mean something, rather than skipping.

import (
	"os"
	"path/filepath"
	"testing"
)

// A token published where no shared file existed yet is created owner-only,
// in an owner-only directory.
func TestPublishShared_CreatesTheTokenOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	loc := IDPPATLocations{Shared: filepath.Join(dir, "home", "key", "pat.txt"), Checkout: filepath.Join(dir, "clone", "pat.txt")}
	if err := os.MkdirAll(filepath.Dir(loc.Checkout), 0o755); err != nil {
		t.Fatal(err)
	}
	writeMode(t, loc.Checkout, "old-forge-token", 0o644)
	if err := loc.PublishShared(loc.Checkout); err != nil {
		t.Fatal(err)
	}
	if got := mode(t, loc.Shared); got != 0o600 {
		t.Errorf("shared token mode = %o, want 0600", got)
	}
	if got := mode(t, filepath.Dir(loc.Shared)); got != 0o700 {
		t.Errorf("shared token directory mode = %o, want 0700", got)
	}
}

// A PAT is a credential: forge leaves every copy it writes owner-only, even
// when the file already existed world-readable (atomicfile.WriteFile keeps an
// existing file's mode) and even when its content did not change.
func TestCopyPAT_LeavesTheTokenOwnerOnly(t *testing.T) {
	for _, tc := range []struct {
		name     string
		existing string
	}{
		{"changed content", "old-token"},
		{"identical content", "token"},
	} {
		dir := t.TempDir()
		loc := IDPPATLocations{Shared: filepath.Join(dir, "home", ".forge", "idp", "key", "pat.txt"), Checkout: filepath.Join(dir, "clone", ".forge", "idp", "pat.txt")}
		for _, p := range []string{loc.Shared, loc.Checkout} {
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(filepath.Dir(p), 0o755); err != nil { // past the umask
				t.Fatal(err)
			}
		}
		// The checkout copy (an older forge's, world-readable) is published,
		// then the shared one is mirrored back over a world-readable file.
		writeMode(t, loc.Checkout, "token", 0o644)
		writeMode(t, loc.Shared, tc.existing, 0o644)
		if err := loc.PublishShared(loc.Checkout); err != nil {
			t.Fatalf("%s: PublishShared: %v", tc.name, err)
		}
		if err := loc.MirrorToCheckout(); err != nil {
			t.Fatalf("%s: MirrorToCheckout: %v", tc.name, err)
		}
		for _, p := range []string{loc.Shared, loc.Checkout} {
			if got := mode(t, p); got != 0o600 {
				t.Errorf("%s: %s mode = %o, want 0600", tc.name, p, got)
			}
			if got := mode(t, filepath.Dir(p)); got != 0o700 {
				t.Errorf("%s: %s mode = %o, want 0700", tc.name, filepath.Dir(p), got)
			}
			if b, _ := os.ReadFile(p); string(b) != "token" {
				t.Errorf("%s: %s = %q, want the published token", tc.name, p, b)
			}
		}
	}

	// A directory forge creates is 0700 from the start; so is the one
	// Zitadel writes into.
	home := t.TempDir()
	loc := IDPPATLocations{Shared: filepath.Join(home, "idp", "key", "pat.txt"), Checkout: filepath.Join(home, "clone", "pat.txt")}
	if err := loc.PrepareWriteDir(); err != nil {
		t.Fatal(err)
	}
	if got := mode(t, filepath.Dir(loc.Shared)); got != 0o700 {
		t.Errorf("PrepareWriteDir: mode = %o, want 0700", got)
	}
	writeMode(t, loc.Shared, "zitadel-wrote-this", 0o644)
	if err := loc.SecureShared(); err != nil {
		t.Fatal(err)
	}
	if got := mode(t, loc.Shared); got != 0o600 {
		t.Errorf("SecureShared: mode = %o, want 0600", got)
	}
}

func writeMode(t *testing.T, p, body string, perm os.FileMode) {
	t.Helper()
	if err := os.WriteFile(p, []byte(body), perm); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, perm); err != nil { // past the umask
		t.Fatal(err)
	}
}

func mode(t *testing.T, p string) os.FileMode {
	t.Helper()
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return st.Mode().Perm()
}
