package cliauth

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/pkg/credentials"
)

func writeLegacyRecord(t *testing.T, credPath, server, token string, a Account) {
	t.Helper()
	key, err := credentials.Normalize(server)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(legacyAccountRecords{Logins: map[string]legacyAccountRecord{
		key: {TokenSHA256: tokenDigest(token), Account: a},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyAccountsPath(credPath), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// A login saved while the account was kept in reliant-accounts.json keeps that
// account: the matching record moves onto the credential, once. A record for
// a login that has since been replaced names nobody.
func TestAdapter_AdoptsTheLegacyRecordedAccount(t *testing.T) {
	const server = "https://api.example.com"
	alice := Account{ID: "ext-alice", Email: "alice@example.test"}

	t.Run("hash matches: adopted and moved onto the credential", func(t *testing.T) {
		svc := New(Deps{Dirs: credentials.Dirs{ForgeHome: t.TempDir()}})
		path, err := svc.Store(server, credentials.Credential{Token: "rlat_alice"})
		if err != nil {
			t.Fatal(err)
		}
		writeLegacyRecord(t, path, server, "rlat_alice", alice)

		c, _, err := svc.Lookup(server)
		if err != nil {
			t.Fatal(err)
		}
		if got := AccountOf(c); got != alice {
			t.Fatalf("account = %+v, want %+v", got, alice)
		}
		if _, err := os.Stat(legacyAccountsPath(path)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the adopted record should be gone: %v", err)
		}
		stored, err := credentials.Lookup(path, server, ClientID)
		if err != nil {
			t.Fatal(err)
		}
		if got := AccountOf(stored); got != alice || stored.Token != "rlat_alice" {
			t.Fatalf("stored = %+v", stored)
		}
	})

	t.Run("hash does not match: unknown", func(t *testing.T) {
		svc := New(Deps{Dirs: credentials.Dirs{ForgeHome: t.TempDir()}})
		path, err := svc.Store(server, credentials.Credential{Token: "rlat_bob"})
		if err != nil {
			t.Fatal(err)
		}
		writeLegacyRecord(t, path, server, "rlat_alice", alice)

		c, _, err := svc.Lookup(server)
		if err != nil {
			t.Fatal(err)
		}
		if AccountOf(c).Known() {
			t.Fatalf("a stale record was adopted: %+v", AccountOf(c))
		}
	})

	t.Run("a new login drops the record", func(t *testing.T) {
		svc := New(Deps{Dirs: credentials.Dirs{ForgeHome: t.TempDir()}})
		path, err := svc.CredentialsPath()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		writeLegacyRecord(t, path, server, "rlat_alice", alice)
		if _, err := svc.Store(server, credentials.Credential{Token: "rlat_alice"}); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(legacyAccountsPath(path)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Store should drop the server's record: %v", err)
		}
		c, _, err := svc.Lookup(server)
		if err != nil {
			t.Fatal(err)
		}
		if AccountOf(c).Known() {
			t.Fatalf("the login Store replaced must not inherit the record: %+v", AccountOf(c))
		}
	})
}
