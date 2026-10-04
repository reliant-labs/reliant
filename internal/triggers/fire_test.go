// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
)

func fireReq(triggerID string) FireRequest {
	return FireRequest{
		TriggerID:      triggerID,
		FireWorkflowID: FireWorkflowID(triggerID) + "-2026-01-02T09:00:00Z",
		ScheduledAt:    time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC),
	}
}

func TestFireLaunchesWithTheTriggersIdentityAndPrompt(t *testing.T) {
	repo := newFakeRepo()
	trigger := testTrigger(t, func(tr *core.Trigger, cfg *core.ScheduleConfig) {
		cfg.Timezone = "America/New_York"
	})
	repo.triggers[trigger.ID] = trigger
	launcher := &fakeLauncher{}

	req := fireReq(trigger.ID)
	out, err := NewFirer(repo, launcher).Fire(context.Background(), req)
	if err != nil {
		t.Fatalf("Fire: %v", err)
	}
	if out.Outcome != string(core.TriggerEventLaunched) {
		t.Fatalf("Outcome = %q, want launched (%+v)", out.Outcome, out)
	}

	calls := launcher.snapshot()
	if len(calls) != 1 {
		t.Fatalf("launcher got %d calls, want 1", len(calls))
	}
	spec, ev := calls[0].Spec, calls[0].Event

	if spec.OwnerUserID != trigger.UserID {
		t.Errorf("OwnerUserID = %q, want the trigger's owner %q", spec.OwnerUserID, trigger.UserID)
	}
	if spec.ProjectID != trigger.ProjectID {
		t.Errorf("ProjectID = %q", spec.ProjectID)
	}
	if spec.Workflow != trigger.Workflow {
		t.Errorf("Workflow = %q", spec.Workflow)
	}
	if spec.DaemonID != trigger.DaemonID {
		t.Errorf("DaemonID = %q, want the trigger's daemon %q", spec.DaemonID, trigger.DaemonID)
	}
	if !spec.Unattended {
		t.Error("a scheduled run must be Unattended: nobody is there to answer")
	}
	if spec.GenerateTitle {
		t.Error("GenerateTitle should be off; the title is derived from the schedule")
	}
	if spec.GreenfieldProbe {
		t.Error("GreenfieldProbe should be off for an automated run")
	}
	if spec.Presets["model"] != "fast" {
		t.Errorf("Presets = %v, want the trigger's presets", spec.Presets)
	}
	if spec.Params["depth"].GetNumberValue() != 2 {
		t.Errorf("Params = %v, want depth=2 as a structpb value", spec.Params)
	}

	// The title carries the scheduled time in the TRIGGER's zone, not UTC:
	// 09:00 UTC is 04:00 in New York.
	if spec.Title == nil || !strings.Contains(*spec.Title, "nightly audit") || !strings.Contains(*spec.Title, "04:00") {
		t.Errorf("Title = %v, want the name and the local scheduled time", spec.Title)
	}

	if len(spec.Messages) != 2 {
		t.Fatalf("Messages = %+v, want the prompt plus the hidden unattended note", spec.Messages)
	}
	if spec.Messages[0].Role != reliantv1.MessageRole_MESSAGE_ROLE_USER || spec.Messages[0].Content != trigger.Message {
		t.Errorf("first message = %+v, want the trigger's prompt as a user message", spec.Messages[0])
	}
	sys := spec.Messages[1]
	if sys.Role != reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM {
		t.Errorf("second message role = %v, want SYSTEM", sys.Role)
	}
	if sys.DisplayStyle == nil || *sys.DisplayStyle != reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN {
		t.Errorf("the unattended note must be hidden, got %v", sys.DisplayStyle)
	}
	if !strings.Contains(sys.Content, trigger.Name) || !strings.Contains(sys.Content, "No human is watching") {
		t.Errorf("unattended note = %q", sys.Content)
	}

	if ev.Kind != core.TriggerEventKindSchedule {
		t.Errorf("Event.Kind = %q", ev.Kind)
	}
	if ev.DedupeKey != req.FireWorkflowID {
		t.Errorf("Event.DedupeKey = %q, want the fire workflow id %q", ev.DedupeKey, req.FireWorkflowID)
	}
	if !ev.OccurredAt.Equal(req.ScheduledAt) {
		t.Errorf("Event.OccurredAt = %v, want the SCHEDULED time %v", ev.OccurredAt, req.ScheduledAt)
	}
	if ev.Payload["trigger_name"] != trigger.Name {
		t.Errorf("payload trigger_name = %v", ev.Payload["trigger_name"])
	}
	if ev.Payload["scheduled_for"] != "2026-01-02T09:00:00Z" {
		t.Errorf("payload scheduled_for = %v", ev.Payload["scheduled_for"])
	}
	if ev.Payload["manual"] != false {
		t.Errorf("payload manual = %v, want false", ev.Payload["manual"])
	}

	// The launcher records the launched event itself, in the chat's
	// transaction. A row written here too would double-count the fire.
	if got := repo.eventsFor(trigger.ID); len(got) != 0 {
		t.Errorf("Fire wrote %d event rows for a successful launch; the launcher owns that write", len(got))
	}
}

