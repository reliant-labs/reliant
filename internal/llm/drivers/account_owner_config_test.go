// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// The driver config carries the account's owning org, so writes about the
// account (limits, bench, needs-sign-in) reach its owner rather than the
// member it served (control-plane docs/design/org-provider-accounts.md §3.1).
func TestAccountDriverConfigCarriesTheOwningOrg(t *testing.T) {
	t.Parallel()
	key := func(a core.ProviderAccount) *core.AccountWithSecrets {
		a.Provider, a.AuthKind, a.ID = "anthropic", core.AuthKindAPIKey, "acct"
		return &core.AccountWithSecrets{ProviderAccount: a, Secrets: core.AccountSecrets{Secret: "sk-ant-xyz"}}
	}

	org, ok := accountDriverConfig(context.Background(), "u1", key(core.ProviderAccount{UserID: "org:org-1", OwnerKind: core.AccountOwnerOrg, OrgID: "org-1"}))
	require.True(t, ok)
	assert.Equal(t, "org-1", org.AccountOrgID)
	assert.Equal(t, "u1", org.UserID, "the run still acts for the member")

	mine, ok := accountDriverConfig(context.Background(), "u1", key(core.ProviderAccount{UserID: "u1", OwnerKind: core.AccountOwnerUser}))
	require.True(t, ok)
	assert.Empty(t, mine.AccountOrgID)

	legacy, ok := accountDriverConfig(context.Background(), "u1", key(core.ProviderAccount{UserID: "u1"}))
	require.True(t, ok)
	assert.Empty(t, legacy.AccountOrgID, "a row predating owner_kind is its user's")
}
