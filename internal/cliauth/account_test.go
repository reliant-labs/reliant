package cliauth

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
// as whoever signed in before.
func TestAdapter_RecordsTheLoginsAccount(t *testing.T) {
	const server = "https://api.example.com"
	home := t.TempDir()
	svc := New(Deps{Dirs: credentials.Dirs{ForgeHome: home}})
	alice := Account{ID: "ext-alice", Email: "alice@example.test"}

	path, err := svc.Store(server, Session{Credential: credentials.Credential{Token: "rlat_alice"}, Account: alice})
	if err != nil {
		t.Fatal(err)
	}
	if s, _, err := svc.LookupSession(server); err != nil || s.Account != alice || s.Token != "rlat_alice" {
		t.Fatalf("after login: %+v %v", s, err)
	}
	// Another server's login is its own.
	if _, err := svc.Store("https://other.example.com", Session{Credential: credentials.Credential{Token: "rlat_bob"}}); err != nil {
		t.Fatal(err)
	}
	if s, _, _ := svc.LookupSession(server); s.Account != alice {
		t.Fatalf("another server's login changed this one's account: %+v", s.Account)
	}

	// Replaced behind the record's back (an older reliant): unknown.
	if err := credentials.Store(path, server, ClientID, credentials.Credential{Token: "rlat_someone"}); err != nil {
		t.Fatal(err)
	}
	if s, _, err := svc.LookupSession(server); err != nil || s.Account.Known() {
		t.Fatalf("a login the record does not describe must be unknown: %+v %v", s.Account, err)
	}

	// A login whose account the control plane did not name clears it.
	if _, err := svc.Store(server, Session{Credential: credentials.Credential{Token: "rlat_alice"}, Account: alice}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Store(server, Session{Credential: credentials.Credential{Token: "rlat_alice"}}); err != nil {
		t.Fatal(err)
	}
	if s, _, _ := svc.LookupSession(server); s.Account.Known() {
		t.Fatalf("an unnamed login kept the previous account: %+v", s.Account)
	}

	// Logout forgets it, and the record file goes with the last record.
	if _, err := svc.Store(server, Session{Credential: credentials.Credential{Token: "rlat_alice"}, Account: alice}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Remove(server); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, accountsFileName)); !os.IsNotExist(err) {
		t.Fatalf("the record file outlived its last record: %v", err)
	}
}

// The record never holds a usable copy of the token.
func TestAccountRecord_HoldsNoToken(t *testing.T) {
	home := t.TempDir()
	svc := New(Deps{Dirs: credentials.Dirs{ForgeHome: home}})
	if _, err := svc.Store("https://api.example.com", Session{
		Credential: credentials.Credential{Token: "rlat_SECRETSECRETSECRET"},
		Account:    Account{ID: "ext-1"},
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(home, accountsFileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "SECRET") {
		t.Fatalf("the record holds the token:\n%s", raw)
	}
	info, err := os.Stat(filepath.Join(home, accountsFileName))
	if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		t.Fatalf("record file mode: %v %v", info.Mode(), err)
	}
}
