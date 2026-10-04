// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"errors"
	"os/exec"
	"sync"
	"testing"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/testsuite"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// The syncer's contract is with the Temporal SERVER — create-vs-update,
// pause semantics, idempotent delete, and what List returns — so these tests
// run against a real ephemeral dev server rather than a mock. A mock would
// only re-assert the calls we chose to make.
//
// StartDevServer binds a free port and keeps all state in memory, so this is
// safe alongside any other Temporal on the machine.

const syncerTestNamespace = "reliant-triggers-test"

var devServer struct {
	once   sync.Once
	server *testsuite.DevServer
	client client.Client
	err    error
}

func temporalForTest(t *testing.T) client.Client {
	t.Helper()
	if testing.Short() {
		t.Skip("boots a Temporal dev server; skipped under -short")
	}

	devServer.once.Do(func() {
		opts := testsuite.DevServerOptions{
			LogLevel:      "never",
			ClientOptions: &client.Options{Namespace: syncerTestNamespace},
		}
		// Prefer the CLI already on PATH; the SDK downloads a cached copy
		// otherwise, which works on CI without extra setup.
		if path, err := exec.LookPath("temporal"); err == nil {
			opts.ExistingPath = path
		}
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()

		srv, err := testsuite.StartDevServer(ctx, opts)
		if err != nil {
			devServer.err = err
			return
		}
		devServer.server = srv
		devServer.client = srv.Client()
	})

	if devServer.err != nil {
		t.Fatalf("start Temporal dev server: %v", devServer.err)
	}
	return devServer.client
}

// newSyncer returns a syncer over the dev server plus the fake repo behind it.
func newSyncer(t *testing.T) (*Syncer, *fakeRepo) {
	t.Helper()
	c := temporalForTest(t)
	repo := newFakeRepo()
	return NewSyncer(c.ScheduleClient(), repo, "triggers-test-queue"), repo
}

// cleanupSchedule removes a schedule however the test ended, so a failure
// cannot leak state into the next test sharing the server.
func cleanupSchedule(t *testing.T, s *Syncer, triggerID string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = s.Delete(ctx, triggerID)
	})
}

func TestSyncCreatesTheSchedule(t *testing.T) {
	s, repo := newSyncer(t)
	ctx := context.Background()

	trigger := testTrigger(t, func(_ *core.Trigger, cfg *core.ScheduleConfig) {
		cfg.Cron = []string{"0 9 * * *"}
		cfg.Timezone = "America/New_York"
		cfg.CatchupWindow = "3m"
	})
	repo.triggers[trigger.ID] = trigger
	cleanupSchedule(t, s, trigger.ID)

	if err := s.Sync(ctx, trigger.ID); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	desc, err := s.schedules.GetHandle(ctx, ScheduleID(trigger.ID)).Describe(ctx)
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if desc.Schedule.State.Paused {
		t.Error("an enabled trigger's schedule must not be paused")
	}
	if desc.Schedule.Spec.TimeZoneName != "America/New_York" {
		t.Errorf("TimeZoneName = %q", desc.Schedule.Spec.TimeZoneName)
	}
	if desc.Schedule.Policy.CatchupWindow != 3*time.Minute {
		t.Errorf("CatchupWindow = %v, want 3m", desc.Schedule.Policy.CatchupWindow)
	}
	if got := desc.Schedule.Policy.Overlap; got != enumspb.SCHEDULE_OVERLAP_POLICY_ALLOW_ALL {
		t.Errorf("Overlap = %v, want ALLOW_ALL so no fire is ever dropped", got)
	}
	if desc.Schedule.Policy.PauseOnFailure {
		t.Error("PauseOnFailure must be off: a transient launch failure must not retire the schedule")
	}

	action, ok := desc.Schedule.Action.(*client.ScheduleWorkflowAction)
	if !ok {
		t.Fatalf("Action is %T, want *client.ScheduleWorkflowAction", desc.Schedule.Action)
	}
	if action.ID != FireWorkflowID(trigger.ID) {
		t.Errorf("action ID = %q, want %q", action.ID, FireWorkflowID(trigger.ID))
	}
	if action.TaskQueue != "triggers-test-queue" {
		t.Errorf("TaskQueue = %q", action.TaskQueue)
	}
	if name, _ := action.Workflow.(string); name != FireWorkflowName {
		t.Errorf("Workflow = %v, want %q", action.Workflow, FireWorkflowName)
	}
	if len(desc.Info.NextActionTimes) == 0 {
		t.Error("an enabled cron schedule should report a next action time")
	}
}

