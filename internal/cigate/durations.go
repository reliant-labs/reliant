package cigate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// DurationHistory is how long each check took the last time its command ran in
// this repository (any worktree, any tree), so the next run can start the
// checks on the longest path first (executionOrder).
//
// WHY. Host slots are granted in execution order, and without history that is
// plan order: forge's static checks, then the project's extras, then the test
// lanes. On control-plane that queued its two longest checks — the unit suite
// (~15 min) and the integration suite (~22 min under load) — behind ~25 slot-
// minutes of shorter checks, so the gate's wall time was the queue plus the
// longest check instead of roughly the longest check. Starting the longest
// path first is the classic list-scheduling answer, and the only input it
// needs is a duration per check.
//
// A duration is a scheduling hint, not a verdict: it is recorded whatever the
// tree's state, it is never trusted for correctness, and a stale one only
// reorders when checks start.
type DurationHistory struct {
	path string
	last map[string]time.Duration
}

const durationsFile = "durations.json"

// OpenDurationHistory reads root's history from dir ("" is the verdict
// cache's default directory). It returns nil — plan order — when root is not
// a git work tree or there is no cache directory.
func OpenDurationHistory(ctx context.Context, root, dir string) *DurationHistory {
	repoDir, why := repoCacheDir(ctx, root, dir)
	if why != "" {
		return nil
	}
	return loadDurationHistory(filepath.Join(repoDir, durationsFile))
}

// loadDurationHistory reads path; a missing or unreadable file is an empty
// history.
func loadDurationHistory(path string) *DurationHistory {
	h := &DurationHistory{path: path, last: map[string]time.Duration{}}
	b, err := os.ReadFile(path)
	if err != nil {
		return h
	}
	var seconds map[string]float64
	if json.Unmarshal(b, &seconds) != nil {
		return h
	}
	for name, s := range seconds {
		if s > 0 {
			h.last[name] = time.Duration(s * float64(time.Second))
		}
	}
	return h
}

// Expected is the recorded duration of every check that has one; nil for a
// nil history.
func (h *DurationHistory) Expected() map[string]time.Duration {
	if h == nil || len(h.last) == 0 {
		return nil
	}
	out := make(map[string]time.Duration, len(h.last))
	for k, v := range h.last {
		out[k] = v
	}
	return out
}

// Observe records how long each check in rep took when its command ran to a
// verdict. A cached, skipped or N/A row says nothing about the check's
// duration, so it leaves the recorded one alone.
func (h *DurationHistory) Observe(rep Report) {
	if h == nil {
		return
	}
	for _, res := range rep.Results {
		if res.Cached != "" || res.Duration <= 0 || (res.Status != Pass && res.Status != Fail) {
			continue
		}
		h.last[res.Name] = res.Duration
	}
}

// Save writes the history atomically. Two gates finishing at once each write
// a whole file and the last one wins, which is all a hint needs.
func (h *DurationHistory) Save() error {
	if h == nil || len(h.last) == 0 {
		return nil
	}
	names := make([]string, 0, len(h.last))
	for k := range h.last {
		names = append(names, k)
	}
	sort.Strings(names)
	seconds := make(map[string]float64, len(names))
	for _, k := range names {
		seconds[k] = h.last[k].Round(100 * time.Millisecond).Seconds()
	}
	b, err := json.MarshalIndent(seconds, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(h.path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(h.path), durationsFile+".*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), h.path)
}
