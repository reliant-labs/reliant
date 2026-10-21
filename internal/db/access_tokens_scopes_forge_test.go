// Copyright (c) 2025 Reliant Labs
package db

import (
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/stretchr/testify/require"

	forgedb "github.com/reliant-labs/reliant/db"
)

// accessTokensScopesCheck matches the definition of the access_tokens_scopes
// CHECK — the baseline's CREATE TABLE line and every later ADD CONSTRAINT —
// up to the end of its scope ARRAY.
var accessTokensScopesCheck = regexp.MustCompile(`(?s)CONSTRAINT access_tokens_scopes CHECK.*?ARRAY\[(.*?)\]`)

var quotedLiteral = regexp.MustCompile(`'([^']+)'`)

// TestAccessTokensScopesMigrationMatchesForge is the hermetic half of the pin
// below: it reads the CHECK out of the embedded migrations, so it needs no
// database and runs under -short. That matters because a forge bump that adds
// a scope changes go.mod only; the PR tier runs -short, where the DB-backed
// test skips, so without this the drift surfaced first as a red main.
func TestAccessTokensScopesMigrationMatchesForge(t *testing.T) {
	entries, err := fs.ReadDir(forgedb.MigrationsFS, "migrations")
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".up.sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names) // version-prefixed: lexical order is apply order

	var latest, from string
	for _, name := range names {
		body, err := fs.ReadFile(forgedb.MigrationsFS, "migrations/"+name)
		require.NoError(t, err)
		if all := accessTokensScopesCheck.FindAllStringSubmatch(string(body), -1); len(all) > 0 {
			latest, from = all[len(all)-1][1], name
		}
	}
	require.NotEmpty(t, from, "no migration defines the access_tokens_scopes CHECK")

	var got []string
	for _, m := range quotedLiteral.FindAllStringSubmatch(latest, -1) {
		got = append(got, m[1])
	}
	want := make([]string, 0, len(accesstoken.AllScopes))
	for _, s := range accesstoken.AllScopes {
		want = append(want, string(s))
	}
	require.ElementsMatch(t, want, got,
		"the access_tokens_scopes CHECK last defined in db/migrations/%s must list exactly "+
			"forge's accesstoken.AllScopes; add a migration (make migration) that replaces it", from)
}

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
