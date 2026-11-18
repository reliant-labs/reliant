// Copyright (c) 2026 Reliant Labs
package shell

import (
	"fmt"
	"strings"
	"time"
)

// largestCommandMax bounds the command line StopLargest describes.
const largestCommandMax = 120

// StopLargest stops the running background command whose process tree is the
// largest, saying why in the daemon log and in its output, and describes what
// it stopped: "" when there was nothing to stop. tasks sizes a command by its
// process group (the leader's pid, proctree.Prepare); a command whose group
// has no tasks counted is not a candidate.
//
// It is the fork watchdog's self-heal (internal/forkwatch; reliant#1919): on a
// machine whose process table stayed full, the largest background command is
// the work most likely to have filled it, and stopping it costs one command
// instead of every agent's. Only what this daemon started and still holds is
// a candidate: never a foreground command an agent is waiting on (those are
// not here), a process recovered from the journal (which it cannot bound), or
// a Commands-panel package command (which a person started).
//
// It runs on the caller's goroutine and forks nothing: the kill is signals.
func (m *BackgroundManager) StopLargest(tasks func(pgid int) uint64, why string) string {
	if tasks == nil {
		return ""
	}
	var (
		largest *BackgroundProcess
		size    uint64
	)
	m.mu.RLock()
	for _, p := range m.processes {
		p.outputMu.RLock()
		candidate := p.Status == "running" && p.cmd != nil && p.cmd.Process != nil && p.cancelFunc != nil && !p.PackageCommand
		pid := 0
		if candidate {
			pid = p.cmd.Process.Pid
		}
		p.outputMu.RUnlock()
		if !candidate || pid <= 0 {
			continue
		}
		n := tasks(pid)
		if n == 0 {
			continue
		}
		// Largest first; on a tie the one started first, then by id, so the
		// choice does not depend on map order.
		if largest == nil || n > size || (n == size && (p.StartTime.Before(largest.StartTime) ||
			(p.StartTime.Equal(largest.StartTime) && p.ID < largest.ID))) {
			largest, size = p, n
		}
	}
	m.mu.RUnlock()
	if largest == nil {
		return ""
	}
	// Claimed first, like every reap: a process that has ended since the scan
	// is spared, and one the idle reaper is already stopping is not stopped
	// twice.
	reason := largest.claimReap(func(p *BackgroundProcess, _ time.Time) string {
		if p.Status != "running" {
			return ""
		}
		return why
	})
	if reason == "" || !m.reap(largest, reason) {
		return ""
	}
	who := "no chat"
	if largest.ChatID != "" {
		who = "chat " + shortChatID(largest.ChatID)
	}
	return fmt.Sprintf("`%s` (%d tasks; background process %s, %s)", boundCommand(largest.Command), size, largest.ID, who)
}

// boundCommand is cmd on one line, at most largestCommandMax long.
func boundCommand(cmd string) string {
	cmd = strings.Join(strings.Fields(cmd), " ")
	if len(cmd) > largestCommandMax {
		return cmd[:largestCommandMax-1] + "…"
	}
	return cmd
}

func shortChatID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
