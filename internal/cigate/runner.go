// Package cigate is the local CI gate: the one definition of what "CI passes"
// means for a forge project, runnable on a laptop and from a workflow.
//
// A scaffolded workflow is a thin wrapper that prepares the environment
// (checkout, toolchains, caches) and calls `task ci:checks`, `task test` and
// `task test:integration`. `task ci` runs all three locally, so "CI did not
// run" never has to mean "unverified". This package is what makes the first and
// last of those honest, with four rules:
//
//  1. Every check runs, even after one fails, so one pass shows every failure.
//     The verdict is a summary table, and the gate exits non-zero if any row
//     failed.
//  2. A check that could not run (a missing tool or prerequisite) is SKIPPED,
//     never PASS, and fails the gate unless CI_SKIP names it. A silent skip is
//     false confidence. Rows a project's own configuration or kind makes moot
//     are N/A, which is not a skip and does not fail the gate.
//  3. Tools run at the versions CI pins (tools.go), not whatever is on PATH.
//  4. Under GitHub Actions the table is appended to $GITHUB_STEP_SUMMARY.
//
// Checks run CONCURRENTLY (scheduler below), because a gate whose ~25 steps
// run one after another takes the sum of them — measured at a p50 of 35 min
// and up to 3 h on a shared machine. Three things bound the concurrency:
//
//   - `after` dependencies: a check starts once the checks it names have
//     finished (whatever their verdict — rule 1 still holds);
//   - tree mutation: a check that writes tracked files (a formatter, an extra
//     declared `mutates_tree`) runs alone, before every check that only reads
//     the tree. verify-generated is not one: it regenerates a scratch copy;
//   - host slots: a heavy check (a whole-module build, test run or render)
//     holds one of the machine's heavy slots (pkg/hostslot) while it runs, so
//     the gate never adds more heavy jobs than the host has room for, and a
//     per-gate job cap bounds the rest.
//
// Each check's output goes to its own log file; the console gets one start and
// one finish line per check, a failing check's last lines, and the table.
package cigate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// Status is one row's verdict.
type Status string

const (
	// Pass is a check that ran and succeeded.
	Pass Status = "PASS"
	// Fail is a check that ran and failed.
	Fail Status = "FAIL"
	// Skip is a check that did not run. It fails the gate unless Allowed.
	Skip Status = "SKIPPED"
	// NA is a check this project has nothing for. It is not a skip.
	NA Status = "N/A"
)

// Lane is a group of checks a CI job runs together.
type Lane string

const (
	// LaneChecks is the static and build lane: the `checks` CI job.
	LaneChecks Lane = "checks"
	// LaneTest is the unit-test lane: `task test`, the `test` CI job.
	LaneTest Lane = "test"
	// LaneIntegration is `task test:integration`, the `integration-test` job.
	LaneIntegration Lane = "integration"
)

// SkipEnv names the checks a caller has decided not to run, comma-separated.
const SkipEnv = "CI_SKIP"

// OnlyEnv names the checks (or groups) a caller wants to run, comma-separated;
// everything else is left out of the run and the table.
const OnlyEnv = "CI_ONLY"

// Check is one row of the gate.
type Check struct {
	Name string
	Lane Lane
	// Group is the label CI_ONLY can select by, besides the name.
	Group string
	// After names checks that must finish (with any verdict) before this one
	// starts. Selecting this check with CI_ONLY selects them too.
	After []string
	// Heavy makes the check hold a host-wide heavy slot while it runs.
	Heavy bool
	// MutatesTree marks a check that writes tracked files: it runs alone,
	// ahead of every check that only reads the tree.
	MutatesTree bool
	// NoCache keeps the verdict cache from ever answering for this check:
	// its verdict depends on state outside the tree.
	NoCache bool

	// NotApplicable, when set, reports the row as N/A with this reason and
	// runs nothing.
	NotApplicable string
	// Done returns a non-empty note when the check's work is already done
	// (dependencies installed); the row is PASS and nothing runs.
	Done func() string
	// Prereq returns a non-empty reason when the check cannot run here. The
	// row is then SKIPPED, which fails the gate.
	Prereq func() string
	// Requires lists binaries that must resolve on the check's own PATH
	// (pinned tool dirs and ci.gate.path included); a missing one SKIPs it.
	Requires []string
	// Probe is a command that must succeed before the check runs; when it
	// fails the row is SKIPPED with ProbeReason.
	Probe       []string
	ProbeReason string
	// Tools are pinned tools the command needs. Each is resolved to its
	// pinned version before the command runs, and its directory is put first
	// on the command's PATH.
	Tools []Tool

	Argv []string
	// SlotArgv, when set, runs instead of Argv while the check holds a host
	// heavy slot of its own. A tool with its own machine-global mutex drops
	// it here, because the slot already bounds what the mutex protected
	// (golangci-lint: --allow-parallel-runners instead of
	// --allow-serial-runners). Without a slot of its own — the limit is off,
	// or the gate itself runs inside a parent's slot — Argv runs.
	SlotArgv []string
	// Dir is project-relative; empty is the project root.
	Dir string
	Env []string
	// SkipExitCode, when non-zero, is the exit status by which the command
	// says it could not run here: the row is SKIPPED with its last output
	// line as the reason.
	SkipExitCode int
	// Note annotates the row when the command runs: how it was narrowed
	// (affected-package mode) or anything else the reader needs.
	Note string

	// OnFail runs after the command failed, to undo what it left behind.
	OnFail func(out io.Writer)
}

