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
// at dispatch (observe_request.go). A node the workflow completes itself is
// observed by the RecordNodeObservation local activity (workflow_observe.go).
// Both go through one nodeObserver, so they share every rule below.
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
	observer := w.observer()
	defer func() {
		if r := recover(); r != nil {
			logging.WarnContext(ctx, "[ActivityWrapper] Observing the node panicked; the node is unaffected",
				"activityType", activityType,
				"stepID", inputInfo.StepID,
				"panic", fmt.Sprint(r))
			observer.rec().CountErrors(ctx, []wfobserve.Error{{Kind: telemetry.ObserveErrorPanic}})
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

	workflowID := inputInfo.WorkflowID
	if workflowID == "" {
		workflowID = info.WorkflowExecution.ID
	}
	observer.observe(ctx, observedNode{
		Request:       req,
		Output:        output,
		ChatID:        inputInfo.ChatID,
		WorkflowID:    workflowID,
		RunID:         inputInfo.RunID,
		StepID:        inputInfo.StepID,
		NodePath:      inputInfo.NodePath,
		LoopNodeID:    inputInfo.LoopNodeID,
		LoopIteration: inputInfo.LoopIteration,
		Attempt:       info.Attempt,
		DurationMs:    durationMs,
	})
}

// observer is the wrapper's nodeObserver, from the settings its registry
// copied in at registration.
func (w *ActivityWrapper[I, O]) observer() nodeObserver {
	var writer ObservationWriter
	if w.observationWriter != nil {
		writer = w.observationWriter()
	}
	return nodeObserver{
		writer:    writer,
		evaluator: w.observeEvaluator,
		recorder:  w.observeRecorder,
		now:       w.observeClock,
		logPrefix: "[ActivityWrapper]",
	}
}

// ============================================================================
// The rules both sites share
// ============================================================================

// nodeObserver evaluates a completed node's observe block and records what it
// produced: the node span's attributes and events, the metrics, and, when the
// result is worth one, the stored row. The zero value is production's, with
// no store (telemetry only).
type nodeObserver struct {
	// writer stores the row; nil records telemetry only.
	writer ObservationWriter
	// evaluator, recorder and now evaluate the block, emit its metrics and
	// stamp its row. Zero values are production's.
	evaluator wfobserve.Evaluator
	recorder  *wfobserve.Recorder
	now       func() time.Time
	// logPrefix names the site in its log lines.
	logPrefix string
}

// observedNode is one completed node to observe: its observe request, the
// output it completed with (as the workflow sees it), and what its stored row
// is keyed by.
type observedNode struct {
	Request *types.ObserveRequest
	Output  map[string]interface{}

	ChatID     string
	WorkflowID string
	RunID      string
	StepID     string
	NodePath   string
	// LoopNodeID is the loop scope ("" outside a loop); LoopIteration is
	// read only when it is set.
	LoopNodeID    string
	LoopIteration int
	Attempt       int32
	DurationMs    int64
}

// observe evaluates n's block and records the result on the span ctx carries,
// the metrics and the store. It never returns an error: what fails is logged
// and counted. The caller recovers panics.
func (o nodeObserver) observe(ctx context.Context, n observedNode) {
	req := n.Request
	if req == nil || req.Block == nil {
		return
	}
	// A node that recorded a skip did not run: there is nothing to observe.
	if model.IsSkippedOutput(n.Output) {
		return
	}

	workflowCtx := req.Workflow
	res := o.evaluator.Evaluate(req.Block, wfobserve.Scope{
		Output:   n.Output,
		Inputs:   req.Inputs,
		Workflow: &workflowCtx,
		Iter:     req.Iter,
		Node: model.ObservedNode{
			ID:         req.NodeID,
			Type:       req.NodeType,
			Path:       req.NodePath,
			Attempt:    int64(n.Attempt),
			DurationMs: n.DurationMs,
		},
	})

	wfobserve.Annotate(trace.SpanFromContext(ctx), res)
	rec := o.rec()
	workflowName := telemetry.RunScopeFromContext(ctx).WorkflowName
	if workflowName == "" {
		workflowName = req.Workflow.Name
	}
	rec.Record(ctx, wfobserve.MetricBase{WorkflowName: workflowName, NodeID: req.NodeID, NodeType: req.NodeType}, res.Metrics)
	rec.CountErrors(ctx, res.Errors)
	if len(res.Errors) > 0 {
		logging.DebugContext(ctx, o.logPrefix+" Node's observe block had expressions that failed",
			"stepID", n.StepID, "errors", len(res.Errors))
	}

	if res.HasRecord() {
		o.write(ctx, n, res)
	}
}

// write stores res best-effort, exactly as a step row is written
// (writeStepExecution): on a detached context with its own short deadline,
// and a failure is logged and counted, never returned.
func (o nodeObserver) write(ctx context.Context, n observedNode, res wfobserve.Result) {
	req := n.Request
	rec := o.rec()
	chatID := n.ChatID
	if chatID == "" {
		chatID = req.Workflow.ChatID
	}
	stepID := n.StepID
	if stepID == "" {
		stepID = req.NodeID
	}
	if o.writer == nil || n.WorkflowID == "" || chatID == "" || stepID == "" {
		logging.DebugContext(ctx, o.logPrefix+" Not storing the node's observation: no store or no identity",
			"workflowID", n.WorkflowID, "chatID", chatID, "stepID", stepID, "hasWriter", o.writer != nil)
		return
	}
	nodePath := req.NodePath
	if nodePath == "" {
		nodePath = n.NodePath
	}
	row := res.Row()
	obs := &db.NodeObservation{
		ID:         uuid.New().String(),
		ChatID:     chatID,
		WorkflowID: n.WorkflowID,
		RunID:      n.RunID,
		StepID:     stepID,
		NodePath:   nodePath,
		Attempt:    n.Attempt,
		Source:     db.NodeObservationSourceWorkflow,
		Attributes: row.Attributes,
		Events:     row.Events,
		Errors:     row.Errors,
		Truncated:  row.Truncated,
		CreatedAt:  o.clock(),
	}
	if n.LoopNodeID != "" {
		loopNode, iteration := n.LoopNodeID, int64(n.LoopIteration)
		obs.LoopNodeID, obs.LoopIteration = &loopNode, &iteration
	}

	writeCtx, cancel := detachedWriteContext(ctx)
	defer cancel()
	outcome, err := o.writer.RecordNodeObservation(writeCtx, obs, wfobserve.MaxRowsPerRun)
	switch {
	case err != nil:
		logging.WarnContext(ctx, o.logPrefix+" Failed to store the node's observation; its telemetry was still emitted",
			"workflowID", n.WorkflowID, "stepID", stepID, "error", err)
		rec.CountErrors(ctx, []wfobserve.Error{{Kind: telemetry.ObserveErrorWrite}})
		rec.CountDropped(ctx, telemetry.ObserveDropWriteFailed)
	case outcome == db.NodeObservationCapped:
		logging.DebugContext(ctx, o.logPrefix+" Run holds its cap of stored observations; this one was not stored",
			"workflowID", n.WorkflowID, "stepID", stepID, "cap", wfobserve.MaxRowsPerRun)
		rec.CountDropped(ctx, telemetry.ObserveDropRowCap)
	}
}

// rec is the observer's metrics recorder, else the process's.
func (o nodeObserver) rec() *wfobserve.Recorder {
	if o.recorder != nil {
		return o.recorder
	}
	return wfobserve.DefaultRecorder()
}

// clock is the stored observation's created_at clock.
func (o nodeObserver) clock() time.Time {
	if o.now != nil {
		return o.now()
	}
	return time.Now()
}
