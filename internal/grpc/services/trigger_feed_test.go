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

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// seedFiring records a schedule firing; when status is non-nil it also creates
// the launched chat and its root workflow, so the run state is real.
func (e *triggerTestEnv) seedFiring(t *testing.T, triggerID string, at time.Time, outcome core.TriggerEventOutcome, status *db.WorkflowStatus) string {
	t.Helper()
	ctx := context.Background()
	eventID := uuid.NewString()
	ev := &core.TriggerEvent{
		ID: eventID, TriggerID: &triggerID, UserID: e.userID, Kind: core.TriggerEventKindSchedule,
		DedupeKey: uuid.NewString(), OccurredAt: at, Payload: map[string]any{},
		Outcome: outcome, OutcomeDetail: string(outcome) + " detail", CreatedAt: at,
	}
	if status != nil {
		chatID := uuid.NewString()
		workflow := "builtin://agent"
		require.NoError(t, e.repo.CreateChat(ctx, &db.Chat{
			ID: chatID, Title: "run " + chatID[:6], ProjectID: e.projectID, UserID: e.userID,
			WorkflowName: &workflow, WorkflowID: &chatID, State: db.ChatStateIdle,
			CreatedAt: at, UpdatedAt: at, LastActive: at,
		}))
		_, err := e.repo.CreateThread(ctx, &db.Thread{ID: chatID, ChatID: chatID, CreatedAt: at})
		require.NoError(t, err)
		wf := &db.Workflow{ID: chatID, ChatID: chatID, WorkflowName: workflow, Thread: chatID, Status: *status, CreatedAt: at}
		if status.State == core.WorkflowStateStopped {
			done := at.Add(time.Minute)
			wf.CompletedAt = &done
		}
		require.NoError(t, e.repo.CreateWorkflow(ctx, wf))
		ev.ChatID = &chatID
	}
	created, err := e.repo.CreateTriggerEvent(ctx, ev)
	require.NoError(t, err)
	require.True(t, created)
	return eventID
}

func (e *triggerTestEnv) listEvents(t *testing.T, req *reliantv1.ListTriggerEventsRequest) *reliantv1.ListTriggerEventsResponse {
	t.Helper()
	resp, err := e.svc.ListTriggerEvents(e.ctx, connect.NewRequest(req))
	require.NoError(t, err)
	return resp.Msg
}

