//go:build windows

package generator

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureRelativeSymlink_JunctionLifecycle(t *testing.T) {
	base := t.TempDir()
	targetA := filepath.Join(base, "a")
	targetB := filepath.Join(base, "b")
	for _, d := range []string{targetA, targetB} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(base, "x", "link")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := createJunction(resolvePath(targetA), link); err != nil {
		t.Fatalf("createJunction: %v", err)
	}
	if got, err := os.Readlink(link); err != nil || !linkMatches(got, "", resolvePath(targetA)) {
		t.Fatalf("Readlink = %q, %v", got, err)
	}
	if changed, err := ensureRelativeSymlink(link, targetA); err != nil || changed {
		t.Errorf("re-run: changed=%v err=%v, want no-op", changed, err)
	}
	if changed, err := ensureRelativeSymlink(link, targetB); err != nil || !changed {
		t.Errorf("retarget: changed=%v err=%v", changed, err)
	}
	if _, err := os.Stat(targetA); err != nil {
		t.Errorf("replacing the link damaged its old target: %v", err)
	}
}

func TestEnsureRelativeSymlink_RefusesRealDir(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "t")
	link := filepath.Join(base, "link")
	for _, d := range []string{target, link} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ensureRelativeSymlink(link, target); err == nil {
		t.Fatal("a real directory was replaced")
	}
}

func TestCreateDirLink_FallbackProducesJunction(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "t")
	link := filepath.Join(base, "link")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	deny := func(_, _ string) error { return errPrivilegeNotHeld }
	if err := symlinkOrJunction("t", resolvePath(target), link, deny, createJunction); err != nil {
		t.Fatal(err)
	}
	if got, err := os.Readlink(link); err != nil || !linkMatches(got, "t", resolvePath(target)) {
		t.Fatalf("Readlink = %q, %v", got, err)
	}
}
