// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"context"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/client"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/forge/pkg/testkit"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/triggers"
)

const fireInputsYAML = `name: approved-run
inputs:
  action_id: {type: string, default: ""}
  count: {type: integer, default: 0}
  mode: {type: string, default: ""}
  fixed: {type: string, default: stored}
  model: {type: model}
triggers:
  - name: approve
    webhook: {}
    inputs:
      mode: "from-event"
  - name: nightly
    schedule: {cron: "0 9 * * *"}
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`

// startRecorder stands in for Temporal: it records the fire workflows the
// real intake starts, and makes no run of them.
type startRecorder struct {
	mu  sync.Mutex
	ids []string
}

func (r *startRecorder) ExecuteWorkflow(_ context.Context, opts client.StartWorkflowOptions, _ any, _ ...any) (client.WorkflowRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ids = append(r.ids, opts.ID)
	return nil, nil
}

func (r *startRecorder) started() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ids...)
}

// fireInputsEnv serves FireTrigger over the REAL intake and event store, so
// dedupe is the database's, with Temporal replaced by a recorder.
func fireInputsEnv(t *testing.T) (*triggerTestEnv, *startRecorder, *core.Trigger) {
	t.Helper()
	env := setupTriggerTest(t)
	rec := &startRecorder{}
	intake := triggers.NewIntake(triggers.NewStores(env.repo), rec, "q").
		WithWorkflows(triggers.LaunchWorkflows{Repo: env.repo})
	env.configure(t, func(d *Deps) { d.Intake = intake })
	env.saveWorkflow(t, "approved-run", fireInputsYAML)
	created := env.create(t, env.activation("approve", func(d *reliantv1.TriggerDefinition) {
		d.Workflow = "approved-run"
		d.Params = map[string]*structpb.Value{"fixed": structpb.NewStringValue("stored")}
	}))
	row, err := env.repo.GetTrigger(context.Background(), created.GetId())
	require.NoError(t, err)
	return env, rec, row
}

func (e *triggerTestEnv) fire(t *testing.T, req *reliantv1.FireTriggerRequest) (*reliantv1.FireTriggerResponse, error) {
	t.Helper()
	resp, err := e.svc.FireTrigger(e.ctx, connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func str(v string) *structpb.Value { return structpb.NewStringValue(v) }
func keyPtr(v string) *string      { return &v }

func TestFireTriggerRefusesUndeclaredInputs(t *testing.T) {
	testkit.Slow(t, "a DB-backed trigger handler test with many round trips to pgtest's Postgres")
	t.Parallel()

	env, rec, trig := fireInputsEnv(t)

	cases := []struct {
		name   string
		inputs map[string]*structpb.Value
		reason string
	}{
		{"undeclared name", map[string]*structpb.Value{"not_an_input": str("x")}, `"not_an_input" is not an input`},
		{"near miss names the right one", map[string]*structpb.Value{"action": str("x")}, "action_id"},
		{"wrong type", map[string]*structpb.Value{"count": str("many")}, "count"},
		{"mapped from the event", map[string]*structpb.Value{"mode": str("attacker")}, "set from each event"},
		{"set by the activation", map[string]*structpb.Value{"fixed": str("other")}, "set by the activation"},
		{"chooses what the run may use", map[string]*structpb.Value{"model": str("anything")}, "cannot be supplied"},
		{"one bad input refuses the whole request", map[string]*structpb.Value{"action_id": str("ok"), "not_an_input": str("x")}, "not_an_input"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := env.fire(t, &reliantv1.FireTriggerRequest{Id: trig.ID, Inputs: tc.inputs, IdempotencyKey: keyPtr("k-refused")})
			require.Error(t, err)
			assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err), err.Error())
			assert.Contains(t, err.Error(), tc.reason)
		})
	}
	assert.Empty(t, rec.started(), "a refused request started nothing")
	assert.Empty(t, env.firings(t, trig.ID), "and recorded nothing, not even under its key")
}

func (e *triggerTestEnv) firings(t *testing.T, triggerID string) []*core.TriggerEventWithRun {
	t.Helper()
	rows, _, err := e.deps.FiringReads.ListTriggerEventsPage(context.Background(), core.TriggerEventFilters{TriggerID: triggerID, UserID: e.userID, Limit: 50})
	require.NoError(t, err)
	return rows
}

