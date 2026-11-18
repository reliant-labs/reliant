// Copyright (c) 2026 Reliant Labs
//go:build !windows

package shell

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The fork watchdog's self-heal (reliant#1919) stops the largest background
// command, by the size its process group is given, and only that one; a
// package command a person started is never a candidate, however large. The
// stopped command's output says why.
func TestStopLargest_StopsOnlyTheLargestAgentCommand(t *testing.T) {
	m, _ := newReapTestManager(t)
	small := startQuiet(t, m, StartProcessOptions{ChatID: "chat-small"})
	large := startQuiet(t, m, StartProcessOptions{ChatID: "chat-large-1234567890", Command: "exec sleep 601"})
	panel := startQuiet(t, m, StartProcessOptions{PackageCommand: true})

	sizes := map[int]uint64{
		small.GetPID(): 4,
		large.GetPID(): 1800,
		panel.GetPID(): 5000,
	}
	stopped := m.StopLargest(func(pgid int) uint64 { return sizes[pgid] }, "this machine's process table stayed full")

	require.Contains(t, stopped, "`exec sleep 601`")
	require.Contains(t, stopped, "1800 tasks")
	require.Contains(t, stopped, "chat chat-lar")
	require.Equal(t, "killed", large.StatusSafe())
	require.Equal(t, "running", small.StatusSafe())
	require.Equal(t, "running", panel.StatusSafe(), "a package command a person started is never stopped")

	require.Eventually(t, func() bool {
		lines, err := m.GetCombinedOutput(large.ID)
		if err != nil {
			return false
		}
		for _, l := range lines {
			if strings.Contains(l.Text, "[reliant] Stopped this background process: this machine's process table stayed full") {
				return true
			}
		}
		return false
	}, 10*time.Second, 10*time.Millisecond, "the stopped command's output says why")
}

// Nothing sized, nothing stopped.
func TestStopLargest_NothingToStop(t *testing.T) {
	m, _ := newReapTestManager(t)
	p := startQuiet(t, m, StartProcessOptions{ChatID: "c"})
	require.Empty(t, m.StopLargest(func(int) uint64 { return 0 }, "why"))
	require.Empty(t, m.StopLargest(nil, "why"))
	require.Equal(t, "running", p.StatusSafe())
}
