// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
)

type runListFixture struct {
	svc    *RunService
	repo   *db.Repo
	ctx    context.Context
	userID string
}

func newRunListFixture(t *testing.T) *runListFixture {
	t.Helper()
	repo := db.NewTestRepo(t)
	userID := uuid.NewString()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	now := time.Now().UTC()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: "p-" + userID, UserID: userID, Name: "proj", Path: t.TempDir(),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	return &runListFixture{svc: NewRunService(repo, nil), repo: repo, ctx: ctx, userID: userID}
}

func (f *runListFixture) project() string { return "p-" + f.userID }

func (f *runListFixture) seed(t *testing.T, id, workflow string, status db.WorkflowStatus, at time.Time) {
	t.Helper()
	workflowID := id
	require.NoError(t, f.repo.CreateChat(f.ctx, &db.Chat{
		ID: id, Title: "run " + id, ProjectID: f.project(), UserID: f.userID,
		WorkflowName: &workflow, WorkflowID: &workflowID, State: db.ChatStateIdle,
		CreatedAt: at, UpdatedAt: at, LastActive: at,
	}))
	_, err := f.repo.CreateThread(f.ctx, &db.Thread{ID: id, ChatID: id, CreatedAt: at})
	require.NoError(t, err)
	require.NoError(t, f.repo.CreateWorkflow(f.ctx, &db.Workflow{
		ID: id, ChatID: id, WorkflowName: workflow, Thread: id, Status: status, CreatedAt: at,
	}))
}

func (f *runListFixture) list(t *testing.T, req *reliantv1.ListRunsRequest) *reliantv1.ListRunsResponse {
	t.Helper()
	resp, err := f.svc.ListRuns(f.ctx, connect.NewRequest(req))
	require.NoError(t, err)
	return resp.Msg
}

func runIDs(runs []*reliantv1.Run) []string {
	ids := make([]string, len(runs))
	for i, r := range runs {
		ids[i] = r.Id
	}
	return ids
}

func TestListRuns_CrossCuttingPagesByTokenWithoutGapsOrDuplicates(t *testing.T) {
	f := newRunListFixture(t)
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	for i := 0; i < 5; i++ {
		// Pairs share a created_at, so page boundaries fall inside ties.
		f.seed(t, fmt.Sprintf("r%d", i), "wf", db.Completed(), base.Add(time.Duration(i/2)*time.Second))
	}

	var got []string
	req := &reliantv1.ListRunsRequest{Limit: 2}
	for page := 0; page < 10; page++ {
		resp := f.list(t, req)
		got = append(got, runIDs(resp.Runs)...)
		if resp.NextPageToken == "" {
			break
		}
		tok := resp.NextPageToken
		req = &reliantv1.ListRunsRequest{Limit: 2, PageToken: &tok}
	}
	assert.Equal(t, []string{"r4", "r3", "r2", "r1", "r0"}, got)
}

func TestListRuns_CrossCuttingLastPageHasNoToken(t *testing.T) {
	f := newRunListFixture(t)
	f.seed(t, "only", "wf", db.Active(), time.Now().UTC())
	resp := f.list(t, &reliantv1.ListRunsRequest{Limit: 1})
	assert.Len(t, resp.Runs, 1)
	assert.Empty(t, resp.NextPageToken)
}