func TestFireTriggerAcceptsDeclaredInputsAndRecordsThem(t *testing.T) {
	testkit.Slow(t, "a DB-backed trigger handler test with many round trips to pgtest's Postgres")
	t.Parallel()

	env, rec, trig := fireInputsEnv(t)
	resp, err := env.fire(t, &reliantv1.FireTriggerRequest{Id: trig.ID, Inputs: map[string]*structpb.Value{
		"action_id": str("act-1"), "count": structpb.NewNumberValue(3),
	}})
	require.NoError(t, err)
	require.NotEmpty(t, resp.EventId)
	assert.Equal(t, triggers.EventFireWorkflowID(resp.EventId), resp.FireWorkflowId)
	assert.Equal(t, []string{resp.FireWorkflowId}, rec.started())

	events := env.firings(t, trig.ID)
	require.Len(t, events, 1)
	assert.Equal(t, resp.EventId, events[0].Event.ID)
	assert.Equal(t, core.TriggerEventPending, events[0].Event.Outcome, "%s", events[0].Event.OutcomeDetail)
	assert.Equal(t, map[string]any{"action_id": "act-1", "count": float64(3)}, events[0].Event.Payload[triggers.PayloadInputs])
	assert.Equal(t, triggers.EventChatID(events[0].Event.Kind, events[0].Event.DedupeKey), resp.ChatId)
}

func TestFireTriggerReplayReturnsTheFirstEvent(t *testing.T) {
	testkit.Slow(t, "a DB-backed trigger handler test with many round trips to pgtest's Postgres")
	t.Parallel()

	env, rec, trig := fireInputsEnv(t)
	key := keyPtr("approve-act-1")

	first, err := env.fire(t, &reliantv1.FireTriggerRequest{Id: trig.ID, IdempotencyKey: key,
		Inputs: map[string]*structpb.Value{"action_id": str("act-1")}})
	require.NoError(t, err)
	require.NotEmpty(t, first.EventId)
	require.NotEmpty(t, first.ChatId)

	// The same key again, even with a different input, is the first event.
	again, err := env.fire(t, &reliantv1.FireTriggerRequest{Id: trig.ID, IdempotencyKey: key,
		Inputs: map[string]*structpb.Value{"action_id": str("act-2")}})
	require.NoError(t, err)
	assert.Equal(t, first.EventId, again.EventId)
	assert.Equal(t, first.ChatId, again.ChatId)
	assert.Equal(t, first.FireWorkflowId, again.FireWorkflowId)

	events := env.firings(t, trig.ID)
	require.Len(t, events, 1, "no second event, so no second run")
	assert.Equal(t, map[string]any{"action_id": "act-1"}, events[0].Event.Payload[triggers.PayloadInputs],
		"the first request's inputs stand")
	assert.Equal(t, 1, len(unique(rec.started())), "every start names the one fire workflow, which Temporal deduplicates")

	// A different key is a new firing.
	other, err := env.fire(t, &reliantv1.FireTriggerRequest{Id: trig.ID, IdempotencyKey: keyPtr("approve-act-2")})
	require.NoError(t, err)
	assert.NotEqual(t, first.EventId, other.EventId)
	assert.NotEqual(t, first.ChatId, other.ChatId)
	assert.Len(t, env.firings(t, trig.ID), 2)
}

func unique(in []string) map[string]bool {
	out := map[string]bool{}
	for _, s := range in {
		out[s] = true
	}
	return out
}

// A key belongs to one trigger: the same key on another of the owner's
// triggers is its own firing, and it never returns the first trigger's event.
func TestFireTriggerKeysAreScopedToTheTrigger(t *testing.T) {
	testkit.Slow(t, "a DB-backed trigger handler test with many round trips to pgtest's Postgres")
	t.Parallel()

	env, _, trig := fireInputsEnv(t)
	second := env.create(t, env.activation("approve", func(d *reliantv1.TriggerDefinition) {
		d.Name = "second approve"
		d.Workflow = "approved-run"
	}))

	a, err := env.fire(t, &reliantv1.FireTriggerRequest{Id: trig.ID, IdempotencyKey: keyPtr("shared")})
	require.NoError(t, err)
	b, err := env.fire(t, &reliantv1.FireTriggerRequest{Id: second.GetId(), IdempotencyKey: keyPtr("shared")})
	require.NoError(t, err)
	assert.NotEqual(t, a.EventId, b.EventId)
	assert.NotEqual(t, a.ChatId, b.ChatId)
	assert.Len(t, env.firings(t, trig.ID), 1)
	assert.Len(t, env.firings(t, second.GetId()), 1)
}

