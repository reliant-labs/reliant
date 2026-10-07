// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
)

// recordingStarter records workflow starts, standing in for Temporal.
type recordingStarter struct {
	mu     sync.Mutex
	starts []recordedStart
	err    error
}

type recordedStart struct {
	ID       string
	Workflow any
	Input    any
}

func (s *recordingStarter) ExecuteWorkflow(_ context.Context, options client.StartWorkflowOptions, workflow any, args ...any) (client.WorkflowRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	var input any
	if len(args) > 0 {
		input = args[0]
	}
	s.starts = append(s.starts, recordedStart{ID: options.ID, Workflow: workflow, Input: input})
	return &stubRun{id: options.ID, runID: "run-" + options.ID}, nil
}

func (s *recordingStarter) snapshot() []recordedStart {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]recordedStart(nil), s.starts...)
}

func webhookTrigger(t *testing.T, filter string) *core.Trigger {
	t.Helper()
	trigger := testTrigger(t, nil)
	trigger.Kind = core.TriggerKindWebhook
	trigger.Name = "deploy hook"
	trigger.Config = json.RawMessage(`{}`)
	trigger.Filter = filter
	return trigger
}

func inbound(trigger *core.Trigger, key string, payload map[string]any) InboundEvent {
	return InboundEvent{
		Kind:       core.TriggerEventKindWebhook,
		DedupeKey:  trigger.ID + ":" + key,
		OccurredAt: time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC),
		Payload:    payload,
		Sender:     &core.TriggerSender{Kind: core.TriggerSenderKindWebhook, ID: trigger.ID, Verified: true},
	}
}

// slackFrom is an event a Slack receiver would hand the intake: sent by
// user, verified or not.
func slackFrom(trigger *core.Trigger, key, user string, verified bool) InboundEvent {
	ev := inbound(trigger, key, map[string]any{"data": map[string]any{"text": "deploy"}})
	ev.Kind = core.TriggerEventKindIntegration
	ev.Sender = &core.TriggerSender{Kind: core.TriggerSenderKindSlack, ID: user, Verified: verified}
	return ev
}

// "Only from": a filter on trigger.sender lets an allowlisted, verified
// sender start a run and records everyone else as skipped, with no fire.
func TestIntakeSenderFilterAdmitsOnlyTheAllowlist(t *testing.T) {
	const onlyFrom = `trigger.sender.verified && trigger.sender.id in ["U123"]`
	repo := newFakeRepo()
	trigger := webhookTrigger(t, onlyFrom)
	repo.triggers[trigger.ID] = trigger
	starter := &recordingStarter{}
	intake := NewIntake(repo, starter, "")
	ctx := context.Background()

	stranger, err := intake.Accept(ctx, trigger, slackFrom(trigger, "e1", "U999", true), AcceptOptions{})
	require.NoError(t, err)
	assert.Equal(t, core.TriggerEventSkipped, stranger.Outcome, "a sender not on the list starts nothing")
	assert.Contains(t, stranger.Detail, onlyFrom)

	// The right id is not enough when the source could not vouch for it.
	unverified, err := intake.Accept(ctx, trigger, slackFrom(trigger, "e2", "U123", false), AcceptOptions{})
	require.NoError(t, err)
	assert.Equal(t, core.TriggerEventSkipped, unverified.Outcome)
	assert.Empty(t, starter.snapshot(), "no run starts for either")

	allowed, err := intake.Accept(ctx, trigger, slackFrom(trigger, "e3", "U123", true), AcceptOptions{})
	require.NoError(t, err)
	assert.Equal(t, core.TriggerEventPending, allowed.Outcome)
	starts := starter.snapshot()
	require.Len(t, starts, 1, "the allowlisted sender's event starts its fire")
	assert.Equal(t, EventFireWorkflowID(allowed.EventID), starts[0].ID)

	// The sender is on every recorded row, run or not: "why did it skip?"
	// has the answer.
	senders := map[string]core.TriggerSender{}
	for _, ev := range repo.eventsFor(trigger.ID) {
		require.NotNil(t, ev.Sender)
		senders[ev.DedupeKey] = *ev.Sender
	}
	assert.Equal(t, map[string]core.TriggerSender{
		trigger.ID + ":e1": {Kind: core.TriggerSenderKindSlack, ID: "U999", Verified: true},
		trigger.ID + ":e2": {Kind: core.TriggerSenderKindSlack, ID: "U123", Verified: false},
		trigger.ID + ":e3": {Kind: core.TriggerSenderKindSlack, ID: "U123", Verified: true},
	}, senders)
}

