// Copyright (c) 2025 Reliant Labs
package forgecred

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/reliant-labs/forge/pkg/cloudcred"
	"github.com/reliant-labs/forge/pkg/credentials"
)

// cacheVersion is the on-disk format version; a file of another version is
// treated as empty (it is a cache — losing it costs one exchange).
const cacheVersion = 1

// tokenCache is the helper's memory across processes. forge runs the helper
// once per command; without this, every `forge` command would mint a token.
//
// KEYED BY THE SESSION THAT MINTED IT. An entry records the control plane it
// is for, the server that exchanged it, and a fingerprint (a hash prefix,
// never the credential) of the session credential it came from. A re-sign-in
// or a different account produces a different fingerprint, so a token minted
// for one identity is never handed out on behalf of another.
//
// BEST-EFFORT ON PURPOSE. A managed daemon's home may be read-only; a write
// that fails costs a mint next time, never the command. A corrupt file is
// ignored and rewritten.
type tokenCache struct{ path string }

type cacheFile struct {
	Version int          `json:"version"`
	Entries []cacheEntry `json:"entries"`
}

type cacheEntry struct {
	Endpoint  string    `json:"endpoint"`
	Server    string    `json:"server"`
	Subject   string    `json:"subject"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	Scopes    []string  `json:"scopes,omitempty"`
	Source    string    `json:"source,omitempty"`
}

// fingerprint identifies a session credential without storing it: the first
// 16 bytes of its SHA-256, hex.
func fingerprint(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:16])
}

func serverKey(server string) string {
	if key, err := credentials.Normalize(server); err == nil {
		return key
	}
	return server
}

func (c tokenCache) load() cacheFile {
	if c.path == "" {
		return cacheFile{Version: cacheVersion}
	}
	raw, err := os.ReadFile(c.path)
	if err != nil {
		return cacheFile{Version: cacheVersion}
	}
	var f cacheFile
	if json.Unmarshal(raw, &f) != nil || f.Version != cacheVersion {
		return cacheFile{Version: cacheVersion}
	}
	return f
}

// lookup returns s's cached token for endpoint while reuseWhileRemaining of
// its life is left.
func (c tokenCache) lookup(endpoint string, s Session, now time.Time) (cloudcred.Token, bool) {
	server, subject := serverKey(s.Server), fingerprint(s.Token)
	for _, e := range c.load().Entries {
		if e.Endpoint != endpoint || e.Server != server || e.Subject != subject {
			continue
		}
		if e.ExpiresAt.Sub(now) < reuseWhileRemaining {
			continue
		}
		exp := e.ExpiresAt
		return cloudcred.Token{Token: e.Token, ExpiresAt: &exp, Scopes: e.Scopes, Source: e.Source}, true
	}
	return cloudcred.Token{}, false
}

// store records tok as s's token for endpoint, replacing the previous one and
// dropping every expired entry. A token with no known expiry is not cached:
// there would be no way to know when it stopped being safe to hand out.
func (c tokenCache) store(endpoint string, s Session, tok cloudcred.Token, now time.Time) {
	if c.path == "" || tok.ExpiresAt == nil {
		return
	}
	server, subject := serverKey(s.Server), fingerprint(s.Token)
	f := c.load()
	kept := f.Entries[:0]
	for _, e := range f.Entries {
		if !e.ExpiresAt.After(now) || (e.Endpoint == endpoint && e.Server == server && e.Subject == subject) {
			continue
		}
		kept = append(kept, e)
	}
	f.Entries = append(kept, cacheEntry{
		Endpoint: endpoint, Server: server, Subject: subject,
		Token: tok.Token, ExpiresAt: tok.ExpiresAt.UTC(), Scopes: tok.Scopes, Source: tok.Source,
	})
	_ = c.save(f)
}

// forgetServer drops every entry exchanged at server.
func (c tokenCache) forgetServer(server string) error {
	if c.path == "" {
		return nil
	}
	if _, err := os.Stat(c.path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	key := serverKey(server)
	f := c.load()
	kept := f.Entries[:0]
	for _, e := range f.Entries {
		if e.Server != key {
			kept = append(kept, e)
		}
	}
	f.Entries = kept
	return c.save(f)
}

// save writes atomically (temp + rename) with mode 0600 in a 0700 directory:
// every entry is a bearer credential.
func (c tokenCache) save(f cacheFile) error {
	f.Version = cacheVersion
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(c.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("forgecred: create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".forge-token-cache-*.json")
	if err != nil {
		return fmt.Errorf("forgecred: create temp file in %s: %w", dir, err)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }() // no-op once renamed
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, c.path)
}