// Result is the outcome of one Check.
type Result struct {
	Name     string
	Lane     Lane
	Status   Status
	Duration time.Duration
	Note     string
	// Allowed marks a SKIPPED row the caller asked for (CI_SKIP), which does
	// not fail the gate.
	Allowed bool
	// Cached is the short commit a reused PASS was recorded at; "" when the
	// check ran.
	Cached string
	// Log is the check's output file, project-relative when under the root.
	Log string
	// Tail is the end of a failed command's output, for the job summary.
	Tail string
}

// Report is every row of one gate run.
type Report struct {
	Lane    string
	Results []Result
	// Head is the commit the gate judged ("" outside git); Dirty says the
	// working tree had uncommitted changes on top of it.
	Head  string
	Dirty bool
	// Wall is the gate's elapsed time.
	Wall time.Duration
}

// Failed reports whether the gate is red: any FAIL, or any SKIPPED the caller
// did not allow.
func (r Report) Failed() bool {
	for _, res := range r.Results {
		if res.Status == Fail || (res.Status == Skip && !res.Allowed) {
			return true
		}
	}
	return false
}

// Tally is the rows of a report counted by outcome. Skipped counts only the
// SKIPPED rows that fail the gate; Allowed counts the ones CI_SKIP named.
type Tally struct{ Passed, Cached, Failed, Skipped, Allowed, NA int }

// Counts tallies the rows by outcome.
func (r Report) Counts() Tally {
	var t Tally
	for _, res := range r.Results {
		switch {
		case res.Status == Pass:
			t.Passed++
			if res.Cached != "" {
				t.Cached++
			}
		case res.Status == Fail:
			t.Failed++
		case res.Status == Skip && res.Allowed:
			t.Allowed++
		case res.Status == Skip:
			t.Skipped++
		case res.Status == NA:
			t.NA++
		}
	}
	return t
}

func (r Report) verdict() string {
	t := r.Counts()
	passed := fmt.Sprintf("%d passed", t.Passed)
	if t.Cached > 0 {
		passed += fmt.Sprintf(" (%d cached)", t.Cached)
	}
	parts := []string{passed}
	if t.Failed > 0 {
		parts = append(parts, fmt.Sprintf("%d FAILED", t.Failed))
	}
	if t.Skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d SKIPPED (not allowed)", t.Skipped))
	}
	if t.Allowed > 0 {
		parts = append(parts, fmt.Sprintf("%d skipped by %s", t.Allowed, SkipEnv))
	}
	if t.NA > 0 {
		parts = append(parts, fmt.Sprintf("%d n/a", t.NA))
	}
	if r.Wall > 0 {
		parts = append(parts, "in "+r.Wall.Round(time.Second).String())
	}
	return strings.Join(parts, ", ")
}

func (r Report) headLine() string {
	if r.Head == "" {
		return ""
	}
	if r.Dirty {
		return "head: " + r.Head + " (+ uncommitted changes)"
	}
	return "head: " + r.Head
}

// RenderText writes the summary table.
func (r Report) RenderText(w io.Writer) {
	width, rw := len("CHECK"), len("RESULT")
	for _, res := range r.Results {
		width = max(width, len(res.Name))
		rw = max(rw, len(res.resultCell()))
	}
	fmt.Fprintf(w, "\nCI gate summary (%s)\n", r.Lane)
	fmt.Fprintf(w, "  %-*s  %-*s  %8s  %s\n", width, "CHECK", rw, "RESULT", "TIME", "NOTE")
	for _, res := range r.Results {
		fmt.Fprintf(w, "  %-*s  %-*s  %8s  %s\n", width, res.Name, rw, res.resultCell(), formatDuration(res), res.note())
	}
	verdict := "PASS"
	if r.Failed() {
		verdict = "FAIL"
	}
	fmt.Fprintf(w, "\n%s — %s\n", verdict, r.verdict())
	if h := r.headLine(); h != "" {
		fmt.Fprintln(w, h)
	}
}

