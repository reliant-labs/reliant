// Copyright (c) 2025 Reliant Labs
package forgecred

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/cloudcred"
)

// The two independently accepted cache dimensions must compose, not replace
// one another. A narrowed answer is never reusable, even within the same org.
func TestMint_OrgAndRequestedScopesAreIndependentCacheDimensions(t *testing.T) {
	clock := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "cache.json")
	ex := &fakeExchanger{ttl: time.Hour, now: func() time.Time { return clock }}
	mint := func(org string, scopes ...string) cloudcred.Token {
		t.Helper()
		h := New(Deps{Org: org, CachePath: path, Exchanger: ex,
			Now: func() time.Time { return clock },
			Sessions: func() ([]Session, error) {
				return []Session{{Server: "https://api.example.com", Token: "session"}}, nil
			},
		})
		tok, err := h.Mint(context.Background(), cloudcred.Request{Endpoint: req.Endpoint, Scopes: scopes})
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	aDeploy := mint("org-a", "deploy:write")
	aObs := mint("org-a", "obs:read", "db:query")
	bDeploy := mint("org-b", "deploy:write")
	bObs := mint("org-b", "obs:read", "db:query")
	if ex.minted != 4 {
		t.Fatalf("distinct org/scope pairs minted=%d, want 4", ex.minted)
	}
	for _, tc := range []struct {
		org    string
		scopes []string
		token  string
	}{
		{"org-a", []string{"deploy:write"}, aDeploy.Token},
		{"org-a", []string{"db:query", "obs:read", "obs:read"}, aObs.Token},
		{"org-b", []string{"deploy:write"}, bDeploy.Token},
		{"org-b", []string{"db:query", "obs:read"}, bObs.Token},
	} {
		if got := mint(tc.org, tc.scopes...); got.Token != tc.token {
			t.Fatalf("org=%s scopes=%v reused %q, want %q", tc.org, tc.scopes, got.Token, tc.token)
		}
	}
	if ex.minted != 4 {
		t.Fatalf("alternating requests evicted another pair: minted=%d", ex.minted)
	}
	ex.grant = []string{"obs:read"}
	first := mint("org-c", "obs:read", "db:query")
	ex.grant = []string{"obs:read", "db:query"}
	second := mint("org-c", "obs:read", "db:query")
	if second.Token == first.Token || ex.minted != 6 {
		t.Fatal("narrow org-c answer was cached")
	}
	if third := mint("org-c", "db:query", "obs:read"); third.Token != second.Token || ex.minted != 6 {
		t.Fatal("complete org-c answer was not reused")
	}
}
