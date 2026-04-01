// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/drivererrors"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/telemetry"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
)

// NOTE: Type conversion code has been removed!
// Temporal's serialization layer handles all JSON marshaling/unmarshaling.
// Activities receive typed inputs directly - no manual map-to-struct conversion needed.
// This simplification eliminates ~400 lines of reflection-based conversion code.

// ============================================================================
// TYPED ACTIVITY INTERFACE (with generics for type safety)
// ============================================================================

// TypedActivity is a generic activity interface with strongly-typed inputs/outputs
// Similar to our tool registry pattern
type TypedActivity[TInput any, TOutput any] interface {
	// Name returns the activity name for registration
	Name() string

	// Execute runs the activity with typed inputs and returns typed output
	Execute(ctx context.Context, input TInput) (TOutput, error)
}

// ============================================================================
// ERROR CLASSIFICATION (copied from activities package to avoid import cycle)
// ============================================================================

// TerminalError indicates an error that should not be retried
type TerminalError struct {
	Message string
	Cause   error
}

func (e *TerminalError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Cause)
	}
	return e.Message
}

func (e *TerminalError) Unwrap() error {
	return e.Cause
}

// classifyError determines if an error is terminal or transient
// Returns a Temporal-compatible error that controls retry behavior
func classifyError(err error) error {
	if err == nil {
		return nil
	}

	// Check if already classified as TerminalError
	var terminalErr *TerminalError
	if errors.As(err, &terminalErr) {
		return temporal.NewNonRetryableApplicationError(terminalErr.Error(), "TerminalError", terminalErr.Cause)
	}

	// Auto-classify based on error content
	errStr := strings.ToLower(err.Error())

	// Check for specific transient error types first (before pattern matching)
	// MalformedJSONError from streaming indicates network/API issues - always retry
	if strings.Contains(errStr, "malformed json in tool input (transient)") {
		return err // Transient - Temporal will retry
	}

	// Deterministic empty-input preflight failure (e.g., Codex no input items)
	if errors.Is(err, drivererrors.ErrEmptyInput) {
		return temporal.NewNonRetryableApplicationError(err.Error(), "TerminalError", err)
	}

	// SDK-level JSON parsing errors during streaming accumulation
	// These occur when the API sends malformed partial JSON chunks
	// Error format: "json: error calling MarshalJSON for type json.RawMessage: ..."
	if strings.Contains(errStr, "json.rawmessage") &&
		(strings.Contains(errStr, "unexpected end of json") ||
			strings.Contains(errStr, "invalid character")) {
		return err // Transient - Temporal will retry
	}

	// Terminal errors - validation, business logic, not found, etc.
	terminalPatterns := []string{
		"not found",
		"does not exist",
		"invalid",
		"malformed",
		"forbidden",
		"unauthorized",
		"permission denied",
		"quota exceeded",
		"bad request",
		"cannot be empty",
		"cannot be nil",
		"unknown model",
		"unsupported",
		"prompt is too long", // Token limit exceeded - won't be fixed by retry
		"too many tokens",    // Alternative token limit error message
		"maximum context",    // Context length exceeded
		"context length",     // Context length error
	}

	for _, pattern := range terminalPatterns {
		if strings.Contains(errStr, pattern) {
			return temporal.NewNonRetryableApplicationError(err.Error(), "TerminalError", err)
		}
	}

	// Transient errors - network, timeout, rate limit, service issues
	// These will be retried by Temporal (returned as normal errors)
	transientPatterns := []string{
		"timeout",
		"connection refused",
		"connection reset",
		"network",
		"rate limit",
		"too many requests",
		"service unavailable",
		"internal server error",
		"bad gateway",
		"gateway timeout",
		"deadline exceeded",
		"context deadline exceeded",
		"context canceled",
		"temporary failure",
		"try again",
		"no such host",
		"dial tcp",
	}

	for _, pattern := range transientPatterns {
		if strings.Contains(errStr, pattern) {
			return err // Return as-is for Temporal retry
		}
	}

	// Default: treat as transient (retryable)
	// Better to retry too much than fail permanently on recoverable errors
	return err
}

