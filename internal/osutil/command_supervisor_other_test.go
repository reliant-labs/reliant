//go:build !linux

package osutil

import (
	"context"
	"fmt"
	"github.com/stretchr/testify/require"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

func TestUnsupportedSupervisionCancellationIsUnconfirmed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/c", "ping -n 30 127.0.0.1 > nul")
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", "exec sleep 30")
	}
	supervision, err := PrepareCommandSupervision(cmd, 10*time.Millisecond)
	require.NoError(t, err)
	defer supervision.Close()
	require.NoError(t, cmd.Start())
	cancel()
	_ = cmd.Wait()
	_, err = supervision.Finish()
	require.ErrorContains(t, err, "unconfirmed")
}

func TestUnsupportedSupervisionStillRunsOrdinaryPayload(t *testing.T) {
	for _, code := range []int{0, 124, 125} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			var cmd *exec.Cmd
			if runtime.GOOS == "windows" {
				cmd = exec.CommandContext(context.Background(), "cmd", "/c", fmt.Sprintf("exit %d", code))
			} else {
				cmd = exec.CommandContext(context.Background(), "sh", "-c", fmt.Sprintf("exit %d", code))
			}
			before := cmd.Path
			supervision, err := PrepareCommandSupervision(cmd, 10*time.Millisecond)
			require.NoError(t, err)
			defer supervision.Close()
			require.Equal(t, before, cmd.Path, "unsupported supervision must not refuse/rewrite normal launch")
			err = cmd.Run()
			if code == 0 {
				require.NoError(t, err)
			} else {
				var exit *exec.ExitError
				require.ErrorAs(t, err, &exit)
				require.Equal(t, code, exit.ExitCode())
			}
			incomplete, err := supervision.Finish()
			require.NoError(t, err)
			require.False(t, incomplete)
		})
	}
}
