// Copyright (c) 2025 Reliant Labs

// Package machinefailure is the one rule for when a machine's "failed to
// start" report is believed.
//
// The control plane reports Failed for moments it recovers from by itself. On
// 2026-10-30 a resumed machine read Failed for 17s while its previous pod
// finished terminating, and every VM death on `cicd` read Failed for ~40s
// before its daemon reconnected. Anything that waited on those machines and
// trusted the first Failed read told the user the machine had failed and gave
// up, and the machine came back on its own seconds later.
//
// So everything that waits for a machine — the wait_for_machine tool, run
// preflight and the resume wake — believes a Failed report only once it has
// held, unbroken, for Settle; until then it keeps waiting. The waiter keeps
// the start of the current run of Failed looks (RunStart) and asks Believed at
// each look. A look at any other state ends the run, so a machine that
// recovers and fails again starts over.
//
// A leaf package, so the tool (internal/llm/tools) and the workflow
// activities share one rule without an import edge between them.
package machinefailure

import "time"

// Settle is how long a failed-to-start report must hold, the machine never
// connecting meanwhile, before a waiter believes it: longer than the ~40s a
// VM death reads Failed for, and the 17s a resume's pod handover does.
const Settle = 2 * time.Minute

// RunStart carries the start of an unbroken run of Failed looks across one
// more look, taken at now: kept while the machine still reports Failed, set to
// now on the first Failed look, and cleared (zero) by any other state.
func RunStart(since time.Time, failed bool, now time.Time) time.Time {
	switch {
	case !failed:
		return time.Time{}
	case since.IsZero():
		return now
	}
	return since
}

// Believed reports whether a run of Failed looks that began at since has held
// for settle by now. No run (a zero since) is never believed.
func Believed(since, now time.Time, settle time.Duration) bool {
	return !since.IsZero() && now.Sub(since) >= settle
}