// The sender comes from the receiver, never the payload: a body that claims
// to be from an allowlisted, verified user is just data.
func TestIntakeSenderIsNeverReadFromThePayload(t *testing.T) {
	repo := newFakeRepo()
	trigger := webhookTrigger(t, `trigger.sender.verified && trigger.sender.id in ["U123"]`)
	repo.triggers[trigger.ID] = trigger
	starter := &recordingStarter{}

	ev := slackFrom(trigger, "e1", "U999", true)
	ev.Payload["sender"] = map[string]any{"kind": "slack", "id": "U123", "verified": true}
	res, err := NewIntake(repo, starter, "").Accept(context.Background(), trigger, ev, AcceptOptions{})
	require.NoError(t, err)
	assert.Equal(t, core.TriggerEventSkipped, res.Outcome)
	assert.Empty(t, starter.snapshot())
}

func TestIntakeRefusesAnEventWithNoSender(t *testing.T) {
	repo := newFakeRepo()
	trigger := webhookTrigger(t, "")
	repo.triggers[trigger.ID] = trigger
	ev := inbound(trigger, "d1", nil)
	ev.Sender = nil
	_, err := NewIntake(repo, &recordingStarter{}, "").Accept(context.Background(), trigger, ev, AcceptOptions{})
	require.Error(t, err)
	assert.Empty(t, repo.eventsFor(trigger.ID))
}

func TestIntakeRecordsAPendingEventAndStartsItsFire(t *testing.T) {
	repo := newFakeRepo()
	trigger := webhookTrigger(t, "")
	repo.triggers[trigger.ID] = trigger
	starter := &recordingStarter{}

	res, err := NewIntake(repo, starter, "").Accept(context.Background(), trigger, inbound(trigger, "d1", map[string]any{"body": "x"}), AcceptOptions{})
	require.NoError(t, err)
	assert.Equal(t, core.TriggerEventPending, res.Outcome)
	assert.False(t, res.Duplicate)

	events := repo.eventsFor(trigger.ID)
	require.Len(t, events, 1)
	assert.Equal(t, core.TriggerEventPending, events[0].Outcome)
	assert.Equal(t, trigger.UserID, events[0].UserID, "the event belongs to the trigger's owner")
	assert.Equal(t, res.EventID, events[0].ID)

	starts := starter.snapshot()
	require.Len(t, starts, 1)
	assert.Equal(t, EventFireWorkflowID(events[0].ID), starts[0].ID)
	assert.Equal(t, EventFireWorkflowName, starts[0].Workflow)
	assert.Equal(t, EventFireInput{TriggerID: trigger.ID, Kind: core.TriggerEventKindWebhook, DedupeKey: trigger.ID + ":d1"}, starts[0].Input)
}

// A redelivery reuses the provider's delivery id. It must not write a second
// row; restarting the fire of a still-pending event is allowed because the
// fire's id is the event's, so Temporal attaches rather than duplicating.
func TestIntakeDuplicateDeliveryWritesOneEvent(t *testing.T) {
	repo := newFakeRepo()
	trigger := webhookTrigger(t, "")
	repo.triggers[trigger.ID] = trigger
	starter := &recordingStarter{}
	intake := NewIntake(repo, starter, "")
	ctx := context.Background()

	first, err := intake.Accept(ctx, trigger, inbound(trigger, "d1", nil), AcceptOptions{})
	require.NoError(t, err)
	second, err := intake.Accept(ctx, trigger, inbound(trigger, "d1", nil), AcceptOptions{})
	require.NoError(t, err)

	assert.True(t, second.Duplicate)
	assert.Equal(t, first.EventID, second.EventID, "the duplicate reports the event already recorded")
	assert.Len(t, repo.eventsFor(trigger.ID), 1)
	for _, s := range starter.snapshot() {
		assert.Equal(t, EventFireWorkflowID(first.EventID), s.ID, "every start names the same fire")
	}
}

