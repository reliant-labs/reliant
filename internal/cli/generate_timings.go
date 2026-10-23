package cli

import (
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"github.com/reliant-labs/forge/internal/shadowdb"
)

// generateTimingsEnv turns on the per-step wall-time report for every
// generate run in the process environment, the same report --timings
// prints. The env spelling exists for the runs nobody types: `forge ci
// verify-generated` and a gate script shell `forge generate` themselves, and
// the question "where did the gate's 7 minutes go" is asked of exactly those.
const generateTimingsEnv = "FORGE_GENERATE_TIMINGS"

// stepTimer records how long each pipeline step ran. Gated-off steps are not
// recorded: they cost a predicate call, and listing them would bury the
// handful of steps that hold the time.
type stepTimer struct {
	start time.Time
	steps []stepTiming
	// shadow is the applied-schema memo's totals when the run started; the
	// report prints what this run added to them.
	shadow shadowdb.Stats
}

type stepTiming struct {
	Name string
	Dur  time.Duration
}

func newStepTimer() *stepTimer {
	return &stepTimer{start: time.Now(), shadow: shadowdb.CurrentStats()}
}

func (t *stepTimer) record(name string, d time.Duration) {
	t.steps = append(t.steps, stepTiming{Name: name, Dur: d})
}

// generateTimingsRequested reports whether this run should print the step
// timing report: the --timings flag, or FORGE_GENERATE_TIMINGS set to
// anything but empty or "0".
func generateTimingsRequested(flag bool) bool {
	if flag {
		return true
	}
	v := os.Getenv(generateTimingsEnv)
	return v != "" && v != "0"
}

// print writes the report: the slowest steps first, every step that took at
// least 50ms, then the total. Printed whether the run succeeded or failed —
// a run that died in its validate build is the one whose timing is wanted.
func (t *stepTimer) print(w io.Writer) {
	total := time.Since(t.start)
	sorted := append([]stepTiming(nil), t.steps...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Dur > sorted[j].Dur })
	var listed, rest time.Duration
	fmt.Fprintf(w, "\n⏱  generate step timings (%d step(s) ran, %s total):\n", len(t.steps), total.Round(10*time.Millisecond))
	hidden := 0
	for _, s := range sorted {
		if s.Dur < 50*time.Millisecond {
			hidden++
			rest += s.Dur
			continue
		}
		listed += s.Dur
		fmt.Fprintf(w, "   %8s  %5.1f%%  %s\n", s.Dur.Round(10*time.Millisecond), pct(s.Dur, total), s.Name)
	}
	if hidden > 0 {
		fmt.Fprintf(w, "   %8s  %5.1f%%  (%d step(s) under 50ms)\n", rest.Round(time.Millisecond), pct(rest, total), hidden)
	}
	if other := total - listed - rest; other > 50*time.Millisecond {
		fmt.Fprintf(w, "   %8s  %5.1f%%  (outside any step: setup, snapshots, state save)\n", other.Round(10*time.Millisecond), pct(other, total))
	}
	printShadowSchemaLine(w, shadowdb.StatsSince(t.shadow))
}

// printShadowSchemaLine says how many times this run applied the migrations
// to a shadow database, and how many readers reused an application instead.
// The apply time is already inside the step that first read the schema; the
// line exists so that a second apply — a regression to one per reader — is
// visible as a number rather than as a slower run. Silent when nothing read
// the schema.
func printShadowSchemaLine(w io.Writer, s shadowdb.Stats) {
	if s.Applies == 0 && s.Reuses == 0 {
		return
	}
	fmt.Fprintf(w, "   shadow schema: migrations applied %d× (%s), reused by %d more reader(s)\n",
		s.Applies, s.ApplyTime.Round(10*time.Millisecond), s.Reuses)
}

func pct(d, total time.Duration) float64 {
	if total <= 0 {
		return 0
	}
	return 100 * float64(d) / float64(total)
}
