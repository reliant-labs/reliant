// Copyright (c) 2025 Reliant Labs

// Package replaytest pins replay compatibility of the DynamicWorkflow against
// recorded Temporal histories.
//
// Why this exists: Temporal replays a workflow's recorded event history
// through the CURRENT workflow code every time a worker picks up an in-flight
// run (worker restart, deploy, sticky-cache eviction). If the code's sequence
// of workflow commands (activities, timers, signals-handling, child workflows,
// side effects, ...) no longer matches the recorded history, the run is
// wedged forever with WORKFLOW_TASK_FAILED_CAUSE_NON_DETERMINISTIC_ERROR
// (TMPRL1100) — exactly the production incident this suite was built after.
//
// The fixtures under fixtures/ are real histories captured from an ephemeral
// Temporal dev server running the production worker registration path
// (workersetup.StartWorker) with a scripted LLM. They are the contract:
// "current workflow code must stay replay-compatible with THESE histories."
//
// See fixtures/README.md for what each fixture pins and how to regenerate
// (make replay-fixtures) — and for the tradeoff you are accepting when you
// regenerate instead of making a change replay-compatible.
package replaytest

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"go.temporal.io/sdk/interceptor"
	tlog "go.temporal.io/sdk/log"
	"go.temporal.io/sdk/worker"
	sdkworkflow "go.temporal.io/sdk/workflow"

	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/observability"
	rtemporal "github.com/reliant-labs/reliant/internal/temporal"
	"github.com/reliant-labs/reliant/internal/workersetup"
	v2workflow "github.com/reliant-labs/reliant/internal/workflow"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// generatorMode is flipped to true by an init() in the build-tag-gated
// generator files (-tags replayfixtures). In generator runs the replay test
// skips: the fixtures on disk are being rewritten by the same binary, so
// replaying them mid-run would be meaningless. `make replay-fixtures` runs
// the untagged replay test immediately after generation instead.
var generatorMode bool

// newProductionMirroredReplayer builds a WorkflowReplayer whose registration
// mirrors the production worker EXACTLY. A replayer with mismatched
// registration produces false failures (or false passes), so every knob here
// is traced to its production counterpart:
//
//   - Workflow registration: workersetup.StartWorker registers
//     v2.DynamicWorkflow under the name v2workflow.WorkflowDynamic and
//     workersetup.GenerateTitleWorkflow under "GenerateTitleWorkflow"
//     (internal/workersetup/setup.go). Mirrored 1:1 below.
//   - DataConverter: production clients are built by
//     temporal.NewExternalClient with rtemporal.NewFlexibleDataConverter()
//     (internal/temporal/external_client.go). The replayer must use the same
//     converter or payload decoding diverges from production.
//   - Interceptors: production worker.Options.Interceptors is
//     [observability.NewOTelWorkerInterceptor()] (internal/workersetup/setup.go).
//     Its workflow inbound is a pass-through today, but it is mirrored so a
//     future interceptor that issues workflow commands is covered by this test.
//
// DisableDeadlockDetection: the replayer's fixed 1s deadlock timeout is
// stricter than the production worker's DeadlockDetectionTimeout of 30s;
// large histories replaying CPU-bound (CEL eval, YAML parse) on a loaded CI
// box can exceed 1s without being wrong. Disabled to avoid false failures;
// `go test`'s own timeout still bounds a genuine hang.
func newProductionMirroredReplayer(t *testing.T) worker.WorkflowReplayer {
	t.Helper()

	replayer, err := worker.NewWorkflowReplayerWithOptions(worker.WorkflowReplayerOptions{
		DataConverter:            rtemporal.NewFlexibleDataConverter(),
		Interceptors:             []interceptor.WorkerInterceptor{observability.NewOTelWorkerInterceptor()},
		DisableDeadlockDetection: true,
	})
	if err != nil {
		t.Fatalf("create workflow replayer: %v", err)
	}

	// Mirror of workersetup.StartWorker's workflow registrations.
	replayer.RegisterWorkflowWithOptions(v2.DynamicWorkflow, sdkworkflow.RegisterOptions{
		Name: v2workflow.WorkflowDynamic,
	})
	replayer.RegisterWorkflowWithOptions(workersetup.GenerateTitleWorkflow, sdkworkflow.RegisterOptions{
		Name: "GenerateTitleWorkflow",
	})

	return replayer
}

// replayLogger returns a quiet Temporal logger; set REPLAY_VERBOSE=1 to see
// the SDK's replay logging while debugging a failure.
func replayLogger() tlog.Logger {
	if os.Getenv("REPLAY_VERBOSE") == "1" {
		return tlog.NewStructuredLogger(slog.Default())
	}
	silent := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 10}))
	return tlog.NewStructuredLogger(silent)
}

// quietProcessLogging silences the repo's own logging package, which some
// workflow-sandbox code calls directly (bypassing the Temporal logger and its
// replay suppression). Same approach as the e2e story suite. Set
// REPLAY_VERBOSE=1 to keep the output while debugging.
func quietProcessLogging() {
	if os.Getenv("REPLAY_VERBOSE") == "1" {
		return
	}
	logging.DefaultOutput = io.Discard
	logging.Setup(slog.LevelError + 10)
}

// TestReplayFixtures replays every checked-in history fixture through the
// current workflow code. Any failure here means: if this change deploys,
// in-flight workflow runs whose histories look like the failing fixture will
// wedge with a non-determinism error (TMPRL1100).
func TestReplayFixtures(t *testing.T) {
	if generatorMode {
		t.Skip("running under -tags replayfixtures: fixtures are being regenerated by this binary; run `go test` (untagged) afterwards to verify")
	}
	quietProcessLogging()

	fixtures, err := filepath.Glob(filepath.Join("fixtures", "*.json"))
	if err != nil {
		t.Fatalf("glob fixtures: %v", err)
	}
	if len(fixtures) == 0 {
		t.Fatal("no history fixtures found under fixtures/ — the replay-compatibility gate is not testing anything; run `make replay-fixtures` and check the results in")
	}

	for _, fixture := range fixtures {
		name := filepath.Base(fixture)
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			replayer := newProductionMirroredReplayer(t)
			if err := replayer.ReplayWorkflowHistoryFromJSONFile(replayLogger(), fixture); err != nil {
				t.Fatalf(`replay of fixture %s FAILED: the current workflow code is not replay-compatible with this recorded history.

Your change altered the workflow command sequence (activities, timers, side
effects, signals, workflow.Go). Regenerate the fixtures and commit them with
your change:

    make replay-fixtures

Do NOT reach for workflow.GetVersion to keep old histories on the old path.
We cut to the new code and delete the old one; a version gate is a permanent
fork that can never be removed. See
internal/workflow/runtime/replaytest/fixtures/README.md.

Know what this means on deploy: in-flight runs with this shape wedge with
TMPRL1100 and are recovered by the reconciler + resume path, losing the old
run's in-memory node outputs. That is the accepted cost of cutting cleanly —
this test exists so it is never a surprise.

Replay error: %v`, name, err)
			}
		})
	}
}
