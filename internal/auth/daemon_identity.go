package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// DaemonIDEnvVar is the environment variable used to inject a daemon ID at
// startup (e.g. when the control-plane provisions a managed daemon pod and
// expects it to register with a specific ID).
const DaemonIDEnvVar = "RELIANT_DAEMON_ID"

const daemonIdentityFileName = "daemon.json"

// DaemonIdentity holds the stable, per-host daemon identity.
// Generated once on first daemon start and persisted to ~/.reliant/daemon.json.
type DaemonIdentity struct {
	DaemonID string            `json:"daemon_id"`
	Name     string            `json:"name,omitempty"`
	Labels   map[string]string `json:"labels,omitempty"`
}

// reliantConfigDir returns ~/.reliant.
func reliantConfigDir() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}
	return filepath.Join(homeDir, ".reliant"), nil
}

// DaemonIdentityFilePath returns the path to the daemon identity file.
func DaemonIdentityFilePath() (string, error) {
	dir, err := reliantConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, daemonIdentityFileName), nil
}

// ReadDaemonIdentity reads the persisted daemon identity from disk.
// Returns nil, nil if no identity file exists yet.
func ReadDaemonIdentity() (*DaemonIdentity, error) {
	path, err := DaemonIdentityFilePath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading daemon identity file: %w", err)
	}

	var identity DaemonIdentity
	if err := json.Unmarshal(data, &identity); err != nil {
		return nil, fmt.Errorf("parsing daemon identity file: %w", err)
	}

	if identity.DaemonID == "" {
		return nil, nil
	}
	return &identity, nil
}

// WriteDaemonIdentity persists the daemon identity to disk.
func WriteDaemonIdentity(identity *DaemonIdentity) error {
	path, err := DaemonIdentityFilePath()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("creating identity directory: %w", err)
	}

	data, err := json.MarshalIndent(identity, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling daemon identity: %w", err)
	}

	return os.WriteFile(path, data, 0600)
}

// EnsureDaemonIdentity returns the existing daemon identity or creates a new
// one with a freshly generated UUID. The name parameter, if non-empty,
// overrides any previously stored name.
//
// If the RELIANT_DAEMON_ID environment variable is set, its value takes
// precedence over any persisted/generated UUID. This allows the control-plane
// to provision managed daemon pods with a known ID so daemon-gateway and the
// control-plane agree on the daemon's identity.
func EnsureDaemonIdentity(name string) (*DaemonIdentity, error) {
	if envID := strings.TrimSpace(os.Getenv(DaemonIDEnvVar)); envID != "" {
		return &DaemonIdentity{
			DaemonID: envID,
			Name:     name,
		}, nil
	}

	identity, err := ReadDaemonIdentity()
	if err != nil {
		return nil, err
	}

	if identity != nil {
		// Update name if explicitly provided.
		if name != "" && identity.Name != name {
			identity.Name = name
			if err := WriteDaemonIdentity(identity); err != nil {
				return nil, fmt.Errorf("updating daemon identity name: %w", err)
			}
		}
		return identity, nil
	}

	// First run: generate stable identity.
	identity = &DaemonIdentity{
		DaemonID: uuid.New().String(),
		Name:     name,
	}
	if err := WriteDaemonIdentity(identity); err != nil {
		return nil, fmt.Errorf("persisting new daemon identity: %w", err)
	}
	return identity, nil
}
