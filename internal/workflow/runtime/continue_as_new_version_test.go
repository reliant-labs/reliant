// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/sdk/client"
	tlog "go.temporal.io/sdk/log"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
)

// The soft continue-as-new threshold went from 40k events to 10k
// (lowerContinueAsNewChangeID). A run whose history is already between the two
// when that deploys recorded no handoff at those boundaries, so the change
// must be versioned — and the version must still let such a run hand off at
// its first LIVE boundary rather than carry on to 40k, which is the whole
// point for the long multi-spawn chats in flight (adcee226, 15k+ events).
//
// These tests record real histories on an ephemeral Temporal dev server and
// replay them with the SDK's replayer, with the same workflow name registered
// to three versions of the code:
//
//   - legacy: the code before the change (no GetVersion; the 40k threshold),
//   - current: the code now (currentHistoryPressure, the production gate),
//   - unversioned: the change made WITHOUT the gate (10k, no GetVersion).
//
// The probe workflow pads its history with SideEffect markers past the new
// threshold and then takes boundaries (a timer, a new workflow task, a
// pressure check). A soft-pressure check hands off (CONTINUE_AS_NEW) instead
// of starting the next timer, so a disagreement between recording and replay
// is a command mismatch the replayer reports.

const canProbeWorkflowName = "can-threshold-probe"

// canProbeInput: pad the history to Pad events in batches, then take Boundaries
// boundaries. WaitForSignal parks after the first boundary until "go" — the
// moment a deploy can land mid-run.
type canProbeInput struct {
	Pad           int
	Boundaries    int
	WaitForSignal bool
}

type canPressureFunc func(ctx workflow.Context, lowered *bool) historyPressure

func legacyPressure(ctx workflow.Context, _ *bool) historyPressure {
	info := workflow.GetInfo(ctx)
	return historyPressureFor(info.GetCurrentHistoryLength(), info.GetCurrentHistorySize(), false)
}

func currentPressure(ctx workflow.Context, lowered *bool) historyPressure {
	return currentHistoryPressure(ctx, "", lowered)
}

func unversionedPressure(ctx workflow.Context, _ *bool) historyPressure {
	info := workflow.GetInfo(ctx)
	return historyPressureFor(info.GetCurrentHistoryLength(), info.GetCurrentHistorySize(), true)
}

func canProbe(pressure canPressureFunc) func(workflow.Context, canProbeInput) (int, error) {
	return func(ctx workflow.Context, in canProbeInput) (int, error) {
		const batch = 2000
		for padded := 0; padded < in.Pad; padded += batch {
			for i := 0; i < batch && padded+i < in.Pad; i++ {
				workflow.SideEffect(ctx, func(workflow.Context) interface{} { return nil })
			}
			if err := workflow.Sleep(ctx, time.Millisecond); err != nil {
				return 0, err
			}
		}
		var lowered bool
		goSignal := workflow.GetSignalChannel(ctx, "go")
		for b := 0; b < in.Boundaries; b++ {
			if err := workflow.Sleep(ctx, time.Millisecond); err != nil {
				return 0, err
			}
			if b == 1 && in.WaitForSignal {
				goSignal.Receive(ctx, nil)
			}
			if pressure(ctx, &lowered) >= historyPressureSoft {
				return 0, workflow.NewContinueAsNewError(ctx, canProbeWorkflowName, canProbeInput{})
			}
		}
		return workflow.GetInfo(ctx).GetCurrentHistoryLength(), nil
	}
}

type canProbeServer struct {
	t      *testing.T
	client client.Client
}