// categorizeError returns the category of an error for logging/metrics
func categorizeError(err error) string {
	if err == nil {
		return "unknown"
	}

	if errors.Is(err, drivererrors.ErrEmptyInput) {
		return "validation"
	}

	errStr := strings.ToLower(err.Error())

	if strings.Contains(errStr, "not found") || strings.Contains(errStr, "does not exist") {
		return "not_found"
	}

	if strings.Contains(errStr, "invalid") || strings.Contains(errStr, "malformed") ||
		strings.Contains(errStr, "cannot be empty") || strings.Contains(errStr, "cannot be nil") {
		return "validation"
	}

	if strings.Contains(errStr, "rate limit") || strings.Contains(errStr, "too many requests") {
		return "rate_limit"
	}

	if strings.Contains(errStr, "timeout") || strings.Contains(errStr, "network") ||
		strings.Contains(errStr, "connection") || strings.Contains(errStr, "deadline exceeded") ||
		strings.Contains(errStr, "no such host") || strings.Contains(errStr, "dial tcp") {
		return "network"
	}

	var terminalErr *TerminalError
	if errors.As(err, &terminalErr) {
		return "terminal"
	}

	return "unknown"
}

// isTerminal checks if an error is terminal (non-retryable)
func isTerminal(err error) bool {
	if err == nil {
		return false
	}

	// Check for TerminalError type
	var terminalErr *TerminalError
	if errors.As(err, &terminalErr) {
		return true
	}

	// Check for Temporal ApplicationError
	var appErr *temporal.ApplicationError
	if errors.As(err, &appErr) {
		return appErr.Type() == "TerminalError"
	}

	return false
}

// ============================================================================
// ACTIVITY WRAPPER (Middleware Infrastructure)
// ============================================================================

// ActivityWrapper provides type-safe activity wrapping with middleware functionality.
//
// The wrapper preserves all middleware functionality:
// - Heartbeating for fast cancellation detection
// - Panic recovery with proper error propagation
// - Chat updates dual-write for UI tracking
// - Structured logging and observability
//
// Type conversion is handled by Temporal's serialization layer - no manual conversion needed!
type ActivityWrapper[I any, O any] struct {
	outputType       reflect.Type
	name             string
	activity         func(ctx context.Context, input I) (O, error)
	repo             db.Repository
	managesLifecycle bool
}

// NewActivityWrapper creates a new type-safe activity wrapper.
//
// Parameters:
//   - name: The activity name for registration with Temporal
//   - activity: The typed activity function to wrap
//   - repo: Database repository for chat updates
func NewActivityWrapper[I any, O any](
	name string,
	activity func(ctx context.Context, input I) (O, error),
	repo db.Repository,
) *ActivityWrapper[I, O] {
	var o O
	return &ActivityWrapper[I, O]{
		outputType: reflect.TypeOf(o),
		name:       name,
		activity:   activity,
		repo:       repo,
	}
}

