//go:build linux && cleanup_acceptance

package worktreereclaim

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

func TestStripSafetyBindMount(t *testing.T) {
	heavyUnderShort(t, "isolated user/mount namespace regression")
	cmd := exec.Command("unshare", "-Urnm", "--", os.Args[0], "-test.run=^TestStripSafetyBindMountChild$", "-test.v")
	cmd.Env = append(os.Environ(), "RELIANT_CLEANUP_MOUNT_ACCEPTANCE=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("isolated mount safety proof failed: %v\n%s", err, out)
	}
}

func TestStripSafetyBindMountChild(t *testing.T) {
	if os.Getenv("RELIANT_CLEANUP_MOUNT_ACCEPTANCE") != "1" {
		t.Skip("only executes inside private user/mount namespace")
	}
	f := newFixture(t)
	ignore(t, f, "/tmp/\n")
	w := f.worktree("bind-mount", "feat/bind-mount")
	target := filepath.Join(w.Path, "tmp")
	if err := os.MkdirAll(target, 0700); err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	protected := filepath.Join(external, "only-copy")
	write(t, protected, "unrelated volume data")
	if err := syscall.Mount(external, target, "", syscall.MS_BIND, ""); err != nil {
		t.Fatal(err)
	}
	defer syscall.Unmount(target, syscall.MNT_DETACH)
	result := Result{}
	f.r.strip(context.Background(), w, nil, []string{w.Path}, &result, func() error { return nil })
	if !exists(protected) {
		t.Fatalf("recursive removal traversed bind mount and deleted external data: %+v", result)
	}
}
