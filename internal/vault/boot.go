// Copyright (c) 2025 Reliant Labs

package vault

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/pkg/crypto"
)

// KeyFileName is the self-hosted key file, inside the data dir.
const KeyFileName = "vault.key"

// BootOptions configures Boot.
type BootOptions struct {
	// Hosted is true when a control plane is configured (tokenauthority
	// ModeControlPlane). A hosted server never invents a key.
	Hosted  bool
	Getenv  func(string) string
	DataDir string
	DB      interface {
		DBTX
	}
	// Warn receives the one-time "generated a key" warning. Defaults to slog.
	Warn func(msg string, args ...any)
}

// Boot resolves the key-encryption key and returns a ready Vault.
//
//   - A present but malformed key is always fatal.
//   - Hosted with no key is fatal.
//   - Self-hosted with no key reuses <DataDir>/vault.key, or generates one
//     (0600, create-exclusive) and warns. It refuses to generate when sealed data
//     already exists: a fresh key would silently orphan it.
func Boot(ctx context.Context, opts BootOptions) (*Vault, error) {
	getenv := opts.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	warn := opts.Warn
	if warn == nil {
		warn = slog.Warn
	}

	raw := strings.TrimSpace(getenv(EnvKey))
	if raw != "" {
		return fromRaw(opts.DB, raw, EnvKey)
	}
	if opts.Hosted {
		return nil, fmt.Errorf("%s is not set: a hosted reliant refuses to start without its vault key. "+
			"Generate one with `openssl rand -base64 32`, then run "+
			"`forge secret set --env <env> %s v1:<that value>` and redeploy", EnvKey, EnvKey)
	}

	path := filepath.Join(opts.DataDir, KeyFileName)
	if data, err := os.ReadFile(path); err == nil {
		return fromRaw(opts.DB, strings.TrimSpace(string(data)), path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("reading vault key file %s: %w", path, err)
	}

	sealed, err := sealedDataExists(ctx, opts.DB)
	if err != nil {
		return nil, err
	}
	if sealed {
		return nil, fmt.Errorf("sealed data exists but no vault key was found (%s unset, %s missing); "+
			"refusing to generate a new key because that would orphan every sealed value. "+
			"Restore the key file or set %s", EnvKey, path, EnvKey)
	}

	keyBytes := make([]byte, dekLen)
	if _, err := rand.Read(keyBytes); err != nil {
		return nil, fmt.Errorf("generating vault key: %w", err)
	}
	generated := "v1:" + base64.StdEncoding.EncodeToString(keyBytes)
	if err := os.MkdirAll(opts.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("creating data dir for vault key: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		// Another process generated it first; use theirs.
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil, fmt.Errorf("reading vault key file %s: %w", path, rerr)
		}
		return fromRaw(opts.DB, strings.TrimSpace(string(data)), path)
	}
	if err != nil {
		return nil, fmt.Errorf("creating vault key file %s: %w", path, err)
	}
	_, werr := f.WriteString(generated + "\n")
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("writing vault key file %s: %w", path, werr)
	}
	warn("generated a vault key; back it up, because losing it loses every saved credential",
		"path", path)
	return fromRaw(opts.DB, generated, path)
}

func fromRaw(db DBTX, raw, source string) (*Vault, error) {
	ring, err := crypto.ParseKeyring(raw)
	if err != nil {
		return nil, fmt.Errorf("vault key from %s is malformed: %w", source, err)
	}
	return New(db, NewEnvKeyWrapper(ring)), nil
}

func sealedDataExists(ctx context.Context, db DBTX) (bool, error) {
	var exists bool
	err := db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM vault_keys) OR EXISTS (SELECT 1 FROM api_keys WHERE api_key_sealed IS NOT NULL)`).
		Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("checking for sealed data: %w", err)
	}
	return exists, nil
}
