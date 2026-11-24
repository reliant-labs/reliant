// Copyright (c) 2026 Reliant Labs
package runtime

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/telemetry"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	wfobserve "github.com/reliant-labs/reliant/internal/workflow/observe"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// ============================================================================
// Observing the nodes the workflow completes itself
// (dev-docs/design/workflow-observe.md, section 4, site B)
// ============================================================================
//
// compute, join, router, loop, workflow, approval and ask_question nodes, a
// tool batch the workflow assembles (spawn/ask_user calls) or settles after an
// interrupt, and a tolerated failure all complete in workflow code: no
// activity runs the node, so the ActivityWrapper never sees it. Their observe
// block is recorded by the RecordNodeObservation local activity, which the
// workflow schedules where the node's output is published (the same places a
// workflow-side save_message is written). An activity-backed node that
// succeeded is observed by its wrapper instead (RunningStep.ObserveDelegated),
// so no node is observed twice.
//
// What the workflow decides is only WHETHER to record, and that is a pure
// function of the definition: the node has an effective observe block that
// fits its size cap. The evaluation itself runs in the local activity, through
// the same nodeObserver as the wrapper: the 50 ms budget reads a clock that
// workflow code must not, the CEL is evaluated once instead of on every
// replay, and a local activity's argument is never written to history, so the
// output it carries costs nothing.
//
// Recording never changes what the run does. The local activity returns no
// error and is not retried; a failure to schedule or run it is logged here and
// the node completes as it would have.

// observeWorkflowNodesChangeID gates the RecordNodeObservation local activity.
// A run that began before the release replays with no marker, answers
// DefaultVersion at every observed node and records nothing, matching its
// history; a run after it records version 1 at its first observed node.
// Asked only at a node that has an observe block, so a run without one never
// records the marker. Pinned by replaytest's frozen
// 2026-11-24-observe-unrecorded set.
const observeWorkflowNodesChangeID = "observe-workflow-nodes"

// observeLocalActivityTimeout bounds one recording: the evaluation's budget
// plus the store's own 5 s write deadline (detachedWriteContext), with room.
const observeLocalActivityTimeout = 10 * time.Second

// observeCompleted records the observe block of node, which this executor's
// scope completed in workflow code with output (the value the graph sees as
// nodes.<id>). durationMs is node.duration_ms, 0 where the workflow does not
// time the node. A node with no effective block, a skipped node and a
// cancelled scope record nothing.
func (e *StepExecutor) observeCompleted(node *reliantv1.Node, output map[string]interface{}, durationMs int64) {
	if e == nil || node == nil || e.ctx == nil || e.ctx.Err() != nil {
		return
	}
	req := e.observeRequest(node)
	if req == nil || model.IsSkippedOutput(output) {
		return
	}
	if workflow.GetVersion(e.ctx, observeWorkflowNodesChangeID, workflow.DefaultVersion, 1) < 1 {
		return
	}

	thread := ""
	if e.execContext != nil {
		thread = e.execContext.Thread
	}
	input := types.RecordNodeObservationInput{
		ChatID:     e.chatID,
		Thread:     thread,
		WorkflowID: e.workflowID,
		StepID:     node.GetId(),
		NodeType:   node.GetType(),
		NodePath:   joinNodePath(e.nodePathPrefix, node.GetId()),
		Telemetry:  e.runtimeTelemetry(),
		Attempt:    1,
		DurationMs: durationMs,
		Observe:    detachObserveRequest(req),
		// The local activity reads it on its own goroutine while this
		// workflow's other coroutines may run: it gets its own copy.
		Output: deepCopyJSONMap(output),
	}
	if e.loopNodeID != "" {
		input.LoopNodeID, input.LoopIteration = e.loopNodeID, e.loopIteration
	}

	ctx := workflow.WithLocalActivityOptions(e.ctx, workflow.LocalActivityOptions{
		StartToCloseTimeout: observeLocalActivityTimeout,
		// One attempt: the activity never fails on purpose, and a recording
		// that could not be made is not worth holding the node for.
		RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1},
	})
	if err := workflow.ExecuteLocalActivity(ctx, types.RecordNodeObservationActivityName, input).Get(e.ctx, nil); err != nil {
		e.logger.Warn("[StepExecutor] Could not record the node's observation; the node is unaffected",
			"stepID", node.GetId(), "error", err)
	}
}

// detachObserveRequest copies the parts of req a worker goroutine reads that
// the workflow's own maps may still share: the inputs and the iteration item.
// The block and the contexts are read-only from here on.
func detachObserveRequest(req *types.ObserveRequest) *types.ObserveRequest {
	cp := *req
	cp.Inputs = deepCopyJSONMap(req.Inputs)
	if req.Iter != nil {
		iter := *req.Iter
		iter.Item = deepCopyJSONValue(req.Iter.Item)
		cp.Iter = &iter
	}
	return &cp
}

// recordNodeObservation is the RecordNodeObservation local activity: it
// evaluates and records a workflow-completed node's observe block, through
// the same nodeObserver, store, recorder and clock as the ActivityWrapper.
//
// The run-scope interceptor opens the node's workflow.node span around it,
// from the input's step_id and node_type (Temporal hands worker interceptors
// to local activities too); its duration is the recording's, not the node's,
// so the span says so (telemetry.AttrNodeRecorded).
//
// It returns no error, ever: a failure is logged and counted, and the node it
// observes has already completed.
func (r *ActivityRegistry) recordNodeObservation(ctx context.Context, in types.RecordNodeObservationInput) error {
	observer := r.observer()
	defer func() {
		if p := recover(); p != nil {
			logging.WarnContext(ctx, "[RecordNodeObservation] Observing the node panicked; the node is unaffected",
				"stepID", in.StepID, "panic", fmt.Sprint(p))
			observer.rec().CountErrors(ctx, []wfobserve.Error{{Kind: telemetry.ObserveErrorPanic}})
		}
	}()

	trace.SpanFromContext(ctx).SetAttributes(attribute.Bool(telemetry.AttrNodeRecorded, true))
	runID := ""
	if t := in.Telemetry; t != nil {
		runID = t.RootRunID
		if runID == "" {
			runID = t.RunID
		}
	}
	observer.observe(ctx, observedNode{
		Request:       in.Observe,
		Output:        in.Output,
		ChatID:        in.ChatID,
		WorkflowID:    in.WorkflowID,
		RunID:         runID,
		StepID:        in.StepID,
		NodePath:      in.NodePath,
		LoopNodeID:    in.LoopNodeID,
		LoopIteration: in.LoopIteration,
		Attempt:       in.Attempt,
		DurationMs:    in.DurationMs,
	})
	return nil
}

// observer is the registry's nodeObserver: the settings every wrapper copies
// at registration, read here at execution time.
func (r *ActivityRegistry) observer() nodeObserver {
	return nodeObserver{
		writer:    r.observationWriter,
		evaluator: r.observeEvaluator,
		recorder:  r.observeRecorder,
		now:       r.observeClock,
		logPrefix: "[RecordNodeObservation]",
	}
}

// observeJoin observes the join processJoinEvents just fired: its output is
// already in nodeOutputs, and its node is in wf, the graph the join belongs to.
func observeJoin(e *StepExecutor, wf *reliantv1.Workflow, joinID string, nodeOutputs map[string]interface{}) {
	out, _ := nodeOutputs[joinID].(map[string]interface{})
	e.observeCompleted(model.FindNode(wf, joinID), out, 0)
}
