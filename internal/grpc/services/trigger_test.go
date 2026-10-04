// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"errors"
	"sync"
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
	"github.com/reliant-labs/reliant/internal/triggers"
)

// fakeTriggerBackend stands in for the Temporal schedule backend. The syncer's
// own contract with Temporal is tested in internal/triggers against a real dev
// server; what matters here is what the HANDLER does with its results.
type fakeTriggerBackend struct {
	mu sync.Mutex

	synced  []*core.Trigger
	deleted []string
	fires   []string

	syncErr   error
	deleteErr error
	fireErr   error
	nextFire  *time.Time
}

func (b *fakeTriggerBackend) Sync(_ context.Context, t *core.Trigger) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.syncErr != nil {
		return b.syncErr
	}
	copied := *t
	b.synced = append(b.synced, &copied)
	return nil
}

func (b *fakeTriggerBackend) Delete(_ context.Context, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.deleteErr != nil {
		return b.deleteErr
	}
	b.deleted = append(b.deleted, id)
	return nil
}

func (b *fakeTriggerBackend) NextFireAt(_ context.Context, _ string) (*time.Time, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.nextFire, nil
}

func (b *fakeTriggerBackend) StartManualFire(_ context.Context, triggerID string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.fireErr != nil {
		return "", b.fireErr
	}
	b.fires = append(b.fires, triggerID)
	return "trigger-fire-" + triggerID + "-manual-abc", nil
}

func (b *fakeTriggerBackend) syncCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.synced)
}

// triggerTestEnv is a service over a real test database with a fake backend.
type triggerTestEnv struct {
	svc       *TriggerService
	backend   *fakeTriggerBackend
	repo      *db.Repo
	userID    string
	projectID string
	ctx       context.Context
}

