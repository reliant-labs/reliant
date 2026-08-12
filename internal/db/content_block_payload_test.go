package db

import (
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/ptr"
	"github.com/stretchr/testify/require"
)

// A spawn's tool-call block must carry the thread it owns.
//
// This is the field whose absence made a running spawn's preview show
// "Starting…" for its entire run: the preview finds the child thread through
// child_workflow_id, the live-update writers did not serialize it, and the
// read paths did — so the UI was wrong while you watched it and right the
// moment you reloaded.
func TestContentBlockPayloads_SpawnCarriesChildWorkflowID(t *testing.T) {
	now := time.Now().UTC()
	childThreadID := "child-thread-id"

	blocks := []*MessageContentBlock{{
		ID: "block-1", MessageID: "msg-1", Position: 0,
		BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
		ToolCallID: ptr.Of("toolu_spawn"),
		ToolName:   ptr.Of("spawn"),
		ToolInput:  ptr.Of(`{"title":"child"}`),
	}}
	calls := map[string]*ToolCall{
		"toolu_spawn": {
			ID: "toolu_spawn", ToolName: "spawn",
			Status:          core.ToolCallStatusExecuting,
			ChildWorkflowID: &childThreadID,
			StartedAt:       &now,
		},
	}

	got := ContentBlockPayloadsWithToolCalls(blocks, calls)
	require.Len(t, got, 1)
	require.Equal(t, "toolu_spawn", got[0]["tool_call_id"])
	require.Equal(t, childThreadID, got[0]["child_workflow_id"],
		"a live spawn block must name the thread it owns or its preview has nothing to render")
	require.EqualValues(t, core.ToolCallStatusExecuting, got[0]["tool_call_status"])
	require.NotEmpty(t, got[0]["started_at"])
}

// A call that started no child must not claim one. An empty-string default
// would make "owns a thread" indistinguishable from "does not" at the
// consumer, which is the ambiguity that produces a wrong render rather than
// an honest empty one.
func TestContentBlockPayloads_NonSpawnHasNoChild(t *testing.T) {
	blocks := []*MessageContentBlock{{
		ID: "block-1", MessageID: "msg-1", Position: 0,
		BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
		ToolCallID: ptr.Of("toolu_bash"),
		ToolName:   ptr.Of("bash"),
	}}
	calls := map[string]*ToolCall{
		"toolu_bash": {ID: "toolu_bash", ToolName: "bash", Status: core.ToolCallStatusExecuting},
	}

	got := ContentBlockPayloadsWithToolCalls(blocks, calls)
	_, hasChild := got[0]["child_workflow_id"]
	require.False(t, hasChild)
}

// An empty child_workflow_id is treated as absent, not as a thread id. A
// consumer that fetched thread "" would ask a real question with a nonsense
// argument.
func TestContentBlockPayloads_EmptyChildIsAbsent(t *testing.T) {
	blocks := []*MessageContentBlock{{
		ID: "block-1", MessageID: "msg-1", Position: 0,
		BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
		ToolCallID: ptr.Of("toolu_spawn"),
		ToolName:   ptr.Of("spawn"),
	}}
	calls := map[string]*ToolCall{
		"toolu_spawn": {ID: "toolu_spawn", ToolName: "spawn", ChildWorkflowID: ptr.Of("")},
	}

	got := ContentBlockPayloadsWithToolCalls(blocks, calls)
	_, hasChild := got[0]["child_workflow_id"]
	require.False(t, hasChild)
}

// The base wire shape every consumer already depends on must be unchanged by
// the consolidation — four call sites used to build this by hand.
func TestContentBlockPayloads_PreservesBaseWireShape(t *testing.T) {
	blocks := []*MessageContentBlock{{
		ID: "block-1", MessageID: "msg-1", Position: 3,
		BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
		Content:   ptr.Of("hello"),
	}, {
		ID: "block-2", MessageID: "msg-1", Position: 4,
		BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT,
		ToolCallID: ptr.Of("toolu_x"),
		Content:    ptr.Of("output"),
		IsError:    ptr.Of(true),
	}}

	got := ContentBlockPayloadsWithToolCalls(blocks, nil)
	require.Len(t, got, 2)

	require.Equal(t, "block-1", got[0]["id"])
	require.Equal(t, reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT, got[0]["type"])
	require.Equal(t, 3, got[0]["index"])
	require.Equal(t, "hello", got[0]["content"])

	require.Equal(t, "toolu_x", got[1]["tool_call_id"])
	require.Equal(t, true, got[1]["is_error"])
	// No tool-call row for it: status fields are absent rather than zeroed.
	_, hasStatus := got[1]["tool_call_status"]
	require.False(t, hasStatus)
}

// AttachmentIDsFromBlocks replaced an inline loop in each writer; it must pick
// exactly the attachment-bearing block types, in order.
func TestAttachmentIDsFromBlocks(t *testing.T) {
	blocks := []*MessageContentBlock{
		{BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT, Content: ptr.Of("not-an-attachment")},
		{BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_IMAGE, Content: ptr.Of("att-1")},
		{BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_FILE_REFERENCE, Content: ptr.Of("att-2")},
		{BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_IMAGE},
	}
	require.Equal(t, []string{"att-1", "att-2"}, AttachmentIDsFromBlocks(blocks))
}