// RenderMarkdown is the same table for $GITHUB_STEP_SUMMARY.
func (r Report) RenderMarkdown(w io.Writer) {
	icon := map[Status]string{Pass: "✅", Fail: "❌", Skip: "⏭️", NA: "➖"}
	fmt.Fprintf(w, "### CI gate — %s\n\n| Check | Result | Time | Note |\n|---|---|---|---|\n", r.Lane)
	for _, res := range r.Results {
		fmt.Fprintf(w, "| `%s` | %s %s | %s | %s |\n",
			res.Name, icon[res.Status], res.resultCell(), formatDuration(res), strings.ReplaceAll(res.note(), "|", "\\|"))
	}
	verdict := "✅ **PASS**"
	if r.Failed() {
		verdict = "❌ **FAIL**"
	}
	fmt.Fprintf(w, "\n%s — %s\n", verdict, r.verdict())
	if h := r.headLine(); h != "" {
		fmt.Fprintf(w, "\n%s\n", h)
	}
	for _, res := range r.Results {
		if res.Status == Fail && res.Tail != "" {
			fmt.Fprintf(w, "\n<details><summary><code>%s</code> output (tail)</summary>\n\n```\n%s\n```\n\n</details>\n", res.Name, res.Tail)
		}
	}
	fmt.Fprintln(w)
}

func (res Result) resultCell() string {
	if res.Cached != "" {
		return string(res.Status) + " (cached " + res.Cached + ")"
	}
	return string(res.Status)
}

func (res Result) note() string {
	note := res.Note
	if res.Status == Skip && res.Allowed {
		note += " (allowed by " + SkipEnv + ")"
	}
	if res.Status == Fail && res.Log != "" {
		if note != "" {
			note += "; "
		}
		note += "log: " + res.Log
	}
	return note
}

func formatDuration(res Result) string {
	if res.Status == Skip || res.Status == NA || res.Duration == 0 {
		return "-"
	}
	return res.Duration.Round(100 * time.Millisecond).String()
}

