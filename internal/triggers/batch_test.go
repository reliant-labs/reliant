// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// countingRepo is the real repository, counting its catalog pins and pinned
// snapshot reads. failPins fails that many pins first, as a transient error.
type countingRepo struct {
	*db.Repo

	mu          sync.Mutex
	pins        int
	pinnedReads int
	failPins    int
}

func (r *countingRepo) PinProjectCatalog(ctx context.Context, scope db.CatalogPinScope) (string, error) {
	r.mu.Lock()
	r.pins++
	fail := r.failPins > 0
	if fail {
		r.failPins--
	}
	r.mu.Unlock()
	if fail {
		return "", errors.New("connection reset by peer")
	}
	return r.Repo.PinProjectCatalog(ctx, scope)
}

func (r *countingRepo) GetPinnedProjectCatalog(ctx context.Context, pin string) (*db.PinnedCatalog, bool, error) {
	r.mu.Lock()
	r.pinnedReads++
	r.mu.Unlock()
	return r.Repo.GetPinnedProjectCatalog(ctx, pin)
}

func (r *countingRepo) counts() (pins, pinnedReads int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pins, r.pinnedReads
}

// webhookTriggers is n webhook triggers like p.webhook: one owner, project
// and machine, so one catalog scope.
func (p *provenanceProject) webhookTriggers(t *testing.T, n int) []*core.Trigger {
	t.Helper()
	out := make([]*core.Trigger, n)
	for i := range out {
		trig := *p.webhook
		trig.ID = uuid.NewString()
		trig.Name = fmt.Sprintf("%s %d", trig.Name, i)
		require.NoError(t, p.repo.CreateTrigger(p.ctx, &trig))
		out[i] = &trig
	}
	return out
}

func webhookEvent(trig *core.Trigger, delivery string) InboundEvent {
	return InboundEvent{
		Kind: core.TriggerEventKindWebhook, DedupeKey: trig.ID + ":" + delivery,
		Payload: map[string]any{"ref": "refs/heads/main"},
		Sender:  &core.TriggerSender{Kind: core.TriggerSenderKindWebhook, ID: "token", Verified: true},
	}
}

// One arrival reaching many triggers of one catalog scope (a pull_request
// delivery reaching every activation of the PR pipeline) pins the catalog
// once and reads the snapshot once, and every trigger still records the full
// definition it resolved, on that one pin. Without a batch each Accept pins
// and reads on its own.
func TestABatchPinsAndReadsTheCatalogOncePerScope(t *testing.T) {
	p := newProvenanceProject(t)
	repo := &countingRepo{Repo: p.repo}
	intake := NewIntake(NewStores(p.repo), &recordingStarter{}, "").WithWorkflows(LaunchWorkflows{Repo: repo})
	trigs := p.webhookTriggers(t, 5)

	batch := NewBatch()
	for _, trig := range trigs {
		_, err := intake.Accept(p.ctx, trig, webhookEvent(trig, "d1"), AcceptOptions{Batch: batch})
		require.NoError(t, err)
	}
	pins, reads := repo.counts()
	assert.Equal(t, 1, pins, "the triggers of one scope pin once per arrival")
	assert.Equal(t, 1, reads, "and read the pinned snapshot once")

	var pin string
	for _, trig := range trigs {
		ev, err := p.repo.GetTriggerEventByDedupe(p.ctx, core.TriggerEventKindWebhook, trig.ID+":d1")
		require.NoError(t, err)
		p.assertProvenance(t, ev.Definition, provenanceWorkflow)
		require.NotEmpty(t, ev.Definition.CatalogPin)
		if pin == "" {
			pin = ev.Definition.CatalogPin
		}
		assert.Equal(t, pin, ev.Definition.CatalogPin, "every trigger records the one pin")
	}

	for _, trig := range trigs {
		_, err := intake.Accept(p.ctx, trig, webhookEvent(trig, "d2"), AcceptOptions{})
		require.NoError(t, err)
	}
	unbatched, unbatchedReads := repo.counts()
	assert.Equal(t, 1+len(trigs), unbatched, "without a batch every Accept pins")
	assert.Greater(t, unbatchedReads-reads, len(trigs), "and reads the snapshot more than once")
}

// A read that fails is not shared: the next trigger of the arrival reads
// again, so one trigger's transient failure does not lose every trigger's
// event (GitHub never redelivers on its own).
func TestABatchDoesNotShareAFailedRead(t *testing.T) {
	p := newProvenanceProject(t)
	repo := &countingRepo{Repo: p.repo, failPins: 1}
	intake := NewIntake(NewStores(p.repo), &recordingStarter{}, "").WithWorkflows(LaunchWorkflows{Repo: repo})
	trigs := p.webhookTriggers(t, 3)

	batch := NewBatch()
	_, err := intake.Accept(p.ctx, trigs[0], webhookEvent(trigs[0], "d1"), AcceptOptions{Batch: batch})
	require.ErrorContains(t, err, "connection reset by peer")
	for _, trig := range trigs[1:] {
		_, err := intake.Accept(p.ctx, trig, webhookEvent(trig, "d1"), AcceptOptions{Batch: batch})
		require.NoError(t, err, "the failure was the first trigger's alone")
		ev, err := p.repo.GetTriggerEventByDedupe(p.ctx, core.TriggerEventKindWebhook, trig.ID+":d1")
		require.NoError(t, err)
		p.assertProvenance(t, ev.Definition, provenanceWorkflow)
	}
	pins, _ := repo.counts()
	assert.Equal(t, 2, pins, "pinned again after the failure, then shared")
}

// A resolver the batch cannot front (not a LaunchWorkflows, or over a store
// that cannot pin) is used as it is.
func TestABatchLeavesAResolverItCannotFrontAlone(t *testing.T) {
	batch := NewBatch()
	plain := fakeWorkflows{}
	assert.Equal(t, WorkflowResolver(plain), batch.workflows(plain))
	var none *Batch
	lw := LaunchWorkflows{Repo: &countingRepo{}}
	assert.Equal(t, WorkflowResolver(lw), none.workflows(lw), "a nil batch memoizes nothing")
	fronted, ok := batch.workflows(lw).(LaunchWorkflows)
	require.True(t, ok, "a fronted resolver is still a LaunchWorkflows, which PinnedTo binds")
	_, ok = fronted.Repo.(batchStore)
	assert.True(t, ok)
}
