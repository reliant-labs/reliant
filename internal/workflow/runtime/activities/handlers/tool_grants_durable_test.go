// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// load_tool's grant is recorded with its result, and only a grant is: the
// durable row a coarse restart rebuilds the thread's grants from
// (runtime.ToolGrantsFromDurableState).
//
// And a Temporal retry of a batch whose load_tool already finished — the
// worker died after the call's terminal write and before the activity
// completed, which every deploy can do — answers that call from its recorded
// row instead of running it again. The row carries what the call granted, so
// the retry reports the grant to the workflow. Before, the replayed result had
// no metadata, the retry reported no grant, and the thread lost the tool.
func TestExecuteTools_LoadToolGrantIsRecordedAndSurvivesARetry(t *testing.T) {
	ctx := context.Background()
	f := setupNoMachineFixture(t, false)
	cfg := toolsConfig(tools.PermissionMutating, []string{tools.ToolView}, []string{"*"}, nil)
	caps := throughHistory(t, f.turn(t, cfg, nil).GetCapabilities())

	load := message.ToolCall{ID: "call-load", Name: tools.ToolLoadTool, Input: `{"name":"sourcegraph"}`}
	refused := message.ToolCall{ID: "call-load-unknown", Name: tools.ToolLoadTool, Input: `{"name":"no_such_tool"}`}
	view := message.ToolCall{ID: "call-view", Name: tools.ToolView, Input: `{"file_path":"a"}`}

	first := f.execute(t, serverToolExecutor(f.h.Repo()), caps, load, refused)
	require.Equal(t, []string{tools.ToolSourcegraph}, first.GetGrantedTools())
	f.execute(t, newMockToolExecutor(), caps, view)

	recorded, err := f.h.Repo().GetToolCallResult(ctx, load.ID)
	require.NoError(t, err)
	require.NotNil(t, recorded)
	assert.Equal(t, []string{tools.ToolSourcegraph}, recorded.GrantedTools, "the grant is recorded with the result")
	for _, id := range []string{refused.ID, view.ID} {
		result, err := f.h.Repo().GetToolCallResult(ctx, id)
		require.NoError(t, err)
		require.NotNil(t, result, id)
		assert.Empty(t, result.GrantedTools, "%s granted nothing, so records nothing", id)
	}

	// The retry runs on a fresh executor, which counts anything run again.
	mock := newMockToolExecutor()
	retried := f.execute(t, mock, caps, load)
	assert.Equal(t, 0, mock.GetExecutionCount(load.ID), "a finished call is not run twice")
	assert.Equal(t, []string{tools.ToolSourcegraph}, retried.GetGrantedTools(), "the retry reports the recorded grant")
}