func TestIntakeFilterMissIsRecordedAsSkippedAndNeverFires(t *testing.T) {
	repo := newFakeRepo()
	trigger := webhookTrigger(t, "trigger.payload.body.action == 'deploy'")
	repo.triggers[trigger.ID] = trigger
	starter := &recordingStarter{}

	res, err := NewIntake(repo, starter, "").Accept(context.Background(), trigger,
		inbound(trigger, "d1", map[string]any{"body": map[string]any{"action": "ping"}}), AcceptOptions{})
	require.NoError(t, err)
	assert.Equal(t, core.TriggerEventSkipped, res.Outcome)

	events := repo.eventsFor(trigger.ID)
	require.Len(t, events, 1, "a filter miss is auditable: it leaves a row")
	assert.Equal(t, core.TriggerEventSkipped, events[0].Outcome)
	assert.Contains(t, events[0].OutcomeDetail, "filter")
	assert.Contains(t, events[0].OutcomeDetail, "trigger.payload.body.action == 'deploy'", "the reason names the filter")
	assert.Empty(t, starter.snapshot(), "a skipped event starts no fire")
}

func TestIntakeFilterThatCannotEvaluateIsRecordedAsFailed(t *testing.T) {
	repo := newFakeRepo()
	trigger := webhookTrigger(t, "trigger.payload.body.missing.field == 1")
	repo.triggers[trigger.ID] = trigger
	starter := &recordingStarter{}

	res, err := NewIntake(repo, starter, "").Accept(context.Background(), trigger,
		inbound(trigger, "d1", map[string]any{"body": map[string]any{}}), AcceptOptions{})
	require.NoError(t, err)
	assert.Equal(t, core.TriggerEventFailed, res.Outcome)
	assert.Contains(t, repo.eventsFor(trigger.ID)[0].OutcomeDetail, "has()", "the detail says how to fix it")
	assert.Empty(t, starter.snapshot())
}

func TestIntakeManualFireBypassesTheFilter(t *testing.T) {
	repo := newFakeRepo()
	trigger := webhookTrigger(t, "false")
	repo.triggers[trigger.ID] = trigger
	starter := &recordingStarter{}

	res, err := NewIntake(repo, starter, "").Accept(context.Background(), trigger,
		inbound(trigger, "manual-1", map[string]any{"manual": true}), AcceptOptions{Manual: true})
	require.NoError(t, err)
	assert.Equal(t, core.TriggerEventPending, res.Outcome)
	starts := starter.snapshot()
	require.Len(t, starts, 1)
	assert.True(t, starts[0].Input.(EventFireInput).Manual)
}

// The row is the durable record; a failed start is repaired by the redriver,
// so it must not turn an accepted delivery into an error the sender sees.
func TestIntakeStartFailureStillAcceptsTheEvent(t *testing.T) {
	repo := newFakeRepo()
	trigger := webhookTrigger(t, "")
	repo.triggers[trigger.ID] = trigger
	starter := &recordingStarter{err: errors.New("temporal unavailable")}

	res, err := NewIntake(repo, starter, "").Accept(context.Background(), trigger, inbound(trigger, "d1", nil), AcceptOptions{})
	require.NoError(t, err)
	assert.Equal(t, core.TriggerEventPending, res.Outcome)
	assert.Len(t, repo.eventsFor(trigger.ID), 1)
}

func pendingEvent(repo *fakeRepo, trigger *core.Trigger, key string, payload map[string]any) *core.TriggerEvent {
	ev := &core.TriggerEvent{
		ID: uuid.NewString(), TriggerID: &trigger.ID, UserID: trigger.UserID,
		Kind: core.TriggerEventKindWebhook, DedupeKey: trigger.ID + ":" + key,
		OccurredAt: time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC), Payload: payload,
		Sender:  &core.TriggerSender{Kind: core.TriggerSenderKindWebhook, ID: trigger.ID, Verified: true},
		Outcome: core.TriggerEventPending, CreatedAt: time.Now(),
	}
	repo.events = append(repo.events, ev)
	return ev
}

