// Copyright (c) 2025 Reliant Labs
package seatmatch_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/seatmatch"
)

// interleaved is the real store with a hook that runs between MatchAccount's read and its clear:
// the deterministic stand-in for another request landing in that window.
type interleaved struct {
	*db.Repo
	beforeClear func()
}

func (s interleaved) ClearWorkspaceSeat(ctx context.Context, orgID, accountID string) (bool, error) {
	if s.beforeClear != nil {
		s.beforeClear()
	}
	return s.Repo.ClearWorkspaceSeat(ctx, orgID, accountID)
}

// MatchAccount reads a seat no binding covers and decides to clear it; before the clear runs,
// the admin binds the workspace again and its rematch attributes the seat. The clear must leave
// that attribution alone (review of #2698, B1).
func TestMatchAccount_DelayedClearDoesNotUndoARebind(t *testing.T) {
	repo, raw, cleanup := db.SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	acct, _, err := repo.UpsertProviderAccount(ctx, core.AccountUpsert{
		UserID: "dana", Provider: "claude", AuthKind: core.AuthKindOAuth,
		IdentityKey: core.ClaudeIdentityKey("dana", "ws-a"), ExternalAccountID: "dana", OrganizationID: "ws-a",
		Secrets: core.AccountSecrets{Secret: "t", Refresh: "r"},
	})
	require.NoError(t, err)
	// The seat as an unbind left it mid-way: attributed, its binding already gone.
	_, err = raw.ExecContext(ctx, `UPDATE provider_accounts SET attributed_org_id = 'org-1', attribution_source = 'workspace' WHERE id = $1`, acct.ID)
	require.NoError(t, err)

	ws := core.OrgProviderWorkspace{OrgID: "org-1", Provider: "claude", ExternalOrgID: "ws-a", DisplayName: "A", BoundByUserID: "ada"}
	store := interleaved{Repo: repo, beforeClear: func() {
		_, _, err := repo.BindOrgProviderWorkspace(ctx, ws)
		require.NoError(t, err)
	}}
	svc, err := seatmatch.New(seatmatch.Deps{
		Store:   store,
		Members: &seatmatch.MockMembers{IsMemberFunc: func(context.Context, string, string) (bool, bool, error) { return true, true, nil }},
		Rollout: &seatmatch.MockRollout{GetProviderWorkspaceSeatsEnabledFunc: func() bool { return true }},
	})
	require.NoError(t, err)

	out, err := svc.MatchAccount(ctx, "dana", acct.ID)
	require.NoError(t, err)
	assert.Equal(t, seatmatch.Unchanged, out, "the rebind covers the seat by the time the clear decides")
	var org *string
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT attributed_org_id FROM provider_accounts WHERE id = $1`, acct.ID).Scan(&org))
	require.NotNil(t, org)
	assert.Equal(t, "org-1", *org)
}
