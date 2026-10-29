// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/daemon"
)

// A package command from the Commands panel runs as a background process
// tagged with its workspace, so the workspace's process list (exec.bg_list)
// can find it, and its log viewer reads the output line by line with each
// line's stream (exec.bg_output with Lines).
func TestExecBG_PackageProcessCarriesItsWorkspaceAndStreams(t *testing.T) {
	dir := t.TempDir()
	payload, err := json.Marshal(daemon.RunCommandRequest{
		Command:    "echo out; echo err >&2",
		WorkingDir: dir,
		WorktreeID: "wt-1",
	})
	require.NoError(t, err)
	raw, err := handleExecBGStart(context.Background(), payload)
	require.NoError(t, err)
	var started struct {
		ProcessID string `json:"process_id"`
	}
	require.NoError(t, json.Unmarshal(raw, &started))

	raw, err = handleExecBGList(context.Background(), nil)
	require.NoError(t, err)
	var listed []*daemon.ProcessInfo
	require.NoError(t, json.Unmarshal(raw, &listed))
	var found *daemon.ProcessInfo
	for _, p := range listed {
		if p.ID == started.ProcessID {
			found = p
		}
	}
	require.NotNil(t, found, "the process is listed")
	assert.Equal(t, "wt-1", found.WorktreeID)
	assert.Equal(t, dir, found.WorkingDir)

	var output daemon.ProcessOutput
	require.Eventually(t, func() bool {
		req, _ := json.Marshal(map[string]any{"process_id": started.ProcessID, "opts": daemon.OutputOpts{Lines: true}})
		raw, err := handleExecBGOutput(context.Background(), req)
		if err != nil || json.Unmarshal(raw, &output) != nil {
			return false
		}
		return len(output.Lines) >= 2
	}, 10*time.Second, 50*time.Millisecond)
	streams := map[string]string{}
	for _, line := range output.Lines {
		streams[line.Text] = line.Stream
	}
	assert.Equal(t, "stdout", streams["out"])
	assert.Equal(t, "stderr", streams["err"])
}