// Execute wraps the typed activity with all middleware functionality.
// This is the function that gets registered with Temporal.
//
// Temporal's serialization layer handles all type conversion automatically.
// Input comes as typed I, output goes as typed O - no manual conversion needed!
func (w *ActivityWrapper[I, O]) Execute(ctx context.Context, input I) (O, error) {
	var zeroOutput O

	// Get activity info from Temporal context
	info := activity.GetInfo(ctx)
	activityType := info.ActivityType.Name
	activityID := info.ActivityID
	attemptNumber := int(info.Attempt)
	workflowID := info.WorkflowExecution.ID
	var maxAttempts int32
	if info.RetryPolicy != nil {
		maxAttempts = info.RetryPolicy.MaximumAttempts
	}

	// Extract step_id - try from input first, fall back to parsing activityID
	// The workflow engine uses activityID format "stepID-timestamp" (e.g., "tally-1234567890")
	inputInfo := extractActivityInputInfo(input)
	logging.Info("[ActivityWrapper] Extracted input info",
		"activityType", activityType,
		"loopNodeID", inputInfo.LoopNodeID,
		"loopIteration", inputInfo.LoopIteration,
		"stepID", inputInfo.StepID,
	)
	stepID := inputInfo.StepID
	if stepID == "" {
		// Parse step ID from activity ID (format: "stepID-timestamp")
		if idx := strings.LastIndex(activityID, "-"); idx > 0 {
			stepID = activityID[:idx]
		}
	}

	// Extract chat_id for node execution events
	chatID := extractChatID(input)

	logging.Info("[ActivityWrapper] Activity execution started",
		"activityType", activityType,
		"activityID", activityID,
		"attemptNumber", attemptNumber,
		"workflowID", workflowID,
		"stepID", stepID)

	// Belt-and-suspenders: if Temporal is executing an activity for this workflow,
	// the workflow IS running. Ensure the DB agrees. This is a fast no-op when the
	// workflow is already marked Running, and self-heals stale status otherwise.
	// Skip lifecycle activities that manage status transitions themselves, and
	// skip if context is already cancelled (pause/shutdown in progress).
	if w.repo != nil && chatID != "" && ctx.Err() == nil && !w.managesLifecycle {
		w.repo.EnsureWorkflowRunning(ctx, workflowID, chatID)
	}

	// Track execution start time for duration calculation
	startTime := time.Now()

	// Emit node execution "started" event for UI streaming
	w.emitNodeExecutionEvent(ctx, "started", stepID, activityType, chatID, workflowID, activityID, &startTime, nil, nil, nil, nil)

	// Start heartbeat goroutine for fast cancellation detection
	// Heartbeats every 500ms. With MaxHeartbeatThrottleInterval=500ms on the worker,
	// every call reaches the server and cancellation propagates within ~500ms.
	heartbeatCtx, cancelHeartbeat := context.WithCancel(ctx)
	defer cancelHeartbeat() // Stop heartbeat when activity completes

	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-heartbeatCtx.Done():
				// Activity completed or context cancelled (worker stopped)
				if ctx.Err() == context.Canceled {
					logging.Debug("[ActivityWrapper] Heartbeat stopped due to worker shutdown",
						"activityType", activityType,
						"activityID", activityID)
				}
				return
			case <-ticker.C:
				// Check for cancellation before sending heartbeat
				// This helps detect worker shutdown faster
				if ctx.Err() != nil {
					logging.Debug("[ActivityWrapper] Context cancelled, stopping heartbeat",
						"activityType", activityType,
						"activityID", activityID,
						"error", ctx.Err())
					return
				}
				// Send heartbeat with current progress
				activity.RecordHeartbeat(ctx, map[string]interface{}{
					"activity_type": activityType,
					"attempt":       attemptNumber,
					"status":        "running",
				})
			}
		}
	}()

	// Setup defer for panic recovery
	var execErr error
	defer func() {
		// Recover from panics
		if r := recover(); r != nil {
			logging.Error("[ActivityWrapper] Activity panicked",
				"activityType", activityType,
				"activityID", activityID,
				"panic", r)

			// Best-effort: Write error event to chat_updates on panic
			panicErr := fmt.Errorf("panic: %v", r)
			w.writeErrorEvent(ctx, input, activityType, activityID, attemptNumber, workflowID, panicErr, maxAttempts)

			// Report panic to telemetry with context (non-blocking)
			go func() {
				telemetry.CaptureExceptionWithContext(panicErr, map[string]string{
					"activity_type": activityType,
					"activity_id":   activityID,
					"workflow_id":   workflowID,
					"chat_id":       chatID,
					"step_id":       stepID,
					"error_type":    "panic",
				}, map[string]interface{}{
					"attempt_number": attemptNumber,
				})
			}()

			// Emit node execution "failed" event
			endTime := time.Now()
			duration := endTime.Sub(startTime).Milliseconds()
			errMsg := panicErr.Error()
			w.emitNodeExecutionEvent(ctx, "failed", stepID, activityType, chatID, workflowID, activityID, &startTime, &endTime, &duration, nil, &errMsg)

			// Re-panic to propagate to Temporal
			panic(r)
		}
	}()

	// Execute the typed activity directly - no conversion needed!
	// Temporal already deserialized the input to type I
	result, execErr := w.activity(ctx, input)

	// Calculate execution duration
	endTime := time.Now()
	durationMs := endTime.Sub(startTime).Milliseconds()

	if execErr != nil {
		// Use Warn for non-terminal errors (e.g. YAML validation failures,
		// missing API keys) so they don't trigger Sentry via the sentry log
		// handler. Terminal errors stay at Error level.
		logFn := logging.Warn
		if isTerminal(execErr) {
			logFn = logging.Error
		}
		logFn("[ActivityWrapper] Activity execution failed",
			"activityType", activityType,
			"activityID", activityID,
			"error", execErr,
			"attemptNumber", attemptNumber,
			"durationMs", durationMs)

		// Best-effort: Write error event to chat_updates
		w.writeErrorEvent(ctx, input, activityType, activityID, attemptNumber, workflowID, execErr, maxAttempts)

		// Report error to telemetry with context (non-blocking)
		// Only report terminal errors to avoid noise from transient failures
		if isTerminal(execErr) {
			go func() {
				telemetry.CaptureExceptionWithContext(execErr, map[string]string{
					"activity_type":  activityType,
					"activity_id":    activityID,
					"workflow_id":    workflowID,
					"chat_id":        chatID,
					"step_id":        stepID,
					"error_type":     "terminal",
					"error_category": categorizeError(execErr),
				}, map[string]interface{}{
					"attempt_number": attemptNumber,
					"duration_ms":    durationMs,
				})
			}()
		}

		// Best-effort: Write step execution record for history tracking
		w.writeStepExecution(ctx, workflowID, stepID, activityType, nil, execErr, durationMs, inputInfo.LoopNodeID, inputInfo.LoopIteration)

		// Emit node execution "failed" event for UI streaming
		errMsg := execErr.Error()
		w.emitNodeExecutionEvent(ctx, "failed", stepID, activityType, chatID, workflowID, activityID, &startTime, &endTime, &durationMs, nil, &errMsg)

		return zeroOutput, execErr
	}

	logging.Info("[ActivityWrapper] Activity execution completed",
		"activityType", activityType,
		"activityID", activityID,
		"success", true,
		"attemptNumber", attemptNumber,
		"durationMs", durationMs)

	// Best-effort: Write step execution record for history tracking
	w.writeStepExecution(ctx, workflowID, stepID, activityType, result, nil, durationMs, inputInfo.LoopNodeID, inputInfo.LoopIteration)

	// Emit node execution "completed" event for UI streaming
	// Try to extract exit_code from result for run steps
	var exitCode *int
	if resultMap := toMapInterface(result); resultMap != nil {
		if ec, ok := resultMap["exit_code"]; ok {
			switch v := ec.(type) {
			case float64:
				ecInt := int(v)
				exitCode = &ecInt
			case int:
				exitCode = &v
			case int64:
				ecInt := int(v)
				exitCode = &ecInt
			}
		}
	}
	w.emitNodeExecutionEvent(ctx, "completed", stepID, activityType, chatID, workflowID, activityID, &startTime, &endTime, &durationMs, exitCode, nil)

	return result, nil
}

