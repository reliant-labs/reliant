package cliev

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gofrs/flock"
)

// stateDoc is telemetry.json.
type stateDoc struct {
	InstallID string `json:"install_id,omitempty"`
	// Enabled is the user's choice (`<app> telemetry on|off`); nil when they
	// never made one.
	Enabled      *bool  `json:"enabled,omitempty"`
	NoticeShown  bool   `json:"notice_shown,omitempty"`
	FirstRunSent bool   `json:"first_run_sent,omitempty"`
	Day          string `json:"day,omitempty"`
	SentToday    int    `json:"sent_today,omitempty"`
	// Attribution is where the install came from, as the install page passed
	// it (SetInstallAttribution): a query string of rid and utm_* only.
	Attribution string `json:"attribution,omitempty"`
}

// state is the file. A file that cannot be read is a fresh install; one that
// cannot be written is simply not remembered.
//
// Several runs of the program can share the file at once (a shell loop, an
// agent running commands in parallel), so every write is update: under a
// cross-process lock it re-reads the file and changes only the fields its
// caller owns. A run that started before `<app> telemetry off` therefore
// cannot write its stale copy back over the opt-out.
type state struct {
	path string
	mu   sync.Mutex
}

// lockWait bounds how long a write waits for another run's lock. Telemetry
// never holds up a command: past it, the write is skipped.
const lockWait = 2 * time.Second

func newState(path string) *state { return &state{path: path} }

func (s *state) load() stateDoc {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read()
}

// update applies fn to the file's current contents and writes the result,
// holding the process mutex and the cross-process lock throughout, and
// returns what it wrote.
func (s *state) update(fn func(*stateDoc)) (stateDoc, error) {
	return s.updateCtx(context.Background(), fn)
}

// updateCtx is update, waiting for another run's lock no longer than ctx
// allows (and never longer than lockWait).
func (s *state) updateCtx(ctx context.Context, fn func(*stateDoc)) (stateDoc, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return stateDoc{}, err
	}
	lock := flock.New(s.path + ".lock")
	ctx, cancel := context.WithTimeout(ctx, lockWait)
	defer cancel()
	ok, err := lock.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		return stateDoc{}, err
	}
	if !ok {
		return stateDoc{}, fmt.Errorf("telemetry state %s is locked by another run", s.path)
	}
	defer func() { _ = lock.Unlock() }()
	doc := s.read()
	fn(&doc)
	return doc, s.write(doc)
}

// read is the file's contents; the caller holds mu.
func (s *state) read() stateDoc {
	var doc stateDoc
	data, err := os.ReadFile(s.path)
	if err != nil || json.Unmarshal(data, &doc) != nil {
		return stateDoc{}
	}
	return doc
}

// write replaces the file with doc; the caller holds mu and the file lock.
func (s *state) write(doc stateDoc) error {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".telemetry-*.json")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}

// newUUID is a random (version 4) UUID.
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
