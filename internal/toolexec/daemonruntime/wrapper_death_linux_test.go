//go:build linux

package daemonruntime

import (
	"context"
	"fmt"
	"github.com/reliant-labs/reliant/internal/osutil"
	"github.com/stretchr/testify/require"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestWrapperDeathRetainsUncertaintyAcrossDaemonRestart(t *testing.T) {
	d := cancelFixture(t)
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	late := filepath.Join(dir, "late")
	cmd := exec.CommandContext(context.Background(), "bash", "-c", fmt.Sprintf("echo ready > %s; sleep 0.2; echo later > %s", ready, late))
	supervision, err := osutil.PrepareCommandSupervision(cmd, 20*time.Millisecond)
	require.NoError(t, err)
	defer supervision.Close()
	finish, err := d.trackRunExecution("owned-wrapper", "chat", "root", func() {})
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, e := os.Stat(ready); e == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("payload not ready")
		}
		time.Sleep(time.Millisecond)
	}
	// Only the exact wrapper created above is killed. The short-lived payload
	// completes by itself; no unknown PID/process-name cleanup is used.
	require.NoError(t, cmd.Process.Kill())
	_ = cmd.Wait()
	_, proofErr := supervision.Finish()
	require.Error(t, proofErr, "wrapper signal death is not complete tree proof")
	d.cancelMu.Lock()
	d.runExecutions["owned-wrapper"].uncertainty = proofErr
	d.cancelMu.Unlock()
	finish()
	restarted := &daemonClient{bootCfg: d.bootCfg}
	_, err = restarted.cancelOwnedRun(context.Background(), []byte(`{"chat_id":"chat","root_first_run_id":"root"}`))
	require.ErrorContains(t, err, "execution markers remain")
	deadline = time.Now().Add(time.Second)
	for {
		if _, e := os.Stat(late); e == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture did not demonstrate surviving payload")
		}
		time.Sleep(time.Millisecond)
	}
}