// Sync is idempotent and converges: calling it after the row changed replaces
// the spec, rather than leaving the old one or creating a second schedule.
func TestSyncUpdatesTheScheduleWhenTheRowChanges(t *testing.T) {
	s, repo := newSyncer(t)
	ctx := context.Background()

	trigger := testTrigger(t, func(_ *core.Trigger, cfg *core.ScheduleConfig) {
		cfg.Cron = []string{"0 9 * * *"}
	})
	repo.triggers[trigger.ID] = trigger
	cleanupSchedule(t, s, trigger.ID)

	if err := s.Sync(ctx, trigger.ID); err != nil {
		t.Fatalf("first Sync: %v", err)
	}

	trigger = testTrigger(t, func(tr *core.Trigger, cfg *core.ScheduleConfig) {
		tr.ID = trigger.ID
		cfg.Cron = nil
		cfg.Interval = "30m"
		cfg.Timezone = "Europe/London"
	})
	repo.triggers[trigger.ID] = trigger
	if err := s.Sync(ctx, trigger.ID); err != nil {
		t.Fatalf("second Sync: %v", err)
	}

	desc, err := s.schedules.GetHandle(ctx, ScheduleID(trigger.ID)).Describe(ctx)
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if len(desc.Schedule.Spec.Intervals) != 1 || desc.Schedule.Spec.Intervals[0].Every != 30*time.Minute {
		t.Errorf("Intervals = %+v, want a single 30m interval", desc.Schedule.Spec.Intervals)
	}
	// Temporal translates CronExpressions into Calendars on create, so the
	// check that the old cron is gone is on Calendars.
	if len(desc.Schedule.Spec.Calendars) != 0 {
		t.Errorf("Calendars = %+v, want the replaced cron gone", desc.Schedule.Spec.Calendars)
	}
	if desc.Schedule.Spec.TimeZoneName != "Europe/London" {
		t.Errorf("TimeZoneName = %q", desc.Schedule.Spec.TimeZoneName)
	}
}

func TestSyncPausesADisabledTriggerAndUnpausesOnReenable(t *testing.T) {
	s, repo := newSyncer(t)
	ctx := context.Background()

	trigger := testTrigger(t, nil)
	repo.triggers[trigger.ID] = trigger
	cleanupSchedule(t, s, trigger.ID)

	if err := s.Sync(ctx, trigger.ID); err != nil {
		t.Fatalf("Sync enabled: %v", err)
	}

	trigger.Enabled = false
	if err := s.Sync(ctx, trigger.ID); err != nil {
		t.Fatalf("Sync disabled: %v", err)
	}
	desc, err := s.schedules.GetHandle(ctx, ScheduleID(trigger.ID)).Describe(ctx)
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if !desc.Schedule.State.Paused {
		t.Fatal("a disabled trigger's schedule must be paused")
	}

	trigger.Enabled = true
	if err := s.Sync(ctx, trigger.ID); err != nil {
		t.Fatalf("Sync re-enabled: %v", err)
	}
	desc, err = s.schedules.GetHandle(ctx, ScheduleID(trigger.ID)).Describe(ctx)
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if desc.Schedule.State.Paused {
		t.Fatal("re-enabling must unpause the schedule")
	}
}

// Sync on a disabled trigger whose schedule does not exist yet must CREATE it
// paused, not skip it — otherwise enabling a trigger that was created disabled
// would have nothing to unpause.
func TestSyncCreatesADisabledTriggersSchedulePaused(t *testing.T) {
	s, repo := newSyncer(t)
	ctx := context.Background()

	trigger := testTrigger(t, func(tr *core.Trigger, _ *core.ScheduleConfig) { tr.Enabled = false })
	repo.triggers[trigger.ID] = trigger
	cleanupSchedule(t, s, trigger.ID)

	if err := s.Sync(ctx, trigger.ID); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	desc, err := s.schedules.GetHandle(ctx, ScheduleID(trigger.ID)).Describe(ctx)
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if !desc.Schedule.State.Paused {
		t.Error("want the schedule created in the paused state")
	}
}

func TestDeleteIsIdempotent(t *testing.T) {
	s, repo := newSyncer(t)
	ctx := context.Background()

	trigger := testTrigger(t, nil)
	repo.triggers[trigger.ID] = trigger

	if err := s.Sync(ctx, trigger.ID); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if err := s.Delete(ctx, trigger.ID); err != nil {
		t.Fatalf("first Delete: %v", err)
	}
	// The caller's intent is "no schedule for this id", which already holds.
	if err := s.Delete(ctx, trigger.ID); err != nil {
		t.Fatalf("second Delete should be success, got %v", err)
	}
	if err := s.Delete(ctx, "never-existed"); err != nil {
		t.Fatalf("Delete of an unknown id should be success, got %v", err)
	}

	if _, err := s.schedules.GetHandle(ctx, ScheduleID(trigger.ID)).Describe(ctx); err == nil {
		t.Error("the schedule is still there after Delete")
	} else if !isNotFound(err) {
		t.Errorf("Describe after Delete = %v, want NotFound", err)
	}
}

