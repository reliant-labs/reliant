// Copyright (c) 2025 Reliant Labs
package shell

import (
	"fmt"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// KillAllRunning sits on the daemon's shutdown path, between SIGTERM and
// process exit. Each kill waits up to KillGraceTimeout for its process group to
// go down, so doing them one at a time costs that per process: a daemon holding
// a few dev servers stayed alive for tens of seconds after being told to stop,
// which is indistinguishable from wedged to anything watching. The processes are
// independent and nothing here is ordered.
func TestKillAllRunningKillsProcessesConcurrently(t *testing.T) {
	const count = 4

	m := &BackgroundManager{processes: map[string]*BackgroundProcess{}}
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("proc-%d", i)
		m.processes[id] = &BackgroundProcess{
			ID:     id,
			Status: "running",
			// A command with no OS process: there is nothing to signal, and
			// done is never closed, so every kill waits out its full grace
			// period. That makes the cost exactly KillGraceTimeout per
			// process, with no dependence on how fast this machine reaps.
			cmd:        &exec.Cmd{},
			cancelFunc: func() {},
			done:       make(chan struct{}),
		}
	}

	start := time.Now()
	m.KillAllRunning()
	elapsed := time.Since(start)

	require.Less(t, elapsed, 2*KillGraceTimeout,
		"serial kills would cost %s here; shutdown must not scale with the number of background processes",
		count*KillGraceTimeout)
	require.GreaterOrEqual(t, elapsed, KillGraceTimeout,
		"each kill must still wait out its grace period — this test would be vacuous otherwise")

	for id, process := range m.processes {
		require.Equal(t, "killed", process.Status, "process %s must be marked killed", id)
	}
}

// Nothing to kill must not cost anything.
func TestKillAllRunningWithNoRunningProcessesReturnsImmediately(t *testing.T) {
	m := &BackgroundManager{processes: map[string]*BackgroundProcess{
		"done": {ID: "done", Status: "completed"},
	}}

	start := time.Now()
	m.KillAllRunning()
	require.Less(t, time.Since(start), time.Second)
}
