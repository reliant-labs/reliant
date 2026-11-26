// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/triggers/lineage"
)

// countingOwners is the fake repository, counting the owner reads every
// filter scope makes (OwnerOf).
type countingOwners struct {
	*fakeRepo
	mu           sync.Mutex
	connReads    int
	subjectReads int
}

func (r *countingOwners) GetConnection(ctx context.Context, userID, id string) (*core.Connection, error) {
	r.mu.Lock()
	r.connReads++
	r.mu.Unlock()
	return r.fakeRepo.GetConnection(ctx, userID, id)
}

func (r *countingOwners) GetIntegrationAccessSubject(ctx context.Context, userID, integrationID string, freshAfter time.Time) (string, error) {
	r.mu.Lock()
	r.subjectReads++
	r.mu.Unlock()
	return r.fakeRepo.GetIntegrationAccessSubject(ctx, userID, integrationID, freshAfter)
}

// countingLedger counts the write-ledger reads, failing the first failFirst.
type countingLedger struct {
	fakeLedger
	reads     int
	failFirst int
}

func (l *countingLedger) GetIntegrationWriteByObject(ctx context.Context, integration, scope, kind, id string) (*core.IntegrationWrite, error) {
	l.reads++
	if l.failFirst > 0 {
		l.failFirst--
		return nil, errors.New("connection reset by peer")
	}
	return l.fakeLedger.GetIntegrationWriteByObject(ctx, integration, scope, kind, id)
}

func (l *countingLedger) ListRecentIntegrationWrites(ctx context.Context, integration, scope string, since time.Time, limit int) ([]*core.IntegrationWrite, error) {
	l.reads++
	return l.fakeLedger.ListRecentIntegrationWrites(ctx, integration, scope, since, limit)
}

// countingRuns counts lineage reads.
type countingRuns struct {
	fakeRuns
	reads int
}

func (r *countingRuns) GetTriggerEventByChatID(ctx context.Context, chatID string) (*core.TriggerEvent, error) {
	r.reads++
	return r.fakeRuns.GetTriggerEventByChatID(ctx, chatID)
}

// One delivery reaching several triggers of one owner reads the owner's
// identity once, however many triggers skip it and however many times each
// builds a filter scope. Every trigger still records its own skip.
func TestABatchReadsTheOwnerOnceForAllItsTriggers(t *testing.T) {
	repo := &countingOwners{fakeRepo: newFakeRepo()}
	repo.subjects["user-1"] = fakeSubject{id: "42", refreshedAt: time.Now()}
	intake := NewIntake(repo, &recordingStarter{}, "")
	batch := NewBatch()

	var skipped int
	for i := range 4 {
		trigger := webhookTrigger(t, "trigger.activation.owner.github_id == '7'")
		repo.triggers[trigger.ID] = trigger
		res, err := intake.Accept(context.Background(), trigger,
			inbound(trigger, "d1", map[string]any{"n": i}), AcceptOptions{Batch: batch})
		require.NoError(t, err)
		if res.Outcome == core.TriggerEventSkipped {
			skipped++
		}
		assert.Len(t, repo.eventsFor(trigger.ID), 1, "each trigger records its own skip")
	}
	assert.Equal(t, 4, skipped)
	assert.Equal(t, 1, repo.subjectReads, "the owner's GitHub id is read once per arrival")
}

// Without a batch every Accept reads for itself, as before.
func TestWithoutABatchEachAcceptReadsTheOwner(t *testing.T) {
	repo := &countingOwners{fakeRepo: newFakeRepo()}
	intake := NewIntake(repo, &recordingStarter{}, "")
	for range 2 {
		trigger := webhookTrigger(t, "trigger.activation.owner.github_id == '7'")
		repo.triggers[trigger.ID] = trigger
		_, err := intake.Accept(context.Background(), trigger, inbound(trigger, "d1", map[string]any{}), AcceptOptions{})
		require.NoError(t, err)
	}
	assert.GreaterOrEqual(t, repo.subjectReads, 2)
}

// The ledger match and the writing run's lineage depend on the event, not the
// trigger: through a batch they are read once. What depends on the trigger
// (same owner, same workflow) is still judged per trigger.
func TestABatchMatchesAnEchoOnceButAttributesItPerTrigger(t *testing.T) {
	w := commentWrite("w1", "user-1", "chat-9", "pr-review", "4242", "x")
	ledger := &countingLedger{fakeLedger: fakeLedger{writes: []*core.IntegrationWrite{w}}}
	runs := &countingRuns{fakeRuns: fakeRuns{byChat: map[string]*core.TriggerEvent{
		"chat-9": launchEventWithLineage(lineage.Lineage{{TriggerID: "t-a", SourceRunID: "r1"}}),
	}}}
	e := newEnricher(ledger, runs, "")
	batch := NewBatch()

	same, err := e.enrichIn(context.Background(), batch, ownTrigger("pr-review"), commentEvent("4242", "x"))
	require.NoError(t, err)
	other, err := e.enrichIn(context.Background(), batch, ownTrigger("review-responder"), commentEvent("4242", "x"))
	require.NoError(t, err)

	assert.Equal(t, 1, ledger.reads, "one exact match, read once")
	assert.Equal(t, 1, runs.reads, "the writing run's lineage, read once")
	assert.Equal(t, core.OriginExact, same.Origin.Confidence)
	assert.Equal(t, core.OriginExact, other.Origin.Confidence)
	assert.True(t, same.Origin.SameWorkflow)
	assert.False(t, other.Origin.SameWorkflow, "same-workflow is the trigger's own question")
	assert.Equal(t, 1, other.Origin.LineageDepth())
}

// A ledger read that failed is the asking trigger's alone: the next trigger
// of the batch reads again and gets the real answer.
func TestABatchDoesNotShareAFailedLedgerRead(t *testing.T) {
	w := commentWrite("w1", "user-1", "chat-9", "pr-review", "4242", "x")
	w.CreatedAt = originNow.Add(-time.Hour) // outside the intent window: only the exact read finds it
	ledger := &countingLedger{fakeLedger: fakeLedger{writes: []*core.IntegrationWrite{w}}, failFirst: 1}
	e := newEnricher(ledger, fakeRuns{}, "")
	batch := NewBatch()

	first, err := e.enrichIn(context.Background(), batch, ownTrigger("pr-review"), commentEvent("4242", "x"))
	require.NoError(t, err)
	second, err := e.enrichIn(context.Background(), batch, ownTrigger("pr-review"), commentEvent("4242", "x"))
	require.NoError(t, err)

	assert.False(t, first.Origin.CausedByReliant, "the outage degrades the first trigger's answer, as before")
	assert.True(t, second.Origin.CausedByReliant, "and is not passed on to the next")
}
