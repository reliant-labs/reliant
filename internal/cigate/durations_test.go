package cigate

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// orderOf is the check names in execution order.
func orderOf(checks []Check, expected map[string]time.Duration) []string {
	_, pos := executionOrder(checks, expected)
	out := make([]string, len(checks))
	for i, p := range pos {
		out[p] = checks[i].Name
	}
	return out
}

// With recorded durations, the read phase starts longest first; without, plan
// order stands exactly as before.
func TestExecutionOrder_LongestFirst(t *testing.T) {
	checks := []Check{check("a", "a"), check("b", "b"), check("c", "c")}
	if got := fmt.Sprint(orderOf(checks, nil)); got != "[a b c]" {
		t.Errorf("no history: order %s, want plan order [a b c]", got)
	}
	got := fmt.Sprint(orderOf(checks, map[string]time.Duration{"a": time.Minute, "b": 10 * time.Minute, "c": 5 * time.Minute}))
	if got != "[b c a]" {
		t.Errorf("order %s, want [b c a] (longest first)", got)
	}
	// A check with no record counts as zero: after every recorded one, in
	// plan order among themselves.
	got = fmt.Sprint(orderOf(checks, map[string]time.Duration{"c": time.Second}))
	if got != "[c a b]" {
		t.Errorf("order %s, want [c a b]", got)
	}
}

// A short check that a long one waits on is on the long path: it goes first,
// ahead of a mid-length check that nothing waits on. This is control-plane's
// kcl-render (2m) -> unit-tests (15m), against build-web (7m).
func TestExecutionOrder_PathIncludesWhatWaits(t *testing.T) {
	render, units, web := check("render", "render"), check("units", "units"), check("web", "web")
	units.After = []string{"render"}
	checks := []Check{web, render, units}
	got := fmt.Sprint(orderOf(checks, map[string]time.Duration{
		"render": 2 * time.Minute, "units": 15 * time.Minute, "web": 7 * time.Minute,
	}))
	if got != "[render units web]" {
		t.Errorf("order %s, want [render units web]: render's path is 17m, web's 7m", got)
	}
}

// History never moves a reader ahead of a tree-mutating check.
func TestExecutionOrder_MutatorsStayFirst(t *testing.T) {
	mut, long := check("mut", "mut"), check("long", "long")
	mut.MutatesTree = true
	got := fmt.Sprint(orderOf([]Check{long, mut}, map[string]time.Duration{"long": time.Hour, "mut": time.Second}))
	if got != "[mut long]" {
		t.Errorf("order %s, want [mut long]", got)
	}
}

// On a one-slot host the heavy check with the longest recorded duration gets
// the slot first, though it is planned last.
func TestScheduler_LongestHeavyCheckGetsTheFirstSlot(t *testing.T) {
	r := newRecorder()
	slots := &fakeSlots{sem: make(chan struct{}, 1)}
	var checks []Check
	for _, n := range []string{"short", "mid", "long"} {
		c := check(n, n)
		c.Heavy = true
		checks = append(checks, c)
	}
	rep := runChecks(t, checks, Options{Exec: r.run, Jobs: 8, Slot: slots.take, Expected: map[string]time.Duration{
		"short": time.Second, "mid": time.Minute, "long": time.Hour,
	}})
	if rep.Failed() {
		t.Fatalf("rows: %+v", rep.Results)
	}
	if !r.before("long", "mid") || !r.before("mid", "short") {
		t.Errorf("start order %v: want long, mid, short", r.start)
	}
	// Rows keep plan order whatever ran first.
	if rep.Results[0].Name != "short" || rep.Results[2].Name != "long" {
		t.Errorf("rows %+v: the table keeps plan order", rep.Results)
	}
}

// Only a duration a command earned is recorded, and the history survives a
// save and reload.
func TestDurationHistory_ObserveSaveLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo", durationsFile)
	h := loadDurationHistory(path)
	if h.Expected() != nil {
		t.Fatalf("a missing file is an empty history, got %v", h.Expected())
	}
	h.last["kept"] = 3 * time.Second
	h.Observe(Report{Results: []Result{
		{Name: "pass", Status: Pass, Duration: 90 * time.Second},
		{Name: "fail", Status: Fail, Duration: 4 * time.Second},
		{Name: "cached", Status: Pass, Duration: 0, Cached: "abc123"},
		{Name: "kept", Status: Pass, Cached: "abc123"},
		{Name: "skipped", Status: Skip, Duration: time.Second},
		{Name: "na", Status: NA},
	}})
	if err := h.Save(); err != nil {
		t.Fatal(err)
	}
	got := loadDurationHistory(path).Expected()
	want := map[string]time.Duration{"pass": 90 * time.Second, "fail": 4 * time.Second, "kept": 3 * time.Second}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("reloaded %v, want %v", got, want)
	}
	var nilHistory *DurationHistory
	nilHistory.Observe(Report{Results: []Result{{Name: "x", Status: Pass, Duration: time.Second}}})
	if err := nilHistory.Save(); err != nil || nilHistory.Expected() != nil {
		t.Errorf("a nil history is a no-op")
	}
}