// The chat id is derived from the fire workflow id so a retried fire resolves
// to the SAME chat, which is what makes the launch idempotent even if the
// event row and the Temporal start were split by a crash.
func TestFireDerivesADeterministicChatID(t *testing.T) {
	repo := newFakeRepo()
	trigger := testTrigger(t, nil)
	repo.triggers[trigger.ID] = trigger
	launcher := &fakeLauncher{}
	firer := NewFirer(repo, launcher)
	req := fireReq(trigger.ID)

	for i := 0; i < 2; i++ {
		if _, err := firer.Fire(context.Background(), req); err != nil {
			t.Fatalf("Fire attempt %d: %v", i, err)
		}
	}
	calls := launcher.snapshot()
	if calls[0].Spec.NewChatID != calls[1].Spec.NewChatID {
		t.Fatalf("NewChatID differed across fires of the same id: %q vs %q",
			calls[0].Spec.NewChatID, calls[1].Spec.NewChatID)
	}
	if calls[0].Spec.NewChatID == "" {
		t.Fatal("NewChatID is empty; a scheduled fire must derive it")
	}
	want := uuid.NewSHA1(chatIDNamespace, []byte(req.FireWorkflowID)).String()
	if calls[0].Spec.NewChatID != want {
		t.Errorf("NewChatID = %q, want %q", calls[0].Spec.NewChatID, want)
	}

	// A different scheduled time is a different fire and must get its own chat.
	other := req
	other.FireWorkflowID = FireWorkflowID(trigger.ID) + "-2026-01-03T09:00:00Z"
	if _, err := firer.Fire(context.Background(), other); err != nil {
		t.Fatalf("Fire for the next day: %v", err)
	}
	calls = launcher.snapshot()
	if calls[2].Spec.NewChatID == calls[0].Spec.NewChatID {
		t.Error("two different scheduled times derived the same chat id")
	}
}

func TestFireSkipsDisabledTriggerAndRecordsIt(t *testing.T) {
	repo := newFakeRepo()
	trigger := testTrigger(t, func(tr *core.Trigger, _ *core.ScheduleConfig) { tr.Enabled = false })
	repo.triggers[trigger.ID] = trigger
	launcher := &fakeLauncher{}

	req := fireReq(trigger.ID)
	out, err := NewFirer(repo, launcher).Fire(context.Background(), req)
	if err != nil {
		t.Fatalf("Fire: %v", err)
	}
	if out.Outcome != string(core.TriggerEventSkipped) {
		t.Fatalf("Outcome = %q, want skipped", out.Outcome)
	}
	if len(launcher.snapshot()) != 0 {
		t.Error("a disabled trigger must not launch")
	}
	events := repo.eventsFor(trigger.ID)
	if len(events) != 1 {
		t.Fatalf("got %d event rows, want 1 skipped row", len(events))
	}
	if events[0].Outcome != core.TriggerEventSkipped || !strings.Contains(events[0].OutcomeDetail, "disabled") {
		t.Errorf("event = %+v, want a skipped row explaining why", events[0])
	}
	if events[0].DedupeKey != req.FireWorkflowID {
		t.Errorf("DedupeKey = %q, want the fire workflow id", events[0].DedupeKey)
	}
}

