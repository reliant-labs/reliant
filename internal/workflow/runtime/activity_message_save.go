// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/logging"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// ============================================================================
// Rule 1 — a node's save_message is written by whoever executes the node.
// ============================================================================
//
// For an activity-backed node that is the worker: the ActivityWrapper writes
// the message right after the activity returns, for every activity, with no
// per-activity code. The workflow attaches a types.SaveMessageRequest at
// dispatch; its presence is the whole decision. The workflow never dispatches
// a separate SaveMessage activity for such a node, so the activity's result is
// not carried through history a second time as that activity's input.

// MessageWriter persists one resolved message. Implemented by the SaveMessage
// activity's write path and injected at registration (runtime cannot import
// the handlers or threads packages), so a message written by the wrapper and
// one written by the SaveMessage activity go through the same code.
//
// idempotencyKey and attempt carry the same retry semantics as the activity:
// attempt 1 converges on an existing row with that key, a later attempt
// replaces it.
type MessageWriter interface {
	WriteMessage(
		ctx context.Context,
		rtx types.RuntimeContext,
		args *reliantv1.SaveMessageNodeArgs,
		idempotencyKey string,
		attempt int32,
	) (*reliantv1.SaveMessageOutput, error)
}

// SetMessageWriter installs the writer every wrapped activity uses for its
// delegated save_message. Read at execution time, so registration order does
// not matter.
func (r *ActivityRegistry) SetMessageWriter(w MessageWriter) {
	r.messageWriter = w
}

// messageWriteBudget bounds how long the wrapper keeps retrying a failed
// message write before failing the activity (and handing the retry to
// Temporal). Short: the activity's result is already in hand, and a database
// that stays down longer than this is Temporal's retry policy's problem.
const messageWriteBudget = 15 * time.Second

// messageWriteBackoff is the first retry delay; it doubles per attempt.
var messageWriteBackoff = 500 * time.Millisecond

