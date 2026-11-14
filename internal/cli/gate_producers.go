package cli

// The `--gate-json` producers: the boundary where a verb's own verdict
// becomes a gate document (control-plane
// docs/design/hosted-deploy-primitives.md §3.3).
//
// WHY THE MAPPING LIVES HERE AND NOT IN EACH VERB. A producing verb knows
// what it checked and how it went; it should not have to know pkg/release's
// gate vocabulary, the closed status set, or the document format. So each verb
// reports its verdict in its own terms and ONE function per boundary maps it.
// internal/cli/lint declares the one-method GateWriter it needs and this
// package installs an implementation — the interface lives at the consumer,
// and the dependency runs from the composer (cli) to the leaf (lint), never
// back.
//
// The practical payoff: lint's three verdict values are three strings in its
// own package, and if the closed set ever grows, the only place that learns
// about it is release.ParseGateStatus and this file.

import (
	"fmt"
	"os"
	"time"

	"github.com/reliant-labs/forge/internal/cli/lint"
	"github.com/reliant-labs/forge/internal/testreport"
	"github.com/reliant-labs/forge/pkg/release"
)

func init() {
	// Installed at init so every entry point gets it — the command tree is
	// built in several places (root.go, tests, the docs generator), and a
	// writer wired at one of them would leave --gate-json silently
	// unsupported in the others.
	lint.SetGateWriter(writeGateFromRequest)
}

// writeGateFromRequest turns a producer's GateRequest into a gate document.
//
// The status goes through release.ParseGateStatus — the STRICT path — so a
// producer that reported a verdict outside the closed set fails HERE, in the
// job that ran the check, rather than later at `gate record` in a different
// job with the check's output long gone.
func writeGateFromRequest(req lint.GateRequest) error {
	status, err := release.ParseGateStatus(req.Status)
	if err != nil {
		return fmt.Errorf("gate document for %q: %w", req.Name, err)
	}
	gate := gateWindow(release.Gate{
		Name:    req.Name,
		Status:  status,
		Summary: req.Summary,
		Details: req.Details,
	}, req.StartedAt, req.FinishedAt)
	// writeGateWithRun stamps the run identity: resolved from the
	// environment rather than flagged on every producer, because a
	// producer runs inside the same CI step as the rest of the attempt, so
	// the environment already names it — and a missing --run-id on one of
	// five verbs is exactly how a half-populated join key happens.
	return writeGateWithRun(req.Path, gate)
}

// osGetenv is os.Getenv behind a variable, so a test can state the CI
// environment without t.Setenv (which cannot be used in a parallel test —
// the same reason runOptions.resolve takes a getenv).
var osGetenv = os.Getenv

// ─── forge ci verify-test-run --gate-json ───────────────────────────────────