// The skip row is idempotent on the dedupe key, so a retried fire that already
// recorded a skip does not accumulate rows.
func TestFireSkipIsIdempotentOnTheDedupeKey(t *testing.T) {
	repo := newFakeRepo()
	trigger := testTrigger(t, func(tr *core.Trigger, _ *core.ScheduleConfig) { tr.Enabled = false })
	repo.triggers[trigger.ID] = trigger
	firer := NewFirer(repo, &fakeLauncher{})
	req := fireReq(trigger.ID)

	for i := 0; i < 3; i++ {
		if _, err := firer.Fire(context.Background(), req); err != nil {
			t.Fatalf("Fire attempt %d: %v", i, err)
		}
	}
	if got := repo.eventsFor(trigger.ID); len(got) != 1 {
		t.Fatalf("three fires of the same id wrote %d rows, want 1", len(got))
	}
}

func TestFireSkipsWhileThePreviousRunIsLive(t *testing.T) {
	// Each of these is a reason not to start a second run: the previous one is
	// still going to produce work. PENDING is deliberately absent: a pending
	// root never started, so it is a stranded launch, not a running one (see
	// TestFireDoesNotTreatAStrandedPendingRunAsLive).
	for _, status := range []core.WorkflowStatus{core.Active(), core.Paused()} {
		t.Run(status.Label(), func(t *testing.T) {
			repo := newFakeRepo()
			trigger := testTrigger(t, nil)
			repo.triggers[trigger.ID] = trigger

			prevChat := "chat-prev"
			repo.statuses[prevChat] = status
			repo.events = append(repo.events, &core.TriggerEvent{
				ID:         uuid.NewString(),
				TriggerID:  &trigger.ID,
				UserID:     trigger.UserID,
				Kind:       core.TriggerEventKindSchedule,
				DedupeKey:  "earlier-fire",
				OccurredAt: time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC),
				Outcome:    core.TriggerEventLaunched,
				ChatID:     &prevChat,
			})

			launcher := &fakeLauncher{}
			out, err := NewFirer(repo, launcher).Fire(context.Background(), fireReq(trigger.ID))
			if err != nil {
				t.Fatalf("Fire: %v", err)
			}
			if out.Outcome != string(core.TriggerEventSkipped) {
				t.Fatalf("Outcome = %q, want skipped while the previous run is %s", out.Outcome, status.Label())
			}
			if got := repo.eventsFor(trigger.ID); len(got) != 2 || got[1].Outcome != core.TriggerEventSkipped {
				t.Errorf("events = %+v, want the earlier launch plus a recorded skip", got)
			}
			if !strings.Contains(out.Reason, prevChat) {
				t.Errorf("Reason = %q, want it to name the blocking chat", out.Reason)
			}
		})
	}
}

func TestFireDoesNotTreatAStrandedPendingRunAsLive(t *testing.T) {
	repo := newFakeRepo()
	trigger := testTrigger(t, nil)
	repo.triggers[trigger.ID] = trigger
	prevChat := "chat-prev"
	repo.statuses[prevChat] = core.Pending()
	repo.events = append(repo.events, &core.TriggerEvent{
		ID: uuid.NewString(), TriggerID: &trigger.ID, UserID: trigger.UserID,
		Kind: core.TriggerEventKindSchedule, DedupeKey: "earlier-fire",
		OccurredAt: time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC),
		Outcome:    core.TriggerEventLaunched, ChatID: &prevChat,
	})
	launcher := &fakeLauncher{}
	out, err := NewFirer(repo, launcher).Fire(context.Background(), fireReq(trigger.ID))
	if err != nil {
		t.Fatalf("Fire: %v", err)
	}
	if out.Outcome != string(core.TriggerEventLaunched) {
		t.Fatalf("Outcome = %q, want launched: a never-started root must not block the next fire", out.Outcome)
	}
}