func TestEventFireLaunchesThePendingEventAsTheOwner(t *testing.T) {
	repo := newFakeRepo()
	trigger := webhookTrigger(t, "")
	repo.triggers[trigger.ID] = trigger
	ev := pendingEvent(repo, trigger, "d1", map[string]any{"body": map[string]any{"ref": "main"}})
	launcher := &fakeLauncher{}

	out, err := NewEventFirer(repo, launcher).Fire(context.Background(),
		EventFireInput{TriggerID: trigger.ID, Kind: ev.Kind, DedupeKey: ev.DedupeKey})
	require.NoError(t, err)
	assert.Equal(t, string(core.TriggerEventLaunched), out.Outcome)

	calls := launcher.snapshot()
	require.Len(t, calls, 1)
	spec, launched := calls[0].Spec, calls[0].Event
	assert.Equal(t, trigger.UserID, spec.OwnerUserID)
	assert.Equal(t, trigger.ProjectID, spec.ProjectID)
	assert.Equal(t, trigger.DaemonID, spec.DaemonID)
	assert.True(t, spec.Unattended)
	assert.Equal(t, ev.Kind, launched.Kind)
	assert.Equal(t, ev.DedupeKey, launched.DedupeKey, "the launch adopts the receiver's row")
	assert.Equal(t, ev.Payload, launched.Payload, "the payload reaches trigger.payload unchanged")
	assert.Equal(t, ev.OccurredAt, launched.OccurredAt)
	assert.Equal(t, ev.Sender, launched.Sender, "the sender intake recorded reaches trigger.sender")

	// The seed carries the event to the agent as labelled DATA in the user
	// message, never in a system message.
	var user, system string
	for _, m := range spec.Messages {
		switch m.Role {
		case reliantv1.MessageRole_MESSAGE_ROLE_USER:
			user += m.Content
		case reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM:
			system += m.Content
		}
	}
	assert.True(t, strings.HasPrefix(user, trigger.Message))
	assert.Contains(t, user, `"ref":"main"`)
	assert.NotContains(t, system, "main", "payload content never reaches a system message")

	again, err := NewEventFirer(repo, launcher).Fire(context.Background(),
		EventFireInput{TriggerID: trigger.ID, Kind: ev.Kind, DedupeKey: ev.DedupeKey})
	require.NoError(t, err)
	assert.Equal(t, spec.NewChatID, launcher.snapshot()[1].Spec.NewChatID, "a re-fire derives the same chat id")
	assert.Equal(t, out.ChatID, again.ChatID)
}

func TestEventFireOfASettledEventLaunchesNothing(t *testing.T) {
	repo := newFakeRepo()
	trigger := webhookTrigger(t, "")
	repo.triggers[trigger.ID] = trigger
	ev := pendingEvent(repo, trigger, "d1", nil)
	ev.Outcome = core.TriggerEventSkipped
	launcher := &fakeLauncher{}

	out, err := NewEventFirer(repo, launcher).Fire(context.Background(),
		EventFireInput{TriggerID: trigger.ID, Kind: ev.Kind, DedupeKey: ev.DedupeKey})
	require.NoError(t, err)
	assert.Equal(t, string(core.TriggerEventSkipped), out.Outcome)
	assert.Empty(t, launcher.snapshot())
}

