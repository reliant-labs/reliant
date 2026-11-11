package kubeconfig

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestDefaultPath_PrefersKUBECONFIG is the reason this package exists rather
// than a `~/.kube/config` literal.
//
// A machine that sets KUBECONFIG has a different default kubeconfig, and
// that is exactly where `k3d kubeconfig merge --kubeconfig-merge-default`
// writes. Handing a host process the home path on such a machine points it at
// a file that either does not exist or does not contain the cluster — which
// client-go reports as "context does not exist", naming neither file.
func TestDefaultPath_PrefersKUBECONFIG(t *testing.T) {
	want := filepath.Join(t.TempDir(), "explicit.yaml")
	t.Setenv("KUBECONFIG", want)
	if got := DefaultPath(); got != want {
		t.Fatalf("DefaultPath() = %q; want the KUBECONFIG value %q", got, want)
	}
}

// TestDefaultPath_TakesFirstKUBECONFIGEntry pins the LIST semantics. KUBECONFIG
// is a path list, and a merge writes to the FIRST entry — so that is the entry
// this must report. Returning the whole list, or a later entry, would name a
// file forge is not the one writing to.
func TestDefaultPath_TakesFirstKUBECONFIGEntry(t *testing.T) {
	sep := ":"
	if runtime.GOOS == "windows" {
		sep = ";"
	}
	t.Setenv("KUBECONFIG", "/first/config"+sep+"/second/config")
	if got := DefaultPath(); got != "/first/config" {
		t.Fatalf("DefaultPath() = %q; want the first entry /first/config", got)
	}
}

// TestDefaultPath_SkipsEmptyLeadingEntry: a KUBECONFIG like ":/real/config"
// (an unset variable interpolated into a list) has an empty first entry.
// Returning "" there would report "unknown" for a machine that does have a
// perfectly good default.
func TestDefaultPath_SkipsEmptyLeadingEntry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("path-list separator differs; the ':' form under test is POSIX")
	}
	t.Setenv("KUBECONFIG", ":/real/config")
	if got := DefaultPath(); got != "/real/config" {
		t.Fatalf("DefaultPath() = %q; want /real/config", got)
	}
}

// TestDefaultPath_FallsBackToHome covers the common case — no KUBECONFIG, so
// the default is the one kubectl itself would use.
func TestDefaultPath_FallsBackToHome(t *testing.T) {
	t.Setenv("KUBECONFIG", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory on this machine: %v", err)
	}
	want := filepath.Join(home, ".kube", "config")
	got := DefaultPath()
	if got != want {
		t.Fatalf("DefaultPath() = %q; want %q", got, want)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("DefaultPath() = %q is relative; a host process resolves it from its own "+
			"working directory, which forge does not control", got)
	}
}
