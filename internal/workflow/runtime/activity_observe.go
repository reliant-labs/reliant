// Copyright (c) 2026 Reliant Labs
package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/trace"
	"go.temporal.io/sdk/activity"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/telemetry"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	wfobserve "github.com/reliant-labs/reliant/internal/workflow/observe"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// ============================================================================
// A node's observe: block is evaluated by whoever executes the node.
// ============================================================================
//
// For an activity-backed node that is the ActivityWrapper, right after the
// node's save_message is written (so a save that fails retries the activity
// before anything is observed) and before the message-only fields are cleared
// (so the result is still whole). The workflow attaches a types.ObserveRequest
// at dispatch (observe_request.go).
//
// Observability can degrade; it can never change what a run does. Nothing
// here returns an error to the activity: an expression that fails, a block
// over its budget, a panic, or a store that refuses the row is logged,
// counted and dropped, and the activity's result goes back unchanged.

// ObservationWriter stores a node observation. *db.Repo satisfies it.
type ObservationWriter interface {
	RecordNodeObservation(ctx context.Context, obs *db.NodeObservation, maxPerRun int) (db.NodeObservationWrite, error)
}

// SetObservationWriter installs the store every wrapped activity records its
// node's observation in. Read at execution time, so registration order does
// not matter. Without one, observations are emitted as telemetry only.
func (r *ActivityRegistry) SetObservationWriter(w ObservationWriter) {
	r.observationWriter = w
}