// writeErrorEvent writes an error event to chat_updates when an activity fails.
// This is a best-effort write - it will not fail the activity if the write fails.
// Only writes errors if chat_id can be extracted from the input.
//
// IMPORTANT: This function uses a background context with a timeout because the
// activity context may already be cancelled or about to be cancelled when an error
// occurs. Using the activity context could cause the error write to fail silently.
func (w *ActivityWrapper[I, O]) writeErrorEvent(
	ctx context.Context,
	input I,
	activityType string,
	activityID string,
	attemptNumber int,
	workflowID string,
	err error,
	maxAttempts int32,
) {
	logging.Info("[ActivityWrapper] writeErrorEvent called",
		"activityType", activityType,
		"activityID", activityID,
		"attemptNumber", attemptNumber,
		"error", err.Error())

	// Extract chat_id from input if available
	chatID := extractChatID(input)
	if chatID == "" {
		logging.Info("[ActivityWrapper] Skipping error event write - no chat_id in input",
			"activityType", activityType,
			"activityID", activityID)
		return
	}

	logging.Info("[ActivityWrapper] Extracted chat_id for error event",
		"chatID", chatID,
		"activityType", activityType,
		"activityID", activityID)

	// Generate unique error ID
	errorID := uuid.New().String()

	// Build error data matching the ErrorUpdate interface expected by frontend
	// See web/src/types/streaming.ts ErrorUpdate interface
	isRetrying := maxAttempts > 0 && int32(attemptNumber) < maxAttempts && !isTerminal(err)
	errorData := map[string]interface{}{
		"update_type":    "error",
		"id":             errorID,
		"chat_id":        chatID,
		"activity_type":  activityType,
		"activity_id":    activityID,
		"error_message":  err.Error(),
		"timestamp":      time.Now().UTC().Format(time.RFC3339Nano),
		"attempt_number": attemptNumber,
		"workflow_id":    workflowID,
		"is_retrying":    isRetrying,
	}
	if maxAttempts > 0 {
		errorData["max_attempts"] = int(maxAttempts)
	}

	// Marshal to JSON
	errorDataJSON, marshalErr := json.Marshal(errorData)
	if marshalErr != nil {
		logging.Error("[ActivityWrapper] Failed to marshal error event data",
			"activityType", activityType,
			"activityID", activityID,
			"marshalError", marshalErr)
		return
	}

	// Use a background context with timeout for the DB write.
	// The activity context may be cancelled or about to be cancelled when an error occurs,
	// which could cause the error write to fail. Using a fresh context ensures the error
	// event is written even if the activity is being terminated.
	writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Check if the original context was already cancelled (for logging purposes)
	if ctx.Err() != nil {
		logging.Warn("[ActivityWrapper] Original context was cancelled, using background context for error write",
			"activityType", activityType,
			"activityID", activityID,
			"chatID", chatID,
			"ctxErr", ctx.Err().Error())
	}

	// Best-effort write to chat_updates
	if writeErr := w.repo.CreateChatUpdate(writeCtx, chatID, db.UpdateTypeError, errorID, string(errorDataJSON)); writeErr != nil {
		logging.Error("[ActivityWrapper] Failed to write error event to chat_updates",
			"activityType", activityType,
			"activityID", activityID,
			"chatID", chatID,
			"errorID", errorID,
			"writeError", writeErr)
		return
	}

	logging.Info("[ActivityWrapper] Successfully wrote error event to chat_updates",
		"activityType", activityType,
		"activityID", activityID,
		"chatID", chatID,
		"errorID", errorID,
		"attemptNumber", attemptNumber)
}

