// Copyright (c) 2026 Reliant Labs
//go:build linux

package proctree

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// fakeDelegated is a plain directory standing in for a delegated cgroup
// whose subtree_control reads ctl. enable stands in for the kernel's answer
// to "+pids": nil marks the file as enabling pids, an error refuses.
func fakeDelegated(t *testing.T, ctl string, enable error) (dir string, writes *int) {
	t.Helper()
	dir = t.TempDir()
	for name, content := range map[string]string{"cgroup.subtree_control": ctl, "cgroup.procs": ""} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	old := enableSubtreePids
	enableSubtreePids = func(d string) error {
		n++
		if enable != nil {
			return enable
		}
		return os.WriteFile(filepath.Join(d, "cgroup.subtree_control"), []byte(ctl+" pids"), 0)
	}
	t.Cleanup(func() { enableSubtreePids = old })
	return dir, &n
}

// A delegated cgroup that already enables pids is used as it is.
func TestDelegatedDirUsesOneThatEnablesPids(t *testing.T) {
	dir, writes := fakeDelegated(t, "cpu pids", nil)
	if got, why := delegatedDir(dir); got != dir || why != "" {
		t.Fatalf("delegatedDir = %q, %q; want %q", got, why, dir)
	}
	if *writes != 0 {
		t.Fatalf("delegatedDir wrote subtree_control %d times for a cgroup that already enables pids", *writes)
	}
}

// A delegated cgroup with an empty subtree_control, which is what systemd's
// Delegate=yes leaves, gets pids enabled and is used.
func TestDelegatedDirEnablesPidsWhenDelegated(t *testing.T) {
	dir, writes := fakeDelegated(t, "", nil)
	if got, why := delegatedDir(dir); got != dir || why != "" {
		t.Fatalf("delegatedDir = %q, %q; want %q", got, why, dir)
	}
	if *writes != 1 {
		t.Fatalf("delegatedDir wrote subtree_control %d times; want 1", *writes)
	}
}

// When the kernel refuses the write (EBUSY: the cgroup itself holds a
// process), the cgroup is not used, and the reason names the error.
func TestDelegatedDirRefusedEnableLeavesCommandsUncgrouped(t *testing.T) {
	dir, _ := fakeDelegated(t, "", syscall.EBUSY)
	got, why := delegatedDir(dir)
	if got != "" {
		t.Fatalf("delegatedDir = %q after the kernel refused +pids; want none", got)
	}
	if !strings.Contains(why, "does not enable the pids controller") || !strings.Contains(why, syscall.EBUSY.Error()) {
		t.Fatalf("why = %q; want the missing controller and the refusal", why)
	}
}

// A cgroup this user cannot write is not delegated to it: nothing is written.
func TestDelegatedDirLeavesAnUndelegatedCgroupAlone(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write a directory whatever its mode")
	}
	dir, writes := fakeDelegated(t, "", errors.New("must not be called"))
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	got, why := delegatedDir(dir)
	if got != "" || !strings.Contains(why, "is not delegated to this user") {
		t.Fatalf("delegatedDir = %q, %q; want not delegated", got, why)
	}
	if *writes != 0 {
		t.Fatalf("delegatedDir tried to enable pids on a cgroup that is not delegated")
	}
}
