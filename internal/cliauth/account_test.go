package cliauth

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

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

// The account now lives in forge's shared file, so pin what the CLI relies on
// from it: the file stays a 0600 secret, the reliant CLI's entry never touches
// forge's entry for the same server, and logout removes only the CLI's own.
func TestAdapter_CredentialFileContract(t *testing.T) {
	const server = "https://api.example.com"
	home := t.TempDir()
	path := filepath.Join(home, credentials.FileName)
	if err := credentials.Store(path, server, "forge-cli", credentials.Credential{Token: "rlat_forge", AccountID: "ext-forge"}); err != nil {
		t.Fatal(err)
	}
	svc := New(Deps{Dirs: credentials.Dirs{ForgeHome: home}})
	if _, err := svc.Store(server, credentials.Credential{Token: "rlat_alice", AccountID: "ext-alice"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		t.Fatalf("credentials file mode: %v %v", info, err)
	}
	if c, err := credentials.Lookup(path, server, "forge-cli"); err != nil || c.Token != "rlat_forge" || c.AccountID != "ext-forge" {
		t.Fatalf("the CLI login changed forge's entry: %+v %v", c, err)
	}
	if existed, _, err := svc.Remove(server); err != nil || !existed {
		t.Fatalf("remove: %v %v", existed, err)
	}
	if _, _, err := svc.Lookup(server); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("after logout: %v", err)
	}
	if c, err := credentials.Lookup(path, server, "forge-cli"); err != nil || c.Token != "rlat_forge" {
		t.Fatalf("logout removed forge's entry: %+v %v", c, err)
	}
}

// A binary from before the account fields shares this file. What it wrote, and
// what it rewrites, must read here as a usable login whose account is unknown —
// never as an error, and never as somebody else's account.
func TestAdapter_ReadsAnOlderWritersFileAsAccountUnknown(t *testing.T) {
	const server = "https://api.example.com"
	home := t.TempDir()
	path := filepath.Join(home, credentials.FileName)
	svc := New(Deps{Dirs: credentials.Dirs{ForgeHome: home}})

	// An entry an older writer saved: no account fields at all.
	old := `{"version":1,"credentials":{"` + server + `":{"` + ClientID + `":{"token":"rlat_old","created_at":"2026-01-01T00:00:00Z"}}}}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if c, _, err := svc.Lookup(server); err != nil || c.Token != "rlat_old" || AccountOf(c).Known() {
		t.Fatalf("older entry: %+v %v", c, err)
	}

	// A newer login names its account; an older writer then rewrites the
	// file through a Credential type that has no account fields, as it does
	// when it stores its own login elsewhere in the file.
	if _, err := svc.Store(server, credentials.Credential{Token: "rlat_new", AccountID: "ext-alice"}); err != nil {
		t.Fatal(err)
	}
	type olderCredential struct {
		Token     string    `json:"token"`
		CreatedAt time.Time `json:"created_at"`
	}
	var f struct {
		Version     int                                   `json:"version"`
		Credentials map[string]map[string]olderCredential `json:"credentials"`
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	f.Credentials["https://other.example.com"] = map[string]olderCredential{ClientID: {Token: "rlat_other"}}
	if raw, err = json.Marshal(f); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if c, _, err := svc.Lookup(server); err != nil || c.Token != "rlat_new" || AccountOf(c).Known() {
		t.Fatalf("after an older writer's rewrite: %+v %v", c, err)
	}
}