func startCANProbeServer(t *testing.T) *canProbeServer {
	t.Helper()
	if testing.Short() {
		t.Skip("boots a Temporal dev server and records a >10k-event history; runs in task test (full mode)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	server, err := testsuite.StartDevServer(ctx, testsuite.DevServerOptions{LogLevel: "never"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = server.Stop() })
	return &canProbeServer{t: t, client: server.Client()}
}

// worker runs the probe registered as pressure's version of the code. A
// replacement worker has nothing cached, so it replays the run from history.
func (s *canProbeServer) worker(queue string, pressure canPressureFunc) worker.Worker {
	s.t.Helper()
	w := worker.New(s.client, queue, temporaltest.WorkerOptions(worker.Options{}))
	w.RegisterWorkflowWithOptions(canProbe(pressure), workflow.RegisterOptions{Name: canProbeWorkflowName})
	require.NoError(s.t, w.Start())
	return w
}

func (s *canProbeServer) start(queue, id string, in canProbeInput) client.WorkflowRun {
	s.t.Helper()
	run, err := s.client.ExecuteWorkflow(context.Background(), client.StartWorkflowOptions{
		ID: id, TaskQueue: queue, WorkflowExecutionTimeout: 5 * time.Minute,
	}, canProbeWorkflowName, in)
	require.NoError(s.t, err)
	return run
}

func (s *canProbeServer) history(id, runID string) *historypb.History {
	s.t.Helper()
	iter := s.client.GetWorkflowHistory(context.Background(), id, runID, false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	h := &historypb.History{}
	for iter.HasNext() {
		ev, err := iter.Next()
		require.NoError(s.t, err)
		h.Events = append(h.Events, ev)
	}
	return h
}

func replayCANProbe(h *historypb.History, pressure canPressureFunc) error {
	r := worker.NewWorkflowReplayer()
	r.RegisterWorkflowWithOptions(canProbe(pressure), workflow.RegisterOptions{Name: canProbeWorkflowName})
	return r.ReplayWorkflowHistory(tlog.NewStructuredLogger(slog.New(slog.NewTextHandler(io.Discard, nil))), h)
}

func closeEvent(h *historypb.History) enumspb.EventType {
	return h.Events[len(h.Events)-1].GetEventType()
}

func versionMarkers(h *historypb.History) int {
	n := 0
	for _, ev := range h.Events {
		if a := ev.GetMarkerRecordedEventAttributes(); a != nil && a.GetMarkerName() == "Version" {
			if strings.Contains(fmt.Sprint(a.GetDetails()), lowerContinueAsNewChangeID) {
				n++
			}
		}
	}
	return n
}

// TestLoweredCANThreshold_VersionGate records the two shapes and replays each
// against every version of the code.
func TestLoweredCANThreshold_VersionGate(t *testing.T) {
	s := startCANProbeServer(t)
	in := canProbeInput{Pad: continueAsNewEventThreshold + 200, Boundaries: 3}

	// A run recorded by the code before the change: past 10k, it takes its
	// boundaries and completes without handing off.
	w := s.worker("can-legacy", legacyPressure)
	run := s.start("can-legacy", "can-probe-legacy", in)
	var length int
	require.NoError(t, run.Get(context.Background(), &length))
	w.Stop()
	require.Greater(t, length, continueAsNewEventThreshold, "the probe must actually cross the new threshold")
	legacy := s.history(run.GetID(), run.GetRunID())
	require.Equal(t, enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_COMPLETED, closeEvent(legacy))

	require.NoError(t, replayCANProbe(legacy, currentPressure),
		"a history recorded before the change must replay on the current code (no marker: the legacy threshold)")
	err := replayCANProbe(legacy, unversionedPressure)
	require.Error(t, err, "the fixture must catch the change made without its GetVersion gate")

	// A run recorded by the current code hands off at its first boundary past
	// 10k, records the version once, and replays.
	w = s.worker("can-current", currentPressure)
	run = s.start("can-current", "can-probe-current", in)
	firstRunID := run.GetRunID()
	err = run.GetWithOptions(context.Background(), nil, client.WorkflowRunGetOptions{DisableFollowingRuns: true})
	w.Stop()
	var canErr *workflow.ContinueAsNewError
	require.True(t, errors.As(err, &canErr), "the run must hand off past 10k, got %v", err)
	current := s.history(run.GetID(), firstRunID)
	require.Equal(t, enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_CONTINUED_AS_NEW, closeEvent(current))
	require.Equal(t, 1, versionMarkers(current))
	require.NoError(t, replayCANProbe(current, currentPressure))
}

// TestLoweredCANThreshold_InFlightRunRollsOverOnDeploy is the deploy itself: a
// run past 10k on the old code is parked when the worker is replaced by the
// current code. The new worker replays the old boundaries without handing off
// (they have no marker), and hands off at the first live one.
func TestLoweredCANThreshold_InFlightRunRollsOverOnDeploy(t *testing.T) {
	s := startCANProbeServer(t)
	const queue = "can-deploy"

	old := s.worker(queue, legacyPressure)
	run := s.start(queue, "can-probe-deploy", canProbeInput{Pad: continueAsNewEventThreshold + 200, Boundaries: 4, WaitForSignal: true})
	runID := run.GetRunID()
	// Parked: every padding timer and both boundary timers have fired, and
	// the task that read the second one completed.
	const padBatches = (continueAsNewEventThreshold + 200 + 1999) / 2000
	require.Eventually(t, func() bool {
		h := s.history(run.GetID(), run.GetRunID())
		fired := 0
		for _, ev := range h.Events {
			if ev.GetEventType() == enumspb.EVENT_TYPE_TIMER_FIRED {
				fired++
			}
		}
		return fired == padBatches+2 && closeEvent(h) == enumspb.EVENT_TYPE_WORKFLOW_TASK_COMPLETED
	}, 3*time.Minute, 200*time.Millisecond, "the run must reach its parked boundary on the old code")
	old.Stop()

	upgraded := s.worker(queue, currentPressure)
	defer upgraded.Stop()
	require.NoError(t, s.client.SignalWorkflow(context.Background(), run.GetID(), runID, "go", nil))
	err := run.GetWithOptions(context.Background(), nil, client.WorkflowRunGetOptions{DisableFollowingRuns: true})
	var canErr *workflow.ContinueAsNewError
	require.True(t, errors.As(err, &canErr), "the in-flight run must hand off on the new code, got %v", err)

	h := s.history(run.GetID(), runID)
	require.Equal(t, enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_CONTINUED_AS_NEW, closeEvent(h))
	require.Equal(t, 1, versionMarkers(h), "one marker, at the first live boundary")
	require.NoError(t, replayCANProbe(h, currentPressure), "the mixed history replays on the current code")
}
