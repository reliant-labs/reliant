// Copyright (c) 2025 Reliant Labs
package db

import (
	"fmt"
	"testing"

	"github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/require"
)

// TestAccessTokensScopesCheckMatchesForge proves, against a database, that
// every scope in forge's closed set satisfies the applied access_tokens CHECK
// and that one outside it does not. The hermetic pin is `forge lint
// --closed-sets`: the CHECK carries `forge:closed-set accesstoken.AllScopes`
// (db/migrations/20261021172252_declare_access_token_closed_sets.up.sql), so a
// forge bump that adds a scope fails that lane on the bump's own PR. The CHECK
// had lagged before (no daemon:resume, cluster:manage, domain:*), so a mint of
// a newer scope failed the constraint.
func TestAccessTokensScopesCheckMatchesForge(t *testing.T) {
	testkit.Slow(t, "a DB-backed test with many round trips to pgtest's Postgres")
	t.Parallel()

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