func setupTriggerTest(t *testing.T) *triggerTestEnv {
	t.Helper()
	repo := db.NewTestRepo(t)
	backend := &fakeTriggerBackend{}

	userID := uuid.NewString()
	projectID := uuid.NewString()
	now := time.Now().UTC()
	require.NoError(t, repo.CreateProject(context.Background(), &db.Project{
		ID:         projectID,
		UserID:     userID,
		Name:       "Test Project",
		Path:       t.TempDir(),
		IsGitRepo:  true,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	return &triggerTestEnv{
		svc:       NewTriggerService(repo, backend, backend),
		backend:   backend,
		repo:      repo,
		userID:    userID,
		projectID: projectID,
		ctx:       context.WithValue(context.Background(), auth.UserIDContextKey, userID),
	}
}

func (e *triggerTestEnv) definition(mutate func(*reliantv1.TriggerDefinition)) *reliantv1.TriggerDefinition {
	def := &reliantv1.TriggerDefinition{
		Name:      "nightly audit",
		ProjectId: e.projectID,
		Workflow:  "builtin://agent",
		Message:   "Audit the dependency tree.",
		Source: &reliantv1.TriggerDefinition_Schedule{
			Schedule: &reliantv1.ScheduleSource{Cron: []string{"0 9 * * *"}},
		},
	}
	if mutate != nil {
		mutate(def)
	}
	return def
}

func (e *triggerTestEnv) create(t *testing.T, def *reliantv1.TriggerDefinition) *reliantv1.Trigger {
	t.Helper()
	resp, err := e.svc.CreateTrigger(e.ctx, connect.NewRequest(&reliantv1.CreateTriggerRequest{Trigger: def}))
	require.NoError(t, err)
	return resp.Msg.GetTrigger()
}

func TestCreateTriggerStoresAndSyncs(t *testing.T) {
	env := setupTriggerTest(t)

	fire := time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC)
	env.backend.nextFire = &fire

	got := env.create(t, env.definition(nil))

	assert.NotEmpty(t, got.GetId())
	assert.Equal(t, "nightly audit", got.GetName())
	assert.Equal(t, env.projectID, got.GetProjectId())
	// An omitted enabled means true: a create that does not mention it wants a
	// working trigger, not a disabled one.
	assert.True(t, got.GetEnabled(), "a create that omits enabled must store an ENABLED trigger")
	require.NotNil(t, got.GetNextFireAt())
	assert.Equal(t, "2026-01-02T09:00:00Z", got.GetNextFireAt())

	// The schedule defaults are rendered explicitly rather than echoed as
	// blanks the client would have to interpret.
	sched := got.GetSchedule()
	require.NotNil(t, sched)
	assert.Equal(t, []string{"0 9 * * *"}, sched.GetCron())
	assert.Equal(t, "UTC", sched.GetTimezone())
	assert.Equal(t, reliantv1.TriggerOverlapPolicy_TRIGGER_OVERLAP_POLICY_SKIP, sched.GetOverlap())
	assert.Equal(t, "10m0s", sched.GetCatchupWindow())

	stored, err := env.repo.GetTrigger(context.Background(), got.GetId())
	require.NoError(t, err)
	assert.Equal(t, env.userID, stored.UserID, "the trigger must be owned by the caller")
	assert.Equal(t, core.TriggerKindSchedule, stored.Kind)
	assert.Equal(t, 1, env.backend.syncCount(), "create must converge the schedule")
}

func TestCreateTriggerRespectsExplicitDisabled(t *testing.T) {
	env := setupTriggerTest(t)
	disabled := false
	got := env.create(t, env.definition(func(d *reliantv1.TriggerDefinition) { d.Enabled = &disabled }))
	assert.False(t, got.GetEnabled())
}

// A create whose schedule cannot be made is rolled back. Reporting success
// would leave a trigger the owner cannot distinguish from a working one.
func TestCreateTriggerRollsBackWhenSyncFails(t *testing.T) {
	env := setupTriggerTest(t)
	env.backend.syncErr = errors.New("temporal unreachable")

	_, err := env.svc.CreateTrigger(env.ctx,
		connect.NewRequest(&reliantv1.CreateTriggerRequest{Trigger: env.definition(nil)}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))

	stored, err := env.repo.ListTriggers(context.Background(), core.TriggerFilters{UserID: env.userID})
	require.NoError(t, err)
	assert.Empty(t, stored, "a trigger whose schedule could not be created must not survive")
}

func TestCreateTriggerValidates(t *testing.T) {
	env := setupTriggerTest(t)

	tests := []struct {
		name string
		def  *reliantv1.TriggerDefinition
	}{
		{"no name", env.definition(func(d *reliantv1.TriggerDefinition) { d.Name = "" })},
		{"no message", env.definition(func(d *reliantv1.TriggerDefinition) { d.Message = "" })},
		{"no project", env.definition(func(d *reliantv1.TriggerDefinition) { d.ProjectId = "" })},
		{"no source", env.definition(func(d *reliantv1.TriggerDefinition) { d.Source = nil })},
		{"empty schedule", env.definition(func(d *reliantv1.TriggerDefinition) {
			d.Source = &reliantv1.TriggerDefinition_Schedule{Schedule: &reliantv1.ScheduleSource{}}
		})},
		{"bad cron", env.definition(func(d *reliantv1.TriggerDefinition) {
			d.Source = &reliantv1.TriggerDefinition_Schedule{
				Schedule: &reliantv1.ScheduleSource{Cron: []string{"not a cron"}}}
		})},
		{"sub-minute interval", env.definition(func(d *reliantv1.TriggerDefinition) {
			every := "10s"
			d.Source = &reliantv1.TriggerDefinition_Schedule{
				Schedule: &reliantv1.ScheduleSource{Interval: &every}}
		})},
		{"unknown timezone", env.definition(func(d *reliantv1.TriggerDefinition) {
			d.Source = &reliantv1.TriggerDefinition_Schedule{
				Schedule: &reliantv1.ScheduleSource{Cron: []string{"0 9 * * *"}, Timezone: "Mars/Olympus"}}
		})},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := env.svc.CreateTrigger(env.ctx,
				connect.NewRequest(&reliantv1.CreateTriggerRequest{Trigger: tc.def}))
			require.Error(t, err)
			assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
			assert.Zero(t, env.backend.syncCount(), "a rejected definition must not touch the schedule backend")
		})
	}
}