// ParseSkips splits a CI_SKIP (or CI_ONLY) value into names.
func ParseSkips(v string) []string {
	var out []string
	for _, n := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' }) {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// UnknownSkips returns the names in skips that no check carries. A typo in
// CI_SKIP would otherwise quietly skip nothing, or — worse — be read as the
// caller having accounted for a check they did not.
func UnknownSkips(skips []string, all []Check) []string {
	known := map[string]bool{}
	for _, c := range all {
		known[c.Name] = true
	}
	var bad []string
	for _, s := range skips {
		if !known[s] {
			bad = append(bad, s)
		}
	}
	sort.Strings(bad)
	return bad
}

// UnknownOnly returns the CI_ONLY names that are neither a check nor a group.
func UnknownOnly(only []string, all []Check) []string {
	known := map[string]bool{}
	for _, c := range all {
		known[c.Name] = true
		if c.Group != "" {
			known[c.Group] = true
		}
	}
	var bad []string
	for _, s := range only {
		if !known[s] {
			bad = append(bad, s)
		}
	}
	sort.Strings(bad)
	return bad
}

// Only keeps the checks CI_ONLY names — by name or by group — plus every
// check they depend on (`after`), transitively, in plan order. An empty only
// keeps everything.
func Only(checks []Check, only []string) []Check {
	if len(only) == 0 {
		return checks
	}
	want := map[string]bool{}
	for _, o := range only {
		want[o] = true
	}
	byName := map[string]Check{}
	for _, c := range checks {
		byName[c.Name] = c
	}
	keep := map[string]bool{}
	var pull func(name string)
	pull = func(name string) {
		c, ok := byName[name]
		if !ok || keep[name] {
			return
		}
		keep[name] = true
		for _, a := range c.After {
			pull(a)
		}
	}
	for _, c := range checks {
		if want[c.Name] || (c.Group != "" && want[c.Group]) {
			pull(c.Name)
		}
	}
	var out []Check
	for _, c := range checks {
		if keep[c.Name] {
			out = append(out, c)
		}
	}
	return out
}

// Options configure a Run.
type Options struct {
	// Out receives the progress lines and the checks' failure tails. Defaults
	// to os.Stdout.
	Out io.Writer
	// Skip names the checks the caller chose not to run.
	Skip []string
	// Resolver pins tools. Nil means checks that declare Tools cannot run.
	Resolver *Resolver
	// Exec runs one command. Nil uses os/exec; tests substitute.
	Exec ExecFunc
	// Root is the project root.
	Root string
	// Groups wraps each check's output in GitHub Actions ::group:: lines,
	// printed whole when the check finishes so concurrent checks never
	// interleave.
	Groups bool
	// Env is the base environment; nil is os.Environ().
	Env []string
	// Path is put first on every check's PATH, ahead of the pinned tools'
	// directories: the project's own declared tool directories
	// (ci.gate.path), which it chose over forge's pins on purpose.
	Path []string
	// Jobs caps how many checks run at once. Zero is DefaultJobs().
	Jobs int
	// Slot takes a host heavy slot for a Heavy check. Nil runs heavy checks
	// without one.
	Slot SlotFunc
	// Verdicts answers for checks whose PASS on this exact tree is already
	// recorded, and records new ones. Nil disables the cache.
	Verdicts Verdicts
	// LogDir receives one <check>.log per check that runs. Empty keeps the
	// output in memory only (tests).
	LogDir string
	// Clock stamps durations; nil is time.Now (tests substitute).
	Clock func() time.Time
	// Expected is how long each check took when it last ran
	// (DurationHistory). The checks that only read the tree start longest
	// path first by it; nil keeps plan order.
	Expected map[string]time.Duration
}

// ExecFunc runs argv in dir with env, writing combined output to out.
type ExecFunc func(ctx context.Context, dir string, env []string, argv []string, out io.Writer) error

// SlotFunc takes one host-wide heavy slot, waiting while every slot is held.
// An error means the gate was interrupted while waiting.
type SlotFunc func(ctx context.Context) (HeldSlot, error)

// HeldSlot is one host slot as the scheduler uses it (*hostslot.Slot is one).
type HeldSlot interface {
	// Env is what the check's command inherits, so the heavy commands it
	// runs in turn (`task test`'s `forge slot run`, verify-generated's
	// `forge generate`) run inside this slot instead of queueing for another.
	Env() []string
	// SetLabel names the check holding the slot, for waiters to see.
	SetLabel(label string)
	Release() error
}

// Verdicts is the verdict cache as the scheduler sees it.
type Verdicts interface {
	// Lookup reports a recorded PASS for c on the tree being judged, and the
	// short commit it was recorded at.
	Lookup(c Check) (commit string, ok bool)
	// Record remembers a PASS c earned in this run.
	Record(c Check)
}

// DefaultJobs is the per-gate cap on concurrently running light checks: half
// the CPUs, between 2 and 8. Heavy checks are bounded by host slots instead.
func DefaultJobs() int {
	return min(max(runtime.NumCPU()/2, 2), 8)
}

// Run executes checks and returns the report, rows in the order given. It
// never stops early: a failing check is a row, not an abort.
func Run(ctx context.Context, lane string, checks []Check, opt Options) Report {
	s := newScheduler(ctx, checks, opt)
	start := s.now()
	s.run()
	rep := Report{Lane: lane, Results: s.results}
	rep.Wall = s.now().Sub(start)
	return rep
}

// ---- scheduler ----------------------------------------------------------------

type scheduler struct {
	ctx    context.Context
	checks []Check
	opt    Options
	out    *lockedWriter
	exec   ExecFunc

	results []Result
	deps    [][]int // indices of each check's selected `after` dependencies
	pos     []int   // position in the execution order
	jobs    chan struct{}

	mu       sync.Mutex
	cond     *sync.Cond
	done     []bool
	active   int  // started (past the start conditions) and not finished
	mutating bool // a MutatesTree check is active

	heavy *heavyQueue
}

func newScheduler(ctx context.Context, checks []Check, opt Options) *scheduler {
	out := opt.Out
	if out == nil {
		out = os.Stdout
	}
	jobs := opt.Jobs
	if jobs <= 0 {
		jobs = DefaultJobs()
	}
	s := &scheduler{
		ctx:     ctx,
		checks:  checks,
		opt:     opt,
		out:     &lockedWriter{w: out},
		exec:    opt.Exec,
		results: make([]Result, len(checks)),
		done:    make([]bool, len(checks)),
		jobs:    make(chan struct{}, jobs),
	}
	if s.exec == nil {
		s.exec = osExec
	}
	s.cond = sync.NewCond(&s.mu)
	s.deps, s.pos = executionOrder(checks, opt.Expected)
	return s
}

func (s *scheduler) now() time.Time {
	if s.opt.Clock != nil {
		return s.opt.Clock()
	}
	return time.Now()
}

// executionOrder resolves `after` names to indices and fixes a total order the
// start conditions are expressed in: a topological order of the dependencies
// in which every tree-mutating check, and everything it depends on, comes
// before every check that only reads the tree. Every wait the scheduler
// performs is on a check EARLIER in this order, so no set of waits can form a
// cycle.
//
// Among the checks that only read the tree, the one with the longest path
// still to run goes first: its own expected duration plus the longest chain of
// checks that wait on it (expected, from DurationHistory). That is the order
// host slots are granted in, so the gate's longest check starts as soon as the
// tree is ready instead of behind every shorter one planned before it. With no
// history every path is zero long and plan order stands. Ties keep plan order.
func executionOrder(checks []Check, expected map[string]time.Duration) (deps [][]int, pos []int) {
	idx := map[string]int{}
	for i, c := range checks {
		if _, dup := idx[c.Name]; !dup {
			idx[c.Name] = i
		}
	}
	deps = make([][]int, len(checks))
	for i, c := range checks {
		for _, a := range c.After {
			if j, ok := idx[a]; ok && j != i {
				deps[i] = append(deps[i], j)
			}
		}
	}
	// Phase 0: mutating checks and their transitive dependencies.
	phase0 := make([]bool, len(checks))
	var mark func(i int)
	mark = func(i int) {
		if phase0[i] {
			return
		}
		phase0[i] = true
		for _, j := range deps[i] {
			mark(j)
		}
	}
	for i, c := range checks {
		if c.MutatesTree {
			mark(i)
		}
	}
	// Kahn's algorithm, picking the lowest (phase, plan index) available.
	indeg := make([]int, len(checks))
	users := make([][]int, len(checks))
	for i := range checks {
		for _, j := range deps[i] {
			indeg[i]++
			users[j] = append(users[j], i)
		}
	}
	path := criticalPaths(checks, users, expected)
	// before reports whether i goes ahead of j when both are ready: mutating
	// phase first, then (outside it) the longer path, then plan order.
	before := func(i, j int) bool {
		if phase0[i] != phase0[j] {
			return phase0[i]
		}
		if !phase0[i] && path[i] != path[j] {
			return path[i] > path[j]
		}
		return i < j
	}
	pos = make([]int, len(checks))
	placed := make([]bool, len(checks))
	for n := 0; n < len(checks); n++ {
		best := -1
		for i := range checks {
			if !placed[i] && indeg[i] == 0 && (best < 0 || before(i, best)) {
				best = i
			}
		}
		if best < 0 {
			// A cycle (the planner rejects these): place the rest in plan
			// order and drop their dependencies, rather than hang.
			for i := range checks {
				if !placed[i] {
					placed[i], pos[i], deps[i] = true, n, nil
					n++
				}
			}
			break
		}
		placed[best], pos[best] = true, n
		for _, u := range users[best] {
			indeg[u]--
		}
	}
	return deps, pos
}

// criticalPaths is, for each check, its expected duration plus the longest
// expected chain of checks that wait on it (users[i] lists them). A check with
// no recorded duration counts as zero. A dependency cycle, which the planner
// rejects before this runs, is cut rather than followed.
func criticalPaths(checks []Check, users [][]int, expected map[string]time.Duration) []time.Duration {
	path := make([]time.Duration, len(checks))
	if len(expected) == 0 {
		return path
	}
	const (
		unseen = iota
		visiting
		visited
	)
	state := make([]int, len(checks))
	var visit func(i int) time.Duration
	visit = func(i int) time.Duration {
		switch state[i] {
		case visiting:
			return 0
		case visited:
			return path[i]
		}
		state[i] = visiting
		var longest time.Duration
		for _, u := range users[i] {
			longest = max(longest, visit(u))
		}
		path[i] = expected[checks[i].Name] + longest
		state[i] = visited
		return path[i]
	}
	for i := range checks {
		visit(i)
	}
	return path
}

func (s *scheduler) run() {
	skip := map[string]bool{}
	for _, n := range s.opt.Skip {
		skip[n] = true
	}
	var pending []int
	for i, c := range s.checks {
		res := Result{Name: c.Name, Lane: c.Lane}
		switch {
		case c.NotApplicable != "":
			res.Status, res.Note = NA, c.NotApplicable
		case skip[c.Name]:
			res.Status, res.Note, res.Allowed = Skip, "not run", true
		default:
			if commit, ok := s.cached(c); ok {
				res.Status, res.Cached, res.Note = Pass, commit, "this tree passed before"
				s.out.printf("✓ %s  PASS (cached %s)\n", c.Name, commit)
			} else {
				pending = append(pending, i)
				continue
			}
		}
		s.results[i] = res
		s.done[i] = true
	}
	if len(pending) == 0 {
		return
	}
	s.heavy = newHeavyQueue(s.ctx, s.opt.Slot)
	defer s.heavy.close()
	// Wake every waiter when the gate is interrupted, so each can record
	// itself as such instead of waiting for a dependency that never starts.
	stop := context.AfterFunc(s.ctx, func() {
		s.mu.Lock()
		s.cond.Broadcast()
		s.mu.Unlock()
	})
	defer stop()

	var wg sync.WaitGroup
	for _, i := range pending {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, started := s.runCheck(i)
			s.results[i] = res
			s.mu.Lock()
			s.done[i] = true
			if started {
				s.active--
				if s.checks[i].MutatesTree {
					s.mutating = false
				}
			}
			s.cond.Broadcast()
			s.mu.Unlock()
		}()
	}
	wg.Wait()
}

