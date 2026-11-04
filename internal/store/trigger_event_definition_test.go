// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// Two fires of one pending inbound event race: one launches against the
// definition it resolved, the other resolved a newer one (say, broken) and
// settles or claims after losing. The definition is written in the same
// pending-guarded statement as the claim or the settlement, so the loser can
// never rewrite what the winner launched against.
func TestPendingEventDefinitionIsWrittenOnlyByTheFireThatWins(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Microsecond)
	require.NoError(t, store.InsertTrigger(ctx, env.ORM, newStoreTrigger("trg-def", "def", base)))
	defA := &core.TriggerEventDefinition{Workflow: "release", Source: "project", Path: ".reliant/workflows/release.yaml", ContentHash: "aaaa"}
	defB := &core.TriggerEventDefinition{Workflow: "release", Source: "project", Path: ".reliant/workflows/release.yaml", ContentHash: "bbbb", Error: "does not parse"}
	intake := &core.TriggerEventDefinition{Workflow: "release", ContentHash: "intake"}
	pending := func(id string) {
		t.Helper()
		ev := newStoreTriggerEvent(id, "trg-def", core.TriggerEventPending, base)
		ev.Kind = core.TriggerEventKindWebhook
		ev.Definition = intake
		_, err := store.InsertTriggerEventIfAbsent(ctx, env.ORM, ev)
		require.NoError(t, err)
	}
	stored := func(id string) *core.TriggerEvent {
		t.Helper()
		ev, err := store.GetTriggerEventByDedupe(ctx, env.ORM, core.TriggerEventKindWebhook, "dd-"+id)
		require.NoError(t, err)
		return ev
	}

	// In order: the claim wins, a later settle and claim with another
	// definition change nothing.
	pending("ev-a")
	claimed, err := store.ClaimPendingTriggerEvent(ctx, env.ORM, "ev-a", map[string]any{}, defA)
	require.NoError(t, err)
	require.True(t, claimed)
	settled, err := store.SettlePendingTriggerEvent(ctx, env.ORM, "ev-a", core.TriggerEventFailed, "refused", defB)
	require.NoError(t, err)
	assert.False(t, settled)
	claimed, err = store.ClaimPendingTriggerEvent(ctx, env.ORM, "ev-a", map[string]any{}, defB)
	require.NoError(t, err)
	assert.False(t, claimed)
	assert.Equal(t, defA, stored("ev-a").Definition, "the launched row keeps the winner's definition")

	// A settlement without a definition keeps the one recorded at intake.
	pending("ev-keep")
	settled, err = store.SettlePendingTriggerEvent(ctx, env.ORM, "ev-keep", core.TriggerEventSkipped, "deleted", nil)
	require.NoError(t, err)
	require.True(t, settled)
	assert.Equal(t, intake, stored("ev-keep").Definition)

	// Concurrently: whichever of the claim and the settlement wins, the row
	// carries that one's definition and outcome together.
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("ev-race-%d", i)
		pending(id)
		var (
			wg                sync.WaitGroup
			claimWon, settled bool
			claimErr, setErr  error
		)
		wg.Add(2)
		go func() {
			defer wg.Done()
			claimWon, claimErr = store.ClaimPendingTriggerEvent(ctx, env.ORM, id, map[string]any{}, defA)
		}()
		go func() {
			defer wg.Done()
			settled, setErr = store.SettlePendingTriggerEvent(ctx, env.ORM, id, core.TriggerEventFailed, "refused", defB)
		}()
		wg.Wait()
		require.NoError(t, claimErr)
		require.NoError(t, setErr)
		require.NotEqual(t, claimWon, settled, "exactly one fire wins")
		ev := stored(id)
		if claimWon {
			assert.Equal(t, core.TriggerEventLaunched, ev.Outcome)
			assert.Equal(t, defA, ev.Definition)
		} else {
			assert.Equal(t, core.TriggerEventFailed, ev.Outcome)
			assert.Equal(t, defB, ev.Definition)
		}
	}
}
