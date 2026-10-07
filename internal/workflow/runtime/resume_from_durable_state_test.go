// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// A coarse fresh restart of a run that had fanned out hands each thread back
// its OWN load_tool grants: the root's to the root, and the sub-agent's to the
// sub-agent it relaunches — never one thread's to the other.
//
// The durable state is what a dead execution leaves behind: its checkpoint, a
// backgrounded spawn that never reported back, and a load_tool result on each
// thread recording what it granted (ExecuteTools writes these; the activity
// side is pinned by TestLoadedToolSurvivesACoarseRestartFromTheCheckpoint).
// The resume input is built by ResumeInputFromDurableState — the one
// SendMessage starts the restart with — and the resumed run's call_llm on each
// thread is checked for what it was handed.
func TestFreshRestart_EachThreadKeepsItsOwnGrants(t *testing.T) {
	t.Parallel()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.Background()

	const chatID = "chat-fresh-restart-grants"
	root := canParentWorkflowID
	parent := "thread-" + chatID
	child := canChildThread("tc-spawn")
	seedDeadFanOut(t, repo, chatID, root, parent, child)
	recordLoad(t, repo, chatID, parent, "tc-load-parent", []string{"sourcegraph"})
	recordLoad(t, repo, chatID, child, "tc-load-child", []string{"fetch"})

	resume := ResumeInputFromDurableState(ctx, repo, chatID, root)
	require.Equal(t, "agent_loop", resume.NodeID)
	require.Len(t, resume.Spawns, 1, "the sub-agent that never reported back is relaunched")
	require.Equal(t, child, resume.Spawns[0].ChildThread)

	input := spawnE2EWorkflowInput(chatID)
	input.Resume = resume
	env := (&testsuiteHolder{}).env()
	e := newCANSpawnEnv(t, env)
	e.scripts[child] = repeatTurns("r", 1)

	env.ExecuteWorkflow(DynamicWorkflow, input)
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	require.NotEmpty(t, e.llmGrants[child], "the relaunched sub-agent takes a turn")
	assert.Equal(t, []string{"fetch"}, e.llmGrants[child][0], "the sub-agent's first turn has its own grant, and only it")
	require.NotEmpty(t, e.llmGrants[parent], "the root takes a turn")
	assert.Equal(t, []string{"sourcegraph"}, e.llmGrants[parent][0], "the root's first turn has its own grant, and only it")
}

type fakeToolGrantLister struct {
	rows []*db.ToolGrant
	err  error
}

func (f *fakeToolGrantLister) ListToolGrants(context.Context, string) ([]*db.ToolGrant, error) {
	return f.rows, f.err
}

// Each thread's grants are the union of its recorded results, sorted — the
// shape the workflow keeps them in — and a failed read is reported rather than
// read as "no grants".
func TestToolGrantsFromDurableState(t *testing.T) {
	t.Parallel()
	got, err := ToolGrantsFromDurableState(context.Background(), &fakeToolGrantLister{rows: []*db.ToolGrant{
		{ThreadID: "root", Tools: []string{"sourcegraph"}},
		{ThreadID: "root", Tools: []string{"fetch", "sourcegraph"}},
		{ThreadID: "child", Tools: []string{"generate_image"}},
		{ThreadID: "", Tools: []string{"orphan"}},
	}}, "chat")
	require.NoError(t, err)
	assert.Equal(t, map[string][]string{
		"root":  {"fetch", "sourcegraph"},
		"child": {"generate_image"},
	}, got)

	none, err := ToolGrantsFromDurableState(context.Background(), &fakeToolGrantLister{}, "chat")
	require.NoError(t, err)
	assert.Nil(t, none)

	_, err = ToolGrantsFromDurableState(context.Background(), &fakeToolGrantLister{err: assert.AnError}, "chat")
	require.ErrorIs(t, err, assert.AnError)
}

// seedDeadFanOut records what a root execution that died with one background
// sub-agent in flight leaves in the database.
func seedDeadFanOut(t *testing.T, repo *db.Repo, chatID, root, parentThread, childThread string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	project := &db.Project{ID: "project-" + chatID, UserID: "user-" + chatID, Name: "Grants", Path: "/project"}
	require.NoError(t, repo.CreateProject(ctx, project))
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{ID: chatID, ProjectID: project.ID, UserID: project.UserID}))
	for _, thread := range []string{chatID, parentThread, childThread} {
		_, err := repo.CreateThread(ctx, &db.Thread{ID: thread, ChatID: chatID, CreatedAt: now})
		require.NoError(t, err)
	}
	require.NoError(t, repo.CreateWorkflow(ctx, &db.Workflow{
		ID: root, ChatID: chatID, WorkflowName: "agent", Thread: parentThread, Status: db.Failed(), CreatedAt: now,
	}))
	require.NoError(t, repo.CreateWorkflow(ctx, &db.Workflow{
		ID: childThread, ParentID: &root, ChatID: chatID, WorkflowName: "agent", Thread: childThread,
		Status: db.Failed(), CreatedAt: now,
	}))
	require.NoError(t, repo.UpsertWorkflowCheckpoint(ctx, &db.WorkflowCheckpoint{
		WorkflowID: root, ChatID: chatID, NodeID: "agent_loop", LoopIteration: 2,
	}))
	require.NoError(t, repo.UpsertToolCall(ctx, &db.ToolCall{
		ID: "tc-spawn", ChatID: chatID, ThreadID: &parentThread, ToolName: "spawn",
		Input: []byte(`{"preset":"general","prompt":"go"}`), Status: core.ToolCallStatusBackgrounded,
		ChildWorkflowID: &childThread, RequestedAt: now, CreatedAt: now, UpdatedAt: now,
	}))
}

// recordLoad writes a completed load_tool call on thread and its result, as
// ExecuteTools records them.
func recordLoad(t *testing.T, repo *db.Repo, chatID, thread, toolCallID string, granted []string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	require.NoError(t, repo.UpsertToolCall(ctx, &db.ToolCall{
		ID: toolCallID, ChatID: chatID, ThreadID: &thread, ToolName: "load_tool",
		Input: []byte(`{"name":"x"}`), Status: core.ToolCallStatusCompleted,
		RequestedAt: now, CompletedAt: &now, CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, repo.UpsertToolCallResult(ctx, chatID, &db.ToolCallResult{
		ToolCallID: toolCallID, Content: "loaded", GrantedTools: granted, CreatedAt: now, UpdatedAt: now,
	}))
}