func TestFireLaunchesWhenThePreviousRunFinished(t *testing.T) {
	for _, status := range []core.WorkflowStatus{core.Completed(), core.Failed(), core.Cancelled()} {
		t.Run(status.Label(), func(t *testing.T) {
			repo := newFakeRepo()
			trigger := testTrigger(t, nil)
			repo.triggers[trigger.ID] = trigger

			prevChat := "chat-prev"
			repo.statuses[prevChat] = status
			repo.events = append(repo.events, &core.TriggerEvent{
				ID: uuid.NewString(), TriggerID: &trigger.ID, UserID: trigger.UserID,
				Kind: core.TriggerEventKindSchedule, DedupeKey: "earlier-fire",
				OccurredAt: time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC),
				Outcome:    core.TriggerEventLaunched, ChatID: &prevChat,
			})

			launcher := &fakeLauncher{}
			out, err := NewFirer(repo, launcher).Fire(context.Background(), fireReq(trigger.ID))
			if err != nil {
				t.Fatalf("Fire: %v", err)
			}
			if out.Outcome != string(core.TriggerEventLaunched) {
				t.Fatalf("Outcome = %q, want launched", out.Outcome)
			}
			if len(launcher.snapshot()) != 1 {
				t.Error("a finished previous run must not block the next fire")
			}
		})
	}
}

func TestFireWithOverlapAllowIgnoresALiveRun(t *testing.T) {
	repo := newFakeRepo()
	trigger := testTrigger(t, func(_ *core.Trigger, cfg *core.ScheduleConfig) {
		cfg.Overlap = core.ScheduleOverlapAllow
	})
	repo.triggers[trigger.ID] = trigger

	prevChat := "chat-prev"
	repo.statuses[prevChat] = core.Active()
	repo.events = append(repo.events, &core.TriggerEvent{
		ID: uuid.NewString(), TriggerID: &trigger.ID, UserID: trigger.UserID,
		Kind: core.TriggerEventKindSchedule, DedupeKey: "earlier-fire",
		OccurredAt: time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC),
		Outcome:    core.TriggerEventLaunched, ChatID: &prevChat,
	})

	launcher := &fakeLauncher{}
	out, err := NewFirer(repo, launcher).Fire(context.Background(), fireReq(trigger.ID))
	if err != nil {
		t.Fatalf("Fire: %v", err)
	}
	if out.Outcome != string(core.TriggerEventLaunched) {
		t.Fatalf("Outcome = %q, want launched with overlap=allow", out.Outcome)
	}
}

// A manual fire is a human pressing "run now" and overrides both policies.
func TestManualFireBypassesEnabledAndOverlap(t *testing.T) {
	repo := newFakeRepo()
	trigger := testTrigger(t, func(tr *core.Trigger, _ *core.ScheduleConfig) { tr.Enabled = false })
	repo.triggers[trigger.ID] = trigger

	prevChat := "chat-prev"
	repo.statuses[prevChat] = core.Active()
	repo.events = append(repo.events, &core.TriggerEvent{
		ID: uuid.NewString(), TriggerID: &trigger.ID, UserID: trigger.UserID,
		Kind: core.TriggerEventKindSchedule, DedupeKey: "earlier-fire",
		OccurredAt: time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC),
		Outcome:    core.TriggerEventLaunched, ChatID: &prevChat,
	})

	req := fireReq(trigger.ID)
	req.Manual = true
	req.FireWorkflowID = ManualFireWorkflowID(trigger.ID)

	launcher := &fakeLauncher{}
	out, err := NewFirer(repo, launcher).Fire(context.Background(), req)
	if err != nil {
		t.Fatalf("Fire: %v", err)
	}
	if out.Outcome != string(core.TriggerEventLaunched) {
		t.Fatalf("Outcome = %q, want launched for a manual fire", out.Outcome)
	}
	calls := launcher.snapshot()
	if len(calls) != 1 {
		t.Fatalf("launcher got %d calls", len(calls))
	}
	if calls[0].Event.Payload["manual"] != true {
		t.Errorf("payload manual = %v, want true", calls[0].Event.Payload["manual"])
	}
}