func (s *scheduler) cached(c Check) (string, bool) {
	if s.opt.Verdicts == nil || c.NoCache {
		return "", false
	}
	return s.opt.Verdicts.Lookup(c)
}

// startable is the start condition, evaluated under s.mu.
func (s *scheduler) startable(i int) bool {
	for _, j := range s.deps[i] {
		if !s.done[j] {
			return false
		}
	}
	if s.mutating {
		return false
	}
	if s.checks[i].MutatesTree {
		if s.active > 0 {
			return false
		}
		for j := range s.checks {
			if s.pos[j] < s.pos[i] && !s.done[j] {
				return false
			}
		}
		return true
	}
	for j, c := range s.checks {
		if c.MutatesTree && s.pos[j] < s.pos[i] && !s.done[j] {
			return false
		}
	}
	return true
}

// runCheck waits until check i may start, runs it, and reports whether it
// got past the start conditions (and so counts as active until it returns).
func (s *scheduler) runCheck(i int) (Result, bool) {
	c := s.checks[i]
	interrupted := Result{Name: c.Name, Lane: c.Lane, Status: Skip, Note: "gate interrupted"}

	s.mu.Lock()
	for !s.startable(i) && s.ctx.Err() == nil {
		s.cond.Wait()
	}
	if s.ctx.Err() != nil {
		s.mu.Unlock()
		return interrupted, false
	}
	s.active++
	if c.MutatesTree {
		s.mutating = true
	}
	s.mu.Unlock()

	// A heavy check is bounded by a host slot, a light one by the gate's job
	// cap — never both. Holding a job token while waiting for a slot could
	// deadlock: a light check's own child (`forge generate` run by an extra)
	// waits for a slot, while the slot's holder waits for that token.
	var slotEnv []string
	if c.Heavy && s.heavy.enabled() {
		slot, err := s.heavy.take(s.pos[i], "ci gate: "+c.Name)
		if err != nil {
			return interrupted, true
		}
		defer func() { _ = slot.Release() }()
		slotEnv = slot.Env()
	} else {
		select {
		case s.jobs <- struct{}{}:
		case <-s.ctx.Done():
			return interrupted, true
		}
		defer func() { <-s.jobs }()
	}

	res, ran := s.execute(c, slotEnv)
	// Only a PASS the command itself earned is worth remembering: a Done
	// short-circuit says nothing about this tree.
	if ran && res.Status == Pass && s.opt.Verdicts != nil && !c.NoCache {
		s.opt.Verdicts.Record(c)
	}
	return res, true
}

