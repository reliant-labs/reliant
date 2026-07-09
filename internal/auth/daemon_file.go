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
// a single (scheme, host, port) endpoint.
//
// user_id is intentionally absent — the server derives it from the PAT and
// tells the daemon at registration time, so we don't need to track it
// client-side.
type DaemonCredentials struct {
	PAT          string    `json:"pat"`
	ServerURL    string    `json:"server_url"`
	GatewayURL   string    `json:"gateway_url,omitempty"`
	RegisteredAt time.Time `json:"registered_at"`
	// DaemonID is the stable, server-assigned identity for this daemon at
	// this origin. The server mints it on first registration and returns it
	// in RegistrationAck; the daemon persists it here and re-asserts it on
	// every reconnect so identity survives daemon restarts and machine
	// hostname changes (macOS flipping between *.lan and *.local). Empty
	// until the first successful registration. Cleared on logout by deleting
	// the whole origin entry.
	DaemonID string `json:"daemon_id,omitempty"`
}

// daemonCredentialsStore is the on-disk format: endpoint key → credentials.
// The key is the server URL collapsed to `scheme://host:port` (see
// endpointKey). Keying by the full origin — not just hostname — lets
// developers run multiple worktrees against different localhost ports
// without their PATs clobbering each other on each `daemon start`.
type daemonCredentialsStore map[string]*DaemonCredentials

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
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return make(daemonCredentialsStore), nil
		}
		return nil, fmt.Errorf("reading daemon credentials file: %w", err)
	}

	var store daemonCredentialsStore
	if err := json.Unmarshal(data, &store); err != nil {
		// Pre-launch the file format changed (hostname-keyed map → origin
		// keyed map → previously a bare single-credential object). We don't
		// carry forward stale entries: just start fresh so the next
		// register/start writes the right key.
		return make(daemonCredentialsStore), nil
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

// ReadDaemonCredentials reads the daemon credentials for the origin
// (scheme://host:port) derived from serverURL.
// Returns nil, nil if no credentials exist for this origin.
func ReadDaemonCredentials(serverURL string) (*DaemonCredentials, error) {
	store, err := readStore()
	if err != nil {
		return nil, err
	}

	key := endpointKey(serverURL)
	if key == "" {
		return nil, nil
	}
	creds, ok := store[key]
	if !ok {
		return nil, nil
	}
	return creds, nil
}

// WriteDaemonCredentials persists daemon credentials, keyed by the origin
// (scheme://host:port) of creds.ServerURL.
func WriteDaemonCredentials(creds *DaemonCredentials) error {
	store, err := readStore()
	if err != nil {
		return err
	}

	key := endpointKey(creds.ServerURL)
	if key == "" {
		return fmt.Errorf("cannot write daemon credentials: invalid server URL %q", creds.ServerURL)
	}

	store[key] = creds
	return writeStore(store)
}

// DeleteDaemonCredentials removes the daemon credentials for the origin
// derived from serverURL. No-op when no entry exists.
func DeleteDaemonCredentials(serverURL string) error {
	store, err := readStore()
	if err != nil {
		return err
	}

	key := endpointKey(serverURL)
	if key == "" {
		return nil
	}
	delete(store, key)
	return writeStore(store)
}
