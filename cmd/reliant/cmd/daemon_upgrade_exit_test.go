// Copyright (c) 2026 Reliant Labs
package cmd

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	forgecli "github.com/reliant-labs/forge/cli"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/toolexec/daemonruntime"
	"github.com/reliant-labs/reliant/internal/toolexec/daemonstate"
)

// An upgrade exit is not a start failure and never re-registers; the process
// exits with the code a supervisor restarts on, also when the error is
// wrapped on the way out.
func TestUpgradeExitExitsWithTheSupervisorRestartCode(t *testing.T) {
	failure, reRegister := daemonStartFailure(daemonruntime.ErrUpgradeExit, false, nil)
	require.False(t, reRegister)
	require.ErrorIs(t, failure, daemonruntime.ErrUpgradeExit)
	require.Equal(t, daemonruntime.UpgradeExitCode, forgecli.ExitCode(failure))

	wrapped := fmt.Errorf("tools-daemon exited with error after re-registration: %w", daemonruntime.ErrUpgradeExit)
	require.Equal(t, daemonruntime.UpgradeExitCode, forgecli.ExitCode(wrapped))
}

// `reliant daemon status` names a restart in progress.
func TestDaemonStatusShowsARestartInProgress(t *testing.T) {
	var out bytes.Buffer
	printDaemonRuntimeRecord(&out, daemonstate.State{
		PID:    42,
		Stream: daemonstate.StreamConnected,
		Upgrade: &daemonstate.Upgrade{
			State: "updating", Since: time.Now().Add(-time.Minute), TargetVersion: "v2.0.0",
			Deadline: time.Now().Add(9 * time.Minute), Detail: "draining",
		},
	}, "/tmp/daemon-state.json")
	require.Contains(t, out.String(), "Restart:  updating for 1m0s, to v2.0.0, deadline ")
	require.Contains(t, out.String(), "draining")
}
