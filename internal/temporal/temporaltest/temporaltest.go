// Copyright (c) 2025 Reliant Labs

// Package temporaltest gives every Temporal test harness in this repository
// the production worker's deadlock detector instead of the SDK's default.
//
// The Temporal SDK fails a workflow task whose goroutine does not yield within
// worker.Options.DeadlockDetectionTimeout ("[TMPRL1101] Potential deadlock
// detected"). Left unset it is ONE second, and testsuite's environments leave
// it unset. One second is a budget for CPU time, enforced against wall time: on
// a loaded CI box a workflow task — the first one especially, which runs on a
// cold process — can take longer than that without being wrong, so tests
// failed intermittently on code that was fine. Measured: at load 119 a cold
// process running TestApproval_AtTopLevel_IsReached alone tripped the detector
// 7 times in 8.
//
// The detector is raised, not disabled. TEMPORAL_DEBUG or
// DisableDeadlockDetection would also make the flake go away, by deleting the
// only signal that a workflow blocks on something Temporal cannot see — a Go
// channel, a mutex, a time.Sleep, an unbounded loop. Such code still fails
// here, with the SDK's goroutine dump, after DeadlockDetectionTimeout.
//
// Use WorkflowTestSuite wherever a test would use testsuite.WorkflowTestSuite;
// it is a drop-in replacement, including as an embedded field. A guard test in
// this package rejects direct uses of the SDK type, so a new test cannot
// quietly fall back to the one-second default.
//
//forge:exclude-contract: test-harness helpers over the Temporal SDK's own testsuite and worker types; no collaborator to fake
package temporaltest

import (
	"os"
	"time"

	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
)

// DeadlockDetectionTimeout is how long a workflow goroutine may run without
// yielding before a test harness fails it.
//
// It is the production worker's value (internal/workersetup/setup.go), so a
// test fails on exactly the code production would fail on, and on nothing
// else. The margin over the SDK's 1s is the point: 30x absorbs a heavily
// loaded machine, while a goroutine that genuinely never yields still fails
// in 30s — well inside `go test`'s timeout, and with the stack of the
// goroutine that hung, which a test timeout does not give you.
const DeadlockDetectionTimeout = 30 * time.Second

// WorkerOptions returns opts with the harness deadlock detector applied. An
// explicit DeadlockDetectionTimeout in opts is kept.
//
// Use it for any worker a test builds (worker.New against a dev server), and
// for any later env.SetWorkerOptions on a workflow environment: SetWorkerOptions
// REPLACES the environment's options wholesale, so passing a bare
// worker.Options{} would silently put the one-second default back.
//
// Under TEMPORAL_DEBUG the timeout is left unset so the SDK's own debug mode
// (no deadlock detection, for sitting on a breakpoint) still applies.
func WorkerOptions(opts worker.Options) worker.Options {
	if opts.DeadlockDetectionTimeout == 0 && os.Getenv("TEMPORAL_DEBUG") == "" {
		opts.DeadlockDetectionTimeout = DeadlockDetectionTimeout
	}
	return opts
}

// WorkflowTestSuite is testsuite.WorkflowTestSuite whose workflow environments
// use DeadlockDetectionTimeout. The zero value is ready to use, exactly like
// the SDK type it embeds.
type WorkflowTestSuite struct {
	testsuite.WorkflowTestSuite
}

// NewTestWorkflowEnvironment shadows the embedded SDK method, so every
// existing `suite.NewTestWorkflowEnvironment()` call picks up the harness
// timeout without changing.
func (s *WorkflowTestSuite) NewTestWorkflowEnvironment() *testsuite.TestWorkflowEnvironment {
	env := s.WorkflowTestSuite.NewTestWorkflowEnvironment()
	env.SetWorkerOptions(WorkerOptions(worker.Options{}))
	return env
}
