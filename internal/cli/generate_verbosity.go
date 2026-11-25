// Run-scoped verbosity for `forge generate`.
//
// The default output of `forge generate` is PROGRESS plus things that need
// ACTION. Everything else is available on demand behind `-v/--verbose`,
// which the command already has.
//
// The measurement that motivated this: control-plane on a clean tree, exit
// 0, zero files changed, printed 269 lines of which 58 were notices — 22
// "Skipped internal/handlers/X/ (all handlers up to date)" lines, ~25
// "Skipped contract_test.go scaffold for internal/X/" lines, and a
// "Preserved 15 existing CRUD page(s)" line. Every one of them reports that
// forge correctly did nothing, and every one of them is identical on the
// next run. Output that is always present carries no information: the
// reader learns to skim the whole block, including the two lines in it that
// did need an answer.
//
// Why a package-level variable rather than threading a flag through:
// these lines are printed by helpers several layers below the pipeline
// context (generateServiceStubs, birthContractTest, writeCIScaffold), and
// they print to stdout via fmt.Printf rather than to a writer anyone owns.
// Threading a bool through every signature to reach a global stdout is
// ceremony around a value that is already global. The pipeline sets this
// once per run, under the same lock that serializes runs (generateMu plus
// the cross-process file lock in runGeneratePipelineFlags), so concurrent
// mutation is not reachable.
package cli

import "fmt"

// generateVerbose reports whether this generate run was asked for the
// routine, no-action-needed lines. Set by setGenerateVerbosity at the head
// of the pipeline; false for every other entry point, which is what makes
// a `forge scaffold`-driven internal generate quiet by default too.
var generateVerbose bool

// setGenerateVerbosity records the run's verbosity and returns a function
// that restores the previous value. Callers defer the restore so an
// in-process embedding (the reliant harness runs forge's cobra tree
// in-process) cannot leak one run's verbosity into the next.
func setGenerateVerbosity(v bool) func() {
	prev := generateVerbose
	generateVerbose = v
	return func() { generateVerbose = prev }
}

// routinef prints a line that reports forge correctly doing NOTHING —
// a file left alone, a scaffold already present, a step with no work.
// Suppressed unless the run asked for it.
//
// The test for whether a line belongs here: would it be identical on the
// next run against an unchanged tree? If yes it is routine. A line that
// appears BECAUSE something changed, or that names something the user must
// act on, is not routine and must print unconditionally.
func routinef(format string, args ...any) {
	if generateVerbose {
		fmt.Printf(format, args...)
	}
}
