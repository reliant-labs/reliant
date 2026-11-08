// Package devtelemetry records where a local env's telemetry goes, as
// `forge env up` decided it, so `forge obs` reads the decision instead of
// re-deriving it from ambient login state.
package devtelemetry

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// Record is .forge/obs/backend-<env>.json.
type Record struct {
	Env           string    `json:"env"`
	Backend       string    `json:"backend"` // "cloud" or "local"
	Reason        string    `json:"reason"`
	OrgID         string    `json:"org_id,omitempty"`
	EnvironmentID string    `json:"environment_id,omitempty"`
	InstanceID    string    `json:"instance_id,omitempty"`
	InstanceLabel string    `json:"instance_label,omitempty"`
	InstanceUser  string    `json:"instance_user,omitempty"`
	Endpoint      string    `json:"endpoint,omitempty"`
	DecidedAt     time.Time `json:"decided_at"`
}

// Path is where env's record lives in projectDir.
func Path(projectDir, env string) string {
	return filepath.Join(projectDir, ".forge", "obs", "backend-"+env+".json")
}

// Read returns env's record; ok is false when none was written.
func Read(projectDir, env string) (rec Record, ok bool, err error) {
	b, err := os.ReadFile(Path(projectDir, env))
	if errors.Is(err, os.ErrNotExist) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	if err := json.Unmarshal(b, &rec); err != nil {
		return Record{}, false, err
	}
	return rec, true, nil
}

// Write replaces env's record atomically.
func Write(projectDir string, rec Record) error {
	p := Path(projectDir, rec.Env)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(p, append(b, '\n'), 0o600)
}
