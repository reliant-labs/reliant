// Copyright (c) 2025 Reliant Labs
package run

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

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/ptr"
)

type runListFixture struct {
	svc    *Service
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
	// The harness stubs Chats: listing never reaches the chat service.
	svc := NewTestRun(t, WithRunDeps(Deps{Repo: repo}))
	return &runListFixture{svc: svc, repo: repo, ctx: ctx, userID: userID}
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
	assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))

	_, err = f.svc.ListRuns(f.ctx, connect.NewRequest(&reliantv1.ListRunsRequest{
		DisplayStates: []reliantv1.RunDisplayState{reliantv1.RunDisplayState_RUN_DISPLAY_STATE_UNSPECIFIED},
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
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

func TestListRuns_ParentChatIDFilterAndFields(t *testing.T) {
	f := newRunListFixture(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	f.seed(t, "parent", "builtin://agent", db.Active(), now)
	f.seed(t, "kid", "builtin://agent", db.Active(), now.Add(time.Second))
	f.seed(t, "stranger", "builtin://agent", db.Active(), now.Add(2*time.Second))
	_, err := f.repo.CreateTriggerEvent(f.ctx, &core.TriggerEvent{
		ID: "evt-kid", UserID: f.userID, Kind: core.TriggerEventKindAgentStartRun, DedupeKey: "d-kid",
		OccurredAt: now, Payload: map[string]any{"parent_chat_id": "parent"},
		Outcome: core.TriggerEventLaunched, ChatID: ptr.Of("kid"), CreatedAt: now,
	})
	require.NoError(t, err)

	parent := "parent"
	resp := f.list(t, &reliantv1.ListRunsRequest{ParentChatId: &parent})
	require.Equal(t, []string{"kid"}, runIDs(resp.Runs))
	assert.Equal(t, "parent", resp.Runs[0].GetParentChatId())
	assert.Equal(t, "run parent", resp.Runs[0].GetParentChatTitle())

	all := f.list(t, &reliantv1.ListRunsRequest{})
	for _, run := range all.Runs {
		if run.Id != "kid" {
			assert.Nil(t, run.ParentChatId, "only agent-started runs carry a parent")
		}
	}
}

// A builder test run is scratch work: the default Runs list leaves it out and
// the "Tests" kind filter shows only those (WORKFLOW_UI.md §13 G6).
func TestListRuns_BuilderTestsHiddenUnlessFilteredByKind(t *testing.T) {
	f := newRunListFixture(t)
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	f.seed(t, "real", "wf-a", db.Completed(), base)
	f.seed(t, "test", "wf-a", db.Completed(), base.Add(time.Minute))
	_, err := f.repo.CreateTriggerEvent(f.ctx, &core.TriggerEvent{
		ID: "evt-test", UserID: f.userID, Kind: core.TriggerEventKindBuilderTest, DedupeKey: "test",
		OccurredAt: base, Outcome: core.TriggerEventLaunched, ChatID: ptr.Of("test"),
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"real"}, runIDs(f.list(t, &reliantv1.ListRunsRequest{}).Runs))
	tests := f.list(t, &reliantv1.ListRunsRequest{LaunchKind: []string{"builder.test"}}).Runs
	assert.Equal(t, []string{"test"}, runIDs(tests))
	assert.Equal(t, "builder.test", tests[0].LaunchKind)
}

// An automation archives each run it finishes successfully, so archived runs
// are history the Runs page asks for (include_archived), each marked as such,
// and they still count as their workflow's last run.
func TestListRuns_ArchivedRunsAreListedOnRequestAndMarked(t *testing.T) {
	f := newRunListFixture(t)
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	f.seed(t, "kept", "wf-a", db.Completed(), base)
	f.seed(t, "archived", "wf-a", db.Completed(), base.Add(time.Minute))
	require.NoError(t, f.repo.UpdateChatState(f.ctx, "archived", db.ChatStateArchived, "automation_succeeded"))

	assert.Equal(t, []string{"kept"}, runIDs(f.list(t, &reliantv1.ListRunsRequest{}).Runs))

	all := f.list(t, &reliantv1.ListRunsRequest{IncludeArchived: ptr.Of(true)}).Runs
	require.Equal(t, []string{"archived", "kept"}, runIDs(all))
	assert.True(t, all[0].Archived)
	assert.False(t, all[1].Archived)

	last, err := f.svc.LastRunPerWorkflow(f.ctx, connect.NewRequest(&reliantv1.LastRunPerWorkflowRequest{}))
	require.NoError(t, err)
	require.Equal(t, []string{"archived"}, runIDs(last.Msg.Runs), "an archived run is still the workflow's last run")
	assert.True(t, last.Msg.Runs[0].Archived)
}

// The run log: a trigger firing that started no run is listed beside the
// runs, in one time order, and paging walks both without losing or
// repeating a row (WORKFLOW_UI.md §5, "why didn't last night's run happen").
func TestListRuns_UnlaunchedEventsInterleaveAndPage(t *testing.T) {
	f := newRunListFixture(t)
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	triggerID := "trg-nightly"
	require.NoError(t, f.repo.CreateTrigger(f.ctx, &core.Trigger{
		ID: triggerID, UserID: f.userID, ProjectID: f.project(),
		Name: "nightly", Kind: core.TriggerKindSchedule, Enabled: true, Workflow: "wf-a",
		Config: []byte(`{"interval":"1h"}`), CreatedAt: base, UpdatedAt: base,
	}))
	event := func(id string, outcome core.TriggerEventOutcome, at time.Time, chatID *string) {
		t.Helper()
		_, err := f.repo.CreateTriggerEvent(f.ctx, &core.TriggerEvent{
			ID: id, TriggerID: &triggerID, UserID: f.userID, Kind: core.TriggerEventKindSchedule,
			DedupeKey: id, OccurredAt: at, Outcome: outcome, OutcomeDetail: "detail " + id, ChatID: chatID, CreatedAt: at,
		})
		require.NoError(t, err)
	}
	f.seed(t, "run-1", "wf-a", db.Completed(), base)
	event("evt-skip", core.TriggerEventSkipped, base.Add(1*time.Minute), nil)
	f.seed(t, "run-2", "wf-a", db.Completed(), base.Add(2*time.Minute))
	event("evt-fail", core.TriggerEventFailed, base.Add(3*time.Minute), nil)
	event("evt-launched", core.TriggerEventLaunched, base.Add(4*time.Minute), ptr.Of("run-2"))

	include := ptr.Of(true)
	without := f.list(t, &reliantv1.ListRunsRequest{})
	assert.Empty(t, without.UnlaunchedEvents, "events only on request")

	all := f.list(t, &reliantv1.ListRunsRequest{IncludeUnlaunchedEvents: include})
	assert.Equal(t, []string{"run-2", "run-1"}, runIDs(all.Runs))
	require.Len(t, all.UnlaunchedEvents, 2, "a launched firing is its run, not an extra row")
	assert.Equal(t, "evt-fail", all.UnlaunchedEvents[0].Id)
	assert.Equal(t, reliantv1.UnlaunchedTriggerEventOutcome_UNLAUNCHED_TRIGGER_EVENT_OUTCOME_FAILED, all.UnlaunchedEvents[0].Outcome)
	assert.Equal(t, "nightly", all.UnlaunchedEvents[0].TriggerName)
	assert.Equal(t, "wf-a", all.UnlaunchedEvents[0].WorkflowName)
	assert.Equal(t, f.project(), all.UnlaunchedEvents[0].ProjectId)
	assert.Equal(t, "schedule", all.UnlaunchedEvents[0].LaunchKind)
	assert.Equal(t, "detail evt-fail", all.UnlaunchedEvents[0].OutcomeDetail)
	assert.Equal(t, "evt-skip", all.UnlaunchedEvents[1].Id)
	assert.Empty(t, all.NextPageToken)

	// Two rows a page: fail+run-2, then skip+run-1 — in time order, no gaps.
	var seen []string
	var token *string
	for page := 0; page < 4; page++ {
		resp := f.list(t, &reliantv1.ListRunsRequest{IncludeUnlaunchedEvents: include, Limit: 2, PageToken: token})
		require.LessOrEqual(t, len(resp.Runs)+len(resp.UnlaunchedEvents), 2)
		for _, e := range resp.UnlaunchedEvents {
			seen = append(seen, e.Id)
		}
		seen = append(seen, runIDs(resp.Runs)...)
		if resp.NextPageToken == "" {
			break
		}
		token = ptr.Of(resp.NextPageToken)
	}
	assert.ElementsMatch(t, []string{"evt-fail", "run-2", "evt-skip", "run-1"}, seen)
	assert.Len(t, seen, 4, "no row repeats across pages")

	// A state or parent filter cannot match an event; the others narrow it.
	failed := f.list(t, &reliantv1.ListRunsRequest{IncludeUnlaunchedEvents: include,
		DisplayStates: []reliantv1.RunDisplayState{reliantv1.RunDisplayState_RUN_DISPLAY_STATE_FAILED}})
	assert.Empty(t, failed.UnlaunchedEvents)
	other := f.list(t, &reliantv1.ListRunsRequest{IncludeUnlaunchedEvents: include, LaunchKind: []string{"webhook"}})
	assert.Empty(t, other.UnlaunchedEvents)
	byTrigger := f.list(t, &reliantv1.ListRunsRequest{IncludeUnlaunchedEvents: include, TriggerId: &triggerID})
	assert.Len(t, byTrigger.UnlaunchedEvents, 2)
	byName := f.list(t, &reliantv1.ListRunsRequest{IncludeUnlaunchedEvents: include, Query: ptr.Of("NIGHT")})
	assert.Len(t, byName.UnlaunchedEvents, 2)
}

func TestMergeRunLog(t *testing.T) {
	at := func(m int) time.Time { return time.Date(2026, 1, 1, 0, m, 0, 0, time.UTC) }
	run := func(id string, m int) *db.RunListItem { return &db.RunListItem{ChatID: id, CreatedAt: at(m)} }
	ev := func(id string, m int) *db.UnlaunchedTriggerEvent {
		return &db.UnlaunchedTriggerEvent{ID: id, OccurredAt: at(m)}
	}

	t.Run("everything fits: no cursor", func(t *testing.T) {
		runs, events, next := mergeRunLog([]*db.RunListItem{run("r2", 4), run("r1", 1)}, false, []*db.UnlaunchedTriggerEvent{ev("e1", 3)}, false, 5)
		assert.Len(t, runs, 2)
		assert.Len(t, events, 1)
		assert.Nil(t, next)
	})
	t.Run("cut at limit: cursor is the last row kept", func(t *testing.T) {
		runs, events, next := mergeRunLog([]*db.RunListItem{run("r2", 4), run("r1", 1)}, false, []*db.UnlaunchedTriggerEvent{ev("e1", 3)}, false, 2)
		require.Len(t, runs, 1)
		require.Len(t, events, 1)
		require.NotNil(t, next)
		assert.Equal(t, db.RunCursor{CreatedAt: at(3), ChatID: "e1"}, *next)
	})
	t.Run("a source with more rows keeps the page open", func(t *testing.T) {
		_, _, next := mergeRunLog([]*db.RunListItem{run("r1", 4)}, true, nil, false, 5)
		require.NotNil(t, next)
		assert.Equal(t, "r1", next.ChatID)
	})
	t.Run("equal times order by id, as both queries do", func(t *testing.T) {
		runs, events, _ := mergeRunLog([]*db.RunListItem{run("b", 2)}, false, []*db.UnlaunchedTriggerEvent{ev("c", 2), ev("a", 2)}, false, 1)
		assert.Empty(t, runs)
		require.Len(t, events, 1)
		assert.Equal(t, "c", events[0].ID)
	})
}
