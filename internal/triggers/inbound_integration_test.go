// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/threads"
)

// These drive the inbound path through the REAL launcher against a real
// database: the receiver's pending row must be ADOPTED by the launch (one row
// per event, no matter how many fires race), which no fake can show.

func newWebhookFixture(t *testing.T, filter string) (*fireFixture, *EventFirer, *Intake, *recordingStarter) {
	t.Helper()
	f := newFireFixture(t, func(tr *core.Trigger, _ *core.ScheduleConfig) {
		tr.Kind = core.TriggerKindWebhook
		tr.Filter = filter
	})
	// newFireFixture stored a schedule config; a webhook trigger has none.
	f.trigger.Config = json.RawMessage(`{}`)
	require.NoError(t, f.repo.UpdateTrigger(context.Background(), f.trigger))
	launcher := launch.NewLauncher(f.repo, threads.NewService(f.repo), f.starter, noopRunRecorder{}, "test-queue", nil)
	starter := &recordingStarter{}
	return f, NewEventFirer(f.repo, launcher), NewIntake(f.repo, starter, "test-queue"), starter
}

func TestInboundDeliveryLaunchesOnceAndAdoptsTheReceiversRow(t *testing.T) {
	f, firer, intake, starter := newWebhookFixture(t, "")
	ctx := context.Background()
	ev := inbound(f.trigger, "delivery-1", map[string]any{"body": map[string]any{"ref": "main"}})

	accepted, err := intake.Accept(ctx, f.trigger, ev, AcceptOptions{})
	require.NoError(t, err)
	require.Equal(t, core.TriggerEventPending, accepted.Outcome)

	// The sender redelivers before the worker gets to it.
	again, err := intake.Accept(ctx, f.trigger, ev, AcceptOptions{})
	require.NoError(t, err)
	assert.True(t, again.Duplicate)

	// Every start the receiver made names the one fire; run it — twice, as
	// a racing redrive would, concurrently.
	input := starter.snapshot()[0].Input.(EventFireInput)
	var wg sync.WaitGroup
	outs := make([]*FireOutput, 2)
	errs := make([]error, 2)
	for i := range outs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			outs[i], errs[i] = firer.Fire(ctx, input)
		}(i)
	}
	wg.Wait()
	for i := range outs {
		require.NoError(t, errs[i])
		assert.Equal(t, string(core.TriggerEventLaunched), outs[i].Outcome)
	}
	assert.Equal(t, 1, f.starter.startCount(), "one delivery, however many fires, starts one run")

	events, _, err := f.repo.ListTriggerEvents(ctx, core.TriggerEventFilters{UserID: f.trigger.UserID, TriggerID: f.trigger.ID, Limit: 10})
	require.NoError(t, err)
	require.Len(t, events, 1, "the launch adopts the receiver's row instead of writing a second one")
	stored := events[0].Event
	assert.Equal(t, accepted.EventID, stored.ID)
	assert.Equal(t, core.TriggerEventLaunched, stored.Outcome)
	require.NotNil(t, stored.ChatID)
	assert.Equal(t, outs[0].ChatID, *stored.ChatID)
	body, _ := stored.Payload["body"].(map[string]any)
	assert.Equal(t, "main", body["ref"], "the event payload survives the adoption")
	assert.NotNil(t, stored.Payload["start"], "and gains the launch's start record")
}

func TestInboundFilterMissNeverReachesTheLauncher(t *testing.T) {
	f, _, intake, starter := newWebhookFixture(t, "trigger.payload.body.ref == 'release'")
	ctx := context.Background()

	res, err := intake.Accept(ctx, f.trigger, inbound(f.trigger, "d1", map[string]any{"body": map[string]any{"ref": "main"}}), AcceptOptions{})
	require.NoError(t, err)
	assert.Equal(t, core.TriggerEventSkipped, res.Outcome)
	assert.Empty(t, starter.snapshot())

	stored, err := f.repo.GetTriggerEventByDedupe(ctx, core.TriggerEventKindWebhook, f.trigger.ID+":d1")
	require.NoError(t, err)
	assert.Equal(t, core.TriggerEventSkipped, stored.Outcome)
	assert.Contains(t, stored.OutcomeDetail, "filter did not match")
}