// emitNodeExecutionEvent emits a node execution event for UI streaming.
// This is a best-effort write - it will not fail the activity if the write fails.
func (w *ActivityWrapper[I, O]) emitNodeExecutionEvent(
	ctx context.Context,
	eventType string,
	nodeID string,
	nodeType string,
	chatID string,
	workflowID string,
	activityID string,
	startedAt *time.Time,
	completedAt *time.Time,
	durationMs *int64,
	exitCode *int,
	errorMessage *string,
) {
	// Skip if we don't have the required IDs
	if chatID == "" || nodeID == "" {
		logging.Debug("[ActivityWrapper] Skipping node execution event - missing chat_id or node_id",
			"chatID", chatID,
			"nodeID", nodeID,
			"eventType", eventType)
		return
	}

	// Determine node status from event type
	var status db.NodeExecutionStatus
	switch eventType {
	case "started":
		status = db.NodeStatusRunning
	case "completed":
		status = db.NodeStatusCompleted
	case "failed":
		status = db.NodeStatusFailed
	case "cancelled":
		status = db.NodeStatusCancelled
	default:
		status = db.NodeStatusRunning
	}

	// Determine node type from activity type (strip V2_ prefix if present)
	cleanNodeType := strings.TrimPrefix(nodeType, "")
	// Map activity types to workflow node types
	switch {
	case strings.HasPrefix(cleanNodeType, "ExecuteAgent"):
		cleanNodeType = "agent"
	case strings.HasPrefix(cleanNodeType, "ExecuteRunStep"):
		cleanNodeType = model.NodeTypeRun
	case strings.HasPrefix(cleanNodeType, "ExecuteApproval"):
		cleanNodeType = model.NodeTypeApproval
	case strings.HasPrefix(cleanNodeType, "CallLLM"):
		cleanNodeType = "llm_call"
	case strings.HasPrefix(cleanNodeType, "ExecuteTools"):
		cleanNodeType = "tools"
	case strings.HasPrefix(cleanNodeType, "SaveMessage"):
		cleanNodeType = model.NodeTypeSaveMessage
	case strings.Contains(cleanNodeType, "."):
		// Action steps with dot notation - extract the prefix as category
		cleanNodeType = "action"
	}

	// Build node execution state
	nodeState := &db.NodeExecutionState{
		NodeID:     nodeID,
		NodeType:   cleanNodeType,
		Status:     status,
		WorkflowID: workflowID,
		ChatID:     chatID,
		ActivityID: &activityID,
	}

	if startedAt != nil {
		nodeState.StartedAt = startedAt
	}
	if completedAt != nil {
		nodeState.CompletedAt = completedAt
	}
	if durationMs != nil {
		nodeState.DurationMs = durationMs
	}
	if exitCode != nil {
		nodeState.ExitCode = exitCode
	}
	if errorMessage != nil {
		nodeState.ErrorMessage = errorMessage
	}

	// Use a background context with timeout for the DB write
	writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Best-effort write to chat_updates
	if err := w.repo.EmitNodeExecutionEvent(writeCtx, eventType, nodeState); err != nil {
		logging.Warn("[ActivityWrapper] Failed to emit node execution event",
			"eventType", eventType,
			"nodeID", nodeID,
			"chatID", chatID,
			"error", err)
		return
	}

	logging.Debug("[ActivityWrapper] Emitted node execution event",
		"eventType", eventType,
		"nodeID", nodeID,
		"nodeType", cleanNodeType,
		"status", status,
		"workflowID", workflowID)
}

// extractChatID attempts to extract chat_id from various input formats.
// Returns empty string if chat_id is not found.
func extractChatID(input interface{}) string {
	if input == nil {
		return ""
	}

	// Try to extract from map[string]interface{}
	if inputMap, ok := input.(map[string]interface{}); ok {
		if chatID, ok := inputMap["chat_id"].(string); ok {
			return chatID
		}
	}

	// Try reflection for struct fields
	val := reflect.ValueOf(input)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}

	if val.Kind() == reflect.Struct {
		// Try to find ChatID or chat_id field
		for i := 0; i < val.NumField(); i++ {
			field := val.Type().Field(i)

			// Check json tag for chat_id
			if jsonTag := field.Tag.Get("json"); jsonTag != "" {
				tagName := strings.Split(jsonTag, ",")[0]
				if tagName == "chat_id" {
					if chatIDVal := val.Field(i); chatIDVal.Kind() == reflect.String {
						return chatIDVal.String()
					}
				}
			}

			// Check field name (ChatID or chat_id)
			if field.Name == "ChatID" || field.Name == "chat_id" {
				if chatIDVal := val.Field(i); chatIDVal.Kind() == reflect.String {
					return chatIDVal.String()
				}
			}
		}
	}

	return ""
}

