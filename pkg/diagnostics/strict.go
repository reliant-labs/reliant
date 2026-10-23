package diagnostics

import (
	"fmt"
	"os"
)

// strict.go — StrictEmitter decorator for `features.strict_wiring`
// projects.
//
// Default emit behavior is warn-only: ignorable for one tick, fixable
// at the user's pace. Production-grade projects opt in to strict mode
// via `forge.yaml: features.strict_wiring: true`, at which point
// Bootstrap wraps the base emitter with StrictEmitter and any
// registered diagnostic terminates the process after the summary line
// flushes.
//
// Why a decorator instead of a "strict" flag on LogEmitter:
//
//   - Keeps LogEmitter's responsibility narrow (just log).
//   - Composes cleanly with MultiEmitter — a strict + metrics pair
//     fans the warn to a dashboard before exiting.
//   - Test seams: substitute the os.Exit indirection without poking
//     at LogEmitter internals.

// osExit is the process-exit indirection StrictEmitter calls. Tests
// override this to capture the exit code without terminating the test
// process. Default points at os.Exit so production behavior matches a
// plain log.Fatalf.
var osExit = os.Exit

// StrictEmitter wraps a base Emitter and terminates the process via
// osExit(1) after Summary when at least one diagnostic was emitted.
//
// The base emitter sees every Emit and Summary call first, so the
// fatal line is the LAST log output — operators see the full list
// before the exit. Empty Summary slice is a clean exit (no
// termination), matching the LogEmitter "no output for zero
// diagnostics" convention.
type StrictEmitter struct {
	Base Emitter
}

// NewStrictEmitter wraps base in a StrictEmitter. A nil base falls
// back to NopEmitter — strict mode with no logging surface is rare
// but legal (e.g. when the caller wants the exit-on-unwired behavior
// without writing log lines).
func NewStrictEmitter(base Emitter) StrictEmitter {
	if base == nil {
		base = NopEmitter{}
	}
	return StrictEmitter{Base: base}
}

// Emit forwards to the base emitter. StrictEmitter does not terminate
// on per-diagnostic Emit — the exit decision is made at Summary so
// the operator sees every line before the process dies.
func (s StrictEmitter) Emit(d Diagnostic) {
	s.Base.Emit(d)
}

// Summary forwards to the base emitter, then terminates the process
// via osExit(1) if any diagnostic was emitted. The format of the
// final stderr line is intentionally plain (not slog-formatted) so
// it's visible even when slog handlers buffer or drop output.
func (s StrictEmitter) Summary(ds []Diagnostic) {
	s.Base.Summary(ds)
	if len(ds) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr,
		"forge.scaffold.unwired: strict_wiring is on and %d unwired scaffold(s) were registered; exiting.\n",
		len(ds),
	)
	osExit(1)
}