func TestEventFireOfADisabledTriggerSettlesSkippedUnlessManual(t *testing.T) {
	repo := newFakeRepo()
	trigger := webhookTrigger(t, "")
	trigger.Enabled = false
	repo.triggers[trigger.ID] = trigger
	launcher := &fakeLauncher{}
	firer := NewEventFirer(repo, launcher)
	ctx := context.Background()

	ev := pendingEvent(repo, trigger, "d1", nil)
	out, err := firer.Fire(ctx, EventFireInput{TriggerID: trigger.ID, Kind: ev.Kind, DedupeKey: ev.DedupeKey})
	require.NoError(t, err)
	assert.Equal(t, string(core.TriggerEventSkipped), out.Outcome)
	assert.Equal(t, core.TriggerEventSkipped, ev.Outcome)
	assert.Empty(t, launcher.snapshot())

	manual := pendingEvent(repo, trigger, "manual", nil)
	out, err = firer.Fire(ctx, EventFireInput{TriggerID: trigger.ID, Kind: manual.Kind, DedupeKey: manual.DedupeKey, Manual: true})
	require.NoError(t, err)
	assert.Equal(t, string(core.TriggerEventLaunched), out.Outcome)
}

func TestEventFireValidationFailureSettlesFailedWithoutRetry(t *testing.T) {
	repo := newFakeRepo()
	trigger := webhookTrigger(t, "")
	repo.triggers[trigger.ID] = trigger
	ev := pendingEvent(repo, trigger, "d1", nil)
	launcher := &fakeLauncher{err: &launch.ValidationError{Reason: "workflow not found"}}

	_, err := NewEventFirer(repo, launcher).Fire(context.Background(),
		EventFireInput{TriggerID: trigger.ID, Kind: ev.Kind, DedupeKey: ev.DedupeKey})
	var appErr *temporal.ApplicationError
	require.ErrorAs(t, err, &appErr)
	assert.True(t, appErr.NonRetryable())
	assert.Equal(t, core.TriggerEventFailed, ev.Outcome)
	assert.Contains(t, ev.OutcomeDetail, "workflow not found")
}

func TestRedriverRestartsStalePendingFiresAndGivesUpOnAncientOnes(t *testing.T) {
	repo := newFakeRepo()
	trigger := webhookTrigger(t, "")
	repo.triggers[trigger.ID] = trigger
	stale := pendingEvent(repo, trigger, "stale", nil)
	stale.CreatedAt = time.Now().Add(-5 * time.Minute)
	fresh := pendingEvent(repo, trigger, "fresh", nil)
	fresh.CreatedAt = time.Now()
	ancient := pendingEvent(repo, trigger, "ancient", nil)
	ancient.CreatedAt = time.Now().Add(-48 * time.Hour)
	starter := &recordingStarter{}

	require.NoError(t, NewRedriver(repo, starter, "").RedriveOnce(context.Background()))

	starts := starter.snapshot()
	require.Len(t, starts, 1, "only the stale, not-yet-abandoned event is restarted")
	assert.Equal(t, EventFireWorkflowID(stale.ID), starts[0].ID)
	assert.Equal(t, core.TriggerEventPending, fresh.Outcome, "a fresh event's own fire is still in flight")
	assert.Equal(t, core.TriggerEventFailed, ancient.Outcome, "a pending event older than the give-up age is closed out")
}

func TestIntegrationEventMatching(t *testing.T) {
	cfg := core.IntegrationConfig{
		Integration: "github",
		Events:      []string{"issues.opened", "pull_request.*"},
		Match:       map[string]string{"repository": "acme/app"},
	}
	attrs := map[string]string{"repository": "acme/app"}
	assert.True(t, IntegrationEventMatches(cfg, "issues.opened", attrs))
	assert.True(t, IntegrationEventMatches(cfg, "pull_request.synchronize", attrs))
	assert.False(t, IntegrationEventMatches(cfg, "issues.closed", attrs))
	assert.False(t, IntegrationEventMatches(cfg, "pull_requestx.opened", attrs), "a wildcard covers one type's actions, not a prefix")
	assert.False(t, IntegrationEventMatches(cfg, "issues.opened", map[string]string{"repository": "acme/other"}))
	assert.False(t, IntegrationEventMatches(cfg, "issues.opened", nil), "a match on an absent attribute is a miss")
	assert.True(t, IntegrationEventMatches(core.IntegrationConfig{Events: []string{"*"}}, "anything", nil))
	assert.False(t, IntegrationEventMatches(core.IntegrationConfig{}, "issues.opened", nil), "no events configured matches nothing")
}
