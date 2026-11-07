package bugreport

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// StateFile is the file, in forge's config directory, that holds this
// machine's install id and the reports filed in the last DedupeWindow.
const StateFile = "report-bug.json"

// State is this machine's report state: a random install id, minted on first
// use, which is the ingest's per-install rate-limit key, and the reports
// filed recently, which answer a repeat with the first report's id.
//
// A state file that cannot be read or written never stops a report: the
// report is sent with a fresh install id and is simply not deduplicated.
type State struct {
	path string

	mu sync.Mutex
}

// Filed is one recorded report.
type Filed struct {
	ID      string    `json:"id"`
	FiledAt time.Time `json:"filed_at"`
}

type stateDoc struct {
	InstallID string           `json:"install_id"`
	Reports   map[string]Filed `json:"reports,omitempty"`
}

// NewState is the state kept in dir.
func NewState(dir string) *State {
	return &State{path: filepath.Join(dir, StateFile)}
}

// Lookup returns the install id (minting and storing one on first use) and
// the report filed under key within DedupeWindow of now, if any.
func (s *State) Lookup(key string, now time.Time) (installID string, prior Filed, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc := s.load()
	if doc.InstallID == "" {
		doc.InstallID = newUUID()
		s.save(doc)
	}
	prior, ok = doc.Reports[key]
	if ok && now.Sub(prior.FiledAt) >= DedupeWindow {
		ok = false
	}
	return doc.InstallID, prior, ok
}

// Record stores that the report under key was filed as id at now, and
// drops every report older than DedupeWindow.
func (s *State) Record(key, id string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc := s.load()
	for k, f := range doc.Reports {
		if now.Sub(f.FiledAt) >= DedupeWindow {
			delete(doc.Reports, k)
		}
	}
	if doc.Reports == nil {
		doc.Reports = map[string]Filed{}
	}
	doc.Reports[key] = Filed{ID: id, FiledAt: now}
	s.save(doc)
}

func (s *State) load() stateDoc {
	var doc stateDoc
	data, err := os.ReadFile(s.path)
	if err != nil || json.Unmarshal(data, &doc) != nil {
		return stateDoc{}
	}
	return doc
}

func (s *State) save(doc stateDoc) {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return
	}
	_ = atomicfile.WriteFile(s.path, append(data, '\n'), 0o600)
}