// execute runs one check that is cleared to start. ran reports whether its
// command ran (as opposed to a Done short-circuit or a skip).
func (s *scheduler) execute(c Check, slotEnv []string) (res Result, ran bool) {
	res = Result{Name: c.Name, Lane: c.Lane}
	if c.Done != nil {
		if note := c.Done(); note != "" {
			res.Status, res.Note = Pass, note
			s.out.printf("✓ %s  PASS (%s)\n", c.Name, note)
			return res, false
		}
	}
	skip := func(reason string) (Result, bool) {
		res.Status, res.Note = Skip, reason
		s.out.printf("⏭ %s  SKIPPED: %s\n", c.Name, reason)
		return res, false
	}
	if c.Prereq != nil {
		if reason := c.Prereq(); reason != "" {
			return skip(reason)
		}
	}

	env := s.opt.Env
	if env == nil {
		env = os.Environ()
	}
	var dirs []string
	for _, t := range c.Tools {
		if s.opt.Resolver == nil {
			return skip(t.Binary + " is not available: no tool resolver")
		}
		dir, err := s.opt.Resolver.Ensure(s.ctx, t)
		if err != nil {
			return skip(fmt.Sprintf("cannot provide %s %s: %v", t.Binary, t.Version, err))
		}
		if dir != "" {
			dirs = append(dirs, dir)
		}
	}
	env = withPathPrefix(env, append(append([]string(nil), s.opt.Path...), dirs...))
	env = append(append(append([]string(nil), env...), c.Env...), slotEnv...)
	for _, bin := range c.Requires {
		if _, err := lookPathIn(env, bin); err != nil {
			return skip(bin + " is not on PATH")
		}
	}

	dir := s.opt.Root
	if c.Dir != "" {
		dir = joinDir(s.opt.Root, c.Dir)
	}
	if len(c.Probe) > 0 {
		if err := s.exec(s.ctx, dir, env, c.Probe, io.Discard); err != nil {
			reason := c.ProbeReason
			if reason == "" {
				reason = "`" + strings.Join(c.Probe, " ") + "` failed"
			}
			return skip(reason)
		}
	}

	log, closeLog := s.openLog(c.Name)
	defer closeLog()
	res.Log = s.relLog(c.Name)
	tail := &tailBuffer{max: 6000}
	var w io.Writer = tail
	if log != nil {
		w = io.MultiWriter(log, tail)
	}
	var full *bytes.Buffer
	if s.opt.Groups && log == nil {
		full = &bytes.Buffer{}
		w = io.MultiWriter(w, full)
	}

	argv := c.Argv
	if len(slotEnv) > 0 && len(c.SlotArgv) > 0 {
		argv = c.SlotArgv
	}
	s.out.printf("▶ %s  %s\n", c.Name, strings.Join(argv, " "))
	start := s.now()
	err := s.exec(s.ctx, dir, env, argv, w)
	res.Duration = s.now().Sub(start)

	switch {
	case err == nil:
		res.Status, res.Note = Pass, c.Note
	case s.ctx.Err() != nil:
		res.Status, res.Note = Skip, "gate interrupted"
	case c.SkipExitCode != 0 && exitCode(err) == c.SkipExitCode:
		res.Status, res.Note = Skip, lastLine(tail)
		if res.Note == "" {
			res.Note = fmt.Sprintf("exit status %d: could not run here", c.SkipExitCode)
		}
	default:
		res.Status, res.Note, res.Tail = Fail, failNote(err), tail.lastLines(25)
		if c.Note != "" {
			res.Note += "; " + c.Note
		}
	}
	if res.Status == Fail && c.OnFail != nil {
		c.OnFail(s.out)
	}
	s.report(c, res, log, full)
	return res, true
}

