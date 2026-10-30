package store_test

import (
	"context"
	"encoding/hex"
	"math/rand/v2"
	"testing"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
	"github.com/reliant-labs/reliant/internal/triggers/triggerspec"
	"github.com/stretchr/testify/require"
)

func TestCapacityLongKeysEnrollWithoutPrefixCollisions(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	// Deterministic, incompressible content also exceeds the B-tree entry
	// limit, not just the renderer's former 4096-byte truncation threshold.
	rng := rand.New(rand.NewPCG(19, 91))
	bytes := make([]byte, 3000)
	for i := range bytes {
		bytes[i] = byte(rng.Uint32())
	}
	prefix := hex.EncodeToString(bytes)
	keyFor := func(value string) string {
		key, err := triggerspec.RenderConcurrencyKey("{{ trigger.payload.key }}", triggerspec.Scope{Trigger: map[string]any{"payload": map[string]any{"key": value}}})
		require.NoError(t, err)
		require.Len(t, key, 64, "only a fixed-size digest is stored/indexed")
		return key
	}
	a, b := keyFor(prefix+"a"), keyFor(prefix+"b")
	require.NotEqual(t, a, b)
	for _, entry := range []struct{ id, key string }{{"long-a", a}, {"long-b", b}, {"new-long-a", a}} {
		require.NoError(t, store.EnqueueTriggerCapacity(ctx, env.ORM, keyedCapacityEvent(entry.id, &entry.key), capacityScope()))
	}
	outcome, _ := outcomeOf(t, env, "long-a")
	require.Equal(t, core.TriggerEventSkipped, outcome)
	for _, id := range []string{"long-b", "new-long-a"} {
		outcome, _ = outcomeOf(t, env, id)
		require.Equal(t, core.TriggerEventPending, outcome)
	}
}

func keyedCapacityEvent(id string, key *string) *core.TriggerEvent {
	ev := capacityEvent(id)
	ev.CapacityKey = key
	return ev
}

func outcomeOf(t *testing.T, env *storetest.Env, id string) (core.TriggerEventOutcome, string) {
	t.Helper()
	ev, err := store.GetTriggerEventByDedupe(context.Background(), env.ORM, core.TriggerEventKindIntegration, id)
	require.NoError(t, err)
	return ev.Outcome, ev.OutcomeDetail
}

// Enrolling a keyed event supersedes the older WAITING events of its scope
// and key, and nothing else: not other keys, not FIFO (unkeyed) events, not a
// launched event, not another scope; and a retried enrollment changes nothing.
func TestTriggerCapacityLatestSupersedesOnlyOlderWaitingOfItsKey(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	scope := capacityScope()
	other := scope
	other.WorkflowRef = "project://other"
	a, b := "reliant-labs/reliant", "reliant-labs/forge"

	require.NoError(t, store.EnqueueTriggerCapacity(ctx, env.ORM, keyedCapacityEvent("running-a", &a), scope))
	claimed, err := claimCapacity(ctx, env, eventID(t, env, "running-a"), scope, 1)
	require.NoError(t, err)
	require.True(t, claimed)
	for _, ev := range []*core.TriggerEvent{
		keyedCapacityEvent("old-a", &a),
		keyedCapacityEvent("old-b", &b),
		keyedCapacityEvent("fifo", nil),
	} {
		require.NoError(t, store.EnqueueTriggerCapacity(ctx, env.ORM, ev, scope))
	}
	require.NoError(t, store.EnqueueTriggerCapacity(ctx, env.ORM, keyedCapacityEvent("other-scope-a", &a), other))

	newest := keyedCapacityEvent("new-a", &a)
	require.NoError(t, store.EnqueueTriggerCapacity(ctx, env.ORM, newest, scope))

	outcome, detail := outcomeOf(t, env, "old-a")
	require.Equal(t, core.TriggerEventSkipped, outcome)
	require.Equal(t, store.TriggerCapacitySuperseded, detail)
	for _, id := range []string{"old-b", "fifo", "other-scope-a", "new-a"} {
		outcome, _ := outcomeOf(t, env, id)
		require.Equal(t, core.TriggerEventPending, outcome, id)
	}
	outcome, _ = outcomeOf(t, env, "running-a")
	require.Equal(t, core.TriggerEventLaunched, outcome)

	// Retrying the newest enrollment (an ack lost after commit) is a no-op.
	require.NoError(t, store.EnqueueTriggerCapacity(ctx, env.ORM, newest, scope))
	outcome, _ = outcomeOf(t, env, "new-a")
	require.Equal(t, core.TriggerEventPending, outcome)

	// The superseded event no longer holds the FIFO head: the next waiting
	// event of the scope launches once the slot is free.
	allowed, err := claimCapacity(ctx, env, eventID(t, env, "old-b"), scope, 2)
	require.NoError(t, err)
	require.True(t, allowed)
}

func eventID(t *testing.T, env *storetest.Env, dedupe string) string {
	t.Helper()
	ev, err := store.GetTriggerEventByDedupe(context.Background(), env.ORM, core.TriggerEventKindIntegration, dedupe)
	require.NoError(t, err)
	return ev.ID
}
