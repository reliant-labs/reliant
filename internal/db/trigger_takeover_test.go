// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// takeoverTrigger is a schedule trigger of creator in acme.
func takeoverTrigger(t *testing.T, repo *Repo, creator string) *core.Trigger {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	acme := "acme"
	project := &Project{ID: uuid.NewString(), UserID: creator, OrgID: &acme, Name: "p", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now, LastActive: now}
	require.NoError(t, repo.CreateProject(ctx, project))
	trig := &core.Trigger{
		ID: uuid.NewString(), UserID: creator, OrgID: &acme, ProjectID: project.ID, Name: "nightly",
		Kind: core.TriggerKindSchedule, Enabled: false, Workflow: "builtin://agent", Message: "go", NoMachine: true,
		Config: json.RawMessage(`{"cron":["0 3 * * *"]}`), CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, repo.CreateTrigger(ctx, trig))
	return trig
}

func offerFor(trig *core.Trigger, offeree string, at time.Time) *core.TriggerOffer {
	return &core.TriggerOffer{
		ID: uuid.NewString(), TriggerID: trig.ID, OrgID: "acme", PreviousOwnerUserID: trig.UserID,
		OfferedByUserID: "ada", OffereeUserID: offeree, Status: core.TriggerOfferPending,
		CreatedAt: at, ExpiresAt: at.Add(7 * 24 * time.Hour),
	}
}

// Concurrent offers of one trigger serialize on its row lock: every one
// completes (none fails the one-pending index), and exactly one stays
// pending, every other superseded.
func TestCreateTriggerOffer_ConcurrentOffersLeaveExactlyOnePending(t *testing.T) {
	repo := NewTestRepo(t)
	trig := takeoverTrigger(t, repo, "cora-"+uuid.NewString()[:8])
	at := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)

	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = repo.CreateTriggerOffer(context.Background(), offerFor(trig, "member-"+uuid.NewString()[:6], at))
		}()
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		require.NoError(t, err, "offer %d", i)
	}

	pending, err := repo.ListPendingTriggerOffers(context.Background(), core.TriggerOfferFilters{TriggerIDs: []string{trig.ID}})
	require.NoError(t, err)
	assert.Len(t, pending, 1, "exactly one offer stays pending")
	var superseded int
	require.NoError(t, repo.ORM().Bun().NewRaw(
		`SELECT count(*) FROM trigger_offers WHERE trigger_id = ? AND status = 'revoked' AND status_reason = ?`,
		trig.ID, core.TriggerOfferRevokedSuperseded).Scan(context.Background(), &superseded))
	assert.Equal(t, n-1, superseded, "every other one was superseded")
}

// An offer is made for the owner it was decided on: once the trigger changed
// hands it is refused and nothing is stored.
func TestCreateTriggerOffer_RefusedOnceTheOwnerChanged(t *testing.T) {
	repo := NewTestRepo(t)
	trig := takeoverTrigger(t, repo, "cora-"+uuid.NewString()[:8])
	o := offerFor(trig, "bob", time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC))
	o.PreviousOwnerUserID = "someone-else"
	assert.ErrorIs(t, repo.CreateTriggerOffer(context.Background(), o), core.ErrTriggerOwnerChanged)
	pending, err := repo.ListPendingTriggerOffers(context.Background(), core.TriggerOfferFilters{TriggerIDs: []string{trig.ID}})
	require.NoError(t, err)
	assert.Empty(t, pending)
}
