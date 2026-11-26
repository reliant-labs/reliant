package testcollect

import (
	"fmt"
	"io"
	"strings"

	"github.com/reliant-labs/forge/internal/obsout"
	"github.com/reliant-labs/forge/pkg/testreport"
)

// Schema is the document `forge test collect --json` prints.
const Schema = "forge.test.collect/v1"

// What a workload's collection came to (WorkloadResult.State).
const (
	// StateCollected: coverage was written into the run.
	StateCollected = "collected"
	// StateReset: --reset zeroed (or, where nothing live could be zeroed,
	// fenced off) the workload's counters.
	StateReset = "reset"
	// StateEmpty: the workload was reached and had no coverage yet (a
	// container whose process has not exited, a build with no counters).
	StateEmpty = "empty"
	// StateUnreachable: the workload is instrumented and nothing of it
	// could be reached: no process, no pod, no container, no flushed file.
	StateUnreachable = "unreachable"
	// StateFailed: it was reached and answered with an error (a binary not
	// built with -cover, a non-atomic build, an unreadable tar).
	StateFailed = "failed"
	// StateNotInstrumented: a forge Go build the env does not instrument.
	StateNotInstrumented = "not_instrumented"
	// StateUnavailable: not a forge Go build, so there is no coverage to
	// collect (a prebuilt image, a non-Go server).
	StateUnavailable = "unavailable"
)

// The coverage line a workload without collectable coverage reports.
const (
	CoverageUnavailable     = "unavailable (not a forge Go build)"
	CoverageNotInstrumented = "not instrumented"
)

// StaleBuildLabel labels a build older than a workload's newest: kept, and
// never merged with it.
const StaleBuildLabel = "stale build"

// Document is forge.test.collect/v1.
type Document struct {
	obsout.Head
	Env string `json:"env"`
	// Run is the run directory coverage was written into; absent for
	// --reset, and when nothing was collected.
	Run string `json:"run,omitempty"`
	// Lane is the lane it was recorded under.
	Lane  string `json:"lane"`
	Reset bool   `json:"reset,omitempty"`
	// Collected counts the workloads whose coverage was written (with
	// --reset: the workloads reset).
	Collected int              `json:"collected"`
	Workloads []WorkloadResult `json:"workloads"`
	Warnings  []string         `json:"warnings,omitempty"`
	Suggested []string         `json:"suggested,omitempty"`
}

// WorkloadResult is one workload's collection.
type WorkloadResult struct {
	Name    string `json:"name"`
	Runtime string `json:"runtime"`
	State   string `json:"state"`
	// Coverage is set when there is none to collect: CoverageUnavailable or
	// CoverageNotInstrumented.
	Coverage string `json:"coverage,omitempty"`
	// Reason says why the workload was not collected, when it was not.
	Reason  string   `json:"reason,omitempty"`
	Sources []Source `json:"sources,omitempty"`
	// Build is the newest build's coverage, as written into the run.
	Build *Build `json:"build,omitempty"`
	// Stale are older builds of the workload's binary (an air rebuild, a
	// pod of the previous rollout), each labelled StaleBuildLabel.
	Stale []Build  `json:"stale,omitempty"`
	Notes []string `json:"notes,omitempty"`
}

// Source is one place a workload's coverage was read from.
type Source struct {
	// Kind is pull (a host's /debug/coverage), exit-flush (a host's
	// GOCOVERDIR), container (a compose container's GOCOVERDIR) or pod (a
	// pod's /debug/coverage through a port-forward).
	Kind string `json:"kind"`
	// From names it: an address, a directory, a container or ns/pod.
	From string `json:"from"`
	// Status is ok (files read), empty (reached, nothing there),
	// unreachable or failed.
	Status string `json:"status"`
	// Files counts the coverage files read.
	Files int    `json:"files"`
	Error string `json:"error,omitempty"`
}

