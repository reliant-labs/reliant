// Copyright (c) 2025 Reliant Labs
package workflowevent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// memRelayStore is an outbox in memory: every unclaimed row is claimable once.
type memRelayStore struct {
	rows       []*core.RunEvent
	claimed    map[string]bool
	dispatched map[string]bool
}

func (s *memRelayStore) ClaimRunEvents(_ context.Context, _, _ time.Time, max int) ([]*core.RunEvent, error) {
	if s.claimed == nil {
		s.claimed = map[string]bool{}
	}
	var out []*core.RunEvent
	for _, ev := range s.rows {
		if len(out) == max {
			break
		}
		if !s.claimed[ev.ID] && !s.dispatched[ev.ID] {
			s.claimed[ev.ID] = true
			out = append(out, ev)
		}
	}
	return out, nil
}

func (s *memRelayStore) MarkRunEventDispatched(_ context.Context, id string, _ time.Time) error {
	if s.dispatched == nil {
		s.dispatched = map[string]bool{}
	}
	s.dispatched[id] = true
	return nil
}

func (s *memRelayStore) DeleteDispatchedRunEventsBefore(context.Context, time.Time) (int64, error) {
	return 0, nil
}

// memRoutes lists fixed triggers, counting the listings.
type memRoutes struct {
	triggers []*core.Trigger
	err      error
	calls    int
}

func (r *memRoutes) ListTriggersForUser(_ context.Context, f core.TriggerFilters) ([]*core.Trigger, error) {
	r.calls++
	if r.err != nil {
		return nil, r.err
	}
	var out []*core.Trigger
	for _, t := range r.triggers {
		if t.UserID == f.UserID {
			out = append(out, t)
		}
	}
	return out, nil
}

func listener(t *testing.T, userID string, src Source) *core.Trigger {
	t.Helper()
	config, err := json.Marshal(src)
	require.NoError(t, err)
	return &core.Trigger{ID: "trigger-" + userID, UserID: userID, Kind: core.TriggerKindWorkflowEvent, Enabled: true, Config: config}
}

func runEvent(id, userID, workflow string, outcome core.RunEventOutcome) *core.RunEvent {
	return &core.RunEvent{ID: id, UserID: userID, WorkflowName: workflow, Outcome: outcome}
}

func TestMayReach(t *testing.T) {
	const user = "u1"
	reviews := listener(t, user, Source{Workflows: []string{"code-review"}, Outcomes: []string{"failed"}})
	declared := "on-review-failure"
	activation := listener(t, user, Source{Workflows: []string{"Code Review"}})
	activation.WorkflowTrigger = &declared
	disabled := listener(t, user, Source{})
	disabled.Enabled = false
	schedule := listener(t, user, Source{})
	schedule.Kind = core.TriggerKindSchedule
	stranger := listener(t, "u2", Source{})
	broken := listener(t, user, Source{})
	broken.Config = json.RawMessage(`{"workflows": "not a list"}`)

	cases := []struct {
		name       string
		candidates []*core.Trigger
		ev         *core.RunEvent
		want       bool
	}{
		{"its workflow and outcome", []*core.Trigger{reviews}, runEvent("e", user, "code-review", core.RunEventFailed), true},
		{"another outcome", []*core.Trigger{reviews}, runEvent("e", user, "code-review", core.RunEventFinished), false},
		{"another workflow", []*core.Trigger{reviews}, runEvent("e", user, "deploy", core.RunEventFailed), false},
		{"an activation by its projection, display name and slug alike", []*core.Trigger{activation}, runEvent("e", user, "code-review", core.RunEventFinished), true},
		{"a source naming no workflow hears every one", []*core.Trigger{listener(t, user, Source{})}, runEvent("e", user, "anything", core.RunEventBlocked), true},
		{"a disabled trigger", []*core.Trigger{disabled}, runEvent("e", user, "deploy", core.RunEventFinished), false},
		{"another kind of trigger", []*core.Trigger{schedule}, runEvent("e", user, "deploy", core.RunEventFinished), false},
		{"another owner's trigger", []*core.Trigger{stranger}, runEvent("e", user, "deploy", core.RunEventFinished), false},
		{"a config that does not parse is dispatched, to record why", []*core.Trigger{broken}, runEvent("e", user, "deploy", core.RunEventFinished), true},
		{"no triggers", nil, runEvent("e", user, "deploy", core.RunEventFinished), false},
		{"any one of several", []*core.Trigger{disabled, reviews, activation}, runEvent("e", user, "code-review", core.RunEventFinished), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, MayReach(tc.candidates, tc.ev))
		})
	}
}