// report prints a finished check: one line, then — so a reader sees what broke
// without opening the log — a failure's last lines, or under GitHub Actions
// the whole output in a collapsible group. Printed under the writer's lock as
// one block, so concurrent checks never interleave.
func (s *scheduler) report(c Check, res Result, log *os.File, full *bytes.Buffer) {
	var b strings.Builder
	mark := map[Status]string{Pass: "✓", Fail: "✗", Skip: "⏭", NA: "-"}[res.Status]
	fmt.Fprintf(&b, "%s %s  %s %s", mark, c.Name, res.Status, res.Duration.Round(100*time.Millisecond))
	if res.Note != "" {
		fmt.Fprintf(&b, " — %s", res.Note)
	}
	if res.Log != "" && res.Status != Pass {
		fmt.Fprintf(&b, " (log: %s)", res.Log)
	}
	b.WriteString("\n")
	switch {
	case s.opt.Groups:
		fmt.Fprintf(&b, "::group::%s (%s)\n", c.Name, res.Status)
		if log != nil {
			if data, err := os.ReadFile(log.Name()); err == nil {
				b.Write(data)
			}
		} else if full != nil {
			b.Write(full.Bytes())
		}
		b.WriteString("\n::endgroup::\n")
		if res.Status == Fail {
			fmt.Fprintf(&b, "::error title=ci gate: %s::%s\n", c.Name, res.Note)
		}
	case res.Status == Fail && res.Tail != "":
		for _, line := range strings.Split(res.Tail, "\n") {
			fmt.Fprintf(&b, "    | %s\n", line)
		}
	}
	s.out.write(b.String())
}

func (s *scheduler) openLog(name string) (*os.File, func()) {
	if s.opt.LogDir == "" {
		return nil, func() {}
	}
	if err := os.MkdirAll(s.opt.LogDir, 0o755); err != nil {
		return nil, func() {}
	}
	f, err := os.Create(filepath.Join(s.opt.LogDir, name+".log"))
	if err != nil {
		return nil, func() {}
	}
	return f, func() { _ = f.Close() }
}

func (s *scheduler) relLog(name string) string {
	if s.opt.LogDir == "" {
		return ""
	}
	p := filepath.Join(s.opt.LogDir, name+".log")
	if s.opt.Root != "" {
		if rel, err := filepath.Rel(s.opt.Root, p); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}
	return p
}

// heavyQueue hands out host heavy slots to the gate's heavy checks, one
// request at a time and lowest execution position first.
//
// One outstanding request per gate is deliberate: a gate that filed a ticket
// per ready check would put all of them ahead of another agent's single
// `forge generate`, which then waits for a whole round of this gate's checks.
// Taking a slot and only then choosing which check gets it also means the
// check that runs is the most urgent one at the moment a slot frees.
type heavyQueue struct {
	ctx  context.Context
	slot SlotFunc

	mu      sync.Mutex
	waiting []heavyReq
	failed  error // set once no slot is coming (the gate was interrupted)
	wake    chan struct{}
	closed  chan struct{}
	once    sync.Once
}

// grantSettle is how long a freshly taken slot waits for the other checks
// that became ready at the same moment to queue, before it picks the most
// urgent one.
const grantSettle = 20 * time.Millisecond

type heavyReq struct {
	pos   int
	label string
	grant chan heavyGrant
}

type heavyGrant struct {
	slot HeldSlot
	err  error
}

func newHeavyQueue(ctx context.Context, slot SlotFunc) *heavyQueue {
	q := &heavyQueue{ctx: ctx, slot: slot, wake: make(chan struct{}, 1), closed: make(chan struct{})}
	if slot != nil {
		go q.serve()
	}
	return q
}

func (q *heavyQueue) enabled() bool { return q != nil && q.slot != nil }

