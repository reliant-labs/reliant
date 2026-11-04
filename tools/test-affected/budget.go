// Copyright (c) 2025 Reliant Labs
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The per-test time budget of the merge tier.
//
// A Go test that takes more than ~2s of an idle machine under -short is a
// slow test: it calls testkit.Slow and runs in the release gate
// (dev-docs/merge-and-release-gates.md, "Slow tests"). Nothing enforced that,
// so an ungated slow test merged silently and taxed every later PR whose
// change reached its package. With a budget, the Go step runs `go test -json`
// and this filter reads the events: it prints what plain `go test` would (the
// package results, and a failing test's output, never a passing one's), and
// times every top-level test.
//
// A test the diff ADDS or EDITS (changedTests) that runs over the budget
// fails the step, naming testkit.Slow. Any other test over it is reported, not
// failed: the PR did not write it, and failing an unrelated change for it
// would be the merge tier failing the wrong author. The report names it so it
// can be gated.
//
// The budget scales with the machine's load like vitest's timeout
// (frontends/web/vite.config.ts): the same test takes several times longer at
// a load of 5x the core count, and that is the host, not the test.

// budgetBase is the per-test budget on an idle machine: five times the 2s
// slow-test line, so a test that sits near the line is not judged by noise.
const budgetBase = 10 * time.Second

// defaultTestBudget is budgetBase scaled 1x..6x by the one-minute load per
// core, as vitest's timeout is.
func defaultTestBudget() time.Duration {
	return budgetBase * time.Duration(loadFactor(loadAverage(), runtime.NumCPU()))
}

func loadFactor(load float64, cpus int) int {
	if cpus < 1 {
		cpus = 1
	}
	f := int(math.Ceil(load / float64(cpus)))
	return min(6, max(1, f))
}

// loadAverage is the one-minute load average (0 where /proc/loadavg is not).
func loadAverage() float64 {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0
	}
	f, err := strconv.ParseFloat(strings.Fields(string(b) + " 0")[0], 64)
	if err != nil {
		return 0
	}
	return f
}

// testEvent is the part of a test2json event (go doc test2json) the filter reads.
type testEvent struct {
	Action  string
	Package string
	Test    string
	Elapsed float64
	Output  string
}

// overBudget is one top-level test that ran longer than the budget.
type overBudget struct {
	Package, Test string
	Elapsed       time.Duration
	Owned         bool // the diff adds or edits it
}

// budgetFilter is an io.Writer for `go test -json` output.
type budgetFilter struct {
	budget time.Duration
	// owned holds "<import path>\x00<TestName>" for the tests the diff adds
	// or edits.
	owned map[string]bool
	out   io.Writer

	mu      sync.Mutex
	partial []byte
	held    map[string][]string // test output, printed only if the test fails
	over    []overBudget
}

func newBudgetFilter(budget time.Duration, owned map[string]bool, out io.Writer) *budgetFilter {
	return &budgetFilter{budget: budget, owned: owned, out: out, held: map[string][]string{}}
}

func (f *budgetFilter) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.partial = append(f.partial, p...)
	for {
		i := bytes.IndexByte(f.partial, '\n')
		if i < 0 {
			break
		}
		f.line(f.partial[:i+1])
		f.partial = f.partial[i+1:]
	}
	return len(p), nil
}

// Close flushes a final line with no newline, and the output of any test
// that never finished.
func (f *budgetFilter) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.partial) > 0 {
		f.line(append(f.partial, '\n'))
		f.partial = nil
	}
	f.flush("")
	return nil
}

// flush prints and drops the held output of the tests whose key has prefix.
func (f *budgetFilter) flush(prefix string) {
	keys := make([]string, 0, len(f.held))
	for k := range f.held {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, o := range f.held[k] {
			_, _ = io.WriteString(f.out, o)
		}
		delete(f.held, k)
	}
}

func (f *budgetFilter) line(b []byte) {
	var ev testEvent
	if len(b) == 0 || b[0] != '{' || json.Unmarshal(b, &ev) != nil {
		_, _ = f.out.Write(b) // not an event (a go command message): as is
		return
	}
	key := ev.Package + "\x00" + ev.Test
	switch ev.Action {
	case "output", "build-output":
		switch {
		case ev.Test == "":
			// Package level: the ok/FAIL lines, build errors, a panic.
			if ev.Output != "PASS\n" {
				_, _ = io.WriteString(f.out, ev.Output)
			}
		case strings.HasPrefix(ev.Output, "=== "):
			// RUN/PAUSE/CONT/NAME: -json implies -v; plain go test prints none.
		default:
			f.held[key] = append(f.held[key], ev.Output)
		}
	case "fail":
		if ev.Test != "" {
			fmt.Fprintf(f.out, "--- FAIL: %s (package %s)\n", ev.Test, ev.Package)
		}
		if ev.Test == "" {
			// A package that failed with tests still running (a panic, a
			// timeout) never reports them: their output is the evidence.
			f.flush(ev.Package + "\x00")
		}
		for _, o := range f.held[key] {
			_, _ = io.WriteString(f.out, o)
		}
		delete(f.held, key)
		f.time(ev)
	case "pass", "skip":
		delete(f.held, key)
		f.time(ev)
	}
}

func (f *budgetFilter) time(ev testEvent) {
	if ev.Test == "" || strings.Contains(ev.Test, "/") || f.budget <= 0 {
		return
	}
	d := time.Duration(ev.Elapsed * float64(time.Second))
	if d > f.budget {
		f.over = append(f.over, overBudget{Package: ev.Package, Test: ev.Test, Elapsed: d, Owned: f.owned[ev.Package+"\x00"+ev.Test]})
	}
}

// verdict prints the over-budget report and reports whether any test the
// diff wrote is over budget.
func (f *budgetFilter) verdict(w io.Writer) (failed bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.over) == 0 {
		return false
	}
	sort.Slice(f.over, func(i, j int) bool { return f.over[i].Elapsed > f.over[j].Elapsed })
	var owned, other []overBudget
	for _, o := range f.over {
		if o.Owned {
			owned = append(owned, o)
		} else {
			other = append(other, o)
		}
	}
	if len(owned) > 0 {
		fmt.Fprintf(w, "\nFAIL: over the merge gate's per-test budget (%s at this load):\n", f.budget)
		for _, o := range owned {
			fmt.Fprintf(w, "  %s %s  %s  (added or edited by this change)\n", o.Package, o.Test, o.Elapsed.Round(100*time.Millisecond))
		}
		fmt.Fprintln(w, "A test this slow under -short is a slow test: make it fast (inject the timeout or backoff it waits\n"+
			"out), or call testkit.Slow(t, \"<what makes it slow>\") first so it runs in the release gate\n"+
			"(dev-docs/merge-and-release-gates.md, \"Slow tests\").")
	}
	if len(other) > 0 {
		fmt.Fprintf(w, "\nover-budget (not failed: this change did not write them; gate them with testkit.Slow), budget %s:\n", f.budget)
		for _, o := range other {
			fmt.Fprintf(w, "  over-budget %s %s  %s\n", o.Package, o.Test, o.Elapsed.Round(100*time.Millisecond))
		}
	}
	return len(owned) > 0
}

// ownedTests keys the diff's added or edited tests for budgetFilter.owned.
func ownedTests(module string, pkgs []changedPkg) map[string]bool {
	out := map[string]bool{}
	for _, p := range pkgs {
		imp := module
		if d := strings.TrimPrefix(p.Dir, "./"); d != "." && d != "" {
			imp = module + "/" + d
		}
		for _, t := range p.Tests {
			out[imp+"\x00"+t] = true
		}
	}
	return out
}
