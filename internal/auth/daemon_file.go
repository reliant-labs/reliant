package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

const daemonFileName = "reliant-daemon.json"

// DaemonCredentials holds the persisted daemon registration credentials.
type DaemonCredentials struct {
	PAT          string    `json:"pat"`
	UserID       string    `json:"user_id"`
	ServerURL    string    `json:"server_url"`
	GatewayURL   string    `json:"gateway_url,omitempty"`
	RegisteredAt time.Time `json:"registered_at"`
}

// DaemonCredentialsFilePath returns the OS-native location of the daemon credentials file.
func DaemonCredentialsFilePath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}

	var authDir string
	switch runtime.GOOS {
	case "darwin":
		authDir = filepath.Join(homeDir, "Library", "Application Support", "reliant", "auth")
	case "windows":
		authDir = filepath.Join(homeDir, "AppData", "Roaming", "reliant", "auth")
	default:
		authDir = filepath.Join(homeDir, ".config", "reliant", "auth")
	}

	return filepath.Join(authDir, daemonFileName), nil
}

// ReadDaemonCredentials reads the daemon credentials from the local file.
// Returns nil, nil if the file does not exist.
func ReadDaemonCredentials() (*DaemonCredentials, error) {
	path, err := DaemonCredentialsFilePath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read daemon credentials file %s: %w", path, err)
	}

	var creds DaemonCredentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("failed to parse daemon credentials file %s: %w", path, err)
	}

	return &creds, nil
}

// WriteDaemonCredentials persists daemon credentials to the local file.
func WriteDaemonCredentials(creds *DaemonCredentials) error {
	path, err := DaemonCredentialsFilePath()
	if err != nil {
		return fmt.Errorf("determining daemon credentials file path: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("creating auth directory: %w", err)
	}

	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling daemon credentials: %w", err)
	}

	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("writing daemon credentials file: %w", err)
	}

	return nil
}

// DeleteDaemonCredentials removes the daemon credentials file.
func DeleteDaemonCredentials() error {
	path, err := DaemonCredentialsFilePath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing daemon credentials file: %w", err)
	}
	return nil
}
