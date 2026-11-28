// Copyright (c) 2026 Reliant Labs. All rights reserved.
package reconciliation

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
)

// blockingReapRepo holds the pass's first query open until its context ends,
// like a query in flight when the process is told to shut down.
type blockingReapRepo struct {
	*mockRepo
	entered chan struct{}
	ended   chan error
}

func (b *blockingReapRepo) ReapOrphanedWorkflowDescendants(ctx context.Context) ([]db.ReapedWorkflow, error) {
	select {
	case b.entered <- struct{}{}:
	default:
	}
	<-ctx.Done()
	select {
	case b.ended <- ctx.Err():
	default:
	}
	return nil, ctx.Err()
}

// Stop cancels the pass in flight and returns once it has, rather than
// leaving it to run on to its own timeout after Stop gave up waiting: the
// process closes its database right after, so a pass that outlived Stop failed
// its queries with "sql: database is closed" (prod 758bde53).
func TestStopCancelsThePassInFlight(t *testing.T) {
	repo := &blockingReapRepo{mockRepo: newMockRepo(), entered: make(chan struct{}, 1), ended: make(chan error, 1)}
	r := New(Deps{Store: repo, RunEvents: repo, Chats: repo, ToolCalls: repo, Workflows: repo, Daemons: repo, Questions: repo, Temporal: &mockReconcilerTemporalClient{}, Config: &ReconcilerConfig{PollInterval: 5 * time.Millisecond}, Mailbox: repo, Orphans: repo})

	r.StartBackgroundReconciliation(context.Background())
	select {
	case <-repo.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("no pass started")
	}

	stopped := make(chan struct{})
	go func() {
		r.Stop()
		close(stopped)
	}()
	select {
	case err := <-repo.ended:
		require.ErrorIs(t, err, context.Canceled, "the pass ends because Stop cancelled it, not on its own timeout")
	case <-time.After(10 * time.Second):
		t.Fatal("Stop did not cancel the pass in flight")
	}
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("Stop did not return once the pass ended")
	}
}
