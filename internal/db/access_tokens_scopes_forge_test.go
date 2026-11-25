// Copyright (c) 2025 Reliant Labs
package db

import (
	"fmt"
	"testing"

	"github.com/lib/pq"
	"github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/require"
)

// TestAccessTokensScopesCheckMatchesForge proves, against a database, that the
// applied access_tokens CHECK is a FORMAT check
// (db/migrations/20261125031938_access_tokens_scopes_format.up.sql): every
// scope in forge's catalog and every `bundle:<name>` ref satisfies it, so a
// forge bump that adds a scope needs no widening migration, and malformed
// entries are still refused. The vocabulary itself is the write path's
// (accesstoken.NewSet refuses an unknown scope at mint) and the read path is
// tolerant (accesstoken.ResolveStored), so the CHECK no longer enumerates the
// catalog. Before it did, and lagged it more than once (no daemon:resume,
// cluster:manage, domain:*), so a mint of a newer scope failed the constraint.
func TestAccessTokensScopesCheckMatchesForge(t *testing.T) {
	testkit.Slow(t, "a DB-backed test with many round trips to pgtest's Postgres")
	t.Parallel()

	_, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()

	// The format CHECK is added NOT VALID and validated by the next
	// migration, so after the chain it must be validated: it holds every
	// existing row, not only new writes.
	var validated bool
	require.NoError(t, raw.QueryRow(
		`SELECT convalidated FROM pg_constraint
		  WHERE conname = 'access_tokens_scopes' AND conrelid = 'access_tokens'::regclass`).Scan(&validated))
	require.True(t, validated, "access_tokens_scopes is NOT VALID after the migration chain: its VALIDATE migration did not run")

	n := 0
	insert := func(scopes []*string) error {
		n++
		_, err := raw.Exec(
			`INSERT INTO access_tokens (id, org_id, name, token_hash, token_prefix, scopes)
			 VALUES ($1, 'org', 'n', $1, 'rlat_x', $2)`,
			fmt.Sprintf("tok-%d", n), pq.Array(scopes))
		return err
	}
	of := func(ss ...string) []*string {
		out := make([]*string, len(ss))
		for i := range ss {
			out[i] = &ss[i]
		}
		return out
	}

	var admitted []string
	for _, scope := range accesstoken.AllScopes {
		admitted = append(admitted, string(scope))
	}
	for _, b := range accesstoken.AllBundles {
		admitted = append(admitted, accesstoken.BundleRef(b))
	}
	// A format-valid entry this binary does not know: a newer release's scope
	// or bundle, which a rollback binary must still be able to store-read.
	admitted = append(admitted, "future:scope", "bundle:future")
	for _, entry := range admitted {
		t.Run("admits "+entry, func(t *testing.T) {
			require.NoError(t, insert(of(entry)), "%q must satisfy the access_tokens format CHECK", entry)
		})
	}
	t.Run("admits every catalog scope and bundle together", func(t *testing.T) {
		require.NoError(t, insert(of(admitted...)))
	})

	for _, tc := range []struct {
		name   string
		scopes []*string
	}{
		{"no scopes", of()},
		{"an empty entry", of("")},
		{"an empty entry beside a valid one", of("deploy:read", "")},
		{"upper case", of("Deploy:read")},
		{"an upper-case bundle", of("bundle:Admin")},
		{"a space", of("deploy: read")},
		{"a leading space", of(" deploy:read")},
		{"a hyphen", of("not:a-scope")},
		{"a digit", of("deploy:v2")},
		{"a trailing colon", of("deploy:")},
		{"a leading colon", of(":deploy")},
		{"an empty bundle name", of("bundle:")},
		{"a comma inside one entry", of("deploy:read,deploy:write")},
		{"a NULL entry", []*string{nil}},
		{"a NULL beside a valid entry", append(of("deploy:read"), nil)},
		{"a comma entry beside a NULL", append(of("deploy:read,deploy:write"), nil)},
	} {
		t.Run("refuses "+tc.name, func(t *testing.T) {
			require.ErrorContains(t, insert(tc.scopes), `violates check constraint "access_tokens_scopes"`,
				"%s must be refused by the access_tokens format CHECK", tc.name)
		})
	}
}
