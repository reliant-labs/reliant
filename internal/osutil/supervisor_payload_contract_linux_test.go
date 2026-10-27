//go:build linux

package osutil

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPayloadGateWithoutPermissionNeverExecutes(t *testing.T) {
	r, w, err := os.Pipe()
	require.NoError(t, err)
	defer r.Close()
	w.Close()
	effect := filepath.Join(t.TempDir(), "not-executed")
	self, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, self, "__reliant-supervisor-payload-gate", "3", "/bin/bash", "-c", fmt.Sprintf("echo bad > %s", effect))
	cmd.ExtraFiles = []*os.File{r}
	require.Error(t, cmd.Run())
	require.NoError(t, ctx.Err())
	_, err = os.Stat(effect)
	require.True(t, os.IsNotExist(err))
}

func TestSupervisorDoesNotLeakControlOrGateDescriptors(t *testing.T) {
	// ExtraFiles contains the caller's explicit FD3 only. Supervisor result
	// socket and trusted gate descriptors must not survive payload exec.
	r, w, err := os.Pipe()
	require.NoError(t, err)
	defer r.Close()
	w.Close()
	cmd := exec.CommandContext(context.Background(), "bash", "-c", `test -e /proc/self/fd/3; test ! -e /proc/self/fd/4; test ! -e /proc/self/fd/5`)
	cmd.ExtraFiles = []*os.File{r}
	s, err := PrepareCommandSupervision(cmd, 20*time.Millisecond)
	require.NoError(t, err)
	defer s.Close()
	require.NoError(t, cmd.Run())
	_, err = s.Finish()
	require.NoError(t, err)
}

func TestSupervisorPreservesExistingPayloadDescriptor(t *testing.T) {
	r, w, err := os.Pipe()
	require.NoError(t, err)
	defer r.Close()
	_, err = w.Write([]byte("sandbox-spec"))
	require.NoError(t, err)
	w.Close()
	cmd := exec.CommandContext(context.Background(), "bash", "-c", "cat <&3")
	cmd.ExtraFiles = []*os.File{r}
	supervision, err := PrepareCommandSupervision(cmd, 20*time.Millisecond)
	require.NoError(t, err)
	defer supervision.Close()
	out, err := cmd.Output()
	require.NoError(t, err)
	require.Equal(t, "sandbox-spec", string(out))
	_, err = supervision.Finish()
	require.NoError(t, err)
}
