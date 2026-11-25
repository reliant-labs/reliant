// Copyright (c) 2026 Reliant Labs
package idlereclaim

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// What a later command needs to know. Nothing reinstalls a stripped
// node_modules on its own except forge's dev loop (`forge env up` and
// `forge generate` install a frontend's dependencies when its node_modules
// is missing; `forge lint` does only with `--install`). Any other command (`npm test`, `npx vitest`,
// `tsc`) fails with "Cannot find module" or "command not found", which reads
// like a broken checkout. So every removal is recorded per directory, and a
// command that fails in one whose removed directories are still missing gets
// a note saying what was removed and how to put it back (Records.Note).

// RecordFile is the removal record's name in the reclaim state directory.
const RecordFile = "idle-removed.json"

// recordTTL is how long a removal is remembered for the note.
const recordTTL = 30 * 24 * time.Hour

// maxNotedPaths bounds the paths one note names.
const maxNotedPaths = 5

// Removal is what one pass removed from one directory: a scratch directory or
// a worktree checkout.
type Removal struct {
	At    time.Time `json:"at"`
	Idle  string    `json:"idle"`
	Paths []string  `json:"paths"`
}

// Records is the removal record, persisted in the reclaim state directory so
// the note outlives a daemon restart. Safe for concurrent use.
type Records struct {
	mu   sync.Mutex
	path string
	// byRoot maps a directory to what was removed from it.
	byRoot map[string]Removal
	loaded bool
}

// NewRecords returns the record persisted at path ("" keeps it in memory).
func NewRecords(path string) *Records {
	return &Records{path: path, byRoot: map[string]Removal{}}
}

func (r *Records) loadLocked() {
	if r.loaded {
		return
	}
	r.loaded = true
	if r.path == "" {
		return
	}
	b, err := os.ReadFile(r.path)
	if err != nil {
		return
	}
	var saved map[string]Removal
	if json.Unmarshal(b, &saved) == nil {
		for k, v := range saved {
			r.byRoot[k] = v
		}
	}
}

// add records that paths were removed from root at, after idle.
func (r *Records) add(root string, at time.Time, idle time.Duration, paths []string) {
	if r == nil || len(paths) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loadLocked()
	prev := r.byRoot[root]
	merged := append(append([]string(nil), prev.Paths...), paths...)
	sort.Strings(merged)
	merged = compactStrings(merged)
	r.byRoot[root] = Removal{At: at, Idle: idle.Round(time.Hour).String(), Paths: merged}
	r.pruneLocked(at)
	r.saveLocked()
}

// forget drops root's record: the directory itself is gone.
func (r *Records) forget(root string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loadLocked()
	if _, ok := r.byRoot[root]; ok {
		delete(r.byRoot, root)
		r.saveLocked()
	}
}

// pruneLocked drops records past recordTTL and those whose removed paths are
// all back.
func (r *Records) pruneLocked(now time.Time) {
	for root, rem := range r.byRoot {
		if now.Sub(rem.At) > recordTTL || len(missing(rem.Paths)) == 0 {
			delete(r.byRoot, root)
		}
	}
}

func (r *Records) saveLocked() {
	if r.path == "" {
		return
	}
	b, err := json.Marshal(r.byRoot)
	if err != nil {
		return
	}
	_ = writeFileAtomic(r.path, b)
}

// Note is the line a command that failed in workingDir gets appended to its
// stderr when the idle reclaim removed regenerable output from the directory
// it ran in, or from one it ran under, and that output is still missing. ""
// otherwise. It reads only the record and stats the removed paths.
func (r *Records) Note(workingDir string) string {
	if r == nil || workingDir == "" {
		return ""
	}
	workingDir = filepath.Clean(workingDir)
	r.mu.Lock()
	r.loadLocked()
	var roots []string
	for root := range r.byRoot {
		if within(root, workingDir) || within(workingDir, root) {
			roots = append(roots, root)
		}
	}
	sort.Strings(roots)
	var gone []string
	var at time.Time
	var idle string
	for _, root := range roots {
		rem := r.byRoot[root]
		gone = append(gone, missing(rem.Paths)...)
		if rem.At.After(at) {
			at, idle = rem.At, rem.Idle
		}
	}
	r.mu.Unlock()
	if len(gone) == 0 {
		return ""
	}
	shown := gone
	if len(shown) > maxNotedPaths {
		shown = shown[:maxNotedPaths]
	}
	names := make([]string, 0, len(shown))
	for _, p := range shown {
		names = append(names, relOrAbs(workingDir, p))
	}
	more := ""
	if len(gone) > len(shown) {
		more = fmt.Sprintf(" and %d more", len(gone)-len(shown))
	}
	return fmt.Sprintf("Note: Reliant removed %s%s on %s, after %s unused, to free disk space. "+
		"If this command failed on a missing module or tool, reinstall the dependencies with the project's package manager "+
		"(npm ci, pnpm install --frozen-lockfile, yarn install, cargo build) in that directory; "+
		"forge env up and forge generate reinstall a frontend's node_modules themselves, and forge lint does with --install.",
		strings.Join(names, ", "), more, at.UTC().Format("2006-01-02"), idle)
}

func missing(paths []string) []string {
	var out []string
	for _, p := range paths {
		if _, err := os.Lstat(p); os.IsNotExist(err) {
			out = append(out, p)
		}
	}
	return out
}

func relOrAbs(base, p string) string {
	if within(base, p) {
		if rel := relOf(base, p); rel != "." {
			return rel
		}
	}
	return p
}

func compactStrings(s []string) []string {
	out := s[:0]
	for i, v := range s {
		if i == 0 || v != s[i-1] {
			out = append(out, v)
		}
	}
	return out
}

// writeFileAtomic replaces path with data: a reader sees the old file or the
// new one, never a torn one.
func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	_, err = tmp.Write(data)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
	}
	return err
}
