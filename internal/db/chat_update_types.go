package db

import (
	"encoding/json"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// ============================================================================
// CHAT UPDATE TYPES
// ============================================================================
// These typed structs replace manual map[string]interface{} construction for
// chat_updates. Each struct corresponds to an update_type in the chat_updates
// table and provides compile-time type safety.
//
// Usage:
//   update := ToolCallUpdate{...}
//   repo.EmitToolCallUpdate(ctx, chatID, update)
// ============================================================================

// UpdateType is the discriminator for chat_update types.
type UpdateType = reliantv1.ChatUpdateType

var (
	UpdateTypeMessage           UpdateType = reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_MESSAGE
	UpdateTypeApproval          UpdateType = reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_APPROVAL
	UpdateTypeThread            UpdateType = reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_THREAD
	UpdateTypeToolCall          UpdateType = reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_TOOL_CALL
	UpdateTypeWorkflowStatus    UpdateType = reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_WORKFLOW_STATUS
	UpdateTypeError             UpdateType = reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_ERROR
	UpdateTypeChat              UpdateType = reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_CHAT
	UpdateTypeRunOutput         UpdateType = reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_RUN_OUTPUT
	UpdateTypeNodeExecution     UpdateType = reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_NODE_EXECUTION
	UpdateTypeExecutionLog      UpdateType = reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_EXECUTION_LOG
	UpdateTypeWorkflowExecution UpdateType = reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_WORKFLOW_EXECUTION
	UpdateTypeInfo              UpdateType = reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_INFO
	UpdateTypeWarning           UpdateType = reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_WARNING
	UpdateTypeRefetch           UpdateType = reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_REFETCH
	UpdateTypeQuestion          UpdateType = reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_QUESTION
	UpdateTypeStreamFinalized   UpdateType = reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_STREAM_FINALIZED
)

// ============================================================================
// TOOL CALL UPDATES
// ============================================================================

// ToolCallStatus represents the status of a tool call
type ToolCallStatus string

const (
	ToolCallStatusPending      ToolCallStatus = "pending"
	ToolCallStatusExecuting    ToolCallStatus = "executing"
	ToolCallStatusCompleted    ToolCallStatus = "completed"
	ToolCallStatusFailed       ToolCallStatus = "failed"
	ToolCallStatusCancelled    ToolCallStatus = "cancelled"
	ToolCallStatusBackgrounded ToolCallStatus = "backgrounded"
)

// ToolCallUpdate represents a tool execution status update
type ToolCallUpdate struct {
	UpdateType     UpdateType     `json:"update_type"` // Always "tool_call"
	ContentBlockID string         `json:"content_block_id"`
	ToolCallID     string         `json:"tool_call_id"`
	ToolName       string         `json:"tool_name"`
	Status         ToolCallStatus `json:"status"`
	Timestamp      string         `json:"timestamp,omitempty"`
	NodeID         string         `json:"node_id,omitempty"`
	RequestedAt    string         `json:"requested_at,omitempty"`
	StartedAt      string         `json:"started_at,omitempty"`
	CompletedAt    string         `json:"completed_at,omitempty"`
}

func (u ToolCallUpdate) Type() UpdateType { return UpdateTypeToolCall }

// ============================================================================
// APPROVAL UPDATES
// ============================================================================

// ApprovalStatus represents the status of an approval request
type ApprovalStatus string

const (
	ApprovalStatusPending  ApprovalStatus = "pending"
	ApprovalStatusApproved ApprovalStatus = "approved"
	ApprovalStatusDenied   ApprovalStatus = "denied"
	ApprovalStatusExpired  ApprovalStatus = "expired"
)

// ============================================================================
// TYPED UPDATE INTERFACE
// ============================================================================

// TypedChatUpdate is the interface for all typed chat updates
type TypedChatUpdate interface {
	Type() UpdateType
}

// MarshalUpdate marshals a typed update to JSON
func MarshalUpdate(update TypedChatUpdate) (string, error) {
	data, err := json.Marshal(update)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// ============================================================================
// QUESTION UPDATES
// ============================================================================

// QuestionUpdate represents a question status update
type QuestionUpdate struct {
	UpdateType UpdateType `json:"update_type"`
	QuestionID string     `json:"question_id"`
	ChatID     string     `json:"chat_id"`
	WorkflowID string     `json:"workflow_id"`
	// ThreadID is the thread that raised the question. Without it a supervision
	// surface reading this feed can see THAT a gate opened but not which of a
	// run's fanned-out threads is waiting.
	ThreadID string `json:"thread_id"`
	StepID   string `json:"step_id"`
	Status   string `json:"status"`
	Metadata string `json:"metadata,omitempty"`
}

func (u QuestionUpdate) Type() UpdateType { return UpdateTypeQuestion }

// ============================================================================
// STREAM FINALIZED UPDATES
// ============================================================================

// StreamFinalizedReason is the terminal state a message stream reached.
type StreamFinalizedReason string

const (
	StreamFinalizedCompleted StreamFinalizedReason = "completed"
	StreamFinalizedAborted   StreamFinalizedReason = "aborted"
	StreamFinalizedCancelled StreamFinalizedReason = "cancelled"
)

// StreamFinalizedUpdate marks that the stream for a pre-allocated assistant
// message id reached a terminal state (delta identity protocol). Emitted
// exactly once per allocated id — the core invariant is "every allocated id
// is eventually finalized" — so consumers can retire streaming placeholders.
// UpdateTypeName is the string discriminator inside the data JSON (the typed
// column carries the proto enum).
type StreamFinalizedUpdate struct {
	UpdateTypeName string                `json:"update_type"` // Always "stream_finalized"
	MessageID      string                `json:"message_id"`
	Thread         string                `json:"thread,omitempty"`
	Reason         StreamFinalizedReason `json:"reason"`
	LastStreamSeq  int64                 `json:"last_stream_seq,omitempty"`
}

func (u StreamFinalizedUpdate) Type() UpdateType { return UpdateTypeStreamFinalized }

// ============================================================================
// ENTITY ID GENERATORS
// ============================================================================

// EntityIDForQuestion generates entity ID for question updates
func EntityIDForQuestion(questionID string) string {
	return "question-" + questionID + "-" + formatTimestamp()
}

// EntityIDForToolCall generates entity ID for tool call updates
func EntityIDForToolCall(contentBlockID string) string {
	return "tool-" + contentBlockID + "-" + formatTimestamp()
}

// EntityIDForToolCancelled generates entity ID for cancelled tool updates
func EntityIDForToolCancelled(toolCallID string) string {
	return "tool-cancelled-" + toolCallID
}

// EntityIDForToolBackgrounded generates entity ID for backgrounded tool updates
func EntityIDForToolBackgrounded(toolCallID string) string {
	return "tool-backgrounded-" + toolCallID
}

// formatTimestamp returns a nanosecond timestamp for entity ID uniqueness
func formatTimestamp() string {
	return time.Now().Format("20060102150405.000000000")
}