func TestListTriggerEventsPagesWithTiedOccurredAt(t *testing.T) {
	env := setupTriggerTest(t)
	trig := env.create(t, env.definition(nil))
	tied := time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC)
	want := map[string]bool{}
	for i := 0; i < 5; i++ {
		want[env.seedFiring(t, trig.Id, tied, core.TriggerEventSkipped, nil)] = true
	}

	seen := map[string]int{}
	var token *string
	pages := 0
	for {
		resp := env.listEvents(t, &reliantv1.ListTriggerEventsRequest{TriggerId: trig.Id, Limit: 2, PageToken: token})
		for _, ev := range resp.Events {
			seen[ev.Id]++
		}
		pages++
		if resp.NextPageToken == "" {
			break
		}
		next := resp.NextPageToken
		token = &next
		require.Less(t, pages, 10)
	}
	assert.Equal(t, 3, pages)
	assert.Len(t, seen, 5)
	for id, n := range seen {
		assert.True(t, want[id])
		assert.Equal(t, 1, n, "event %s must appear exactly once", id)
	}

	bad := "not-a-token"
	_, err := env.svc.ListTriggerEvents(env.ctx, connect.NewRequest(&reliantv1.ListTriggerEventsRequest{TriggerId: trig.Id, PageToken: &bad}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestListTriggerEventsFiltersByOutcomeAndCarriesRun(t *testing.T) {
	env := setupTriggerTest(t)
	trig := env.create(t, env.definition(nil))
	base := time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC)
	failedRun := db.Failed()
	doneRun := db.Completed()
	env.seedFiring(t, trig.Id, base, core.TriggerEventLaunched, &doneRun)
	env.seedFiring(t, trig.Id, base.Add(time.Hour), core.TriggerEventLaunched, &failedRun)
	env.seedFiring(t, trig.Id, base.Add(2*time.Hour), core.TriggerEventSkipped, nil)

	all := env.listEvents(t, &reliantv1.ListTriggerEventsRequest{TriggerId: trig.Id})
	require.Len(t, all.Events, 3)
	assert.Nil(t, all.Events[0].Run, "a skipped firing has no run")
	require.NotNil(t, all.Events[1].Run)
	assert.Equal(t, reliantv1.RunDisplayState_RUN_DISPLAY_STATE_FAILED, all.Events[1].Run.DisplayState)
	assert.Equal(t, reliantv1.WorkflowStopReason_WORKFLOW_STOP_REASON_FAILED, all.Events[1].Run.StopReason)
	assert.NotEmpty(t, all.Events[1].Run.Title)
	assert.Equal(t, reliantv1.RunDisplayState_RUN_DISPLAY_STATE_COMPLETED, all.Events[2].Run.DisplayState)

	only := env.listEvents(t, &reliantv1.ListTriggerEventsRequest{
		TriggerId: trig.Id,
		Outcomes:  []reliantv1.TriggerEventOutcome{reliantv1.TriggerEventOutcome_TRIGGER_EVENT_OUTCOME_SKIPPED},
	})
	require.Len(t, only.Events, 1)
	assert.Equal(t, reliantv1.TriggerEventOutcome_TRIGGER_EVENT_OUTCOME_SKIPPED, only.Events[0].Outcome)

	_, err := env.svc.ListTriggerEvents(env.ctx, connect.NewRequest(&reliantv1.ListTriggerEventsRequest{
		TriggerId: trig.Id, Outcomes: []reliantv1.TriggerEventOutcome{reliantv1.TriggerEventOutcome_TRIGGER_EVENT_OUTCOME_UNSPECIFIED},
	}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestListTriggersCarriesNamesHealthAndLastRun(t *testing.T) {
	env := setupTriggerTest(t)
	host := "build-box"
	require.NoError(t, env.repo.UpsertDaemon(context.Background(), &db.Daemon{ID: env.daemonID, UserID: env.userID, Hostname: &host}))

	failing := env.create(t, env.definition(nil))
	healthy := env.create(t, env.definition(func(d *reliantv1.TriggerDefinition) { d.Name = "healthy one" }))
	fresh := env.create(t, env.definition(func(d *reliantv1.TriggerDefinition) { d.Name = "never fired" }))

	base := time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC)
	died := db.Failed()
	done := db.Completed()
	// Launched, then the run failed, twice: failing without any launch error.
	env.seedFiring(t, failing.Id, base, core.TriggerEventLaunched, &died)
	env.seedFiring(t, failing.Id, base.Add(time.Hour), core.TriggerEventLaunched, &died)
	env.seedFiring(t, healthy.Id, base, core.TriggerEventLaunched, &done)

	resp, err := env.svc.ListTriggers(env.ctx, connect.NewRequest(&reliantv1.ListTriggersRequest{}))
	require.NoError(t, err)
	byID := map[string]*reliantv1.Trigger{}
	for _, tr := range resp.Msg.Triggers {
		byID[tr.Id] = tr
	}
	require.Len(t, byID, 3)

	for _, tr := range byID {
		assert.Equal(t, "Test Project", tr.ProjectName)
		assert.Equal(t, "build-box", tr.DaemonName)
	}

	f := byID[failing.Id]
	assert.Equal(t, reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_FAILING, f.Health.Status)
	assert.Equal(t, int32(2), f.Health.ConsecutiveFailures)
	assert.NotEmpty(t, f.Health.LastFailureDetail)
	require.NotNil(t, f.LastEvent)
	require.NotNil(t, f.LastEvent.Run, "last_event must carry the launched run")
	assert.Equal(t, reliantv1.RunDisplayState_RUN_DISPLAY_STATE_FAILED, f.LastEvent.Run.DisplayState)

	h := byID[healthy.Id]
	assert.Equal(t, reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_HEALTHY, h.Health.Status)
	assert.Equal(t, reliantv1.RunDisplayState_RUN_DISPLAY_STATE_COMPLETED, h.LastEvent.Run.DisplayState)

	n := byID[fresh.Id]
	assert.Equal(t, reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_UNKNOWN, n.Health.Status)
	assert.Nil(t, n.LastEvent)

	got, err := env.svc.GetTrigger(env.ctx, connect.NewRequest(&reliantv1.GetTriggerRequest{Id: failing.Id}))
	require.NoError(t, err)
	assert.Equal(t, "build-box", got.Msg.Trigger.DaemonName)
	assert.Equal(t, reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_FAILING, got.Msg.Trigger.Health.Status)
}

func TestListTriggersUsesConstantQueries(t *testing.T) {
	// ListTriggers must not do per-trigger work against the database.
	env := setupTriggerTest(t)
	for i := 0; i < 6; i++ {
		trig := env.create(t, env.definition(func(d *reliantv1.TriggerDefinition) { d.Name = fmt.Sprintf("t%d", i) }))
		done := db.Completed()
		env.seedFiring(t, trig.Id, time.Now().UTC(), core.TriggerEventLaunched, &done)
	}
	counting := &triggerQueryCounter{Repository: env.repo}
	svc := NewTriggerService(counting, env.backend, env.backend)
	resp, err := svc.ListTriggers(env.ctx, connect.NewRequest(&reliantv1.ListTriggersRequest{}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.Triggers, 6)
	assert.Equal(t, 1, counting.recent, "one batched firings query, not one per trigger")
	assert.Equal(t, 0, counting.latest)
}

type triggerQueryCounter struct {
	db.Repository
	recent, latest int
}

func (c *triggerQueryCounter) RecentTriggerFirings(ctx context.Context, userID string, ids []string, n int) (map[string][]*core.TriggerEventWithRun, error) {
	c.recent++
	return c.Repository.RecentTriggerFirings(ctx, userID, ids, n)
}

func (c *triggerQueryCounter) GetLatestTriggerEvent(ctx context.Context, id string, o *core.TriggerEventOutcome) (*core.TriggerEvent, error) {
	c.latest++
	return c.Repository.GetLatestTriggerEvent(ctx, id, o)
}

func TestTriggerFeedHidesOtherUsersData(t *testing.T) {
	env := setupTriggerTest(t)
	trig := env.create(t, env.definition(nil))
	env.seedFiring(t, trig.Id, time.Now().UTC(), core.TriggerEventSkipped, nil)

	otherCtx := context.WithValue(context.Background(), auth.UserIDContextKey, uuid.NewString())
	_, err := env.svc.ListTriggerEvents(otherCtx, connect.NewRequest(&reliantv1.ListTriggerEventsRequest{TriggerId: trig.Id}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	list, err := env.svc.ListTriggers(otherCtx, connect.NewRequest(&reliantv1.ListTriggersRequest{}))
	require.NoError(t, err)
	assert.Empty(t, list.Msg.Triggers)
}