// A spec that can never launch is recorded as failed and NOT retried: retrying
// would re-reach the same conclusion every backoff interval.
func TestFireRecordsValidationFailureAndDoesNotRetry(t *testing.T) {
	for _, launchErr := range []error{
		&launch.ValidationError{Reason: "workflow builtin://gone does not exist"},
		launch.ErrNotFound,
	} {
		t.Run(launchErr.Error(), func(t *testing.T) {
			repo := newFakeRepo()
			trigger := testTrigger(t, nil)
			repo.triggers[trigger.ID] = trigger
			launcher := &fakeLauncher{err: launchErr}

			_, err := NewFirer(repo, launcher).Fire(context.Background(), fireReq(trigger.ID))
			if err == nil {
				t.Fatal("want an error")
			}
			var appErr *temporal.ApplicationError
			if !errors.As(err, &appErr) {
				t.Fatalf("error is %T, want *temporal.ApplicationError: %v", err, err)
			}
			if appErr.Type() != nonRetryableFireError {
				t.Errorf("error type = %q, want %q so the retry policy skips it",
					appErr.Type(), nonRetryableFireError)
			}
			if !appErr.NonRetryable() {
				t.Error("validation failures must be non-retryable")
			}

			events := repo.eventsFor(trigger.ID)
			if len(events) != 1 {
				t.Fatalf("got %d event rows, want 1 failed row", len(events))
			}
			if events[0].Outcome != core.TriggerEventFailed {
				t.Errorf("outcome = %q, want failed", events[0].Outcome)
			}
			if !strings.Contains(events[0].OutcomeDetail, launchErr.Error()) {
				t.Errorf("OutcomeDetail = %q, want the launch error's detail", events[0].OutcomeDetail)
			}
		})
	}
}

// Already-launched is the desired state, so it is success — the fire that
// launched the run already recorded the event.
func TestFireTreatsAlreadyLaunchedAsSuccess(t *testing.T) {
	repo := newFakeRepo()
	trigger := testTrigger(t, nil)
	repo.triggers[trigger.ID] = trigger
	launcher := &fakeLauncher{err: &launch.AlreadyLaunchedError{ChatID: "chat-x", EventID: "event-x"}}

	out, err := NewFirer(repo, launcher).Fire(context.Background(), fireReq(trigger.ID))
	if err != nil {
		t.Fatalf("Fire: %v", err)
	}
	if out.Outcome != string(core.TriggerEventLaunched) {
		t.Fatalf("Outcome = %q, want launched", out.Outcome)
	}
	if out.ChatID != "chat-x" || out.EventID != "event-x" {
		t.Errorf("out = %+v, want the existing chat and event ids", out)
	}
	if got := repo.eventsFor(trigger.ID); len(got) != 0 {
		t.Errorf("wrote %d rows for an already-launched fire; the first launch owns the row", len(got))
	}
}