// Another user's key is never reachable: ownership is checked first, and
// answered exactly as before.
func TestFireTriggerKeyOfAnotherOwnersTriggerIsNotFound(t *testing.T) {
	testkit.Slow(t, "a DB-backed trigger handler test with many round trips to pgtest's Postgres")
	t.Parallel()

	env, _, trig := fireInputsEnv(t)
	_, err := env.fire(t, &reliantv1.FireTriggerRequest{Id: trig.ID, IdempotencyKey: keyPtr("k")})
	require.NoError(t, err)

	otherCtx := context.WithValue(context.Background(), auth.UserIDContextKey, uuid.NewString())
	_, err = env.svc.FireTrigger(otherCtx, connect.NewRequest(&reliantv1.FireTriggerRequest{Id: trig.ID, IdempotencyKey: keyPtr("k"),
		Inputs: map[string]*structpb.Value{"action_id": str("x")}}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, svcerr.Code(err))
}

func TestFireTriggerBoundsAreFailClosed(t *testing.T) {
	testkit.Slow(t, "a DB-backed trigger handler test with many round trips to pgtest's Postgres")
	t.Parallel()

	env, rec, trig := fireInputsEnv(t)
	many := map[string]*structpb.Value{}
	for i := 0; i < maxFireInputs+1; i++ {
		many[strings.Repeat("a", i+1)] = str("x")
	}
	cases := []struct {
		name string
		req  *reliantv1.FireTriggerRequest
	}{
		{"empty key", &reliantv1.FireTriggerRequest{Id: trig.ID, IdempotencyKey: keyPtr("")}},
		{"key too long", &reliantv1.FireTriggerRequest{Id: trig.ID, IdempotencyKey: keyPtr(strings.Repeat("k", maxIdempotencyKeyLen+1))}},
		{"key with a space", &reliantv1.FireTriggerRequest{Id: trig.ID, IdempotencyKey: keyPtr("a b")}},
		{"key with a newline", &reliantv1.FireTriggerRequest{Id: trig.ID, IdempotencyKey: keyPtr("a\nb")}},
		{"key not ASCII", &reliantv1.FireTriggerRequest{Id: trig.ID, IdempotencyKey: keyPtr("clé")}},
		{"too many inputs", &reliantv1.FireTriggerRequest{Id: trig.ID, Inputs: many}},
		{"input name too long", &reliantv1.FireTriggerRequest{Id: trig.ID, Inputs: map[string]*structpb.Value{strings.Repeat("n", maxFireInputNameLen+1): str("x")}}},
		{"empty input name", &reliantv1.FireTriggerRequest{Id: trig.ID, Inputs: map[string]*structpb.Value{"": str("x")}}},
		{"oversized value", &reliantv1.FireTriggerRequest{Id: trig.ID, Inputs: map[string]*structpb.Value{"action_id": str(strings.Repeat("v", maxFireInputsBytes+1))}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := env.fire(t, tc.req)
			require.Error(t, err)
			assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err), err.Error())
		})
	}
	assert.Empty(t, rec.started())

	_, err := env.fire(t, &reliantv1.FireTriggerRequest{Id: trig.ID, IdempotencyKey: keyPtr(strings.Repeat("k", maxIdempotencyKeyLen))})
	require.NoError(t, err, "255 characters is the limit, and within it")
}

// Only an event trigger takes inputs or a key; a schedule trigger refuses
// them rather than quietly dropping what the caller asked for.
func TestFireTriggerScheduleRefusesInputsAndKey(t *testing.T) {
	testkit.Slow(t, "a DB-backed trigger handler test with many round trips to pgtest's Postgres")
	t.Parallel()

	env := setupTriggerTest(t)
	created := env.create(t, env.definition(nil))

	for name, req := range map[string]*reliantv1.FireTriggerRequest{
		"inputs": {Id: created.GetId(), Inputs: map[string]*structpb.Value{"a": str("b")}},
		"key":    {Id: created.GetId(), IdempotencyKey: keyPtr("k")},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := env.fire(t, req)
			require.Error(t, err)
			assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
		})
	}
	env.backend.mu.Lock()
	assert.Empty(t, env.backend.fires)
	env.backend.mu.Unlock()
}

// An ad hoc event trigger declares no inputs to supply.
func TestFireTriggerAdHocTriggerRefusesInputs(t *testing.T) {
	testkit.Slow(t, "a DB-backed trigger handler test with many round trips to pgtest's Postgres")
	t.Parallel()

	env, intake := setupInboundTriggerTest(t)
	created := env.create(t, webhookDefinition(env, nil))
	_, err := env.fire(t, &reliantv1.FireTriggerRequest{Id: created.GetId(), Inputs: map[string]*structpb.Value{"a": str("b")}})
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
	assert.Empty(t, intake.accepted)
}

// With no inputs and no key a request behaves exactly as before: a fresh
// manual event each press, never deduplicated.
func TestFireTriggerWithoutInputsOrKeyIsUnchanged(t *testing.T) {
	testkit.Slow(t, "a DB-backed trigger handler test with many round trips to pgtest's Postgres")
	t.Parallel()

	env, rec, trig := fireInputsEnv(t)
	a, err := env.fire(t, &reliantv1.FireTriggerRequest{Id: trig.ID})
	require.NoError(t, err)
	b, err := env.fire(t, &reliantv1.FireTriggerRequest{Id: trig.ID})
	require.NoError(t, err)
	assert.NotEqual(t, a.EventId, b.EventId)
	assert.Len(t, rec.started(), 2)
	for _, e := range env.firings(t, trig.ID) {
		assert.NotContains(t, e.Event.Payload, triggers.PayloadInputs)
		assert.True(t, strings.HasPrefix(e.Event.DedupeKey, trig.ID+":manual-"))
	}
}
