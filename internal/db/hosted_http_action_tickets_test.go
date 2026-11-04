package db

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/stretchr/testify/require"
)

func ticketFixture(t *testing.T) (*Repo, core.HostedHTTPActionTicket) {
	t.Helper()
	repo, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.Background()
	chatID := seedChatForRun(t, repo, ctx, "test-user")
	runID := uuid.NewString()
	owner := "test-user"
	require.NoError(t, repo.CreateWorkflow(ctx, &Workflow{ID: runID, ChatID: chatID, WorkflowName: "trader-bot", Thread: runID, Status: Active(), CreatedAt: time.Now(), OwnerUserID: &owner}))
	return repo, core.HostedHTTPActionTicket{ID: uuid.NewString(), WorkflowID: runID, TemporalRunID: uuid.NewString(), ActivityID: "scheduled-action-42", NodeID: "fixed-http-node", GrantID: "approved-grant", RequestSHA256: strings.Repeat("a", 64), ExpiresAt: time.Now().UTC().Add(45 * time.Second)}
}

func TestHostedHTTPActionTicketDurableSingleUse(t *testing.T) {
	repo, ticket := ticketFixture(t)
	ctx := context.Background()
	ok, err := repo.CreateHostedHTTPActionTicket(ctx, ticket)
	require.NoError(t, err)
	require.True(t, ok)
	// Foreign grant/digest denials cannot burn the legitimate claim.
	got, err := repo.RedeemHostedHTTPActionTicket(ctx, ticket.ID, "foreign", ticket.RequestSHA256)
	require.NoError(t, err)
	require.Nil(t, got)
	got, err = repo.RedeemHostedHTTPActionTicket(ctx, ticket.ID, ticket.GrantID, strings.Repeat("b", 64))
	require.NoError(t, err)
	require.Nil(t, got)
	// A different Repo (API replica) sees the same durable ticket. Every racer
	// hits the actual conditional UPDATE; exactly one receives authority.
	replica, err := NewRepoWithDriver(repo.ORM().DB(), DriverPostgres)
	require.NoError(t, err)
	var wg sync.WaitGroup
	var success atomic.Int32
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := replica.RedeemHostedHTTPActionTicket(ctx, ticket.ID, ticket.GrantID, ticket.RequestSHA256)
			require.NoError(t, err)
			if got != nil {
				success.Add(1)
				require.Equal(t, "test-user", got.OwnerUserID)
				require.Equal(t, "test-project", got.ProjectID)
				require.Equal(t, "trader-bot", got.WorkflowName)
				require.Equal(t, ticket.NodeID, got.NodeID)
				require.NotNil(t, got.RedeemedAt)
			}
		}()
	}
	wg.Wait()
	require.EqualValues(t, 1, success.Load())
	// A Temporal retry must not mint again even after consumption.
	ticket.ID = uuid.NewString()
	ok, err = repo.CreateHostedHTTPActionTicket(ctx, ticket)
	require.NoError(t, err)
	require.False(t, ok)
}

func TestHostedHTTPActionTicketCurrentAuthority(t *testing.T) {
	for _, kind := range []string{"inactive-at-issue", "stopped-before-redeem", "owner-changed", "project-owner-changed", "name-changed", "expired", "expired-issue", "too-long", "legacy-owner"} {
		t.Run(kind, func(t *testing.T) {
			repo, ticket := ticketFixture(t)
			ctx := context.Background()
			switch kind {
			case "inactive-at-issue":
				_, err := repo.ORM().Bun().NewRaw("UPDATE workflows SET state=3 WHERE id=?", ticket.WorkflowID).Exec(ctx)
				require.NoError(t, err)
			case "expired-issue":
				ticket.ExpiresAt = time.Now().Add(-time.Second)
			case "too-long":
				ticket.ExpiresAt = time.Now().Add(2 * time.Minute)
			case "legacy-owner":
				_, err := repo.ORM().Bun().NewRaw("UPDATE workflows SET owner_user_id=NULL WHERE id=?", ticket.WorkflowID).Exec(ctx)
				require.NoError(t, err)
			}
			ok, err := repo.CreateHostedHTTPActionTicket(ctx, ticket)
			require.NoError(t, err)
			if kind == "inactive-at-issue" || kind == "expired-issue" || kind == "too-long" || kind == "legacy-owner" {
				require.False(t, ok)
				return
			}
			require.True(t, ok)
			switch kind {
			case "stopped-before-redeem":
				_, err = repo.ORM().Bun().NewRaw("UPDATE workflows SET state=3 WHERE id=?", ticket.WorkflowID).Exec(ctx)
			case "owner-changed":
				_, err = repo.ORM().Bun().NewRaw("UPDATE workflows SET owner_user_id='foreign' WHERE id=?", ticket.WorkflowID).Exec(ctx)
			case "project-owner-changed":
				_, err = repo.ORM().Bun().NewRaw("UPDATE projects SET user_id='foreign' WHERE id='test-project'").Exec(ctx)
			case "name-changed":
				_, err = repo.ORM().Bun().NewRaw("UPDATE workflows SET workflow_name='other' WHERE id=?", ticket.WorkflowID).Exec(ctx)
			case "expired":
				_, err = repo.ORM().Bun().NewRaw("UPDATE hosted_http_action_tickets SET created_at=CURRENT_TIMESTAMP-INTERVAL '1 minute',expires_at=CURRENT_TIMESTAMP-INTERVAL '1 second' WHERE id=?", ticket.ID).Exec(ctx)
			}
			require.NoError(t, err)
			got, err := repo.RedeemHostedHTTPActionTicket(ctx, ticket.ID, ticket.GrantID, ticket.RequestSHA256)
			require.NoError(t, err)
			require.Nil(t, got)
		})
	}
}