// observeRequestFrom finds the observe request in an activity input:
// ActivityInput carries it on Runtime, the run step's flat input at its top
// level. nil when the workflow attached none.
func observeRequestFrom(input interface{}) (*types.ObserveRequest, error) {
	if ai, ok := input.(types.ActivityInput); ok {
		return ai.Runtime.Observe, nil
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, nil
	}
	var envelope struct {
		Observe *types.ObserveRequest `json:"observe"`
		Runtime *struct {
			Observe *types.ObserveRequest `json:"observe"`
		} `json:"runtime"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		// Not a JSON object (a primitive, a list), or a request this build
		// cannot read: either way, nothing to observe.
		return nil, err
	}
	if envelope.Observe != nil {
		return envelope.Observe, nil
	}
	if envelope.Runtime != nil {
		return envelope.Runtime.Observe, nil
	}
	return nil, nil
}

// observeNode evaluates and records the node's observe block over the
// activity's result. It never fails, and it never panics out.
func (w *ActivityWrapper[I, O]) observeNode(
	ctx context.Context,
	input I,
	inputInfo activityInputInfo,
	info activity.Info,
	result O,
	durationMs int64,
) {
	activityType := info.ActivityType.Name
	defer func() {
		if r := recover(); r != nil {
			logging.WarnContext(ctx, "[ActivityWrapper] Observing the node panicked; the node is unaffected",
				"activityType", activityType,
				"stepID", inputInfo.StepID,
				"panic", fmt.Sprint(r))
			w.recorder().CountErrors(ctx, []wfobserve.Error{{Kind: telemetry.ObserveErrorPanic}})
		}
	}()

	req, err := observeRequestFrom(input)
	if err != nil {
		logging.DebugContext(ctx, "[ActivityWrapper] Could not read the node's observe request; not observing it",
			"activityType", activityType, "stepID", inputInfo.StepID, "error", err)
		return
	}
	if req == nil || req.Block == nil {
		return
	}
	output, err := activityResultMap(resultForSave(result))
	if err != nil {
		logging.WarnContext(ctx, "[ActivityWrapper] Could not render the result for the node's observe block; not observing it",
			"activityType", activityType, "stepID", inputInfo.StepID, "error", err)
		return
	}
	output = normalizeActivityOutput(output, activityType)
	// A node that recorded a skip did not run: there is nothing to observe.
	if model.IsSkippedOutput(output) {
		return
	}

	workflowCtx := req.Workflow
	res := w.observeEvaluator.Evaluate(req.Block, wfobserve.Scope{
		Output:   output,
		Inputs:   req.Inputs,
		Workflow: &workflowCtx,
		Iter:     req.Iter,
		Node: model.ObservedNode{
			ID:         req.NodeID,
			Type:       req.NodeType,
			Path:       req.NodePath,
			Attempt:    int64(info.Attempt),
			DurationMs: durationMs,
		},
	})

	wfobserve.Annotate(trace.SpanFromContext(ctx), res)
	rec := w.recorder()
	workflowName := telemetry.RunScopeFromContext(ctx).WorkflowName
	if workflowName == "" {
		workflowName = req.Workflow.Name
	}
	rec.Record(ctx, wfobserve.MetricBase{WorkflowName: workflowName, NodeID: req.NodeID, NodeType: req.NodeType}, res.Metrics)
	rec.CountErrors(ctx, res.Errors)
	if len(res.Errors) > 0 {
		logging.DebugContext(ctx, "[ActivityWrapper] Node's observe block had expressions that failed",
			"activityType", activityType, "stepID", inputInfo.StepID, "errors", len(res.Errors))
	}

	if res.HasRecord() {
		w.writeObservation(ctx, req, inputInfo, info, res)
	}
}

// writeObservation stores res best-effort, exactly as a step row is written
// (writeStepExecution): on a detached context with its own short deadline,
// and a failure is logged and counted, never returned.
func (w *ActivityWrapper[I, O]) writeObservation(
	ctx context.Context,
	req *types.ObserveRequest,
	inputInfo activityInputInfo,
	info activity.Info,
	res wfobserve.Result,
) {
	rec := w.recorder()
	var writer ObservationWriter
	if w.observationWriter != nil {
		writer = w.observationWriter()
	}
	workflowID := inputInfo.WorkflowID
	if workflowID == "" {
		workflowID = info.WorkflowExecution.ID
	}
	chatID := inputInfo.ChatID
	if chatID == "" {
		chatID = req.Workflow.ChatID
	}
	stepID := inputInfo.StepID
	if stepID == "" {
		stepID = req.NodeID
	}
	if writer == nil || workflowID == "" || chatID == "" || stepID == "" {
		logging.DebugContext(ctx, "[ActivityWrapper] Not storing the node's observation: no store or no identity",
			"workflowID", workflowID, "chatID", chatID, "stepID", stepID, "hasWriter", writer != nil)
		return
	}
	nodePath := req.NodePath
	if nodePath == "" {
		nodePath = inputInfo.NodePath
	}
	row := res.Row()
	obs := &db.NodeObservation{
		ID:         uuid.New().String(),
		ChatID:     chatID,
		WorkflowID: workflowID,
		RunID:      inputInfo.RunID,
		StepID:     stepID,
		NodePath:   nodePath,
		Attempt:    info.Attempt,
		Source:     db.NodeObservationSourceWorkflow,
		Attributes: row.Attributes,
		Events:     row.Events,
		Errors:     row.Errors,
		Truncated:  row.Truncated,
		CreatedAt:  w.observeNow(),
	}
	if inputInfo.LoopNodeID != "" {
		loopNode, iteration := inputInfo.LoopNodeID, int64(inputInfo.LoopIteration)
		obs.LoopNodeID, obs.LoopIteration = &loopNode, &iteration
	}

	writeCtx, cancel := detachedWriteContext(ctx)
	defer cancel()
	outcome, err := writer.RecordNodeObservation(writeCtx, obs, wfobserve.MaxRowsPerRun)
	switch {
	case err != nil:
		logging.WarnContext(ctx, "[ActivityWrapper] Failed to store the node's observation; its telemetry was still emitted",
			"workflowID", workflowID, "stepID", stepID, "error", err)
		rec.CountErrors(ctx, []wfobserve.Error{{Kind: telemetry.ObserveErrorWrite}})
		rec.CountDropped(ctx, telemetry.ObserveDropWriteFailed)
	case outcome == db.NodeObservationCapped:
		logging.DebugContext(ctx, "[ActivityWrapper] Run holds its cap of stored observations; this one was not stored",
			"workflowID", workflowID, "stepID", stepID, "cap", wfobserve.MaxRowsPerRun)
		rec.CountDropped(ctx, telemetry.ObserveDropRowCap)
	}
}

// recorder is the wrapper's metrics recorder: its registry's, else the
// process's.
func (w *ActivityWrapper[I, O]) recorder() *wfobserve.Recorder {
	if w.observeRecorder != nil {
		return w.observeRecorder
	}
	return wfobserve.DefaultRecorder()
}

// observeNow is the stored observation's created_at clock.
func (w *ActivityWrapper[I, O]) observeNow() time.Time {
	if w.observeClock != nil {
		return w.observeClock()
	}
	return time.Now()
}
