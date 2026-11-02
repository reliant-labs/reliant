package cliauth

import (
	"testing"

	"github.com/reliant-labs/forge/pkg/credentials"
	"github.com/reliant-labs/forge/pkg/oauth2"
)

func TestAccount_KnownSameAndString(t *testing.T) {
	a := Account{ID: "ext-1", Email: "a@example.test"}
	for _, tc := range []struct {
		name string
		x, y Account
		same bool
	}{
		{"same id", a, Account{ID: "ext-1"}, true},
		{"different id", a, Account{ID: "ext-2", Email: "a@example.test"}, false},
		{"two unknowns are not the same", Account{}, Account{}, false},
		{"unknown is not anyone", a, Account{Email: "a@example.test"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.x.Same(tc.y); got != tc.same {
				t.Fatalf("Same = %v, want %v", got, tc.same)
			}
		})
	}
	if a.String() != "a@example.test" || (Account{ID: "ext-1"}).String() != "account ext-1" || (Account{}).String() != "" {
		t.Fatalf("String: %q %q %q", a, Account{ID: "ext-1"}, Account{})
	}
}

func TestAccountFromToken(t *testing.T) {
	if got := accountFromToken(&oauth2.Token{Raw: map[string]any{"account_id": "ext-1", "account_email": "a@example.test"}}); got != (Account{ID: "ext-1", Email: "a@example.test"}) {
		t.Fatalf("named: %+v", got)
	}
	// An older control plane names no one; a malformed field is not a name.
	for _, raw := range []map[string]any{nil, {"org_id": "org-1"}, {"account_id": 7}} {
		if got := accountFromToken(&oauth2.Token{Raw: raw}); got.Known() {
			t.Fatalf("%v: %+v", raw, got)
		}
	}
}

// The account a login signed in as is kept with it, and only with it: a login
// replaced by something that did not record an account reads as unknown, never
// The account a login signed in as rides on its credential in forge's file,
// and only there: a login stored without one reads as unknown, never as
// whoever signed in before.
func TestAdapter_KeepsTheLoginsAccountOnItsCredential(t *testing.T) {
	const server = "https://api.example.com"
	svc := New(Deps{Dirs: credentials.Dirs{ForgeHome: t.TempDir()}})
	alice := Account{ID: "ext-alice", Email: "alice@example.test"}

	if _, err := svc.Store(server, credentials.Credential{Token: "rlat_alice", AccountID: alice.ID, AccountEmail: alice.Email}); err != nil {
		t.Fatal(err)
	}
	if c, _, err := svc.Lookup(server); err != nil || AccountOf(c) != alice || c.Token != "rlat_alice" {
		t.Fatalf("after login: %+v %v", c, err)
	}
	// Another server's login is its own.
	if _, err := svc.Store("https://other.example.com", credentials.Credential{Token: "rlat_bob"}); err != nil {
		t.Fatal(err)
	}
	if c, _, _ := svc.Lookup(server); AccountOf(c) != alice {
		t.Fatalf("another server's login changed this one's account: %+v", AccountOf(c))
	}
	// A login whose account the control plane did not name replaces it.
	if _, err := svc.Store(server, credentials.Credential{Token: "rlat_someone"}); err != nil {
		t.Fatal(err)
	}
	if c, _, err := svc.Lookup(server); err != nil || AccountOf(c).Known() {
		t.Fatalf("an unnamed login kept the previous account: %+v %v", AccountOf(c), err)
	}
}
