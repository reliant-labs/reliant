package testflakes

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/testcapture"
	"github.com/reliant-labs/forge/pkg/testreport"
)

// The flake gate: every test a change adds or edits runs three times, the
// port of forge's scripts/ci-changed-tests.sh and reliant's
// scripts/test-flake.sh. "Passed on re-run" is never an acceptable verdict,
// and the cheapest moment to catch a flaky test is before it lands: a test
// run once proves it CAN pass; three runs are a cheap probe that it passes
// reliably (order dependence, leaked global state, a timing assumption). The
// race detector joins them under --full.
//
// -count=3 is not a cache loss: only the selected tests run under it — tests
// the change adds or edits, whose package's test binary changed, so no cached
// result existed for them. Every other test in the package is excluded by
// -run, not re-run. control-plane's variant (--cpu 1,2,4) runs them once at
// each GOMAXPROCS instead, which `go test` caches.

// The gate's exit codes.
const (
	// GatePassed: every changed test passed every run.
	GatePassed = 0
	// GateFailed: a changed test failed at least once.
	GateFailed = 1
	// GateUndetermined: no changed test could be identified (none in the
	// diff, or the diff could not be read), or the command could not start.
	GateUndetermined = 2
)

// GateOptions configures Gate.
type GateOptions struct {
	// Root is the module root; the command runs there.
	Root string
	// Base is the revision the change is measured from.
	Base string
	// Full adds -race (the main-branch and release tier).
	Full bool
	// CPU, when set ("1,2,4"), replaces -count=3 with -cpu=<CPU>: each test
	// runs once per GOMAXPROCS value, which Go's test cache keeps.
	CPU string
	// Command is the project's own `go test`: the argv every flag and
	// package is appended to. Default ["go", "test"].
	Command []string
	Stdout  io.Writer
	Stderr  io.Writer
	// Environ is the command's environment; default os.Environ().
	Environ []string
	Getenv  func(string) string
	Now     func() time.Time
	Git     Git
	List    ListPackages
	Runner  Runner
	// Begin opens the run the gate records into (lane flake-gate).
	Begin testcapture.BeginOptions
}

func (o *GateOptions) defaults() {
	if len(o.Command) == 0 {
		o.Command = []string{"go", "test"}
	}
	if o.Stdout == nil {
		o.Stdout = os.Stdout
	}
	if o.Stderr == nil {
		o.Stderr = os.Stderr
	}
	if o.Environ == nil {
		o.Environ = os.Environ()
	}
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Runner == nil {
		o.Runner = ExecRunner
	}
	if o.Begin.Getenv == nil {
		o.Begin.Getenv = o.Getenv
	}
}

// GateResult is what the gate found.
type GateResult struct {
	ExitCode int
	// Message is the one-line verdict.
	Message string
	Changed ChangedTests
	// Argv is the first command the gate ran; empty when it ran nothing.
	// Argvs is every command, one per Batch (a selection too large for one
	// -run argument is split across several).
	Argv  []string
	Argvs [][]string
	// LaneDir is where the run was recorded; "" when it was not.
	LaneDir string
	// Failed are the changed tests that failed at least one run, and
	// RetryPassed those of them that also passed one: flakes.
	Failed      []testreport.TestID
	RetryPassed []testreport.TestID
	// CommandFailed says the selected tests' command could not start (no
	// `go`, an unexecutable wrapper). ExitCode is then GateUndetermined, as
	// it always was, but the gate did not run what it selected: the
	// flake-check step fails it rather than passing it as "no tests".
	CommandFailed bool
}

// GateFlags are the `go test` flags the gate passes before -run: -short
// -count=3 -timeout 10m (a budget per package's test binary, so per package
// and per part of a split package), with -cpu=<cpu> instead of -count=3 when cpu is set,
// and -race under full.
func GateFlags(full bool, cpu string) []string {
	flags := []string{"-short", "-count=3", "-timeout", "10m"}
	if cpu != "" {
		flags[1] = "-cpu=" + cpu
	}
	if full {
		flags = append(flags, "-race")
	}
	return flags
}

