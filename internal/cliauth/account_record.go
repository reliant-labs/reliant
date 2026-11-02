package cliauth

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/reliant-labs/forge/pkg/credentials"
)

// ── WHERE THE CLI LOGIN'S ACCOUNT IS KEPT ─────────────────────────────
// The login itself lives in forge's shared credentials file, but the forge
// release reliant pins has no field there for the account it signed in as
// (forge gains credentials.Credential.AccountID / AccountEmail). Until reliant
// pins a forge that has them, the account is kept beside that file, in
// accountsFileName: one record per server, tied to the login it describes by
// a hash of its token.
//
// The hash is what keeps it honest. Anything that replaces the login without
// writing a record here — an older reliant, or a hand edit — leaves a record
// whose hash no longer matches, and the login reads as account unknown rather
// than as whoever signed in before. Unknown is always safe: every caller then
// behaves as it did before accounts were recorded.

// accountsFileName is the record file, in the credentials file's directory.
const accountsFileName = "reliant-accounts.json"

type accountRecords struct {
	// Logins is normalized server endpoint → the account its login signed
	// in as.
	Logins map[string]accountRecord `json:"logins"`
}

type accountRecord struct {
	// TokenSHA256 identifies the login the record describes, without
	// holding a usable copy of it.
	TokenSHA256 string `json:"token_sha256"`
	Account
}

func tokenDigest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func accountsPath(credentialsPath string) string {
	return filepath.Join(filepath.Dir(credentialsPath), accountsFileName)
}

// readAccountRecords reads the record file. A missing or unreadable one is
// empty: a record is only ever an answer to "do we know who this is".
func readAccountRecords(path string) accountRecords {
	recs := accountRecords{Logins: map[string]accountRecord{}}
	data, err := os.ReadFile(path)
	if err != nil {
		return recs
	}
	if err := json.Unmarshal(data, &recs); err != nil || recs.Logins == nil {
		return accountRecords{Logins: map[string]accountRecord{}}
	}
	return recs
}

func writeAccountRecords(path string, recs accountRecords) error {
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
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), accountsFileName+".*")
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

// recordAccount notes who the login for server, holding token, signed in as.
// An unknown account removes the server's record: the login it described has
// just been replaced.
func recordAccount(credentialsPath, server, token string, a Account) error {
	key, err := credentials.Normalize(server)
	if err != nil {
		return err
	}
	path := accountsPath(credentialsPath)
	recs := readAccountRecords(path)
	if a.Known() {
		recs.Logins[key] = accountRecord{TokenSHA256: tokenDigest(token), Account: a}
	} else {
		delete(recs.Logins, key)
	}
	if err := writeAccountRecords(path, recs); err != nil {
		return fmt.Errorf("recording the login's account in %s: %w", path, err)
	}
	return nil
}

// recordedAccount is the account the login for server, holding token, signed
// in as, or zero when no record describes that login.
func recordedAccount(credentialsPath, server, token string) Account {
	key, err := credentials.Normalize(server)
	if err != nil {
		return Account{}
	}
	rec, ok := readAccountRecords(accountsPath(credentialsPath)).Logins[key]
	if !ok || rec.TokenSHA256 != tokenDigest(token) {
		return Account{}
	}
	return rec.Account
}

// forgetAccount drops the server's record.
func forgetAccount(credentialsPath, server string) error {
	return recordAccount(credentialsPath, server, "", Account{})
}
