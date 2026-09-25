package auth

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const daemonFileName = "daemon.json"

// DaemonCredentials holds the persisted daemon registration credentials for
// one account at one endpoint.
//
// user_id is intentionally absent — the server derives it from the PAT and
// tells the daemon at registration time, so we don't need to track it
// client-side.
//
// The stable server-assigned daemon id is NOT here. It used to be, keyed by
// origin alone, which meant every worktree on one machine read and re-asserted
// the same id and the gateway evicted one of them on every registration. It now
// lives in the instance's own data directory — see
// internal/toolexec/bootstrap.ReadDaemonID — because identity is per instance
// (origin, account, workspace) while a PAT is per account.
type DaemonCredentials struct {
	PAT          string    `json:"pat"`
	ServerURL    string    `json:"server_url"`
	GatewayURL   string    `json:"gateway_url,omitempty"`
	RegisteredAt time.Time `json:"registered_at"`
	// Sub is the Supabase subject the PAT was minted for. It is part of the
	// store key, not a passenger: two accounts signed in against one origin
	// each keep their own PAT instead of overwriting each other. It is
	// repeated inside the entry so a credential carries its own identity when
	// it is passed around detached from the store.
	Sub string `json:"sub,omitempty"`
	// ExpiresAt is when the PAT stops being accepted, when that is known.
	// Daemon credentials minted by TokenService.CreateToken (kind DAEMON) or by
	// control-plane for managed daemons are intentionally non-expiring, so
	// this is nil for them. It is populated only when the credential
	// originates from a bounded token (e.g. the Electron preflight persisting a web-UI token's expiry), so `daemon start` can
	// proactively re-mint before it lapses instead of booting on — and then
	// fatally failing with — a dead PAT. nil means "never expires".
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// DefaultAccount is the account name an entry takes when the caller has no
// subject to name it by — not signed in, or self-hosted with no accounts at
// all. It is a real key rather than "", so the store never holds an entry whose
// account is indistinguishable from an absent one.
//
// It is spelled exactly like daemoninstance.DefaultSubSegment so a credential
// and the instance directory that consumes it agree about what "no account"
// is called.
const DefaultAccount = "_default"

// daemonCredentialsStore is the on-disk format: origin → account → credentials.
//
// The origin is the server URL collapsed to `scheme://host:port` (see
// endpointKey), which lets one machine hold credentials for dev, staging and
// prod — and for several worktrees on distinct dynamic localhost ports — at
// once. The account is the Supabase subject the PAT was minted for, which lets
// two people (or one person's two accounts) share a machine against ONE origin:
// keyed by origin alone, the second `daemon start` overwrote the first's PAT.
type daemonCredentialsStore struct {
	// Origins is the nested credential map. The outer key is an origin, the
	// inner key an account (DefaultAccount when unknown).
	Origins map[string]map[string]*DaemonCredentials `json:"origins"`
	// DefaultAccounts names, per origin, the account a lookup resolves to when
	// the caller does not name one. Without it a no-flag `daemon start` on an
	// origin holding two accounts would have to pick arbitrarily — and would
	// pick differently as the map iteration order changed. Last write wins,
	// which makes "the account I most recently registered" the default.
	DefaultAccounts map[string]string `json:"default_accounts,omitempty"`
}

// newStore returns an empty, fully-initialized store. Both maps are non-nil so
// every caller can write without a nil check.
func newStore() daemonCredentialsStore {
	return daemonCredentialsStore{
		Origins:         make(map[string]map[string]*DaemonCredentials),
		DefaultAccounts: make(map[string]string),
	}
}

// accountKey normalizes an account name. An empty or whitespace-only subject
// becomes DefaultAccount, so "" and "_default" are one entry rather than two.
func accountKey(sub string) string {
	if s := strings.TrimSpace(sub); s != "" {
		return s
	}
	return DefaultAccount
}

// resolveAccount picks which account's entry a lookup means for one origin.
//
// An explicitly named account is used verbatim, present or not — a caller that
// asked for an account and got someone else's credential is the split-brain
// this nesting exists to prevent. With no account named, the recorded default
// wins; failing that, a lone entry is unambiguous and is used; and an origin
// holding several entries with no recorded default resolves to nothing rather
// than to an arbitrary one.
func (s daemonCredentialsStore) resolveAccount(origin, sub string) string {
	if strings.TrimSpace(sub) != "" {
		return accountKey(sub)
	}
	if def, ok := s.DefaultAccounts[origin]; ok {
		if _, exists := s.Origins[origin][def]; exists {
			return def
		}
	}
	accounts := s.Origins[origin]
	if len(accounts) == 1 {
		for account := range accounts {
			return account
		}
	}
	if _, ok := accounts[DefaultAccount]; ok {
		return DefaultAccount
	}
	return ""
}

// daemonAuthDir returns the per-user state directory `~/.reliant`. Same
// path on every supported OS — Windows tolerates the leading dot just fine
// (no hidden-file convention attached) and several widely-used CLIs ship
// the same layout (`~/.aws`, `~/.kube`, `~/.gh`).
func daemonAuthDir() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}
	return filepath.Join(homeDir, ".reliant"), nil
}