// Gate runs the tests the change adds or edits three times through the
// project's own command and records the run as lane flake-gate. It reports;
// whether a failure blocks is the caller's (the flake-check gate step's grade).
func Gate(ctx context.Context, o GateOptions) GateResult {
	o.defaults()
	started := o.Now()
	changed, err := FindChangedTests(ctx, ChangedOptions{Root: o.Root, Base: o.Base, Git: o.Git, List: o.List})
	res := GateResult{Changed: changed}
	for _, s := range changed.Skipped {
		fmt.Fprintln(o.Stdout, s)
	}
	if err != nil {
		res.ExitCode, res.Message = GateUndetermined, fmt.Sprintf("flake gate: could not identify the changed tests in %s..HEAD: %v", o.Base, err)
		return res
	}
	if len(changed.Packages) == 0 {
		res.ExitCode, res.Message = GateUndetermined, fmt.Sprintf("flake gate: no new or edited tests in %s..HEAD", o.Base)
		o.summary("flake gate: no new or edited tests in this change.")
		return res
	}

	flags := GateFlags(o.Full, o.CPU)
	pkgs := make([]string, 0, len(changed.Packages))
	for _, p := range changed.Packages {
		pkgs = append(pkgs, "./"+strings.TrimPrefix(p.Rel, "./"))
	}
	// One invocation per Batch: every selected test, and only those, however
	// many there are (never every test of the packages).
	batches := changed.Batches(MaxRunBytes)
	for _, b := range batches {
		argv := append(append(append([]string(nil), o.Command...), "-json"), flags...)
		res.Argvs = append(res.Argvs, append(append(argv, "-run", b.Run), b.Packages...))
	}
	res.Argv = res.Argvs[0]

	mode := "short"
	if o.Full {
		mode = "full"
	}
	actions := o.Getenv("GITHUB_ACTIONS") == "true"
	o.summary(fmt.Sprintf("### flake gate (%s): new and edited tests, %s\n", mode, strings.Join(flags, " ")))
	for _, p := range pkgs {
		o.summary(fmt.Sprintf("- selected package: %#q", p))
	}
	if actions {
		fmt.Fprintf(o.Stdout, "::group::changed tests (%s) — %d packages, scheduled by go test\n", mode, len(pkgs))
	} else {
		fmt.Fprintf(o.Stdout, "changed tests (%s) — %d packages:\n", mode, len(pkgs))
	}
	fmt.Fprintln(o.Stdout, strings.Join(changed.Names(), "\n"))
	if actions {
		fmt.Fprintln(o.Stdout, "::endgroup::")
	}

	runDir, recording, warnings := openRun(ctx, o.Begin)
	for _, w := range warnings {
		fmt.Fprintf(o.Stderr, "forge test flakes gate: warning: %s\n", w)
	}
	if len(batches) > 1 {
		fmt.Fprintf(o.Stdout, "the selection is too large for one -run argument: %d go test invocations\n", len(batches))
	}
	laneStart := o.Now()
	code, failedBatches, stream, startErr := o.runBatches(ctx, runDir, res.Argvs, batches)
	laneEnd := o.Now()
	if recording {
		l := laneRun{Lane: LaneFlakeGate, Command: laneCommand(res.Argvs), StartedAt: laneStart, FinishedAt: laneEnd, ExitCode: code, Stream: stream}
		if startErr != nil {
			l.Error = startErr.Error()
		}
		dir, err := recordLane(runDir, l)
		if err != nil {
			fmt.Fprintf(o.Stderr, "forge test flakes gate: warning: %v\n", err)
		} else {
			res.LaneDir = dir
		}
	}
	res.Failed, res.RetryPassed = gateFailures(parseStream(stream, LaneFlakeGate))

	elapsed := o.Now().Sub(started).Round(time.Second)
	var repro []string
	for _, b := range failedBatches {
		repro = append(repro, fmt.Sprintf("%s %s -run '%s' %s", strings.Join(o.Command, " "), strings.Join(flags, " "), b.Run, strings.Join(b.Packages, " ")))
	}
	reproduce := strings.Join(repro, "; ")
	switch {
	case startErr != nil:
		res.ExitCode, res.Message = GateUndetermined, fmt.Sprintf("flake gate: could not run %s: %v", o.Command[0], startErr)
		res.CommandFailed = true
	case code == 0:
		res.ExitCode = GatePassed
		res.Message = fmt.Sprintf("flake gate: selected tests passed %s in %s (%d packages)", passedOf(o.CPU), elapsed, len(pkgs))
		o.summary(res.Message)
	default:
		res.ExitCode = GateFailed
		res.Message = fmt.Sprintf("flake gate: FAILED after %s; reproduce with: %s", elapsed, reproduce)
		if actions {
			fmt.Fprintf(o.Stdout, "::error::changed tests failed; reproduce with: %s\n", reproduce)
		}
		o.summary(fmt.Sprintf("flake gate: **FAILED** after %s", elapsed))
	}
	return res
}

// runBatches runs each invocation in turn, teeing every stream into one: the
// first non-zero exit, the batches that failed, the concatenated `go test
// -json` stream, and the error of a command that could not start (it stops there).
func (o *GateOptions) runBatches(ctx context.Context, runDir string, argvs [][]string, batches []Batch) (code int, failed []Batch, stream []byte, startErr error) {
	tee := newStreamTee(o.Stdout, false)
	for i, argv := range argvs {
		c, err := o.Runner(ctx, o.Root, argv, childEnv(o.Environ, runDir, LaneFlakeGate), tee, o.Stderr)
		if err != nil {
			startErr = err
			break
		}
		if c != 0 {
			failed = append(failed, batches[i])
			if code == 0 {
				code = c
			}
		}
	}
	tee.Close()
	return code, failed, tee.buf.Bytes(), startErr
}

// laneCommand is the recorded lane's command: every invocation, separated by ";".
func laneCommand(argvs [][]string) []string {
	var command []string
	for i, argv := range argvs {
		if i > 0 {
			command = append(command, ";")
		}
		command = append(command, argv...)
	}
	return command
}

// passedOf says how many runs each test passed.
func passedOf(cpu string) string {
	if cpu == "" {
		return "3/3"
	}
	n := len(strings.Split(cpu, ","))
	return fmt.Sprintf("%d/%d (-cpu=%s)", n, n, cpu)
}

// summary appends a line to $GITHUB_STEP_SUMMARY, when there is one.
func (o *GateOptions) summary(line string) {
	path := o.Getenv("GITHUB_STEP_SUMMARY")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	fmt.Fprintln(f, line)
}

// gateFailures are the top-level tests with a failed run, and those of them
// that passed another run.
func gateFailures(r *testreport.Results) (failed, retryPassed []testreport.TestID) {
	if r == nil {
		return nil, nil
	}
	fail := map[testreport.TestID]bool{}
	pass := map[testreport.TestID]bool{}
	for _, c := range r.Leaves() {
		top := c.ID.TopLevel()
		if c.Status == testreport.StatusFail || c.FailedAttempts > 0 {
			fail[top] = true
		}
		if c.RetryPassed {
			pass[top] = true
		}
	}
	for id := range fail {
		failed = append(failed, id)
		if pass[id] {
			retryPassed = append(retryPassed, id)
		}
	}
	sortIDs(failed)
	sortIDs(retryPassed)
	return failed, retryPassed
}

func sortIDs(ids []testreport.TestID) {
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
}