// saveMessageRequestFrom finds the delegated save request in an activity input:
// ActivityInput carries it on Runtime, the run step's flat input at its top
// level. nil when the workflow attached none.
func saveMessageRequestFrom(input interface{}) (*types.SaveMessageRequest, error) {
	if ai, ok := input.(types.ActivityInput); ok {
		return ai.Runtime.SaveMessage, nil
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, nil
	}
	var envelope struct {
		SaveMessage *types.SaveMessageRequest `json:"save_message"`
		Runtime     *struct {
			SaveMessage *types.SaveMessageRequest `json:"save_message"`
		} `json:"runtime"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		// The input is not a JSON object (a primitive, a list): it cannot
		// carry a request.
		var syntaxErr *json.UnmarshalTypeError
		if errors.As(err, &syntaxErr) {
			return nil, nil
		}
		return nil, fmt.Errorf("decoding save_message request: %w", err)
	}
	if envelope.SaveMessage != nil {
		return envelope.SaveMessage, nil
	}
	if envelope.Runtime != nil {
		return envelope.Runtime.SaveMessage, nil
	}
	return nil, nil
}

// activityResultMap renders an activity result exactly as the workflow will
// decode it: the data converter writes proto results with protojson
// (UseProtoNames, unpopulated fields omitted) and everything else with
// encoding/json, and the workflow decodes that JSON into a map.
func activityResultMap(result interface{}) (map[string]interface{}, error) {
	var raw []byte
	var err error
	if pm := protoMessageOf(result); pm != nil {
		raw, err = protojson.MarshalOptions{UseProtoNames: true}.Marshal(pm)
	} else {
		raw, err = json.Marshal(result)
	}
	if err != nil {
		return nil, err
	}
	var out map[string]interface{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// protoMessageOf returns result as a proto.Message, whether the activity
// returns a message pointer or a message value (the converter accepts both).
func protoMessageOf(result interface{}) proto.Message {
	if pm, ok := result.(proto.Message); ok {
		return pm
	}
	return nil
}

// saveActivityMessage performs a delegated save_message after a successful
// activity: resolve it against the result as the workflow will see it, write
// it, and record the "<node>-save" step row the UI keys on.
//
// Returns an error only when the message should have been written and was
// not; the caller fails the activity so Temporal retries it.
func (w *ActivityWrapper[I, O]) saveActivityMessage(
	ctx context.Context,
	input I,
	inputInfo activityInputInfo,
	info activity.Info,
	result O,
) error {
	req, err := saveMessageRequestFrom(input)
	if err != nil {
		return err
	}
	if req == nil || req.Config == nil {
		return nil
	}
	activityType := info.ActivityType.Name
	stepID := inputInfo.StepID

	output, err := activityResultMap(resultForSave(result))
	if err != nil {
		return fmt.Errorf("rendering %s result for save_message: %w", activityType, err)
	}

	workflowID := inputInfo.WorkflowID
	if workflowID == "" {
		workflowID = info.WorkflowExecution.ID
	}

	saveInput, skip, err := resolveDelegatedSaveMessage(req, activityType, output, DelegatedSaveIdentity{
		ChatID:     inputInfo.ChatID,
		Thread:     extractThread(input),
		WorkflowID: workflowID,
		StepID:     stepID,
	})
	if err != nil {
		// Resolution is a pure function of the result and the request, so it
		// fails identically on every retry — and a retry of this activity
		// re-runs the work itself (for CallLLM, the whole LLM call). Fail the
		// step instead; the workflow's retry-exhaustion path pauses the chat
		// with the error rather than looping.
		return temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("save_message for %s: %v", stepID, err), "SaveMessageResolution", err)
	}
	if saveInput == nil {
		logSaveMessageSkip(packageLogger{}, stepID, skip)
		return nil
	}
	if strings.EqualFold(saveInput.Role, "assistant") {
		saveInput.AssistantMessageID = extractInputString(input, "assistant_message_id", "AssistantMessageID")
	}
	if inputInfo.LoopNodeID != "" {
		saveInput.LoopNodeID = inputInfo.LoopNodeID
		saveInput.LoopIteration = inputInfo.LoopIteration
	}

	writer := w.writer()
	if writer == nil {
		return fmt.Errorf("save_message for %s: no message writer registered", stepID)
	}

	// Same key shape as the SaveMessage activity's RunID-scoped key, suffixed
	// so it can never collide with the producing activity's own writes (and
	// deliberately NOT callLLMIdempotencyKey: an interrupted turn's partial is
	// persisted under that key, and converging onto the partial would drop
	// the completed turn's content).
	idempotencyKey := workflowID + "-" + info.WorkflowExecution.RunID + "-" + info.ActivityID + "-save"
	rtx := saveMessageRuntimeContext(saveInput)
	args := buildSaveMessageNode(saveInput).GetSaveMessageNode()

	start := time.Now()
	saveOutput, err := writeMessageWithRetry(ctx, writer, rtx, args, idempotencyKey, info.Attempt)
	if err != nil {
		return fmt.Errorf("save_message for %s: %w", stepID, err)
	}
	durationMs := time.Since(start).Milliseconds()

	logging.Info("[ActivityWrapper] Saved node message",
		"activityType", activityType,
		"stepID", stepID,
		"messageID", saveOutput.GetMessageId(),
		"role", saveInput.Role,
		"thread", saveInput.Thread)

	saveStepID := stepID + "-save"
	w.writeStepExecution(ctx, workflowID, saveStepID, "SaveMessage", saveOutput, nil, durationMs, inputInfo.LoopNodeID, inputInfo.LoopIteration)
	end := time.Now()
	w.emitNodeExecutionEvent(ctx, "completed", false, saveStepID, "SaveMessage", inputInfo.ChatID, workflowID, info.ActivityID, &start, &end, &durationMs, nil, nil)
	return nil
}

// DelegatedSaveIdentity is the runtime identity a delegated save is written
// under. Thread is the activity's own thread; the request's inputs.thread,
// when set, takes precedence (the same rule the workflow-side save applies).
type DelegatedSaveIdentity struct {
	ChatID     string
	Thread     string
	WorkflowID string
	StepID     string // the producing node's step id; the save uses "<StepID>-save"
}

// ResolveDelegatedSaveMessage resolves a node's delegated save_message against
// an activity result map exactly as the ActivityWrapper does after a real
// activity — normalization, condition, templates, content-free guard — without
// writing anything. It is the ONE resolution path: the wrapper calls it too.
//
// Exported for harnesses that mock activities (the scenario backend), so a
// scenario exercises the same save_message evaluation production runs rather
// than skipping it. Returns (nil, nil) when there is nothing to save.
func ResolveDelegatedSaveMessage(
	req *types.SaveMessageRequest,
	activityType string,
	output map[string]interface{},
	id DelegatedSaveIdentity,
) (*types.SaveMessageInput, error) {
	in, _, err := resolveDelegatedSaveMessage(req, activityType, output, id)
	return in, err
}

func resolveDelegatedSaveMessage(
	req *types.SaveMessageRequest,
	activityType string,
	output map[string]interface{},
	id DelegatedSaveIdentity,
) (*types.SaveMessageInput, saveMessageSkip, error) {
	if req == nil || req.Config == nil {
		return nil, saveSkipNoConfig, nil
	}
	output = normalizeActivityOutput(output, activityType)
	thread := id.Thread
	if req.Inputs != nil {
		if t, ok := req.Inputs["thread"].(string); ok && t != "" {
			thread = t
		}
	}
	workflowCtx := req.Workflow
	return resolveSaveMessage(req.Config, output, saveMessageScope{
		Inputs:     req.Inputs,
		Workflow:   &workflowCtx,
		Iter:       req.Iter,
		AgentName:  req.AgentName,
		ChatID:     id.ChatID,
		Thread:     thread,
		WorkflowID: id.WorkflowID,
		StepID:     id.StepID + "-save",
	})
}

// packageLogger adapts the logging package to the workflow logger shape
// logSaveMessageSkip takes, so the worker and workflow report skips alike.
type packageLogger struct{}

func (packageLogger) Info(msg string, kv ...interface{}) { logging.Info(msg, kv...) }
func (packageLogger) Warn(msg string, kv ...interface{}) { logging.Warn(msg, kv...) }

// resultForSave returns the result in the form activityResultMap expects:
// a proto message value (not pointer) is addressed so it marshals with
// protojson, as the data converter does.
func resultForSave[O any](result O) interface{} {
	if pm, ok := any(&result).(proto.Message); ok {
		if _, isPtr := any(result).(proto.Message); !isPtr {
			return pm
		}
	}
	return result
}

// writeMessageWithRetry writes through writer, retrying with doubling backoff
// until messageWriteBudget is spent. The last error is returned, never
// swallowed: a message that could not be written must fail the activity.
func writeMessageWithRetry(
	ctx context.Context,
	writer MessageWriter,
	rtx types.RuntimeContext,
	args *reliantv1.SaveMessageNodeArgs,
	idempotencyKey string,
	attempt int32,
) (*reliantv1.SaveMessageOutput, error) {
	deadline := time.Now().Add(messageWriteBudget)
	backoff := messageWriteBackoff
	for try := 1; ; try++ {
		out, err := writer.WriteMessage(ctx, rtx, args, idempotencyKey, attempt)
		if err == nil {
			return out, nil
		}
		if ctx.Err() != nil || time.Now().Add(backoff).After(deadline) {
			return nil, fmt.Errorf("writing message (after %d tries): %w", try, err)
		}
		logging.Warn("[ActivityWrapper] Message write failed, retrying",
			"stepID", rtx.StepID, "try", try, "backoff", backoff, "error", err)
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("writing message (after %d tries): %w", try, err)
		case <-time.After(backoff):
		}
		backoff *= 2
	}
}

// ============================================================================
// Rule 2 — message-only fields never go back to the workflow.
// ============================================================================

// clearMessageOnlyFields clears every top-level field annotated
// [(reliant) = {message_only: true}] on an activity's proto result. Such a
// field exists to be persisted with the message (it is in the map the save
// evaluates) and must not enter workflow history. Non-proto results carry no
// annotations and are left alone.
func clearMessageOnlyFields[O any](result *O) {
	var pm proto.Message
	if m, ok := any(*result).(proto.Message); ok {
		pm = m
	} else if m, ok := any(result).(proto.Message); ok {
		pm = m
	}
	if pm == nil {
		return
	}
	msg := pm.ProtoReflect()
	if !msg.IsValid() {
		return
	}
	fields := msg.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if wfcel.IsMessageOnly(fd) && msg.Has(fd) {
			msg.Clear(fd)
		}
	}
}