// writeTestRunGate records what a `go test -json` run proved.
//
// THE FOUR STATES MAP ONTO THE CLOSED SET EXACTLY, which is why this is a
// switch and not a bool:
//
//   - UNDETERMINED → `error`. forge could not obtain the facts (an empty
//     pipe, a stream that ends mid-run). That is the gate vocabulary's whole
//     reason for having `error` beside `failed`, and it is the same
//     distinction exit 2 draws from exit 1: reporting it as a failure would
//     blame the release for a truncated log.
//   - FAIL → `failed`. Real findings: failing packages, or packages that
//     skipped so much their pass proves nothing.
//   - NOT APPLICABLE → `skipped`. The stream carried no package with tests.
//     NOT a pass: a suite that ran nothing has not vouched for anything, and
//     this is the exact green-while-blind shape the command exists to expose.
//   - PASS → `passed`.
//
// The counts ride along in details, so the evidence says "412 passed, 0
// failed, 3 skipped" rather than just "passed" — a fact a reviewer can act on
// without re-running anything.
func writeTestRunGate(path string, analysis testreport.Analysis, startedAt time.Time) error {
	totals := analysis.Totals

	var status release.GateStatus
	switch analysis.Status() {
	case testreport.StatusUndetermined:
		status = release.GateStatusError
	case testreport.StatusFail:
		status = release.GateStatusFailed
	case testreport.StatusNotApplicable:
		status = release.GateStatusSkipped
	default:
		status = release.GateStatusPassed
	}

	summary := fmt.Sprintf("%d passed, %d failed, %d skipped of %d test(s) in %d package(s)",
		totals.Passed, totals.Failed, totals.Skipped, totals.Tests, totals.Packages)
	switch status {
	case release.GateStatusError:
		summary = fmt.Sprintf("UNDETERMINED — %d fact(s) could not be obtained from the test stream",
			len(analysis.Undetermined))
	case release.GateStatusSkipped:
		summary = "no package in the stream ran a test — this run proved nothing"
	case release.GateStatusFailed:
		if totals.SuiteFailed {
			summary = fmt.Sprintf("%d package(s) failed; %s", totals.FailedPackages, summary)
		} else {
			summary = fmt.Sprintf("%d package(s) skipped so much that the pass proves nothing; %s",
				len(analysis.Findings), summary)
		}
	}

	gate := gateWindow(release.Gate{
		Name:    testGateName,
		Status:  status,
		Summary: summary,
		Details: map[string]any{
			"tests": totals.Tests, "passed": totals.Passed,
			"failed": totals.Failed, "skipped": totals.Skipped,
			"packages": totals.Packages, "findings": len(analysis.Findings),
		},
	}, startedAt, time.Now())
	return writeGateWithRun(path, gate)
}

// ─── forge build --gate-json ────────────────────────────────────────────────

// writeBuildGate records what a build produced.
//
// A BUILD THAT BUILT NOTHING IS `skipped`. `forge build --target web` in a
// project with no frontends succeeds having done no work, and a gate claiming
// `passed` for it would attest to artifacts that do not exist — the same rule
// as a smoke with no routes and a lint with no lanes. The three producers
// agree on this deliberately: "nothing to do" is a third outcome, and every
// one of them had a path that used to report it as success.
func writeBuildGate(path string, succeeded, failed int, startedAt time.Time) error {
	status := statusForFindings(failed, succeeded+failed)

	summary := fmt.Sprintf("%d target(s) built, %d failed", succeeded, failed)
	if status == release.GateStatusSkipped {
		summary = "no build target matched — nothing was built"
	}
	return writeGateWithRun(path, gateWindow(release.Gate{
		Name:    buildGateName,
		Status:  status,
		Summary: summary,
		Details: map[string]any{"succeeded": succeeded, "failed": failed},
	}, startedAt, time.Now()))
}

// testGateName and buildGateName are the check names these producers record
// under — the verb's own vocabulary ("test", "build"), not its command line,
// so an evidence trail reads the same whichever runner produced it.
const (
	testGateName  = "test"
	buildGateName = "build"
)

// writeGateWithRun stamps the run identity and writes the document. One
// place, so every producer joins its gate to the pipeline attempt the same
// way (see writeGateFromRequest for why this is read from the environment
// rather than flagged per verb).
func writeGateWithRun(path string, gate release.Gate) error {
	if run := runFromEnvironment(osGetenv); !run.Zero() {
		if gate.RunID == "" {
			gate.RunID = run.ID
		}
		if gate.URL == "" {
			gate.URL = run.URL
		}
	}
	return writeGateDocument(path, gate)
}

// emitBuildGate is the --gate-json hook `forge build` calls. A function
// rather than four lines inline because runBuild is already at its statement
// budget, and because the no-flag case belongs with the gate code rather than
// as another conditional in the build's main line.
func emitBuildGate(opts buildOptions, succeeded, failed int, startedAt time.Time) error {
	if opts.gateJSON == "" {
		return nil
	}
	return writeBuildGate(opts.gateJSON, succeeded, failed, startedAt)
}
