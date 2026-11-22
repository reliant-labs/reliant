package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

// stateSchema versions state.json. A reader that meets another schema
// treats the file as absent rather than guessing.
const stateSchema = "forge.selfupdate.state/v1"

// Swap phases.
const (
	phaseSwapped    = "swapped"    // the new binary is in place, unconfirmed
	phaseConfirmed  = "confirmed"  // it confirmed itself
	phaseReverted   = "reverted"   // the previous binary is back
	phaseSuperseded = "superseded" // something else replaced the binary since
)

// maxUnconfirmedCrashes is the boot-loop guard: a new binary whose processes
// died this many times without confirming is reverted at the next start.
const maxUnconfirmedCrashes = 2

// stateFile is StateDir/state.json.
type stateFile struct {
	Schema string         `json:"schema"`
	Swap   *swapRecord    `json:"swap,omitempty"`
	Failed []failedRecord `json:"failed,omitempty"`
}

// swapRecord is the last swap of this machine's binary.
type swapRecord struct {
	Phase string `json:"phase"`
	// Path is the install that was replaced; Previous the saved old binary.
	Path     string `json:"path"`
	Previous string `json:"previous"`
	// Retained are rollback images of earlier swaps that were never
	// confirmed, kept until this one is.
	Retained    []string  `json:"retained,omitempty"`
	FromVersion string    `json:"from_version"`
	ToVersion   string    `json:"to_version"`
	ToSHA256    string    `json:"to_sha256"`
	SwappedAt   time.Time `json:"swapped_at"`
	// ConfirmWithinMS travels with the swap, so Boot needs no Config.
	ConfirmWithinMS int64 `json:"confirm_within_ms"`
	// Boots are the processes that started the new binary and have not
	// confirmed it. One that is no longer alive crashed unconfirmed.
	Boots     []bootRecord `json:"boots,omitempty"`
	UpdatedAt time.Time    `json:"updated_at"`
	Reason    string       `json:"reason,omitempty"`
}

type bootRecord struct {
	PID int       `json:"pid"`
	At  time.Time `json:"at"`
}

// failedRecord is a version reverted on this machine, never offered again.
type failedRecord struct {
	Version string    `json:"version"`
	At      time.Time `json:"at"`
	Reason  string    `json:"reason"`
}

func (s stateFile) failed(version string) (failedRecord, bool) {
	for _, f := range s.Failed {
		if sameVersion(f.Version, version) {
			return f, true
		}
	}
	return failedRecord{}, false
}

func (s *stateFile) markFailed(version, reason string, at time.Time) {
	if _, ok := s.failed(version); ok {
		return
	}
	s.Failed = append(s.Failed, failedRecord{Version: version, At: at, Reason: reason})
}

func statePath(dir string) string { return filepath.Join(dir, "state.json") }

func loadState(dir string) (stateFile, error) {
	data, err := os.ReadFile(statePath(dir))
	if errors.Is(err, fs.ErrNotExist) {
		return stateFile{Schema: stateSchema}, nil
	}
	if err != nil {
		return stateFile{}, fmt.Errorf("selfupdate: read state: %w", err)
	}
	var st stateFile
	if err := json.Unmarshal(data, &st); err != nil || st.Schema != stateSchema {
		// A torn or foreign file must not wedge the updater: start over.
		return stateFile{Schema: stateSchema}, nil
	}
	return st, nil
}

// saveState writes state.json atomically: temp file, fsync, rename, fsync dir.
func saveState(dir string, st stateFile) error {
	st.Schema = stateSchema
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomic(statePath(dir), append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("selfupdate: write state: %w", err)
	}
	return nil
}

// withLock runs fn holding StateDir's lock, which serialises staging,
// swapping and booting across every instance sharing the state directory.
func withLock(ctx context.Context, dir string, fn func() error) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("selfupdate: create state dir: %w", err)
	}
	lock := flock.New(filepath.Join(dir, "lock"))
	ok, err := lock.TryLockContext(ctx, 50*time.Millisecond)
	if err != nil {
		return fmt.Errorf("selfupdate: lock %s: %w", dir, err)
	}
	if !ok {
		return fmt.Errorf("selfupdate: lock %s: not acquired", dir)
	}
	defer func() { _ = lock.Unlock() }()
	return fn()
}
