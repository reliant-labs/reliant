package db

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/stretchr/testify/require"
)

func TestHostedHTTPActionTicketConcurrentIssueClaimsOnce(t *testing.T) {
	repo, ticket := ticketFixture(t)
	ctx := context.Background()
	var success atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(tkt core.HostedHTTPActionTicket) {
			defer wg.Done()
			tkt.ID = uuid.NewString()
			ok, err := repo.CreateHostedHTTPActionTicket(ctx, tkt)
			require.NoError(t, err)
			if ok {
				success.Add(1)
			}
		}(ticket)
	}
	wg.Wait()
	require.EqualValues(t, 1, success.Load())
	// Expiry must not allow reissuance by an automatic Temporal retry.
	_, err := repo.ORM().Bun().NewRaw("UPDATE hosted_http_action_tickets SET created_at=CURRENT_TIMESTAMP-INTERVAL '1 minute',expires_at=CURRENT_TIMESTAMP-INTERVAL '1 second' WHERE workflow_id=?", ticket.WorkflowID).Exec(ctx)
	require.NoError(t, err)
	ticket.ID = uuid.NewString()
	ok, err := repo.CreateHostedHTTPActionTicket(ctx, ticket)
	require.NoError(t, err)
	require.False(t, ok)
}
