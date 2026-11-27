// Copyright (c) 2025 Reliant Labs
package core

import (
	"encoding/json"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// MessageUpdateData is the payload for update_type="message" chat_updates —
// the row that renders as a message in the transcript. Four call sites build
// this payload (SaveMessage's hot path in internal/threads, EnrichMessageUpdate,
// SaveMessageToThreadWithID, and the tool-denial message in
// internal/handlers/approval/approval.go); route all of them through this struct
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
	// Sender attributes a message another agent wrote. It is parsed straight
	// into reliantv1.Message.sender on the client, hence the proto JSON names.
	Sender *MessageSenderPayload `json:"sender,omitempty"`
}

// MessageSenderPayload is the JSON form of reliantv1.MessageSender.
type MessageSenderPayload struct {
	ThreadID string `json:"thread_id"`
	Title    string `json:"title,omitempty"`
	Kind     int32  `json:"kind"`
	Subject  string `json:"subject,omitempty"`
}

// NewMessageSenderPayload builds the sender of a live message update. title
// is the sender thread's title, nil when it has none or the thread is gone:
// the attribution is still worth showing without it. subject is the sending
// agent's one-line subject, "" when it gave none. It returns nil for a
// message with no sender.
func NewMessageSenderPayload(senderThreadID string, title *string, kind reliantv1.AgentMessageKind, subject string) *MessageSenderPayload {
	if senderThreadID == "" {
		return nil
	}
	p := &MessageSenderPayload{ThreadID: senderThreadID, Kind: int32(kind), Subject: subject}
	if title != nil {
		p.Title = *title
	}
	return p
}

func (u MessageUpdateData) Type() reliantv1.ChatUpdateType {
	return reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_MESSAGE
}

// Marshal serializes the update to the JSON string CreateChatUpdate expects.
func (u MessageUpdateData) Marshal() (string, error) {
	data, err := json.Marshal(u)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
