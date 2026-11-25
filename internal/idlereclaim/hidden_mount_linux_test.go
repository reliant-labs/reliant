// Copyright (c) 2026 Reliant Labs
//go:build linux

package idlereclaim

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/require"
)

func TestScratchHiddenBindMount(t *testing.T) {
	testkit.Slow(t, "mount namespace subprocess")
	if os.Getenv("IDLE_MOUNT_CHILD") != "1" {
		cmd := exec.Command("unshare", "--user", "--map-root-user", "--mount", os.Args[0], "-test.run=^TestScratchHiddenBindMount$")
		cmd.Env = append(os.Environ(), "IDLE_MOUNT_CHILD=1")
		out, err := cmd.CombinedOutput()
		if err != nil && (strings.Contains(string(out), "unshare:") || strings.Contains(err.Error(), "executable file not found")) {
			t.Skipf("mount namespace unavailable: %v: %s", err, out)
		}
		require.NoError(t, err, "%s", out)
		return
	}
	h := newHarness(t)
	dir := h.chat("c1")
	target := filepath.Join(dir, "node_modules", "hidden")
	require.NoError(t, os.MkdirAll(target, 0o755))
	external := t.TempDir()
	write(t, external, "only-copy", "protected")
	h.age(dir, 8*day)
	require.NoError(t, syscall.Mount(external, target, "", syscall.MS_BIND, ""))
	defer syscall.Unmount(target, syscall.MNT_DETACH)
	h.pass()
	require.True(t, exists(filepath.Join(external, "only-copy")))
	require.True(t, exists(dir))
}
