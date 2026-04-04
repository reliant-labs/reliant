// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm/tools/shell"
	"github.com/reliant-labs/reliant/internal/logging"
)

const processOutputPollInterval = 250 * time.Millisecond

// processOutputSubTracker tracks active process output subscriptions so they
// can be cancelled individually or all at once on disconnect.
type processOutputSubTracker struct {
	mu   sync.Mutex
	subs map[string]func() // processID -> stop function
}

func newProcessOutputSubTracker() *processOutputSubTracker {
	return &processOutputSubTracker{
		subs: make(map[string]func()),
	}
}

func (t *processOutputSubTracker) add(processID string, stop func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if existing, ok := t.subs[processID]; ok {
		existing()
	}
	t.subs[processID] = stop
}

func (t *processOutputSubTracker) remove(processID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.subs, processID)
}

func (t *processOutputSubTracker) stop(processID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if cancel, ok := t.subs[processID]; ok {
		cancel()
		delete(t.subs, processID)
	}
}

func (t *processOutputSubTracker) stopAll() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for id, stop := range t.subs {
		stop()
		delete(t.subs, id)
	}
}

// handleProcessOutputSubscribe starts a goroutine that polls the background
// manager for new output lines and sends ProcessOutputChunkMessage on the
// stream.
func (d *daemonClient) handleProcessOutputSubscribe(
	msg *reliantv1.ProcessOutputSubscribeMessage,
) {
	processID := msg.GetProcessId()
	if processID == "" {
		return
	}

	done := make(chan struct{})
	stop := func() {
		select {
		case <-done:
		default:
			close(done)
		}
	}

	d.processOutputSubs.add(processID, stop)

	go func() {
		defer d.processOutputSubs.remove(processID)

		mgr := shell.GetBackgroundManager()
		var seq atomic.Uint64

		// Determine the starting sequence. If NewOnly is set, skip existing
		// output by reading current latest sequence.
		var afterSeq int64
		if msg.GetNewOnly() {
			_, latestSeq, err := mgr.GetCombinedOutputWithSeq(processID, 0)
			if err != nil {
				logging.Warn(logPrefix+" Failed to get initial output seq for process subscription",
					"processID", processID, "error", err)
				return
			}
			afterSeq = latestSeq
		}

		ticker := time.NewTicker(processOutputPollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-done:
				return
			case <-ticker.C:
			}

			lines, latestSeq, err := mgr.GetCombinedOutputWithSeq(processID, afterSeq)
			if err != nil {
				logging.Warn(logPrefix+" Failed to get process output",
					"processID", processID, "error", err)
				return
			}

			for _, line := range lines {
				seqNum := seq.Add(1)
				sendErr := d.send(&reliantv1.DaemonMessage{
					Message: &reliantv1.DaemonMessage_ProcessOutputChunk{
						ProcessOutputChunk: &reliantv1.ProcessOutputChunkMessage{
							ProcessId: processID,
							Data:      line.Text,
							Stream:    line.Type,
							Sequence:  seqNum,
						},
					},
				})
				if sendErr != nil {
					logging.Warn(logPrefix+" Failed to send process output chunk",
						"processID", processID, "error", sendErr)
					return
				}
			}

			afterSeq = latestSeq

			// Check if process has completed.
			status, isComplete, exitCode, statusErr := mgr.GetProcessStatus(processID)
			if statusErr != nil {
				logging.Warn(logPrefix+" Failed to get process status",
					"processID", processID, "error", statusErr)
				return
			}
			if isComplete {
				var code int32
				if exitCode != nil {
					code = int32(*exitCode)
				}
				msg := "process " + status
				if strings.Contains(status, "kill") {
					msg = "process was killed"
				}
				_ = d.send(&reliantv1.DaemonMessage{
					Message: &reliantv1.DaemonMessage_ProcessOutputChunk{
						ProcessOutputChunk: &reliantv1.ProcessOutputChunkMessage{
							ProcessId:  processID,
							Data:       msg,
							Stream:     "stdout",
							Sequence:   seq.Add(1),
							IsComplete: true,
							ExitCode:   code,
						},
					},
				})
				return
			}
		}
	}()
}

// handleProcessOutputUnsubscribe stops an active process output subscription.
func (d *daemonClient) handleProcessOutputUnsubscribe(msg *reliantv1.ProcessOutputUnsubscribeMessage) {
	processID := msg.GetProcessId()
	if processID == "" {
		return
	}
	d.processOutputSubs.stop(processID)
}
