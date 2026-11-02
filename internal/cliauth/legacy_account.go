package cliauth

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/reliant-labs/forge/pkg/credentials"
)

// ── THE LOGIN'S ACCOUNT, FROM BEFORE IT RODE ON THE CREDENTIAL ────────
// Earlier releases kept the account a CLI login signed in as beside forge's
// credentials file, in legacyAccountsFileName: one record per server, tied to
// the login it describes by a SHA-256 of its token. A login saved then has no
// AccountID on its credential, and reading it as unknown would let `daemon
// start` fall back to the default daemon sign-in — possibly another account's.
//
// So Lookup adopts a record whose hash matches the stored token: the account
// moves onto the credential and the record is dropped, once. A record whose
// hash does not match describes a login that has since been replaced, and is
// never adopted: that login stays unknown, never "whoever signed in before".
// Store and Remove drop the server's record, so nothing outlives the login it
// described.

// legacyAccountsFileName is the record file, in the credentials file's
// directory.
const legacyAccountsFileName = "reliant-accounts.json"

var errLegacyLoginReplaced = errors.New("the login was replaced since it was read")

type legacyAccountRecords struct {
	// Logins is normalized server endpoint → the account its login signed
	// in as.
	Logins map[string]legacyAccountRecord `json:"logins"`
}

type legacyAccountRecord struct {
	TokenSHA256 string `json:"token_sha256"`
	Account
}

func legacyAccountsPath(credentialsPath string) string {
	return filepath.Join(filepath.Dir(credentialsPath), legacyAccountsFileName)
}

func tokenDigest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// readLegacyAccounts reads the record file. A missing or unreadable one is
// empty: a record only ever answers "do we know who this is".
func readLegacyAccounts(path string) legacyAccountRecords {
	var recs legacyAccountRecords
	data, err := os.ReadFile(path)
	if err != nil {
		return legacyAccountRecords{}
	}
	if err := json.Unmarshal(data, &recs); err != nil {
		return legacyAccountRecords{}
	}
	return recs
}

// legacyAccount is the account the record for server names, when it
// describes the login holding token; zero otherwise.
func legacyAccount(credentialsPath, server, token string) Account {
	key, err := credentials.Normalize(server)
	if err != nil {
		return Account{}
	}
	rec, ok := readLegacyAccounts(legacyAccountsPath(credentialsPath)).Logins[key]
	if !ok || rec.TokenSHA256 != tokenDigest(token) {
		return Account{}
	}
	return rec.Account
}

// dropLegacyAccount removes server's record, and the file once it is empty.
func dropLegacyAccount(credentialsPath, server string) error {
	path := legacyAccountsPath(credentialsPath)
	recs := readLegacyAccounts(path)
	key, err := credentials.Normalize(server)
	if err != nil {
		return err
	}
	if _, ok := recs.Logins[key]; !ok && len(recs.Logins) > 0 {
		return nil
	}
	delete(recs.Logins, key)
	if len(recs.Logins) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	data, err := json.MarshalIndent(recs, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), legacyAccountsFileName+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // gone after the rename
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// adoptLegacyAccount names c's account from a hash-matching legacy record,
// moves it onto the stored credential and drops the record. Persisting is
// best-effort: the account is returned on c either way, and a failed write
// leaves the record to be adopted on the next read.
func adoptLegacyAccount(credentialsPath, server string, c credentials.Credential) credentials.Credential {
	if AccountOf(c).Known() {
		return c
	}
	a := legacyAccount(credentialsPath, server, c.Token)
	if !a.Known() {
		return c
	}
	c.AccountID, c.AccountEmail = a.ID, a.Email
	// Only onto the login the record describes: one that replaced it since
	// the read is left alone.
	err := credentials.Update(credentialsPath, func(f *credentials.File) error {
		cur, err := f.Get(server, ClientID)
		if err != nil || cur.Token != c.Token {
			return errLegacyLoginReplaced
		}
		cur.AccountID, cur.AccountEmail = a.ID, a.Email
		return f.Put(server, ClientID, cur)
	})
	if err == nil {
		_ = dropLegacyAccount(credentialsPath, server)
	}
	return c
}