func TestListRuns_CrossCuttingNeverReturnsAnotherUsersRuns(t *testing.T) {
	f := newRunListFixture(t)
	f.seed(t, "mine", "wf", db.Active(), time.Now().UTC())

	// A foreign user's run in the SAME database.
	foreignUser := uuid.NewString()
	foreignProject := "p-" + foreignUser
	foreignCtx := context.WithValue(context.Background(), auth.UserIDContextKey, foreignUser)
	now := time.Now().UTC()
	require.NoError(t, f.repo.CreateProject(foreignCtx, &db.Project{
		ID: foreignProject, UserID: foreignUser, Name: "theirs", Path: t.TempDir(),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	wf, workflowID := "wf", "theirs"
	require.NoError(t, f.repo.CreateChat(foreignCtx, &db.Chat{
		ID: "theirs", Title: "x", ProjectID: foreignProject, UserID: foreignUser,
		WorkflowName: &wf, WorkflowID: &workflowID, State: db.ChatStateIdle,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))

	assert.Equal(t, []string{"mine"}, runIDs(f.list(t, &reliantv1.ListRunsRequest{}).Runs))

	// Naming the other user's project does not widen scope.
	resp := f.list(t, &reliantv1.ListRunsRequest{ProjectId: &foreignProject})
	assert.Empty(t, resp.Runs)
}

func TestListRuns_CrossCuttingFiltersAndNewFields(t *testing.T) {
	f := newRunListFixture(t)
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	f.seed(t, "ok", "wf-a", db.Completed(), base)
	f.seed(t, "bad", "wf-b", db.Failed(), base.Add(time.Minute))
	f.seed(t, "live", "wf-a", db.Active(), base.Add(2*time.Minute))

	resp := f.list(t, &reliantv1.ListRunsRequest{
		DisplayStates: []reliantv1.RunDisplayState{reliantv1.RunDisplayState_RUN_DISPLAY_STATE_FAILED},
	})
	require.Len(t, resp.Runs, 1)
	bad := resp.Runs[0]
	assert.Equal(t, "bad", bad.Id)
	assert.Equal(t, "run bad", bad.Title)
	assert.Equal(t, f.project(), bad.ProjectId)
	assert.Equal(t, reliantv1.RunDisplayState_RUN_DISPLAY_STATE_FAILED, bad.DisplayState)
	assert.Equal(t, reliantv1.WorkflowState_WORKFLOW_STATE_STOPPED, bad.State)

	assert.Equal(t, []string{"live", "ok"}, runIDs(f.list(t, &reliantv1.ListRunsRequest{Workflow: []string{"wf-a"}}).Runs))
	after := timestamppb.New(base.Add(time.Minute))
	assert.Equal(t, []string{"live", "bad"}, runIDs(f.list(t, &reliantv1.ListRunsRequest{StartedAfter: after}).Runs))
	q := "RUN OK"
	assert.Equal(t, []string{"ok"}, runIDs(f.list(t, &reliantv1.ListRunsRequest{Query: &q}).Runs))
}

func TestListRuns_RejectsBadInput(t *testing.T) {
	f := newRunListFixture(t)
	bad := "not-a-token!!"
	_, err := f.svc.ListRuns(f.ctx, connect.NewRequest(&reliantv1.ListRunsRequest{PageToken: &bad}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	_, err = f.svc.ListRuns(f.ctx, connect.NewRequest(&reliantv1.ListRunsRequest{
		DisplayStates: []reliantv1.RunDisplayState{reliantv1.RunDisplayState_RUN_DISPLAY_STATE_UNSPECIFIED},
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestListRuns_LimitIsCapped(t *testing.T) {
	f := newRunListFixture(t)
	base := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < 3; i++ {
		f.seed(t, fmt.Sprintf("c%d", i), "wf", db.Completed(), base.Add(time.Duration(i)*time.Second))
	}
	assert.Len(t, f.list(t, &reliantv1.ListRunsRequest{}).Runs, 3, "default limit covers a small list")
	assert.Len(t, f.list(t, &reliantv1.ListRunsRequest{Limit: 100000}).Runs, 3)
}

func TestLastRunPerWorkflowRPC(t *testing.T) {
	f := newRunListFixture(t)
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	f.seed(t, "a-old", "wf-a", db.Completed(), base)
	f.seed(t, "a-new", "wf-a", db.Failed(), base.Add(time.Minute))
	f.seed(t, "b-only", "wf-b", db.Active(), base)

	resp, err := f.svc.LastRunPerWorkflow(f.ctx, connect.NewRequest(&reliantv1.LastRunPerWorkflowRequest{}))
	require.NoError(t, err)
	assert.Equal(t, []string{"a-new", "b-only"}, runIDs(resp.Msg.Runs))
	assert.Equal(t, reliantv1.RunDisplayState_RUN_DISPLAY_STATE_FAILED, resp.Msg.Runs[0].DisplayState)
}

func TestRunCursorRoundTrip(t *testing.T) {
	in := db.RunCursor{CreatedAt: time.Date(2026, 3, 4, 5, 6, 7, 123456000, time.UTC), ChatID: "chat|with|pipes"}
	out, err := decodeRunCursor(encodeRunCursor(in))
	require.NoError(t, err)
	assert.True(t, in.CreatedAt.Equal(out.CreatedAt))
	assert.Equal(t, in.ChatID, out.ChatID)

	_, err = decodeRunCursor("")
	assert.Error(t, err)
}