func (q *heavyQueue) take(pos int, label string) (HeldSlot, error) {
	req := heavyReq{pos: pos, label: label, grant: make(chan heavyGrant, 1)}
	q.mu.Lock()
	if q.failed != nil {
		q.mu.Unlock()
		return nil, q.failed
	}
	q.waiting = append(q.waiting, req)
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
	g := <-req.grant
	return g.slot, g.err
}

func (q *heavyQueue) serve() {
	for {
		q.mu.Lock()
		n := len(q.waiting)
		q.mu.Unlock()
		if n == 0 {
			select {
			case <-q.wake:
				continue
			case <-q.closed:
				return
			case <-q.ctx.Done():
				q.failAll(q.ctx.Err())
				return
			}
		}
		slot, err := q.slot(q.ctx)
		if err != nil {
			q.failAll(err)
			return
		}
		// Let the checks that became ready together finish asking. When the
		// tree-mutating phase ends, every reader wakes at once and races to
		// take(); a slot that is free right away would otherwise go to
		// whichever goroutine ran first rather than to the longest path.
		// Short against any check's run, and only paid once per slot.
		select {
		case <-time.After(grantSettle):
		case <-q.ctx.Done():
			_ = slot.Release()
			q.failAll(q.ctx.Err())
			return
		}
		q.mu.Lock()
		best := 0
		for k := range q.waiting {
			if q.waiting[k].pos < q.waiting[best].pos {
				best = k
			}
		}
		req := q.waiting[best]
		q.waiting = append(q.waiting[:best], q.waiting[best+1:]...)
		q.mu.Unlock()
		slot.SetLabel(req.label)
		req.grant <- heavyGrant{slot: slot}
	}
}

// failAll answers every waiter with err, and every later request too: once
// the gate is interrupted no slot is coming.
func (q *heavyQueue) failAll(err error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, r := range q.waiting {
		r.grant <- heavyGrant{err: err}
	}
	q.waiting = nil
	q.failed = err
}

func (q *heavyQueue) close() { q.once.Do(func() { close(q.closed) }) }

// lockedWriter serializes the scheduler's console output.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func (l *lockedWriter) write(s string) { _, _ = l.Write([]byte(s)) }

func (l *lockedWriter) printf(format string, a ...any) { l.write(fmt.Sprintf(format, a...)) }

func failNote(err error) string {
	var ee *exec.ExitError
	if asExit(err, &ee) {
		return fmt.Sprintf("exit status %d", ee.ExitCode())
	}
	return err.Error()
}

func exitCode(err error) int {
	var ee *exec.ExitError
	if asExit(err, &ee) {
		return ee.ExitCode()
	}
	var coded interface{ ExitCode() int }
	if errors.As(err, &coded) {
		return coded.ExitCode()
	}
	return -1
}

func lastLine(t *tailBuffer) string {
	lines := strings.Split(strings.TrimRight(t.buf.String(), "\n"), "\n")
	for k := len(lines) - 1; k >= 0; k-- {
		if l := strings.TrimSpace(lines[k]); l != "" {
			return l
		}
	}
	return ""
}

func osExec(ctx context.Context, dir string, env []string, argv []string, out io.Writer) error {
	if len(argv) == 0 {
		return fmt.Errorf("empty command")
	}
	// exec.Command resolves argv[0] against THIS process's PATH, not the PATH in
	// env, so a pinned tool's directory placed first on env's PATH would be
	// ignored and the copy at another version would run. Resolve against env.
	bin, err := lookPathIn(env, argv[0])
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, bin, argv[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = out
	cmd.Stderr = out
	// A killed check must not leave its children holding the pipe open.
	cmd.WaitDelay = 10 * time.Second
	return cmd.Run()
}

// tailBuffer keeps the last max bytes written to it. Safe for the concurrent
// writes of a command's stdout and stderr.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf bytes.Buffer
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf.Write(p)
	if over := t.buf.Len() - t.max; over > 0 {
		t.buf.Next(over)
	}
	return len(p), nil
}

func (t *tailBuffer) lastLines(n int) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := strings.Split(strings.TrimRight(t.buf.String(), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// lookPathIn finds file on the PATH carried by env. A name with a path
// separator is used as is.
func lookPathIn(env []string, file string) (string, error) {
	if strings.ContainsRune(file, filepath.Separator) || strings.ContainsRune(file, '/') {
		return file, nil
	}
	path := ""
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			path = strings.TrimPrefix(kv, "PATH=")
		}
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			dir = "."
		}
		cand := filepath.Join(dir, file)
		if fi, err := os.Stat(cand); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return cand, nil
		}
		if exeSuffix() != "" {
			if fi, err := os.Stat(cand + exeSuffix()); err == nil && !fi.IsDir() {
				return cand + exeSuffix(), nil
			}
		}
	}
	return "", &exec.Error{Name: file, Err: exec.ErrNotFound}
}
