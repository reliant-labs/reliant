// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
)

type fakeLiveSpawnLister struct {
	rows []*db.LiveBackgroundSpawn
	root string
}

func (f *fakeLiveSpawnLister) ListLiveBackgroundSpawns(_ context.Context, rootWorkflowID string) ([]*db.LiveBackgroundSpawn, error) {
	f.root = rootWorkflowID
	return f.rows, nil
}

// The coarse fresh restart of a history-limit death has no handoff to read —
// the spawns were goroutines in the terminated execution. It derives them
// from the backgrounded tool_calls rows instead, preset and title included.
func TestResumableSpawnsFromDurableState(t *testing.T) {
	t.Parallel()
	lister := &fakeLiveSpawnLister{rows: []*db.LiveBackgroundSpawn{
		{ToolCallID: "tc-1", ParentThreadID: "root", ChildThreadID: "child-1", ChildWorkflowID: "child-1",
			IssuingWorkflowID: "root", Depth: 0,
			ToolInput: []byte(`{"preset":"researcher","prompt":"look","title":"Research"}`)},
		{ToolCallID: "tc-2", ParentThreadID: "child-1", ChildThreadID: "child-2", ChildWorkflowID: "child-2",
			IssuingWorkflowID: "child-1", Depth: 1, ToolInput: []byte(`not json`)},
	}}

	got, err := ResumableSpawnsFromDurableState(context.Background(), lister, "root")
	require.NoError(t, err)
	require.Equal(t, "root", lister.root)
	require.Equal(t, []SpawnHandoff{
		{ToolCallID: "tc-1", ParentThread: "root", ChildThread: "child-1", ChildWorkflowID: "child-1",
			ParentWorkflowID: "root", SpawnDepth: 1, Preset: "researcher", Title: "Research"},
		{ToolCallID: "tc-2", ParentThread: "child-1", ChildThread: "child-2", ChildWorkflowID: "child-2",
			ParentWorkflowID: "child-1", SpawnDepth: 2},
	}, got)
}

// End to end on the successor side: a fresh restart whose Resume carries a
// durable-derived spawn (iteration 0, no child inputs) relaunches it on its
// existing thread, the parent waits on it, and it reports exactly once.
func TestFreshRestart_RelaunchesDurableSpawn(t *testing.T) {
	t.Parallel()
	const chatID = "chat-fresh-restart-spawn"
	child := canChildThread("tc-spawn")
	parent := "thread-" + chatID
	input := spawnE2EWorkflowInput(chatID)
	input.Resume = &ResumeInput{
		NodeID:        "agent_loop",
		LoopIteration: 96,
		Spawns: []SpawnHandoff{{
			ToolCallID: "tc-spawn", ParentThread: parent, ChildThread: child,
			ChildWorkflowID: child, ParentWorkflowID: canParentWorkflowID,
			SpawnDepth: 1, Preset: "general",
		}},
	}

	env := (&testsuiteHolder{}).env()
	e := newCANSpawnEnv(t, env)
	e.scripts[child] = repeatTurns("r", 2)

	env.ExecuteWorkflow(DynamicWorkflow, input)
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	require.NotContains(t, e.createdRows, child, "the durable thread is reused")
	require.Equal(t, 3, e.llmCalls[child], "the relaunched spawn runs its turns")
	require.Equal(t, 1, e.enqueuedFor("tc-spawn"))
	require.Contains(t, e.toolStatusesFor("tc-spawn"), "completed")
	require.GreaterOrEqual(t, e.llmCalls[parent], 1, "the parent reacts to the relaunched spawn's report")
}
