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
	tree, err := inspectRetirementTree(context.Background(), w.Path)
	if tree != nil {
		tree.close()
	}
	if err == nil {
		t.Fatal("bind mount must fail closed during retirement inventory")
	}
	if !exists(protected) {
		t.Fatal("retirement traversed bind mount and deleted external data")
	}
	if err := syscall.Unmount(target, 0); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(target, "generated"), "old data")
	tree, err = inspectRetirementTree(context.Background(), w.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.close()
	mounted := false
	err = tree.remove(context.Background(), func() error {
		if !mounted {
			mounted = true
			if err := syscall.Mount(external, target, "", syscall.MS_BIND, ""); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	})
	if err == nil || !mounted {
		t.Fatal("mount replacement at destructive boundary must hold")
	}
	if !exists(protected) || !exists(filepath.Join(w.Path, ".git")) {
		t.Fatal("mount race lost external data or recovery pointer")
	}
}
