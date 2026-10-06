// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
)

// The pieces are individually tested above; this proves they are wired to each
// other. A trigger row goes in, and the fake launcher sees a launch carrying
// the fire's dedupe key — through a real Temporal Schedule, a real schedule
// action, the real workflow and the real activity.
func TestScheduleFiresThroughToTheLauncher(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a Temporal dev server and a worker; skipped under -short")
	}

	c := temporalForTest(t)
	ctx := context.Background()

	// A real interval short enough to observe. The 1m floor exists because a
	// whole agent run cannot finish faster; the fake launcher returns
	// instantly, so the floor is not protecting anything here.
	restore := SetMinIntervalForTest(time.Second)
	defer restore()

	repo := newFakeRepo()
	launcher := &fakeLauncher{}

	cfg := core.ScheduleConfig{Interval: "2s", Overlap: core.ScheduleOverlapAllow}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	trigger := testTrigger(t, nil)
	trigger.Config = raw
	repo.triggers[trigger.ID] = trigger

	const taskQueue = "triggers-e2e-queue"
	w := worker.New(c, taskQueue, temporaltest.WorkerOptions(worker.Options{}))
	w.RegisterWorkflowWithOptions(TriggerFireWorkflow, workflow.RegisterOptions{Name: FireWorkflowName})
	w.RegisterActivityWithOptions(NewFirer(repo, launcher).Fire, activity.RegisterOptions{Name: FireActivityName})
	if err := w.Start(); err != nil {
		t.Fatalf("start worker: %v", err)
	}
	defer w.Stop()

	s := NewSyncer(c.ScheduleClient(), repo, taskQueue)
	cleanupSchedule(t, s, trigger.ID)
	if err := s.Sync(ctx, trigger.ID); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	calls := waitForLaunch(t, launcher, 60*time.Second)
	call := calls[0]

	if call.Event.Kind != core.TriggerEventKindSchedule {
		t.Errorf("Event.Kind = %q", call.Event.Kind)
	}
	// Temporal appends the scheduled time to the action's workflow id, and
	// that id is the dedupe key — which is what makes each scheduled time
	// launch exactly once.
	if !strings.HasPrefix(call.Event.DedupeKey, FireWorkflowID(trigger.ID)) {
		t.Errorf("DedupeKey = %q, want the fire workflow id %q plus the scheduled time",
			call.Event.DedupeKey, FireWorkflowID(trigger.ID))
	}
	if call.Event.DedupeKey == FireWorkflowID(trigger.ID) {
		t.Error("DedupeKey carries no scheduled time, so every fire would dedupe to one event")
	}
	// The scheduled time must come from the server's
	// TemporalScheduledStartTime, not from when the fire happened to run.
	// Temporal appends exactly that time to the action's workflow id, so the
	// dedupe key is an independent witness: if the fallback to
	// WorkflowStartTime had been taken, the two would disagree.
	if call.Event.OccurredAt.IsZero() {
		t.Fatal("OccurredAt is zero; the scheduled time did not reach the activity")
	}
	suffix := strings.TrimPrefix(call.Event.DedupeKey, FireWorkflowID(trigger.ID)+"-")
	wantStamp, err := time.Parse(time.RFC3339, suffix)
	if err != nil {
		t.Fatalf("fire id suffix %q is not an RFC3339 time: %v", suffix, err)
	}
	if !call.Event.OccurredAt.Equal(wantStamp) {
		t.Errorf("OccurredAt = %v, want the scheduled time %v from TemporalScheduledStartTime",
			call.Event.OccurredAt.UTC(), wantStamp.UTC())
	}
	if call.Spec.OwnerUserID != trigger.UserID || !call.Spec.Unattended {
		t.Errorf("Spec = %+v, want the trigger's owner and an unattended run", call.Spec)
	}
	if call.Spec.NewChatID == "" {
		t.Error("NewChatID is empty; a scheduled fire must derive a deterministic chat id")
	}
}

// A manual fire reaches the same path without a schedule firing, which is what
// "run now" has to do for a trigger that is paused.
func TestManualFireReachesTheLauncher(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a Temporal dev server and a worker; skipped under -short")
	}

	c := temporalForTest(t)
	ctx := context.Background()

	repo := newFakeRepo()
	launcher := &fakeLauncher{}
	trigger := testTrigger(t, func(tr *core.Trigger, _ *core.ScheduleConfig) { tr.Enabled = false })
	repo.triggers[trigger.ID] = trigger

	const taskQueue = "triggers-manual-queue"
	w := worker.New(c, taskQueue, temporaltest.WorkerOptions(worker.Options{}))
	w.RegisterWorkflowWithOptions(TriggerFireWorkflow, workflow.RegisterOptions{Name: FireWorkflowName})
	w.RegisterActivityWithOptions(NewFirer(repo, launcher).Fire, activity.RegisterOptions{Name: FireActivityName})
	if err := w.Start(); err != nil {
		t.Fatalf("start worker: %v", err)
	}
	defer w.Stop()

	fireID, err := StartManualFire(ctx, c, trigger.ID, taskQueue)
	if err != nil {
		t.Fatalf("StartManualFire: %v", err)
	}
	if !strings.Contains(fireID, "-manual-") {
		t.Errorf("fire id = %q, want a manual id", fireID)
	}

	calls := waitForLaunch(t, launcher, 60*time.Second)
	if calls[0].Event.DedupeKey != fireID {
		t.Errorf("DedupeKey = %q, want the manual fire id %q", calls[0].Event.DedupeKey, fireID)
	}
	if calls[0].Event.Payload["manual"] != true {
		t.Errorf("payload manual = %v, want true", calls[0].Event.Payload["manual"])
	}
}

func waitForLaunch(t *testing.T, launcher *fakeLauncher, within time.Duration) []launchCall {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if calls := launcher.snapshot(); len(calls) > 0 {
			return calls
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("no launch within %s", within)
	return nil
}