// activityInputInfo holds common fields extracted from activity inputs for tracking.
type activityInputInfo struct {
	ChatID        string
	StepID        string
	WorkflowID    string
	LoopNodeID    string // The loop node that spawned this activity (if any)
	LoopIteration int    // The iteration index within the loop (0-indexed, -1 if not in loop)
}

// extractActivityInputInfo extracts common fields from a typed input using JSON.
// This is needed because we don't know the exact type at compile time.
func extractActivityInputInfo(input interface{}) activityInputInfo {
	info := activityInputInfo{
		LoopIteration: -1, // Default to -1 to indicate "not in a loop"
	}

	// Use JSON to convert to map and extract fields
	jsonBytes, err := json.Marshal(input)
	if err != nil {
		return info
	}

	var m map[string]interface{}
	if err := json.Unmarshal(jsonBytes, &m); err != nil {
		return info
	}

	if chatID, exists := m["chat_id"]; exists {
		if chatIDStr, ok := chatID.(string); ok {
			info.ChatID = chatIDStr
		}
	}
	if stepID, exists := m["step_id"]; exists {
		if stepIDStr, ok := stepID.(string); ok {
			info.StepID = stepIDStr
		}
	}
	if workflowID, exists := m["workflow_id"]; exists {
		if workflowIDStr, ok := workflowID.(string); ok {
			info.WorkflowID = workflowIDStr
		}
	}
	if loopNodeID, exists := m["loop_node_id"]; exists {
		if loopNodeIDStr, ok := loopNodeID.(string); ok {
			info.LoopNodeID = loopNodeIDStr
		}
	}
	if loopIter, exists := m["loop_iteration"]; exists {
		switch v := loopIter.(type) {
		case float64:
			info.LoopIteration = int(v)
		case int:
			info.LoopIteration = v
		case int64:
			info.LoopIteration = int(v)
		}
	}
	return info
}

// toMapInterface converts an interface to map[string]interface{} if possible.
func toMapInterface(v interface{}) map[string]interface{} {
	if m, ok := v.(map[string]interface{}); ok {
		return m
	}

	// Try JSON round-trip for structs
	jsonBytes, err := json.Marshal(v)
	if err != nil {
		return nil
	}

	var m map[string]interface{}
	if err := json.Unmarshal(jsonBytes, &m); err != nil {
		return nil
	}
	return m
}