// endpointKey collapses a server URL to its origin (scheme://host:port) for
// use as the credentials-store key. Path, query, and fragment are dropped so
// `https://staging.reliantapi.com/grpc` and `https://staging.reliantapi.com/api`
// share the same entry, while `http://localhost:3123` and
// `http://localhost:8123` get distinct entries.
//
// If the URL has no explicit port, the scheme's default port is implicit and
// the host portion remains unchanged (e.g. `https://staging.reliantapi.com`).
// Returns "" for unparseable input — callers must reject empty keys.
func endpointKey(serverURL string) string {
	s := strings.TrimSpace(serverURL)
	if s == "" {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme == "" {
		scheme = "https"
	}
	host := strings.ToLower(u.Host)
	return scheme + "://" + host
}

// DaemonCredentialsFilePath returns the path to the daemon credentials file.
func DaemonCredentialsFilePath() (string, error) {
	authDir, err := daemonAuthDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(authDir, daemonFileName), nil
}

// readStore reads the full credentials store from disk.
// Returns an empty store if the file doesn't exist.
func readStore() (daemonCredentialsStore, error) {
	path, err := DaemonCredentialsFilePath()
	if err != nil {
		return newStore(), err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return newStore(), nil
		}
		return newStore(), fmt.Errorf("reading daemon credentials file: %w", err)
	}

	var store daemonCredentialsStore
	if err := json.Unmarshal(data, &store); err != nil || store.Origins == nil {
		// Pre-launch the file format has changed several times (bare
		// single-credential object → hostname-keyed map → origin-keyed map →
		// this origin→account map). We don't carry stale entries forward or
		// write migration code for a product that has not shipped: start
		// fresh, and the next register/start writes the right key. A
		// successfully-parsed document with no `origins` member is an older
		// format, not an empty new one, and takes the same path.
		return newStore(), nil
	}
	if store.DefaultAccounts == nil {
		store.DefaultAccounts = make(map[string]string)
	}
	return store, nil
}

// writeStore writes the full credentials store to disk.
func writeStore(store daemonCredentialsStore) error {
	path, err := DaemonCredentialsFilePath()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("creating auth directory: %w", err)
	}

	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling daemon credentials: %w", err)
	}

	return os.WriteFile(path, data, 0600)
}

// ReadDaemonCredentials reads the credentials for one account at the origin
// (scheme://host:port) derived from serverURL.
//
// An empty sub means "whichever account this origin defaults to" — the
// recorded default, or a lone entry when there is exactly one. It never picks
// arbitrarily among several: an origin with two accounts and no recorded
// default returns nil rather than a coin flip, so a caller that needs a
// specific account must name it.
//
// Returns nil, nil when no credential matches.
func ReadDaemonCredentials(serverURL, sub string) (*DaemonCredentials, error) {
	store, err := readStore()
	if err != nil {
		return nil, err
	}

	origin := endpointKey(serverURL)
	if origin == "" {
		return nil, nil
	}
	account := store.resolveAccount(origin, sub)
	if account == "" {
		return nil, nil
	}
	creds, ok := store.Origins[origin][account]
	if !ok {
		return nil, nil
	}
	return creds, nil
}

// WriteDaemonCredentials persists one account's credentials at the origin of
// creds.ServerURL, and records that account as the origin's default.
//
// The account is creds.Sub, or DefaultAccount when that is empty. Writing
// always claims the default so the most recently registered account is the one
// a no-flag invocation resolves to — the alternative, leaving a stale default
// pointing at an account the user has moved off, is the more surprising of the
// two.
func WriteDaemonCredentials(creds *DaemonCredentials) error {
	store, err := readStore()
	if err != nil {
		return err
	}

	origin := endpointKey(creds.ServerURL)
	if origin == "" {
		return fmt.Errorf("cannot write daemon credentials: invalid server URL %q", creds.ServerURL)
	}
	account := accountKey(creds.Sub)

	if store.Origins[origin] == nil {
		store.Origins[origin] = make(map[string]*DaemonCredentials)
	}
	store.Origins[origin][account] = creds
	store.DefaultAccounts[origin] = account
	return writeStore(store)
}

// DeleteDaemonCredentials removes one account's credentials at the origin
// derived from serverURL. No-op when no entry exists.
//
// An empty sub deletes whatever the origin resolves to by default, which is
// what logout means for the signed-in account. Emptying an origin removes the
// origin itself rather than leaving an empty map behind, so the file shrinks
// back to nothing when the last account logs out.
func DeleteDaemonCredentials(serverURL, sub string) error {
	store, err := readStore()
	if err != nil {
		return err
	}

	origin := endpointKey(serverURL)
	if origin == "" {
		return nil
	}
	account := store.resolveAccount(origin, sub)
	if account == "" {
		return nil
	}

	delete(store.Origins[origin], account)
	if len(store.Origins[origin]) == 0 {
		delete(store.Origins, origin)
		delete(store.DefaultAccounts, origin)
	} else if store.DefaultAccounts[origin] == account {
		// The default pointed at the account just removed. Leaving it dangling
		// would make every no-account lookup on this origin fall through to
		// "several entries, no default" and resolve to nothing, even though a
		// perfectly good credential remains.
		delete(store.DefaultAccounts, origin)
		for remaining := range store.Origins[origin] {
			store.DefaultAccounts[origin] = remaining
			break
		}
	}
	return writeStore(store)
}
