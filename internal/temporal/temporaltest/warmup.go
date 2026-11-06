// Copyright (c) 2025 Reliant Labs

package temporaltest

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// warmUpBudget bounds the warm-up's own run. It is generous on purpose: the
// warm-up exists to absorb a slow cold start, so it must not fail on one.
const warmUpBudget = 10 * time.Minute

const warmUpName = "TemporalTestWarmUp"

var warmUpOnce sync.Once

// WarmUp pays the Temporal SDK's one-time per-process cost before any caller's
// clock starts. NewTestWorkflowEnvironment calls it, so a harness never needs to.
//
// The cost: the first activity a process dispatches through any test
// environment makes the SDK hash the whole running executable to derive a
// default build id (internal getBinaryChecksum: an md5 of os.Executable(), held
// under a process-wide lock). The test environment offers no option to supply a
// build id instead. For the reliant binary that is hundreds of megabytes,
// measured at ~0.5s on a quiet machine and over 20s at load average ~100.
// Without this it is paid INSIDE the first workflow task of the first run: it
// counted against the first scenario's 20s run budget (which then failed as a
// "non-terminating loop" in a workflow with no loops), and against the
// deadlock detector of whichever test ran first.
//
// The warm-up runs one no-op activity through a throwaway environment. Nothing
// it does is visible to later environments except the cached checksum, which is
// exactly the value the SDK would have computed for them anyway. A failure here
// is ignored: the later run then pays the cost itself, as it did before.
func WarmUp() {
	warmUpOnce.Do(func() {
		defer func() { _ = recover() }()
		var suite testsuite.WorkflowTestSuite
		suite.SetLogger(log.NewStructuredLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
		env := suite.NewTestWorkflowEnvironment()
		env.SetWorkerOptions(worker.Options{DeadlockDetectionTimeout: warmUpBudget})
		env.SetTestTimeout(warmUpBudget)
		env.RegisterActivityWithOptions(func(context.Context) error { return nil },
			activity.RegisterOptions{Name: warmUpName})
		env.RegisterWorkflowWithOptions(func(ctx workflow.Context) error {
			ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: warmUpBudget})
			return workflow.ExecuteActivity(ctx, warmUpName).Get(ctx, nil)
		}, workflow.RegisterOptions{Name: warmUpName})
		env.ExecuteWorkflow(warmUpName)
	})
}
