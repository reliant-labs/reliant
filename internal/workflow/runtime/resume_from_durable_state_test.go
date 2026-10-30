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

// A restart from the checkpoint resumes the interrupted run against the
// catalog it started with: the pin rides the checkpoint, and the restarted
// root load reads it rather than pinning whatever was pushed since. A pin no
// longer stored is dropped, so the conversation still continues.
func TestFreshRestart_KeepsTheCatalogTheRunStartedWith(t *testing.T) {
	t.Parallel()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.Background()
	now := time.Now().UTC()
	projectID, userID := "project-restart-pin", "user-restart-pin"
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: userID, Name: "p", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	daemonID := "daemon-restart-pin"
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: daemonID, UserID: userID}))
	push := func(version string, at time.Time) {
		wj := `[{"slug":"w","name":"w","relative_path":".reliant/workflows/w.yaml","yaml_content":"name: w\ndescription: ` + version + `\n"}]`
		_, err := repo.SyncProjectConfigSnapshot(ctx, &db.ProjectConfigRecord{
			ProjectID: projectID, DaemonID: daemonID, ProjectWorkflowsJSON: &wj, PushedAt: at,
		}, true)
		require.NoError(t, err)
	}
	push("v1", now)
	pin, err := repo.PinProjectCatalog(ctx, db.CatalogPinScope{ProjectID: projectID, DaemonID: daemonID, UserID: userID})
	require.NoError(t, err)

	const chatID, workflowID = "chat-restart-pin", "wf-restart-pin"
	require.NoError(t, repo.UpsertWorkflowCheckpoint(ctx, &core.WorkflowCheckpoint{
		WorkflowID: workflowID, ChatID: chatID, NodeID: "agent_loop", CatalogPin: pin,
	}))
	push("v2", now.Add(time.Minute))

	resume := ResumeInputFromDurableState(ctx, repo, chatID, workflowID)
	assert.Equal(t, pin, resume.CatalogPin, "the restart carries the interrupted run's pin")
	assert.Equal(t, pin, rootCatalogPin(WorkflowInput{Resume: resume}), "and its root load reads it")
	catalog, found, err := repo.GetPinnedProjectCatalog(ctx, pin)
	require.NoError(t, err)
	require.True(t, found)
	assert.Contains(t, *catalog.Workflows, "description: v1", "the pinned catalog is the one the run started with")

	assert.Equal(t, "pin-of-a-successor", rootCatalogPin(WorkflowInput{
		ExecContext: &ExecutionContext{CatalogPin: "pin-of-a-successor"}, Resume: resume,
	}), "a continue-as-new successor's own pin wins")
	assert.Empty(t, rootCatalogPin(WorkflowInput{}), "a new run pins anew")

	require.NoError(t, repo.UpsertWorkflowCheckpoint(ctx, &core.WorkflowCheckpoint{
		WorkflowID: workflowID, ChatID: chatID, NodeID: "agent_loop", CatalogPin: "0000gone",
	}))
	assert.Empty(t, ResumeInputFromDurableState(ctx, repo, chatID, workflowID).CatalogPin,
		"a pin no longer stored is dropped: the restart pins the current catalog")
}