// The source kinds and statuses.
const (
	SourcePull      = "pull"
	SourceExitFlush = "exit-flush"
	SourceContainer = "container"
	SourcePod       = "pod"

	SourceOK          = "ok"
	SourceEmpty       = "empty"
	SourceUnreachable = "unreachable"
	SourceFailed      = "failed"
)

// Build is one binary build's coverage in the run.
type Build struct {
	// MetaHash is the build's covmeta hash.
	MetaHash     string `json:"meta_hash"`
	CounterFiles int    `json:"counter_files"`
	// Dir is where its files are, relative to the run directory.
	Dir   string `json:"dir"`
	Label string `json:"label,omitempty"`
	// Profile, when set, is the build's counts combined process by process
	// (a reset's baselines subtracted, a process's snapshots deduplicated),
	// relative to the run directory: the lane's input instead of Dir.
	Profile string `json:"profile,omitempty"`
	// SinceReset is set when a reset's baselines bore on the build.
	SinceReset *SinceReset `json:"since_reset,omitempty"`
	// Coverage is the build converted (`go tool covdata textfmt`): covered
	// over instrumented statements, generated code excluded. Absent when it
	// was not converted (Notes say why); the files are kept either way.
	Coverage *testreport.Tally `json:"coverage,omitempty"`
}

// SinceReset is a build's counts since `forge test collect --reset`.
type SinceReset struct {
	// Processes counts the processes whose reset baseline was subtracted.
	Processes int `json:"processes"`
	// Restarted counts the processes with a baseline that had restarted
	// since the reset, and were counted whole.
	Restarted int `json:"restarted,omitempty"`
}

// WriteText prints the document for a person: one line per workload.
func WriteText(w io.Writer, d *Document) {
	verb, where := "collect", ""
	if d.Reset {
		verb = "reset"
	} else if d.Run != "" {
		where = " → " + d.Run + "/" + d.Lane
	}
	fmt.Fprintf(w, "forge test %s %s%s\n", verb, d.Env, where)
	if len(d.Workloads) == 0 {
		fmt.Fprintln(w, "  no Go workloads in this env")
	}
	width := 0
	for _, wl := range d.Workloads {
		width = max(width, len(wl.Name))
	}
	for _, wl := range d.Workloads {
		fmt.Fprintf(w, "  %-*s  %-8s %s\n", width, wl.Name, wl.Runtime, workloadLine(wl))
		pad := strings.Repeat(" ", width+13)
		for _, s := range wl.Stale {
			fmt.Fprintf(w, "%s%s %s: %d counter file(s) in %s, never merged\n", pad, s.Label, short(s.MetaHash), s.CounterFiles, s.Dir)
		}
		for _, n := range wl.Notes {
			fmt.Fprintf(w, "%s%s\n", pad, n)
		}
	}
	for _, warn := range d.Warnings {
		fmt.Fprintf(w, "warning: %s\n", warn)
	}
	for _, s := range d.Suggested {
		fmt.Fprintf(w, "  next: %s\n", s)
	}
}

func workloadLine(wl WorkloadResult) string {
	switch wl.State {
	case StateCollected:
		b := wl.Build
		line := fmt.Sprintf("collected  build %s, %d counter file(s)", short(b.MetaHash), b.CounterFiles)
		if b.Coverage != nil {
			line += fmt.Sprintf(", %.1f%% of %d statements", b.Coverage.Pct, b.Coverage.Statements)
		}
		return line + " (" + sourceList(wl.Sources) + ")"
	case StateUnavailable, StateNotInstrumented:
		return "coverage: " + wl.Coverage
	}
	line := wl.State
	if wl.Reason != "" {
		line += ": " + wl.Reason
	}
	return line
}

func sourceList(ss []Source) string {
	var parts []string
	for _, s := range ss {
		if s.Status == SourceOK {
			parts = append(parts, s.Kind+" "+s.From)
		}
	}
	return strings.Join(parts, ", ")
}

func short(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}