// writeStepExecution writes a step execution record for workflow history tracking.
// This enables CEL expressions to query step history (e.g., size(history.filter(exit_code != 0)) >= 3).
func (w *ActivityWrapper[I, O]) writeStepExecution(ctx context.Context, workflowID, stepID, activityType string, output interface{}, execErr error, durationMs int64, loopNodeID string, loopIteration int) {
	// Skip if we don't have the required IDs
	if workflowID == "" || stepID == "" {
		logging.Info("[ActivityWrapper] Skipping step execution write - missing workflow_id or step_id",
			"workflowID", workflowID,
			"stepID", stepID,
			"activityType", activityType)
		return
	}

	// Marshal output to JSON
	var outputJSON sql.NullString
	if output != nil {
		if jsonBytes, err := json.Marshal(output); err == nil {
			outputJSON = sql.NullString{String: string(jsonBytes), Valid: true}
		}
	}

	// Extract exit_code if available in output
	var exitCode sql.NullInt64
	if output != nil {
		if outputMap := toMapInterface(output); outputMap != nil {
			if ec, ok := outputMap["exit_code"]; ok {
				switch v := ec.(type) {
				case int:
					exitCode = sql.NullInt64{Int64: int64(v), Valid: true}
				case int64:
					exitCode = sql.NullInt64{Int64: v, Valid: true}
				case float64:
					exitCode = sql.NullInt64{Int64: int64(v), Valid: true}
				}
			}
		}
	}

	// Determine success based on error and exit_code
	var success sql.NullBool
	if execErr != nil {
		success = sql.NullBool{Bool: false, Valid: true}
	} else if exitCode.Valid {
		success = sql.NullBool{Bool: exitCode.Int64 == 0, Valid: true}
	} else {
		success = sql.NullBool{Bool: true, Valid: true}
	}

	// Build loop context fields
	var loopNodeIDSQL sql.NullString
	var loopIterationSQL sql.NullInt64
	if loopNodeID != "" {
		loopNodeIDSQL = sql.NullString{String: loopNodeID, Valid: true}
		loopIterationSQL = sql.NullInt64{Int64: int64(loopIteration), Valid: true}
	}

	exec := &db.StepExecution{
		ID:            uuid.New().String(),
		WorkflowID:    workflowID,
		StepID:        stepID,
		ActivityName:  activityType,
		OutputJSON:    outputJSON,
		ExitCode:      exitCode,
		Success:       success,
		DurationMs:    sql.NullInt64{Int64: durationMs, Valid: true},
		LoopNodeID:    loopNodeIDSQL,
		LoopIteration: loopIterationSQL,
		CreatedAt:     time.Now(),
	}

	// Use a background context with timeout for the DB write.
	// The activity context may be cancelled or about to be cancelled,
	// which could cause the write to fail. Using a fresh context ensures the
	// step execution is written even if the activity is being terminated.
	writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Best-effort write to step_executions (don't fail activity if this fails)
	if err := w.repo.CreateStepExecution(writeCtx, exec); err != nil {
		logging.Error("[ActivityWrapper] Failed to create step execution",
			"error", err,
			"workflowID", workflowID,
			"stepID", stepID,
			"activityType", activityType)
	} else {
		logging.Info("[ActivityWrapper] Step execution recorded",
			"workflowID", workflowID,
			"stepID", stepID,
			"activityType", activityType,
			"success", success.Bool,
			"durationMs", durationMs)
	}
}

// NOTE: writeActivityUpdate has been removed.
// Activity status updates were written to chat_updates with update_type='activity_status',
// but this is not in the allowed list of update_types (message, approval, thread, tool_call,
// streaming_delta, workflow_status, error) and caused constraint violations.
// These updates were also never consumed by the UI, as confirmed in BACKEND_CLEANUP_AUDIT.md.
// Temporal already tracks all activity execution state, so these dual-writes were redundant.

// ============================================================================
// ACTIVITY REGISTRY
// ============================================================================

// ActivityRegistry manages activity registration and wrapping with middleware
// Similar to tool registry but for Temporal activities
type ActivityRegistry struct {
	repo        db.Repository
	activities  map[string]interface{}  // activity name -> wrapped activity function
	outputTypes map[string]reflect.Type // activity name -> output type for schema introspection
}

// NewActivityRegistry creates a new activity registry
func NewActivityRegistry(repo db.Repository) *ActivityRegistry {
	return &ActivityRegistry{
		repo:        repo,
		activities:  make(map[string]interface{}),
		outputTypes: make(map[string]reflect.Type),
	}
}

// RegisterActivity registers a typed activity with automatic middleware wrapping.
// Temporal handles all JSON serialization/deserialization - no manual conversion needed!
func RegisterActivity[TInput any, TOutput any](
	registry *ActivityRegistry,
	act TypedActivity[TInput, TOutput],
) {
	registerActivityInternal(registry, act, false)
}

// RegisterLifecycleActivity registers a typed activity that manages workflow lifecycle
// transitions (e.g. status changes, cleanup). These activities are excluded from the
// automatic "ensure workflow running" check in the activity wrapper, since they manage
// status transitions themselves.
func RegisterLifecycleActivity[TInput any, TOutput any](
	registry *ActivityRegistry,
	act TypedActivity[TInput, TOutput],
) {
	registerActivityInternal(registry, act, true)
}

func registerActivityInternal[TInput any, TOutput any](
	registry *ActivityRegistry,
	act TypedActivity[TInput, TOutput],
	managesLifecycle bool,
) {
	name := act.Name()

	// Create an ActivityWrapper that handles:
	// - Heartbeating for fast cancellation detection
	// - Panic recovery with proper error propagation
	// - Chat updates dual-write for UI tracking
	// - Structured logging and observability
	wrapper := NewActivityWrapper(name, act.Execute, registry.repo)
	wrapper.managesLifecycle = managesLifecycle

	// Wrap with error classification middleware
	// The function signature matches what Temporal expects for typed activities
	wrappedFn := func(ctx context.Context, input TInput) (TOutput, error) {
		// Pre-execution middleware (logging)
		logger := getActivityLogger(ctx)
		activityInfo := activity.GetInfo(ctx)
		logger.Info("[Workflow Runtime Registry] Activity starting",
			"activity", name,
			"activity_id", activityInfo.ActivityID,
			"attempt", activityInfo.Attempt,
		)

		// Execute the activity wrapper (handles middleware)
		output, err := wrapper.Execute(ctx, input)

		// Post-execution middleware - Smart error handling
		if err != nil {
			// Classify and handle the error
			classified := classifyError(err)
			category := categorizeError(err)
			terminal := isTerminal(classified)

			// Use Warn for non-terminal errors so they don't trigger Sentry.
			if terminal {
				logger.Error("[Workflow Runtime Registry] Activity failed",
					"activity", name, "error", err, "category", category, "is_terminal", terminal)
			} else {
				logger.Warn("[Workflow Runtime Registry] Activity failed",
					"activity", name, "error", err, "category", category, "is_terminal", terminal)
			}

			// Return the classified error (terminal or transient)
			// Temporal will handle retry logic based on error type
			return output, classified
		}

		logger.Info("[Workflow Runtime Registry] Activity completed", "activity", name)

		return output, nil
	}

	// Store the wrapped function and output type
	registry.activities[name] = wrappedFn

	// Store output type for schema introspection (enables GetOutputDefaults)
	var o TOutput
	registry.outputTypes[name] = reflect.TypeOf(o)

	// Register input/output types with schema for static validation.
	// This enables validation of activity input field names and output field references.
	//
	// NOTE: Some activities (e.g., V2_CallLLM) pre-register in init() with flat input types
	// to allow workflows to use args like `model: "claude-4-sonnet"` instead of nested
	// `node: { model: "..." }`. Don't overwrite those registrations.
	if !schema.IsActivityTypeRegistered(name) {
		var i TInput
		schema.RegisterActivityType(name, reflect.TypeOf(i), reflect.TypeOf(o))
	}
}

// RegisterWithWorker registers all activities with a Temporal worker
func (r *ActivityRegistry) RegisterWithWorker(w worker.Worker) {
	for name, activityFn := range r.activities {
		w.RegisterActivityWithOptions(activityFn, activity.RegisterOptions{
			Name: name,
		})
	}
}

// Get retrieves a registered activity by name
func (r *ActivityRegistry) Get(name string) (interface{}, error) {
	activity, ok := r.activities[name]
	if !ok {
		return nil, fmt.Errorf("activity not found: %s", name)
	}
	return activity, nil
}

// List returns all registered activity names
func (r *ActivityRegistry) List() []string {
	names := make([]string, 0, len(r.activities))
	for name := range r.activities {
		names = append(names, name)
	}
	return names
}

// GetOutputDefaults returns the zero-value fields for an activity's output type.
// This is used to ensure source data has all expected fields before CEL evaluation,
// preventing "no such key" errors when workflows reference optional fields.
//
// The returned map contains all fields from the output struct with their zero values
// (nil for pointers/slices, 0 for numbers, "" for strings, etc.)
func (r *ActivityRegistry) GetOutputDefaults(activityName string) (map[string]interface{}, error) {
	outputType, ok := r.outputTypes[activityName]
	if !ok {
		return nil, fmt.Errorf("activity not found: %s", activityName)
	}

	// Handle nil type (shouldn't happen but be safe)
	if outputType == nil {
		return make(map[string]interface{}), nil
	}

	// Create zero value of the output type
	var zeroValue reflect.Value
	if outputType.Kind() == reflect.Ptr {
		zeroValue = reflect.New(outputType.Elem())
	} else {
		zeroValue = reflect.New(outputType).Elem()
	}

	// Marshal to JSON and back to get map with all fields
	jsonBytes, err := json.Marshal(zeroValue.Interface())
	if err != nil {
		return nil, fmt.Errorf("failed to marshal zero value: %w", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(jsonBytes, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal to map: %w", err)
	}

	return result, nil
}

// ============================================================================
// HELPER FUNCTIONS
// ============================================================================

// getActivityLogger gets the Temporal activity logger
func getActivityLogger(ctx context.Context) log.Logger {
	return activity.GetLogger(ctx)
}

// ============================================================================
// EXAMPLE USAGE
// ============================================================================

/*
// Define a typed activity
type ProcessMessageActivity struct {
	repo db.Repository
	llm  llm.Client
}

type ProcessMessageInput struct {
	MessageID string
	ChatID    string
}

type ProcessMessageOutput struct {
	ResponseMessageID string
	HasToolCalls      bool
	ToolCalls         []string
}

func (a *ProcessMessageActivity) Name() string {
	return "ProcessMessage"
}

func (a *ProcessMessageActivity) Execute(ctx context.Context, input ProcessMessageInput) (ProcessMessageOutput, error) {
	// Pure business logic - no state management!
	// Middleware handles logging, state tracking, etc.

	// ... implementation ...

	return ProcessMessageOutput{
		ResponseMessageID: "msg-123",
		HasToolCalls:      true,
		ToolCalls:         []string{"tool-1", "tool-2"},
	}, nil
}

// Register it
registry := NewActivityRegistry(repo)
RegisterActivity(registry, &ProcessMessageActivity{repo: repo, llm: llmClient})

// Register with Temporal worker
registry.RegisterWithWorker(worker)
*/
