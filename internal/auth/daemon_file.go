package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const daemonFileName = "reliant-daemon.json"

// DaemonCredentials holds the persisted daemon registration credentials for a single host.
type DaemonCredentials struct {
	PAT          string    `json:"pat"`
	UserID       string    `json:"user_id"`
	ServerURL    string    `json:"server_url"`
	GatewayURL   string    `json:"gateway_url,omitempty"`
	RegisteredAt time.Time `json:"registered_at"`
}

// daemonCredentialsStore is the on-disk format: a map of hostname → credentials.
type daemonCredentialsStore map[string]*DaemonCredentials

// daemonAuthDir returns the OS-native auth directory path.
func daemonAuthDir() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}

	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(homeDir, "Library", "Application Support", "reliant", "auth"), nil
	case "windows":
		return filepath.Join(homeDir, "AppData", "Roaming", "reliant", "auth"), nil
	default:
		return filepath.Join(homeDir, ".config", "reliant", "auth"), nil
	}
}

// hostFromServerURL extracts just the hostname from a server URL.
// e.g. "https://localhost:3118" → "localhost", "https://staging.reliantapi.com/grpc" → "staging.reliantapi.com"
func hostFromServerURL(serverURL string) string {
	host := serverURL
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	host = strings.SplitN(host, ":", 2)[0]
	host = strings.SplitN(host, "/", 2)[0]
	return host
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

	// Try new format (map of host → creds)
	var store daemonCredentialsStore
	if err := json.Unmarshal(data, &store); err == nil && len(store) > 0 {
		return store, nil
	}

	// Try legacy format (single credential object) and migrate
	var legacy DaemonCredentials
	if err := json.Unmarshal(data, &legacy); err == nil && legacy.PAT != "" {
		host := hostFromServerURL(legacy.ServerURL)
		if host == "" {
			host = "default"
		}
		store = daemonCredentialsStore{host: &legacy}
		// Migrate: rewrite in new format
		_ = writeStore(store)
		return store, nil
	}

	return make(daemonCredentialsStore), nil
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

// ReadDaemonCredentials reads the daemon credentials for a specific server URL (keyed by hostname).
// Returns nil, nil if no credentials exist for this host.
func ReadDaemonCredentials(serverURL string) (*DaemonCredentials, error) {
	store, err := readStore()
	if err != nil {
		return nil, err
	}

	host := hostFromServerURL(serverURL)
	creds, ok := store[host]
	if !ok {
		return nil, nil
	}
	return creds, nil
}

// WriteDaemonCredentials persists daemon credentials, keyed by the hostname from ServerURL.
func WriteDaemonCredentials(creds *DaemonCredentials) error {
	store, err := readStore()
	if err != nil {
		return err
	}

	host := hostFromServerURL(creds.ServerURL)
	if host == "" {
		return fmt.Errorf("cannot write daemon credentials: empty server URL")
	}

	store[host] = creds
	return writeStore(store)
}

// DeleteDaemonCredentials removes the daemon credentials for a specific server URL.
func DeleteDaemonCredentials(serverURL string) error {
	store, err := readStore()
	if err != nil {
		return err
	}

	host := hostFromServerURL(serverURL)
	delete(store, host)
	return writeStore(store)
}