// A run event no trigger listens to is settled without a dispatch workflow;
// one a trigger listens to is still dispatched. The owner's triggers are
// listed once for the pass.
func TestRelay_SettlesUnreachedRunEventsWithoutAWorkflow(t *testing.T) {
	ctx := context.Background()
	outbox := &memRelayStore{rows: []*core.RunEvent{
		runEvent("heard", "u1", "code-review", core.RunEventFailed),
		runEvent("unheard-1", "u1", "deploy", core.RunEventFinished),
		runEvent("unheard-2", "u1", "code-review", core.RunEventFinished),
	}}
	routes := &memRoutes{triggers: []*core.Trigger{listener(t, "u1", Source{Workflows: []string{"code-review"}, Outcomes: []string{"failed"}})}}
	starter := &recordingStarter{}
	relay := NewRelay(outbox, starter, "test-queue").WithRoutes(routes)

	handed, err := relay.RelayOnce(ctx)
	require.NoError(t, err)
	assert.Equal(t, 3, handed, "every row is settled")
	assert.Equal(t, map[string]bool{DispatchWorkflowID("heard"): true}, starter.started, "only the heard event starts a dispatch")
	assert.Equal(t, map[string]bool{"heard": true, "unheard-1": true, "unheard-2": true}, outbox.dispatched)
	assert.Equal(t, 1, routes.calls, "one listing per owner per pass")

	handed, err = relay.RelayOnce(ctx)
	require.NoError(t, err)
	assert.Zero(t, handed, "settled rows are not claimed again")
}

// Triggers that cannot be listed never drop an event: it is dispatched, and
// the dispatch decides as it always did.
func TestRelay_DispatchesWhenTheOwnersTriggersCannotBeListed(t *testing.T) {
	outbox := &memRelayStore{rows: []*core.RunEvent{runEvent("e1", "u1", "deploy", core.RunEventFinished)}}
	starter := &recordingStarter{}
	relay := NewRelay(outbox, starter, "test-queue").WithRoutes(&memRoutes{err: errors.New("database unavailable")})

	handed, err := relay.RelayOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, handed)
	assert.True(t, starter.started[DispatchWorkflowID("e1")])
}

// Without routes the relay dispatches every row, as before.
func TestRelay_WithoutRoutesDispatchesEveryRow(t *testing.T) {
	outbox := &memRelayStore{rows: []*core.RunEvent{runEvent("e1", "u1", "deploy", core.RunEventFinished)}}
	starter := &recordingStarter{}
	handed, err := NewRelay(outbox, starter, "test-queue").RelayOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, handed)
	assert.True(t, starter.started[DispatchWorkflowID("e1")])
}

// Against the real outbox and trigger table: the owner's trigger listens to
// code-review, so a finished deploy run is settled with no dispatch.
func TestRelay_RealStore_UnheardRunEventStartsNoDispatch(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.addTrigger(f.userID, f.projectID, "listener", "builtin://agent", Source{Workflows: []string{"code-review"}})
	heard, err := emitFinished(ctx, f, f.humanRun("code-review"))
	require.NoError(t, err)
	_, err = emitFinishedAs(ctx, f, f.humanRun("deploy"), "deploy")
	require.NoError(t, err)

	starter := &recordingStarter{}
	relay := NewRelay(store.NewRunEventExtStore(f.repo.ORM()), starter, "test-queue").WithRoutes(store.NewTriggerExtStore(f.repo.ORM()))
	handed, err := relay.RelayOnce(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, handed)
	assert.Equal(t, map[string]bool{DispatchWorkflowID(heard.ID): true}, starter.started)

	handed, err = relay.RelayOnce(ctx)
	require.NoError(t, err)
	assert.Zero(t, handed, "the unheard row is settled, not left for a later pass")
}
