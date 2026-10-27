//go:build linux

package osutil

import (
	"context"
	"fmt"
	"github.com/stretchr/testify/require"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestLocalSupervisorReapsForkDuringCancelAndProtectsSibling(t *testing.T) {
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	late := filepath.Join(dir, "late")
	effect := filepath.Join(dir, "effect")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	script := fmt.Sprintf(`trap 'bash -c "trap \"\" TERM; echo \$\$ > %s; sleep 0.6; echo bad > %s; sleep 30" >/dev/null 2>&1 & exit 0' TERM; echo ready > %s; while :; do sleep 0.01; done`, late, effect, ready)
	cmd := exec.CommandContext(ctx, "bash", "-c", script)
	require.NoError(t, SuperviseCommand(cmd, 150*time.Millisecond))
	sibling := exec.Command("sleep", "30")
	require.NoError(t, sibling.Start())
	defer func() { _ = sibling.Process.Kill(); _ = sibling.Wait() }()
	require.NoError(t, cmd.Start())
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, e := os.Stat(ready); e == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("not ready")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	_ = cmd.Wait()
	data, e := os.ReadFile(late)
	require.NoError(t, e, "TERM handler must fork after cancellation")
	pid, e := strconv.Atoi(strings.TrimSpace(string(data)))
	require.NoError(t, e)
	_, e = os.Stat(fmt.Sprintf("/proc/%d", pid))
	require.True(t, os.IsNotExist(e), "successful supervisor wait must mean descendant reaped")
	require.NoError(t, sibling.Process.Signal(syscall.Signal(0)))
	time.Sleep(650 * time.Millisecond)
	_, e = os.Stat(effect)
	require.True(t, os.IsNotExist(e), "no later side effects")
}
func TestLocalSupervisorLeaderExitKeepsCleanupGrace(t *testing.T) {
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	clean := filepath.Join(dir, "clean")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	script := fmt.Sprintf(`bash -c 'trap "sleep 0.05; echo clean > %s; exit 0" TERM; echo ready > %s; while :; do sleep 0.01; done' >/dev/null 2>&1 & wait`, clean, ready)
	cmd := exec.CommandContext(ctx, "bash", "-c", script)
	require.NoError(t, SuperviseCommand(cmd, 150*time.Millisecond))
	require.NoError(t, cmd.Start())
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, e := os.Stat(ready); e == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("not ready")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	_ = cmd.Wait()
	b, e := os.ReadFile(clean)
	require.NoError(t, e)
	require.Equal(t, "clean", strings.TrimSpace(string(b)))
}

func TestLocalSupervisorPreservesExitCode(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "bash", "-c", "exit 7")
	require.NoError(t, SuperviseCommand(cmd, 10*time.Millisecond))
	err := cmd.Run()
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit)
	require.Equal(t, 7, exit.ExitCode())
}
