// Copyright (c) 2025 Reliant Labs
package db

import (
	"fmt"
	"testing"

	"github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/stretchr/testify/require"
)

// TestAccessTokensScopesCheckMatchesForge pins reliant's access_tokens CHECK to
// forge's closed scope set. The CHECK had lagged (no daemon:resume, cluster:manage,
// domain:*), so a mint of a newer scope failed the constraint.
func TestAccessTokensScopesCheckMatchesForge(t *testing.T) {
	_, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()

	for _, scope := range accesstoken.AllScopes {
		t.Run(string(scope), func(t *testing.T) {
			_, err := raw.Exec(
				`INSERT INTO access_tokens (id, org_id, name, token_hash, token_prefix, scopes)
				 VALUES ($1, 'org', 'n', $1, 'rlat_x', ARRAY[$2]::text[])`,
				fmt.Sprintf("tok-%s", scope), string(scope))
			require.NoError(t, err, "forge scope %q must satisfy the access_tokens CHECK", scope)
		})
	}

	_, err := raw.Exec(
		`INSERT INTO access_tokens (id, org_id, name, token_hash, token_prefix, scopes)
		 VALUES ('tok-bogus', 'org', 'n', 'tok-bogus', 'rlat_x', ARRAY['not:a-scope']::text[])`)
	require.Error(t, err, "a scope outside forge's set must still be rejected")
}