func TestCreateTriggerRejectsAnotherUsersProject(t *testing.T) {
	env := setupTriggerTest(t)

	otherCtx := context.WithValue(context.Background(), auth.UserIDContextKey, uuid.NewString())
	_, err := env.svc.CreateTrigger(otherCtx,
		connect.NewRequest(&reliantv1.CreateTriggerRequest{Trigger: env.definition(nil)}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// Without a schedule backend there is nothing to make a trigger fire, so a
// create must fail rather than store one that silently never runs.
func TestCreateTriggerWithoutABackendIsUnavailable(t *testing.T) {
	env := setupTriggerTest(t)
	svc := NewTriggerService(env.repo, nil, nil)

	_, err := svc.CreateTrigger(env.ctx,
		connect.NewRequest(&reliantv1.CreateTriggerRequest{Trigger: env.definition(nil)}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
}

// NewTriggerServiceFor is what the server uses. A nil Temporal client must
// produce a service whose nil checks actually fire — a nil *triggers.Backend
// in an interface is NOT nil, and would panic here instead.
func TestNewTriggerServiceForWithoutTemporalDoesNotPanic(t *testing.T) {
	env := setupTriggerTest(t)
	svc := NewTriggerServiceFor(env.repo, nil, "")

	_, err := svc.CreateTrigger(env.ctx,
		connect.NewRequest(&reliantv1.CreateTriggerRequest{Trigger: env.definition(nil)}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
}

func TestGetTriggerHidesOtherUsersTriggers(t *testing.T) {
	env := setupTriggerTest(t)
	created := env.create(t, env.definition(nil))

	otherCtx := context.WithValue(context.Background(), auth.UserIDContextKey, uuid.NewString())
	_, err := env.svc.GetTrigger(otherCtx,
		connect.NewRequest(&reliantv1.GetTriggerRequest{Id: created.GetId()}))
	require.Error(t, err)
	// NotFound, not PermissionDenied: telling a caller an id exists but is not
	// theirs leaks other users' triggers for nothing.
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestListTriggersIsScopedToTheCaller(t *testing.T) {
	env := setupTriggerTest(t)
	env.create(t, env.definition(nil))
	env.create(t, env.definition(func(d *reliantv1.TriggerDefinition) { d.Name = "second" }))

	resp, err := env.svc.ListTriggers(env.ctx, connect.NewRequest(&reliantv1.ListTriggersRequest{}))
	require.NoError(t, err)
	assert.Len(t, resp.Msg.GetTriggers(), 2)

	otherCtx := context.WithValue(context.Background(), auth.UserIDContextKey, uuid.NewString())
	resp, err = env.svc.ListTriggers(otherCtx, connect.NewRequest(&reliantv1.ListTriggersRequest{}))
	require.NoError(t, err)
	assert.Empty(t, resp.Msg.GetTriggers())
}

func TestUpdateTriggerReplacesAndReconverges(t *testing.T) {
	env := setupTriggerTest(t)
	created := env.create(t, env.definition(nil))

	every := "30m"
	resp, err := env.svc.UpdateTrigger(env.ctx, connect.NewRequest(&reliantv1.UpdateTriggerRequest{
		Id: created.GetId(),
		Trigger: env.definition(func(d *reliantv1.TriggerDefinition) {
			d.Name = "renamed"
			d.Message = "A different prompt."
			d.Source = &reliantv1.TriggerDefinition_Schedule{
				Schedule: &reliantv1.ScheduleSource{Interval: &every, Timezone: "Europe/London"}}
		}),
	}))
	require.NoError(t, err)

	got := resp.Msg.GetTrigger()
	assert.Equal(t, created.GetId(), got.GetId(), "the id is not updatable")
	assert.Equal(t, "renamed", got.GetName())
	assert.Equal(t, "30m", got.GetSchedule().GetInterval())
	assert.Empty(t, got.GetSchedule().GetCron(), "a full replacement must drop the old cron")
	assert.Equal(t, "Europe/London", got.GetSchedule().GetTimezone())
	assert.Equal(t, 2, env.backend.syncCount(), "update must reconverge the schedule")
}

// An update that does not mention enabled must not resume a paused trigger.
func TestUpdateTriggerLeavesEnabledAloneWhenUnset(t *testing.T) {
	env := setupTriggerTest(t)
	created := env.create(t, env.definition(nil))

	_, err := env.svc.SetTriggerEnabled(env.ctx,
		connect.NewRequest(&reliantv1.SetTriggerEnabledRequest{Id: created.GetId(), Enabled: false}))
	require.NoError(t, err)

	resp, err := env.svc.UpdateTrigger(env.ctx, connect.NewRequest(&reliantv1.UpdateTriggerRequest{
		Id:      created.GetId(),
		Trigger: env.definition(func(d *reliantv1.TriggerDefinition) { d.Message = "edited" }),
	}))
	require.NoError(t, err)
	assert.False(t, resp.Msg.GetTrigger().GetEnabled(),
		"an update with enabled unset must not resume a trigger the owner paused")
}

func TestUpdateTriggerAppliesExplicitEnabled(t *testing.T) {
	env := setupTriggerTest(t)
	created := env.create(t, env.definition(nil))

	disabled := false
	resp, err := env.svc.UpdateTrigger(env.ctx, connect.NewRequest(&reliantv1.UpdateTriggerRequest{
		Id:      created.GetId(),
		Trigger: env.definition(func(d *reliantv1.TriggerDefinition) { d.Enabled = &disabled }),
	}))
	require.NoError(t, err)
	assert.False(t, resp.Msg.GetTrigger().GetEnabled())
}

func TestSetTriggerEnabledPersistsAndReconverges(t *testing.T) {
	env := setupTriggerTest(t)
	created := env.create(t, env.definition(nil))

	resp, err := env.svc.SetTriggerEnabled(env.ctx,
		connect.NewRequest(&reliantv1.SetTriggerEnabledRequest{Id: created.GetId(), Enabled: false}))
	require.NoError(t, err)
	assert.False(t, resp.Msg.GetTrigger().GetEnabled())

	stored, err := env.repo.GetTrigger(context.Background(), created.GetId())
	require.NoError(t, err)
	assert.False(t, stored.Enabled)

	// The syncer is what pauses the Temporal schedule, so the state change is
	// only real if it was handed the new value.
	env.backend.mu.Lock()
	last := env.backend.synced[len(env.backend.synced)-1]
	env.backend.mu.Unlock()
	assert.False(t, last.Enabled, "the syncer must be told the trigger is now disabled")
}

// The schedule is dropped BEFORE the row. A schedule outliving its row keeps
// firing for a trigger nobody can see or stop.
func TestDeleteTriggerRemovesTheScheduleFirst(t *testing.T) {
	env := setupTriggerTest(t)
	created := env.create(t, env.definition(nil))

	_, err := env.svc.DeleteTrigger(env.ctx,
		connect.NewRequest(&reliantv1.DeleteTriggerRequest{Id: created.GetId()}))
	require.NoError(t, err)

	env.backend.mu.Lock()
	assert.Equal(t, []string{created.GetId()}, env.backend.deleted)
	env.backend.mu.Unlock()

	_, err = env.repo.GetTrigger(context.Background(), created.GetId())
	assert.ErrorIs(t, err, core.ErrTriggerNotFound)
}

func TestDeleteTriggerKeepsTheRowWhenTheScheduleCannotBeRemoved(t *testing.T) {
	env := setupTriggerTest(t)
	created := env.create(t, env.definition(nil))
	env.backend.deleteErr = errors.New("temporal unreachable")

	_, err := env.svc.DeleteTrigger(env.ctx,
		connect.NewRequest(&reliantv1.DeleteTriggerRequest{Id: created.GetId()}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))

	// The row must survive: deleting it while the schedule lives on would
	// create exactly the orphan this ordering exists to prevent.
	_, err = env.repo.GetTrigger(context.Background(), created.GetId())
	require.NoError(t, err)
}

func TestFireTriggerStartsAManualFire(t *testing.T) {
	env := setupTriggerTest(t)
	created := env.create(t, env.definition(nil))

	resp, err := env.svc.FireTrigger(env.ctx,
		connect.NewRequest(&reliantv1.FireTriggerRequest{Id: created.GetId()}))
	require.NoError(t, err)
	assert.Contains(t, resp.Msg.GetFireWorkflowId(), created.GetId())
	assert.Contains(t, resp.Msg.GetFireWorkflowId(), "manual")

	env.backend.mu.Lock()
	assert.Equal(t, []string{created.GetId()}, env.backend.fires)
	env.backend.mu.Unlock()
}

func TestFireTriggerHidesOtherUsersTriggers(t *testing.T) {
	env := setupTriggerTest(t)
	created := env.create(t, env.definition(nil))

	otherCtx := context.WithValue(context.Background(), auth.UserIDContextKey, uuid.NewString())
	_, err := env.svc.FireTrigger(otherCtx,
		connect.NewRequest(&reliantv1.FireTriggerRequest{Id: created.GetId()}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	env.backend.mu.Lock()
	assert.Empty(t, env.backend.fires, "a trigger the caller does not own must not fire")
	env.backend.mu.Unlock()
}

func TestListTriggerEventsReturnsFiringsNewestFirst(t *testing.T) {
	env := setupTriggerTest(t)
	created := env.create(t, env.definition(nil))

	base := time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		chatID := uuid.NewString()
		_ = chatID
		created, err := env.repo.CreateTriggerEvent(context.Background(), &core.TriggerEvent{
			ID:            uuid.NewString(),
			TriggerID:     &created.Id,
			UserID:        env.userID,
			Kind:          core.TriggerEventKindSchedule,
			DedupeKey:     uuid.NewString(),
			OccurredAt:    base.Add(time.Duration(i) * time.Hour),
			Payload:       map[string]any{"trigger_name": "nightly audit"},
			Outcome:       core.TriggerEventSkipped,
			OutcomeDetail: "trigger is disabled",
		})
		require.NoError(t, err)
		require.True(t, created)
	}

	resp, err := env.svc.ListTriggerEvents(env.ctx,
		connect.NewRequest(&reliantv1.ListTriggerEventsRequest{TriggerId: created.GetId()}))
	require.NoError(t, err)
	events := resp.Msg.GetEvents()
	require.Len(t, events, 3)
	assert.Equal(t, "2026-01-02T11:00:00Z", events[0].GetOccurredAt(), "newest first")
	assert.Equal(t, reliantv1.TriggerEventOutcome_TRIGGER_EVENT_OUTCOME_SKIPPED, events[0].GetOutcome())
	assert.Equal(t, "trigger is disabled", events[0].GetOutcomeDetail())
	assert.Equal(t, reliantv1.TriggerEventKind_TRIGGER_EVENT_KIND_SCHEDULE, events[0].GetKind())
}

func TestListTriggerEventsHidesOtherUsersTriggers(t *testing.T) {
	env := setupTriggerTest(t)
	created := env.create(t, env.definition(nil))

	otherCtx := context.WithValue(context.Background(), auth.UserIDContextKey, uuid.NewString())
	_, err := env.svc.ListTriggerEvents(otherCtx,
		connect.NewRequest(&reliantv1.ListTriggerEventsRequest{TriggerId: created.GetId()}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// A limit of zero is "the server default", not "no rows", and an unbounded
// request is capped rather than honored.
func TestListTriggerEventsBoundsTheLimit(t *testing.T) {
	env := setupTriggerTest(t)
	created := env.create(t, env.definition(nil))

	for i := 0; i < 3; i++ {
		ok, err := env.repo.CreateTriggerEvent(context.Background(), &core.TriggerEvent{
			ID:         uuid.NewString(),
			TriggerID:  &created.Id,
			UserID:     env.userID,
			Kind:       core.TriggerEventKindSchedule,
			DedupeKey:  uuid.NewString(),
			OccurredAt: time.Now().UTC().Add(time.Duration(i) * time.Minute),
			Outcome:    core.TriggerEventLaunched,
		})
		require.NoError(t, err)
		require.True(t, ok)
	}

	resp, err := env.svc.ListTriggerEvents(env.ctx,
		connect.NewRequest(&reliantv1.ListTriggerEventsRequest{TriggerId: created.GetId(), Limit: 0}))
	require.NoError(t, err)
	assert.Len(t, resp.Msg.GetEvents(), 3, "limit 0 must apply the default, not return nothing")

	resp, err = env.svc.ListTriggerEvents(env.ctx,
		connect.NewRequest(&reliantv1.ListTriggerEventsRequest{TriggerId: created.GetId(), Limit: 1}))
	require.NoError(t, err)
	assert.Len(t, resp.Msg.GetEvents(), 1)
}

// The handler's rendered trigger must round-trip through the same parser the
// syncer uses, or the API would accept configs the backend then rejects.
func TestRenderedScheduleRoundTripsThroughTheParser(t *testing.T) {
	env := setupTriggerTest(t)
	every := "45m"
	created := env.create(t, env.definition(func(d *reliantv1.TriggerDefinition) {
		d.Source = &reliantv1.TriggerDefinition_Schedule{Schedule: &reliantv1.ScheduleSource{
			Cron:     []string{"0 9 * * 1-5"},
			Interval: &every,
			Timezone: "America/New_York",
			Overlap:  reliantv1.TriggerOverlapPolicy_TRIGGER_OVERLAP_POLICY_ALLOW,
		}}
	}))

	cfg := triggers.ScheduleConfigFromProto(created.GetSchedule())
	sched, err := triggers.ParseScheduleConfig(cfg)
	require.NoError(t, err)
	assert.Equal(t, []string{"0 9 * * 1-5"}, sched.Spec.CronExpressions)
	assert.Equal(t, "America/New_York", sched.Spec.TimeZoneName)
	assert.False(t, sched.SkipOnOverlap(), "overlap=allow must survive the round trip")
}

func TestLastEventIsProjectedOntoTheTrigger(t *testing.T) {
	env := setupTriggerTest(t)
	created := env.create(t, env.definition(nil))

	ok, err := env.repo.CreateTriggerEvent(context.Background(), &core.TriggerEvent{
		ID:            uuid.NewString(),
		TriggerID:     &created.Id,
		UserID:        env.userID,
		Kind:          core.TriggerEventKindSchedule,
		DedupeKey:     uuid.NewString(),
		OccurredAt:    time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC),
		Outcome:       core.TriggerEventFailed,
		OutcomeDetail: "workflow does not exist",
	})
	require.NoError(t, err)
	require.True(t, ok)

	resp, err := env.svc.GetTrigger(env.ctx,
		connect.NewRequest(&reliantv1.GetTriggerRequest{Id: created.GetId()}))
	require.NoError(t, err)

	last := resp.Msg.GetTrigger().GetLastEvent()
	require.NotNil(t, last, "a trigger that has fired must report its last firing")
	assert.Equal(t, reliantv1.TriggerEventOutcome_TRIGGER_EVENT_OUTCOME_FAILED, last.GetOutcome())
	assert.Equal(t, "workflow does not exist", last.GetOutcomeDetail())
}
