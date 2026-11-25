package tokenauthority

import (
	"context"
	"errors"
	"testing"

	"github.com/lib/pq"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"

	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/db"
)

// A self-hosted server reads its own access_tokens rows tolerantly: a row a
// newer release wrote (read after a rollback) may name a scope or a bundle
// this binary does not know. The unknown entries are skipped, a row of
// nothing but unknown entries is refused, and a stored bundle:admin never
// yields a sensitive scope.
func TestLocalStore_IntrospectReadsStoredScopesTolerantly(t *testing.T) {
	ctx := context.Background()
	repo, raw, cleanup := db.SetupTestDBWithRawDB(t)
	t.Cleanup(cleanup)
	// access_tokens_scopes pins the column to THIS binary's catalog, which
	// is exactly what a newer writer's row would not satisfy. Drop it in this
	// test's own copy of the database to stand in for that row.
	if _, err := raw.ExecContext(ctx, `ALTER TABLE access_tokens DROP CONSTRAINT access_tokens_scopes`); err != nil {
		t.Fatal(err)
	}
	s := NewLocalStore(repo.ORM())

	mintWith := func(t *testing.T, stored []string) string {
		t.Helper()
		m, err := s.MintForUser(ctx, MintRequest{UserID: "user-1", Name: t.Name(), Scopes: []fat.Scope{fat.ScopeDeployRead}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := raw.ExecContext(ctx, `UPDATE access_tokens SET scopes = $1 WHERE id = $2`, pq.Array(stored), m.TokenID); err != nil {
			t.Fatal(err)
		}
		return m.Plaintext
	}

	t.Run("one known and one unknown is usable with only the known", func(t *testing.T) {
		p, err := s.Introspect(ctx, mintWith(t, []string{"deploy:read", "future:scope"}))
		if err != nil {
			t.Fatalf("a token with one known scope was refused: %v", err)
		}
		if len(p.Scopes) != 1 || !p.Scopes.Has(fat.ScopeDeployRead) {
			t.Fatalf("scopes = %s, want only deploy:read", p.Scopes)
		}
	})

	t.Run("all unknown is refused", func(t *testing.T) {
		p, err := s.Introspect(ctx, mintWith(t, []string{"future:scope", "bundle:future"}))
		if !errors.Is(err, accesstokenclient.ErrNoRecognisedScope) {
			t.Fatalf("err = %v (principal %+v), want ErrNoRecognisedScope", err, p)
		}
	})

	t.Run("bundle:admin never yields a sensitive scope", func(t *testing.T) {
		p, err := s.Introspect(ctx, mintWith(t, []string{"bundle:admin"}))
		if err != nil {
			t.Fatalf("bundle:admin was refused: %v", err)
		}
		if !p.Scopes.Has(fat.ScopeDeployWrite) {
			t.Fatalf("bundle:admin did not expand: %s", p.Scopes)
		}
		for _, sc := range append([]fat.Scope{fat.ScopeDBQuery, fat.ScopeSecretWrite, fat.ScopeTokenWrite},
			fat.ScopesOfClass(fat.ClassSensitive)...) {
			if p.Scopes.Has(sc) {
				t.Errorf("bundle:admin yielded the sensitive scope %s", sc)
			}
		}
	})
}
