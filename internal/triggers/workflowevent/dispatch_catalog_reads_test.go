// Copyright (c) 2025 Reliant Labs
package workflowevent

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/triggers"
)

// dispatchReadCountingRepo is the real store, counting the catalog reads and
// pins a dispatch repeats for every trigger it matches the event against.
type dispatchReadCountingRepo struct {
	*db.Repo
	mu                         sync.Mutex
	machineReads, pins, pinned int
}

func (r *dispatchReadCountingRepo) count(n *int) {
	r.mu.Lock()
	*n++
	r.mu.Unlock()
}

func (r *dispatchReadCountingRepo) GetProjectMachineWorkflowsJSON(ctx context.Context, projectID, daemonID string) (*string, bool, error) {
	r.count(&r.machineReads)
	return r.Repo.GetProjectMachineWorkflowsJSON(ctx, projectID, daemonID)
}

func (r *dispatchReadCountingRepo) PinProjectCatalog(ctx context.Context, scope db.CatalogPinScope) (string, error) {
	r.count(&r.pins)
	return r.Repo.PinProjectCatalog(ctx, scope)
}

func (r *dispatchReadCountingRepo) GetPinnedProjectCatalog(ctx context.Context, pin string) (*db.PinnedCatalog, bool, error) {
	r.count(&r.pinned)
	return r.Repo.GetPinnedProjectCatalog(ctx, pin)
}

// One run event is matched against every workflow_event trigger of its
// owner, and each match resolves its workflow's declaration live, pins the
// catalog, and resolves it again in the pin. Every one of those re-read the
// same machine catalog and the same snapshot: prod's DispatchRunEvent made 30
// machine-catalog reads, 32 snapshot reads and 4 pins in one 6.7s activity
// (trace b7c5ef7b…, 2026-11-24); these three activations made 6 machine
// catalog reads, 18 snapshot reads and 3 pins.
// The dispatch reads through one memo now (triggers.ForOneArrival): each
// catalog once, and each scope pinned once.
func TestDispatchReadsEachCatalogOncePerEvent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	repo := &dispatchReadCountingRepo{Repo: f.repo}
	f.dispatcher.WithWorkflows(triggers.LaunchWorkflows{Repo: repo})
	f.syncReactor(reactorYAML, time.UnixMilli(1000))
	const activations = 3
	for i := range activations {
		f.addNamedActivation(fmt.Sprintf("reactor %d", i), "reactor", "on-review-failure",
			Source{Workflows: []string{"code-review"}, Outcomes: []string{"failed"}}, nil)
	}

	results, err := f.dispatcher.Dispatch(ctx, f.finish(f.humanRun("code-review"), core.RunEventFailed))
	require.NoError(t, err)
	require.Len(t, results, activations)
	pins := map[string]bool{}
	for _, r := range results {
		require.Equal(t, core.TriggerEventLaunched, r.Outcome, r.Detail)
		ev, err := store.GetTriggerEventByChatID(ctx, f.repo.ORM(), r.ChatID)
		require.NoError(t, err)
		require.NotEmpty(t, ev.Definition.CatalogPin)
		pins[ev.Definition.CatalogPin] = true
	}
	assert.Len(t, pins, 1, "every match of one event launches on the one pin")

	repo.mu.Lock()
	defer repo.mu.Unlock()
	assert.Equal(t, 1, repo.machineReads, "the machine's catalog is read once for the whole dispatch")
	assert.Equal(t, 1, repo.pins, "the scope is pinned once")
	assert.Equal(t, 1, repo.pinned, "the snapshot is read once")
}
