// Copyright (c) 2025 Reliant Labs

package app

import (
	"context"

	"github.com/reliant-labs/reliant/internal/credentials/orgaccounts"
	"github.com/reliant-labs/reliant/internal/credentials/provideraccounts"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// seatmatchStore is what the seat matcher reads and writes: a person's accounts
// (credentials/provideraccounts) and the org's bindings and seats (credentials/orgaccounts).
type seatmatchStore struct {
	accounts provideraccounts.Service
	org      orgaccounts.Service
}

func (s seatmatchStore) ListProviderAccounts(ctx context.Context, userID string) ([]core.ProviderAccount, error) {
	return s.accounts.ListProviderAccounts(ctx, userID)
}

func (s seatmatchStore) ProviderWorkspaceBinding(ctx context.Context, provider, externalOrgID string) (*core.OrgProviderWorkspace, error) {
	return s.org.ProviderWorkspaceBinding(ctx, provider, externalOrgID)
}

func (s seatmatchStore) ListWorkspaceSignIns(ctx context.Context, provider, externalOrgID string) ([]core.ProviderAccount, error) {
	return s.org.ListWorkspaceSignIns(ctx, provider, externalOrgID)
}

func (s seatmatchStore) AttributeWorkspaceSeat(ctx context.Context, w core.OrgProviderWorkspace, accountID string) (bool, error) {
	return s.org.AttributeWorkspaceSeat(ctx, w, accountID)
}

func (s seatmatchStore) ClearWorkspaceSeat(ctx context.Context, orgID, accountID string) (bool, error) {
	return s.org.ClearWorkspaceSeat(ctx, orgID, accountID)
}