func TestSyncRejectsAnInvalidConfigWithoutTouchingTemporal(t *testing.T) {
	s, repo := newSyncer(t)
	ctx := context.Background()

	trigger := testTrigger(t, func(_ *core.Trigger, cfg *core.ScheduleConfig) {
		cfg.Cron = nil
		cfg.Interval = ""
	})
	repo.triggers[trigger.ID] = trigger

	err := s.Sync(ctx, trigger.ID)
	if err == nil {
		t.Fatal("want a rejection for a schedule with no cron and no interval")
	}
	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("error is %T, want *ConfigError: %v", err, err)
	}
	if _, err := s.schedules.GetHandle(ctx, ScheduleID(trigger.ID)).Describe(ctx); err == nil {
		t.Error("a rejected config must not leave a schedule behind")
	}
}

// SyncAll converges every row and deletes schedules whose row is gone. An
// orphan keeps firing for a trigger nobody can see or stop, which is the drift
// that actually hurts.
func TestSyncAllConvergesAndRemovesOrphans(t *testing.T) {
	s, repo := newSyncer(t)
	ctx := context.Background()

	enabled := testTrigger(t, nil)
	disabled := testTrigger(t, func(tr *core.Trigger, _ *core.ScheduleConfig) { tr.Enabled = false })
	repo.triggers[enabled.ID] = enabled
	repo.triggers[disabled.ID] = disabled
	cleanupSchedule(t, s, enabled.ID)
	cleanupSchedule(t, s, disabled.ID)

	// An orphan: a schedule whose row was deleted while Temporal was
	// unreachable.
	orphan := testTrigger(t, nil)
	repo.triggers[orphan.ID] = orphan
	if err := s.Sync(ctx, orphan.ID); err != nil {
		t.Fatalf("seed orphan: %v", err)
	}
	delete(repo.triggers, orphan.ID)
	cleanupSchedule(t, s, orphan.ID)

	if err := s.SyncAll(ctx); err != nil {
		t.Fatalf("SyncAll: %v", err)
	}

	for _, tc := range []struct {
		id         string
		wantPaused bool
	}{{enabled.ID, false}, {disabled.ID, true}} {
		desc, err := s.schedules.GetHandle(ctx, ScheduleID(tc.id)).Describe(ctx)
		if err != nil {
			t.Fatalf("Describe %s: %v", tc.id, err)
		}
		if desc.Schedule.State.Paused != tc.wantPaused {
			t.Errorf("schedule %s paused = %v, want %v", tc.id, desc.Schedule.State.Paused, tc.wantPaused)
		}
	}

	// Orphan cleanup depends on the schedule List, which is backed by
	// visibility and is eventually consistent — a schedule created moments ago
	// may not be listed yet. SyncAll is therefore retried here, exactly as it
	// is in production by running at every api-server startup. Observed
	// against Server 1.28.0: a single pass right after Sync does NOT see the
	// orphan, which is why this loop is not a flake workaround but the
	// contract.
	deadline := time.Now().Add(30 * time.Second)
	for {
		_, err := s.schedules.GetHandle(ctx, ScheduleID(orphan.ID)).Describe(ctx)
		if err != nil {
			if !isNotFound(err) {
				t.Fatalf("Describe orphan = %v, want NotFound", err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the orphan schedule survived repeated SyncAll passes")
		}
		time.Sleep(500 * time.Millisecond)
		if err := s.SyncAll(ctx); err != nil {
			t.Fatalf("SyncAll retry: %v", err)
		}
	}
}

// SyncAll must not abandon the fleet because one row is malformed.
func TestSyncAllKeepsGoingPastABadTrigger(t *testing.T) {
	s, repo := newSyncer(t)
	ctx := context.Background()

	bad := testTrigger(t, func(_ *core.Trigger, cfg *core.ScheduleConfig) {
		cfg.Cron = []string{"not a cron"}
	})
	good := testTrigger(t, nil)
	repo.triggers[bad.ID] = bad
	repo.triggers[good.ID] = good
	cleanupSchedule(t, s, good.ID)
	cleanupSchedule(t, s, bad.ID)

	err := s.SyncAll(ctx)
	if err == nil {
		t.Fatal("want the bad trigger's error reported")
	}
	if _, derr := s.schedules.GetHandle(ctx, ScheduleID(good.ID)).Describe(ctx); derr != nil {
		t.Fatalf("the good trigger was not synced: %v (SyncAll err: %v)", derr, err)
	}
}

func TestNextFireAt(t *testing.T) {
	s, repo := newSyncer(t)
	ctx := context.Background()

	trigger := testTrigger(t, nil)
	repo.triggers[trigger.ID] = trigger
	cleanupSchedule(t, s, trigger.ID)

	// No schedule yet: nil rather than an error, so a handler can render a
	// trigger whose schedule has not been created.
	next, err := s.NextFireAt(ctx, trigger.ID)
	if err != nil {
		t.Fatalf("NextFireAt before create: %v", err)
	}
	if next != nil {
		t.Errorf("next = %v, want nil before the schedule exists", next)
	}

	if err := s.Sync(ctx, trigger.ID); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	next, err = s.NextFireAt(ctx, trigger.ID)
	if err != nil {
		t.Fatalf("NextFireAt: %v", err)
	}
	if next == nil || next.Before(time.Now().Add(-time.Minute)) {
		t.Errorf("next = %v, want an upcoming time", next)
	}

	// Paused means there is no next fire, which is what the UI needs to say.
	trigger.Enabled = false
	if err := s.Sync(ctx, trigger.ID); err != nil {
		t.Fatalf("Sync disabled: %v", err)
	}
	next, err = s.NextFireAt(ctx, trigger.ID)
	if err != nil {
		t.Fatalf("NextFireAt paused: %v", err)
	}
	if next != nil {
		t.Errorf("next = %v, want nil for a paused schedule", next)
	}
}

// M5b: Sync converges from the row's current state, and a row that is gone
// converges to no schedule — there is no caller-supplied copy to go stale.
func TestSyncConvergesFromTheCurrentRowAndRemovesTheScheduleOfAMissingRow(t *testing.T) {
	s, repo := newSyncer(t)
	ctx := context.Background()

	trigger := testTrigger(t, nil)
	repo.triggers[trigger.ID] = trigger
	cleanupSchedule(t, s, trigger.ID)
	if err := s.Sync(ctx, trigger.ID); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	// A write lands after the caller read its copy; Sync must see it.
	repo.mu.Lock()
	repo.triggers[trigger.ID].Enabled = false
	repo.mu.Unlock()
	if err := s.Sync(ctx, trigger.ID); err != nil {
		t.Fatalf("Sync after write: %v", err)
	}
	desc, err := s.schedules.GetHandle(ctx, ScheduleID(trigger.ID)).Describe(ctx)
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if !desc.Schedule.State.Paused {
		t.Error("Sync must converge to the stored row's enabled=false")
	}

	repo.mu.Lock()
	delete(repo.triggers, trigger.ID)
	repo.mu.Unlock()
	if err := s.Sync(ctx, trigger.ID); err != nil {
		t.Fatalf("Sync of a missing row: %v", err)
	}
	if _, err := s.schedules.GetHandle(ctx, ScheduleID(trigger.ID)).Describe(ctx); !isNotFound(err) {
		t.Errorf("Describe after Sync of a missing row = %v, want NotFound", err)
	}
}

// staleSnapshotRepo reports an empty trigger list while GetTrigger still finds
// the row: a trigger created after SyncAll took its snapshot.
type staleSnapshotRepo struct{ *fakeRepo }

func (r staleSnapshotRepo) ListAllTriggers(context.Context) ([]*core.Trigger, error) { return nil, nil }

// M-D: the orphan sweep re-checks the row before deleting, so a trigger
// created after the snapshot keeps its schedule.
func TestSyncAllKeepsTheScheduleOfATriggerCreatedAfterTheSnapshot(t *testing.T) {
	c := temporalForTest(t)
	fake := newFakeRepo()
	s := NewSyncer(c.ScheduleClient(), staleSnapshotRepo{fake}, "triggers-test-queue")
	ctx := context.Background()

	trigger := testTrigger(t, nil)
	fake.triggers[trigger.ID] = trigger
	cleanupSchedule(t, s, trigger.ID)
	if err := s.Sync(ctx, trigger.ID); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	// Schedule listing is eventually consistent; wait until the sweep would
	// actually see it, or the test proves nothing.
	deadline := time.Now().Add(30 * time.Second)
	for !scheduleListed(t, s, ScheduleID(trigger.ID)) {
		if time.Now().After(deadline) {
			t.Fatal("the schedule never appeared in the schedule listing")
		}
		time.Sleep(250 * time.Millisecond)
	}

	if err := s.SyncAll(ctx); err != nil {
		t.Fatalf("SyncAll: %v", err)
	}
	if _, err := s.schedules.GetHandle(ctx, ScheduleID(trigger.ID)).Describe(ctx); err != nil {
		t.Fatalf("SyncAll deleted the schedule of a live trigger: %v", err)
	}
}

func scheduleListed(t *testing.T, s *Syncer, id string) bool {
	t.Helper()
	iter, err := s.schedules.List(context.Background(), client.ScheduleListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for iter.HasNext() {
		entry, err := iter.Next()
		if err != nil {
			t.Fatalf("List next: %v", err)
		}
		if entry.ID == id {
			return true
		}
	}
	return false
}
