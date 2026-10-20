// Copyright (c) 2025 Reliant Labs
package osutil

import (
	"os"
	"os/exec"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsSelfOrAncestor(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("parent lookup is implemented on linux and darwin only")
	}
	require.True(t, IsSelfOrAncestor(os.Getpid()), "this process")
	require.True(t, IsSelfOrAncestor(os.Getppid()), "its parent")

	child := exec.Command("sleep", "30")
	require.NoError(t, child.Start())
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	require.False(t, IsSelfOrAncestor(child.Process.Pid), "a child is not an ancestor")

	require.False(t, IsSelfOrAncestor(0))
	require.False(t, IsSelfOrAncestor(-1))
}

func TestParentPID(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("parent lookup is implemented on linux and darwin only")
	}
	parent, err := ParentPID(os.Getpid())
	require.NoError(t, err)
	require.Equal(t, os.Getppid(), parent)
}
