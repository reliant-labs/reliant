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
	UpdateTypeMessage              UpdateType = reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_MESSAGE
	UpdateTypeApproval             UpdateType = reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_APPROVAL
	UpdateTypeThread               UpdateType = reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_THREAD
	UpdateTypeToolCall             UpdateType = reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_TOOL_CALL
	UpdateTypeWorkflowStatus       UpdateType = reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_WORKFLOW_STATUS
	UpdateTypeError                UpdateType = reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_ERROR
	UpdateTypeChat                 UpdateType = reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_CHAT
	UpdateTypeRunOutput            UpdateType = reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_RUN_OUTPUT
	UpdateTypeNodeExecution        UpdateType = reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_NODE_EXECUTION
	UpdateTypeExecutionLog         UpdateType = reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_EXECUTION_LOG
	UpdateTypeWorkflowExecution    UpdateType = reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_WORKFLOW_EXECUTION
	UpdateTypeInfo                 UpdateType = reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_INFO
	UpdateTypeWarning              UpdateType = reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_WARNING
	UpdateTypeRefetch              UpdateType = reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_REFETCH
	UpdateTypeQuestion             UpdateType = reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_QUESTION
	UpdateTypeStreamFinalized      UpdateType = reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_STREAM_FINALIZED
	UpdateTypeAgentMessagesDrained UpdateType = reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_AGENT_MESSAGES_DRAINED
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

// ToolCallUpdate represents a tool execution status update.
//
// ToolCallID is the canonical identity of the card this status describes: the
// LLM-issued tool-call id (e.g. "toolu_01..."). It is the ONLY id that can key
// live status, because it is the only one that exists for the whole lifetime
// of a tool call. It arrives with the very first streaming event and is
// carried through persistence unchanged.
//
// A content-block UUID cannot do either job. It is empty while the assistant
// message is still streaming, and it is minted fresh when the message is
// persisted — which happens BEFORE tools execute. Keying status on it means
// every status emitted after persistence is filed under an id no reader ever
// asks for, so live status silently misses every card. That is why this struct
// carries no content-block id: the tool-status channel is addressed purely by
// tool-call id.
//
// Note this is a DIFFERENT identifier space from the one approvals use.
// Approvals legitimately key on content_block_id; tool status does not. The
// two must not be conflated.
type ToolCallUpdate struct {
	UpdateType  UpdateType     `json:"update_type"` // Always "tool_call"
	ToolCallID  string         `json:"tool_call_id"`
	ToolName    string         `json:"tool_name"`
	Status      ToolCallStatus `json:"status"`
	Timestamp   string         `json:"timestamp,omitempty"`
	NodeID      string         `json:"node_id,omitempty"`
	RequestedAt string         `json:"requested_at,omitempty"`
	StartedAt   string         `json:"started_at,omitempty"`
	CompletedAt string         `json:"completed_at,omitempty"`
}

func (u ToolCallUpdate) Type() UpdateType { return UpdateTypeToolCall }

// ============================================================================
// MESSAGE UPDATES
// ============================================================================

