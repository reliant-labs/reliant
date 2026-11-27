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

// Shared identity reads must include the owner in both cache keys, including
// a denied read of another owner's connection with the same connection id.
func TestABatchKeepsOwnerIdentitiesSeparate(t *testing.T) {
	for _, order := range [][]string{{"alice", "bob"}, {"bob", "alice"}} {
		repo := &countingOwners{fakeRepo: newFakeRepo()}
		repo.subjects["alice"] = fakeSubject{id: "alice-linked", refreshedAt: time.Now()}
		repo.subjects["bob"] = fakeSubject{id: "bob-linked", refreshedAt: time.Now()}
		repo.connections["shared-id"] = &core.Connection{
			ID: "shared-id", UserID: "alice", IntegrationID: "github", SenderID: strPtr("alice-connection"),
		}
		intake := NewIntake(repo, &recordingStarter{}, "").inBatch(NewBatch())
		for range 2 {
			for _, owner := range order {
				trigger := &core.Trigger{UserID: owner}
				assert.Equal(t, owner+"-linked", OwnerOf(context.Background(), intake.repo, trigger).GitHubID)
				trigger.ConnectionID = strPtr("shared-id")
				want := ""
				if owner == "alice" {
					want = "alice-connection"
				}
				assert.Equal(t, want, OwnerOf(context.Background(), intake.repo, trigger).GitHubID)
			}
		}
		assert.Equal(t, 2, repo.subjectReads, "one linked identity read per owner")
		assert.Equal(t, 3, repo.connReads, "Alice's success is cached; Bob's denied reads are retried, never borrowing Alice's answer")
	}
}

// The ledger and lineage can be shared, but the resulting identity cannot:
// foreign triggers may learn only that Reliant caused the event and its depth.
func TestABatchRedactsOriginAcrossOwnersInEitherOrder(t *testing.T) {
	for _, order := range [][]string{{"user-1", "user-2"}, {"user-2", "user-1"}} {
		w := commentWrite("w1", "user-1", "chat-9", "pr-review", "4242", "x")
		ledger := &countingLedger{fakeLedger: fakeLedger{writes: []*core.IntegrationWrite{w}}}
		runs := &countingRuns{fakeRuns: fakeRuns{byChat: map[string]*core.TriggerEvent{
			"chat-9": launchEventWithLineage(lineage.Lineage{{TriggerID: "private-trigger", SourceRunID: "private-run"}}),
		}}}
		e := newEnricher(ledger, runs, "")
		batch := NewBatch()
		for range 2 {
			for _, owner := range order {
				trigger := ownTrigger("pr-review")
				trigger.UserID = owner
				got, err := e.enrichIn(context.Background(), batch, trigger, commentEvent("4242", "x"))
				require.NoError(t, err)
				require.NotNil(t, got.Origin)
				assert.True(t, got.Origin.CausedByReliant)
				assert.Equal(t, core.OriginExact, got.Origin.Confidence)
				assert.Equal(t, 1, got.Origin.LineageDepth())
				if owner == "user-1" {
					assert.False(t, got.Origin.Foreign)
					assert.Equal(t, w.ChatID, got.Origin.RunID)
					assert.Equal(t, w.WorkflowName, got.Origin.Workflow)
					assert.Equal(t, w.TriggerName, got.Origin.TriggerName)
					assert.Equal(t, w.ID, got.Origin.WriteID)
					assert.True(t, got.Origin.SameWorkflow)
					assert.Len(t, got.Origin.Lineage, 1)
				} else {
					assert.True(t, got.Origin.Foreign)
					assert.Empty(t, got.Origin.RunID)
					assert.Empty(t, got.Origin.Workflow)
					assert.Empty(t, got.Origin.TriggerName)
					assert.Empty(t, got.Origin.WriteID)
					assert.False(t, got.Origin.SameWorkflow)
					assert.Empty(t, got.Origin.Lineage)
				}
			}
		}
		assert.Equal(t, 1, ledger.reads)
		assert.Equal(t, 1, runs.reads)
	}
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
