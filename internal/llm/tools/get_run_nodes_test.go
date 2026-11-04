package tools

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/threads"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/rundiagnostics"
	"github.com/stretchr/testify/require"
)

func TestGetRunNodeOutput(t *testing.T) {
	rows := []*db.StepExecution{{ID: "a", WorkflowID: "root", StepID: "gate", ActivityName: "ExecuteRunStep", ExitCode: sql.NullInt64{Int64: 2, Valid: true}, DurationMs: sql.NullInt64{Int64: 53, Valid: true}, OutputJSON: sql.NullString{String: `{"stdout":"building","stderr":"password=hidden\ncompile failed"}`, Valid: true}}}
	var b strings.Builder
	writeRunNodes(&b, rundiagnostics.Nodes(rows), true, false)
	require.Contains(t, b.String(), "gate [failed] (workflow root, attempt 0, 53 ms, exit 2)")
	require.Contains(t, b.String(), "stdout (tail):\nbuilding")
	require.Contains(t, b.String(), "stderr (tail):\n[REDACTED: credential-bearing diagnostic omitted]")
	require.NotContains(t, b.String(), "hidden")
	require.Contains(t, b.String(), "Older node attempts omitted")
}

func TestGetRunDoesNotReadForeignNodes(t *testing.T) {
	repo := &MockgetRunStore{}
	repo.GetChatFunc = func(_ context.Context, id string) (*db.Chat, error) {
		user := "owner"
		if id == "target" {
			user = "foreign"
		}
		return &db.Chat{ID: id, UserID: user}, nil
	}
	called := false
	repo.ListRunNodeExecutionsFunc = func(context.Context, string, int) ([]*db.StepExecution, error) { called = true; return nil, nil }
	resp, err := (&getRunTool{repo: repo}).Execute(callerRC("caller", "tc"), GetRunParams{RunID: "target"})
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.False(t, called)
}

func TestGetRunExecuteIncludesNodeMetadata(t *testing.T) {
	root := "root"
	repo := &MockgetRunStore{}
	repo.GetChatFunc = func(_ context.Context, id string) (*db.Chat, error) {
		return &db.Chat{ID: id, UserID: "owner", WorkflowID: &root}, nil
	}
	repo.GetLatestContextWindowFunc = func(context.Context, string) (*db.ContextWindow, error) { return nil, sql.ErrNoRows }
	repo.ListRunNodeExecutionsFunc = func(_ context.Context, id string, limit int) ([]*db.StepExecution, error) {
		require.Equal(t, root, id)
		return []*db.StepExecution{{ID: "step", StepID: "gate", ActivityName: "ExecuteRunStep", ExitCode: sql.NullInt64{Int64: 2, Valid: true}, OutputJSON: sql.NullString{String: `{"stderr":"token=hidden"}`, Valid: true}}}, nil
	}
	resp, err := (&getRunTool{repo: repo, threads: threads.NewService(repo)}).Execute(callerRC("caller", "tc"), GetRunParams{RunID: "target"})
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)
	var detail RunDetail
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &detail))
	require.Len(t, detail.Nodes, 1)
	require.Equal(t, "failed", detail.Nodes[0].Status)
	require.NotContains(t, resp.Metadata, "hidden")
	require.Contains(t, resp.Content, "Nodes (newest attempts first)")
}

// A run parked on a usage limit has an ACTIVE root, so its state alone reads
// "running"; get_run must say what it is waiting for and that it can be
// cancelled (prod chat 10c43cfa sat in that wait for 2.5h).
func TestGetRunReportsAUsageLimitWait(t *testing.T) {
	root := "root"
	activity := int(pb.ChatActivity_CHAT_ACTIVITY_WAITING_FOR_USAGE_LIMIT)
	resumes := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	repo := &MockgetRunStore{}
	repo.GetChatFunc = func(_ context.Context, id string) (*db.Chat, error) {
		return &db.Chat{ID: id, UserID: "owner", WorkflowID: &root, Activity: &activity,
			UsageLimitProvider: "Claude subscription", UsageLimitResumesAt: &resumes,
			RootStatus: db.WorkflowStatus{State: db.WorkflowStateActive}}, nil
	}
	repo.GetLatestContextWindowFunc = func(context.Context, string) (*db.ContextWindow, error) { return nil, sql.ErrNoRows }
	repo.ListRunNodeExecutionsFunc = func(context.Context, string, int) ([]*db.StepExecution, error) { return nil, nil }
	resp, err := (&getRunTool{repo: repo, threads: threads.NewService(repo)}).Execute(callerRC("caller", "tc"), GetRunParams{RunID: "target"})
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)
	require.Contains(t, resp.Content, "State: running\n")
	require.Contains(t, resp.Content, "Waiting: waiting for usage limit (Claude subscription), retries by itself at 2026-10-28T09:00:00Z. It is live: control_run cancel stops it")
	var detail RunDetail
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &detail))
	require.Equal(t, "waiting for usage limit", detail.Waiting)
	require.Equal(t, "2026-10-28T09:00:00Z", detail.UsageLimitRetriesAt)
}