// MessageUpdateData is the payload for update_type="message" chat_updates —
// the row that renders as a message in the transcript. Four call sites build
// this payload (SaveMessage's hot path in internal/threads, EnrichMessageUpdate,
// SaveMessageToThreadWithID, and the tool-denial message in
// internal/grpc/services/approval.go); route all of them through this struct
// instead of a hand-built map so a field rename (e.g. ordinal -> seq) fails to
// compile at every call site instead of silently vanishing from the JSON.
//
// The four writers populate different subsets of these fields. Model that
// with omitempty + pointers rather than forcing every caller to supply every
// field — a nil pointer omits the key entirely, matching a writer that never
// set it, while a non-nil pointer to a zero value (0, or an empty slice)
// still serializes, matching a writer that always sets it. Do not infer "this
// field is unused" from one caller; check all four before removing one.
type MessageUpdateData struct {
	UpdateType string `json:"update_type"` // always "message"
	ID         string `json:"id"`
	// Role is the MessageRole proto enum (int32) on three writers, and the
	// literal string "tool" on the approval-denial writer. That
	// inconsistency predates this struct; interface{} preserves each
	// caller's existing wire value rather than silently normalizing it.
	Role          interface{}              `json:"role"`
	Seq           int64                    `json:"seq"`
	Ordinal       int64                    `json:"ordinal"`
	Thread        string                   `json:"thread"`
	ContentBlocks []map[string]interface{} `json:"content_blocks"`
	CreatedAt     string                   `json:"created_at"`

	ChatID          string `json:"chat_id,omitempty"`
	UpdatedAt       string `json:"updated_at,omitempty"`
	ContextWindowID string `json:"context_window_id,omitempty"`
	StreamingState  string `json:"streaming_state,omitempty"`
	// ContextSequence is a pointer because 0 is a valid sequence: only a nil
	// pointer (the approval-denial writer, which never sets it) omits the key.
	ContextSequence *int `json:"context_sequence,omitempty"`
	// Attachments is a pointer to a slice so a writer that always emits
	// attachments (even as "[]") is distinguishable from the approval-denial
	// writer, which never sets this field at all.
	Attachments         *[]map[string]interface{} `json:"attachments,omitempty"`
	ThreadTokenCount    *int                      `json:"thread_token_count,omitempty"`
	CompactionThreshold *int                      `json:"compaction_threshold,omitempty"`
	DisplayStyle        *int32                    `json:"display_style,omitempty"`
	TokenCount          *int                      `json:"token_count,omitempty"`
}

func (u MessageUpdateData) Type() UpdateType { return UpdateTypeMessage }

// Marshal serializes the update to the JSON string CreateChatUpdate expects.
func (u MessageUpdateData) Marshal() (string, error) {
	data, err := json.Marshal(u)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

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

// AgentMessagesDrainedUpdate names the mailbox rows that just became transcript
// messages.
//
// The pending-queue strip is fed by a poll (ListQueuedAgentMessages), so
// without this the only thing that ever retires a row is the next poll
// happening to come back without it. Between the drain committing and that
// poll landing, the message is in the transcript AND still in the strip --
// the same words shown twice, for up to one poll interval.
//
// Shortening the poll is not a fix; it narrows the window and leaves the
// double-display reachable. This closes it instead: the update is written
// inside the drain's own transaction, so it is visible to a reader exactly
// when the messages it announces are, and it rides the same ordered,
// sequenced chat_updates channel those messages do. A client that applies a
// batch in order therefore commits "row leaves the strip" and "message enters
// the transcript" together.
//
// MessageIDs carries the ids the client already knows -- the agent_messages
// row ids, which are what the strip is keyed by. Note this is deliberately
// NOT the delivered_message_id link that agent_messages already stores: that
// column points at the HIDDEN envelope, a message the transcript never
// renders, so it cannot tell a client which VISIBLE entry replaced a given
// row. The ids are what the strip needs, and they are enough.
type AgentMessagesDrainedUpdate struct {
	UpdateTypeName string `json:"update_type"` // Always "agent_messages_drained"
	// Thread is the mailbox's owner -- the recipient thread whose strip
	// these rows were showing in.
	Thread string `json:"thread"`
	// MessageIDs are agent_messages row ids, not messages.id.
	MessageIDs []string `json:"message_ids"`
}

func (u AgentMessagesDrainedUpdate) Type() UpdateType { return UpdateTypeAgentMessagesDrained }

// ============================================================================
// ENTITY ID GENERATORS
// ============================================================================

// EntityIDForQuestion generates entity ID for question updates
func EntityIDForQuestion(questionID string) string {
	return "question-" + questionID + "-" + formatTimestamp()
}

// EntityIDForAgentMessagesDrained generates the entity ID for a drain
// announcement. The timestamp makes it unique per drain -- see
// EmitAgentMessagesDrainedUpdate for why per-thread would be wrong.
func EntityIDForAgentMessagesDrained(thread string) string {
	return "agent-messages-drained-" + thread + "-" + formatTimestamp()
}

// EntityIDForToolCall generates entity ID for tool call updates.
// The embedded id is the LLM tool-call id; GetLatestNonMessageUpdatesPerEntity
// parses it back out to dedup a call's status transitions down to the latest.
func EntityIDForToolCall(toolCallID string) string {
	return "tool-" + toolCallID + "-" + formatTimestamp()
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