// An unexpected launch error is retryable: a database blip should be absorbed
// by the retry policy, not recorded as the trigger's verdict.
func TestFireReturnsRetryableErrorForUnexpectedFailures(t *testing.T) {
	repo := newFakeRepo()
	trigger := testTrigger(t, nil)
	repo.triggers[trigger.ID] = trigger
	launcher := &fakeLauncher{err: errors.New("connection reset by peer")}

	_, err := NewFirer(repo, launcher).Fire(context.Background(), fireReq(trigger.ID))
	if err == nil {
		t.Fatal("want an error")
	}
	var appErr *temporal.ApplicationError
	if errors.As(err, &appErr) && appErr.NonRetryable() {
		t.Fatalf("a transient failure must stay retryable, got %v", err)
	}
	if got := repo.eventsFor(trigger.ID); len(got) != 0 {
		t.Errorf("wrote %d rows for a transient failure; the retry may yet succeed", len(got))
	}
}

func TestFireEndsQuietlyWhenTheTriggerWasDeleted(t *testing.T) {
	repo := newFakeRepo()
	launcher := &fakeLauncher{}

	out, err := NewFirer(repo, launcher).Fire(context.Background(), fireReq("gone"))
	if err != nil {
		t.Fatalf("Fire: %v", err)
	}
	if out.Outcome != string(core.TriggerEventSkipped) {
		t.Fatalf("Outcome = %q, want skipped", out.Outcome)
	}
	if len(launcher.snapshot()) != 0 {
		t.Error("a deleted trigger must not launch")
	}
}

func TestFireRejectsAnIncompleteRequest(t *testing.T) {
	firer := NewFirer(newFakeRepo(), &fakeLauncher{})
	for _, req := range []FireRequest{
		{FireWorkflowID: "x"},
		{TriggerID: "t"},
	} {
		if _, err := firer.Fire(context.Background(), req); err == nil {
			t.Errorf("Fire(%+v) = nil error, want a rejection", req)
		}
	}
}

// A deleted daemon must fail loudly. Falling back to another daemon would run
// the trigger somewhere the owner never chose.
func TestFireFailsNonRetryablyWhenTheTriggersDaemonIsGone(t *testing.T) {
	repo := newFakeRepo()
	trigger := testTrigger(t, func(tr *core.Trigger, _ *core.ScheduleConfig) {
		tr.DaemonID = "daemon-deleted"
	})
	repo.triggers[trigger.ID] = trigger
	launcher := &fakeLauncher{}

	_, err := NewFirer(repo, launcher).Fire(context.Background(), fireReq(trigger.ID))
	var appErr *temporal.ApplicationError
	if !errors.As(err, &appErr) || !appErr.NonRetryable() || appErr.Type() != nonRetryableFireError {
		t.Fatalf("want a non-retryable fire error, got %T: %v", err, err)
	}

	if calls := launcher.snapshot(); len(calls) != 0 {
		t.Fatalf("launcher got %d calls, want none: never launch on another daemon", len(calls))
	}
	events := repo.eventsFor(trigger.ID)
	if len(events) != 1 || events[0].Outcome != core.TriggerEventFailed {
		t.Fatalf("want one failed event, got %+v", events)
	}
	if !strings.Contains(events[0].OutcomeDetail, "trigger's daemon no longer exists; edit the trigger to choose another") {
		t.Errorf("OutcomeDetail = %q", events[0].OutcomeDetail)
	}
}

func TestFireRecordsWhenTheFireActuallyRan(t *testing.T) {
	repo := newFakeRepo()
	trigger := testTrigger(t, nil)
	repo.triggers[trigger.ID] = trigger
	launcher := &fakeLauncher{}

	req := fireReq(trigger.ID)
	late := req.ScheduledAt.Add(4 * time.Minute)
	firer := NewFirer(repo, launcher)
	firer.now = func() time.Time { return late }
	_, err := firer.Fire(context.Background(), req)
	require.NoError(t, err)

	ev := launcher.snapshot()[0].Event
	assert.Equal(t, "2026-01-02T09:04:00Z", ev.Payload["fired_at"])
	assert.Equal(t, "2026-01-02T09:00:00Z", ev.Payload["scheduled_for"])
	assert.True(t, ev.OccurredAt.Equal(req.ScheduledAt), "OccurredAt must stay the scheduled time")
}
