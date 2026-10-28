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
	writeRunNodes(&b, rundiagnostics.Nodes(rows), true)
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

func TestRunNodesEmptyIsExplicit(t *testing.T) {
	var b strings.Builder
	writeRunNodes(&b, []*pb.RunNodeExecution{}, false)
	require.Contains(t, b.String(), "No recorded nodes yet")
}
