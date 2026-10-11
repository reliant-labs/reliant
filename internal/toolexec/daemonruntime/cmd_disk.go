// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/reliant-labs/reliant/internal/diskwatch"
)

func init() {
	RegisterCommand(diskwatch.ClearCachesCommand, handleDiskClearCaches)
}

// handleDiskClearCaches starts the user-confirmed clean-up of rebuildable
// caches and returns at once; the clean-up's progress and result reach the UI
// on the heartbeat's disk report. diskwatch decides what is safe to clear —
// the server only forwards what the user ticked.
func handleDiskClearCaches(_ context.Context, payload []byte) ([]byte, error) {
	var req diskwatch.ClearCachesRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	started, skipped := diskwatch.Default().ClearCaches(req.Kinds)
	return json.Marshal(diskwatch.ClearCachesResponse{Started: started, Skipped: skipped})
}

// withNoSpaceExplanation appends the disk explanation to an error message that
// reports ENOSPC: a file write, a git clone, a worktree create. Unchanged when
// the message says nothing about space or no watched volume can be blamed.
func withNoSpaceExplanation(w *diskwatch.Watcher, msg string) string {
	if note := w.ExplainNoSpace(msg); note != "" {
		return appendLine(msg, note)
	}
	return msg
}

// appendLine joins a note onto text, newline-separated, without a leading
// blank line.
func appendLine(text, note string) string {
	if text == "" {
		return note
	}
	return text + "\n" + note
}