// Bugs fe84af3c / eef49267: release-forge run 5104d4ee failed in the second it
// was created, with no node attempts, and get_run said only "State failed, No
// recorded nodes yet". A failed run reports the reason recorded on the run
// itself, and says the run never reached a node.
func TestGetRunReportsWhyARunFailedBeforeItsFirstNode(t *testing.T) {
	root := "root"
	reason := "workflow input validation failed: validation failed with 1 error(s):\n  1. release-forge.inputs.asap: input 'asap' expects boolean, got string \"true\""
	repo := &MockgetRunStore{}
	repo.GetChatFunc = func(_ context.Context, id string) (*db.Chat, error) {
		return &db.Chat{ID: id, UserID: "owner", WorkflowID: &root,
			RootStatus: db.WorkflowStatus{State: db.WorkflowStateStopped, StopReason: db.StopReasonFailed}}, nil
	}
	repo.GetWorkflowFunc = func(_ context.Context, id string) (*db.Workflow, error) {
		require.Equal(t, root, id)
		return &db.Workflow{ID: id, FailureDetail: &reason}, nil
	}
	repo.GetLatestContextWindowFunc = func(context.Context, string) (*db.ContextWindow, error) { return nil, sql.ErrNoRows }
	repo.ListRunNodeExecutionsFunc = func(context.Context, string, int) ([]*db.StepExecution, error) { return nil, nil }

	resp, err := (&getRunTool{repo: repo, threads: threads.NewService(repo)}).Execute(callerRC("caller", "tc"), GetRunParams{RunID: "target"})
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)
	require.Contains(t, resp.Content, "State: failed\nFailure: workflow input validation failed")
	require.Contains(t, resp.Content, "input 'asap' expects boolean")
	require.Contains(t, resp.Content, "None: the run failed before its first node ran")
	var detail RunDetail
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &detail))
	require.Equal(t, reason, detail.Failure)
}

func TestRunNodesEmptyIsExplicit(t *testing.T) {
	var b strings.Builder
	writeRunNodes(&b, []*pb.RunNodeExecution{}, false, false)
	require.Contains(t, b.String(), "No recorded nodes yet")
}

// Bug 938cc4e2: a run that went through an inline sub-workflow and then ended
// at an `outcome: failure` save_message showed only the sub-workflow's
// children and a generic failure. Every node attempt is listed — the nested
// children by path, and the node after the sub-workflow with the redacted text
// it wrote as the error.
func TestGetRunShowsNestedNodesAndTheDeclaringNode(t *testing.T) {
	rows := []*db.StepExecution{
		{ID: "s", WorkflowID: "root", StepID: "stopped", ActivityName: "SaveMessage", Attempt: sql.NullInt32{Int32: 1, Valid: true},
			Success:      sql.NullBool{Bool: false, Valid: true},
			ErrorMessage: sql.NullString{String: "declared the run's outcome: failure", Valid: true},
			OutputJSON:   sql.NullString{String: `{"message":"Review did not finish: Bearer abcdefghijklmnop"}`, Valid: true}},
		{ID: "c", WorkflowID: "root", StepID: "confirm", ActivityName: "GitHubAction", Success: sql.NullBool{Bool: true, Valid: true}},
		{ID: "l", WorkflowID: "root", StepID: "call_llm", NodePath: sql.NullString{String: "review.agent_loop.call_llm", Valid: true},
			ActivityName: "CallLLM", Success: sql.NullBool{Bool: true, Valid: true}, LoopIteration: sql.NullInt64{Int64: 3, Valid: true}},
	}
	var b strings.Builder
	writeRunNodes(&b, rundiagnostics.Nodes(rows), false, false)
	out := b.String()
	require.Contains(t, out, "- stopped [failed]")
	require.Contains(t, out, "Error: declared the run's outcome: failure: Review did not finish: Bearer [REDACTED]")
	require.NotContains(t, out, "abcdefghijklmnop")
	require.Contains(t, out, "- confirm [completed]")
	require.Contains(t, out, "- review.agent_loop.call_llm [completed] (workflow root, attempt 0, iteration 3)")
}
