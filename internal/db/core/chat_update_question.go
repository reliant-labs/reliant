// Copyright (c) 2025 Reliant Labs
package core

import (
	"encoding/json"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// TypedChatUpdate is a chat update with a typed payload.
type TypedChatUpdate interface {
	Type() reliantv1.ChatUpdateType
}

// MarshalUpdate marshals a typed update to JSON.
func MarshalUpdate(update TypedChatUpdate) (string, error) {
	data, err := json.Marshal(update)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// QuestionUpdate represents a question status update
type QuestionUpdate struct {
	UpdateType reliantv1.ChatUpdateType `json:"update_type"`
	QuestionID string                   `json:"question_id"`
	ChatID     string                   `json:"chat_id"`
	WorkflowID string                   `json:"workflow_id"`
	// ThreadID is the thread that raised the question. Without it a supervision
	// surface reading this feed can see THAT a gate opened but not which of a
	// run's fanned-out threads is waiting.
	ThreadID string `json:"thread_id"`
	StepID   string `json:"step_id"`
	Status   string `json:"status"`
	Metadata string `json:"metadata,omitempty"`
	// Async marks a question asked without waiting: no thread is parked on
	// it, so a client must not treat it as the chat's or a thread's pending
	// gate (the composer, the pill), only as an answerable timeline card.
	Async bool `json:"async,omitempty"`
	// ToolCallID is the ask_user tool call that raised the question, when
	// one did: the timeline card it belongs to.
	ToolCallID string `json:"tool_call_id,omitempty"`
	// WithdrawnReason is set on the "resolved" update of a question its
	// agent withdrew: the timeline card shows it instead of an answer.
	WithdrawnReason string `json:"withdrawn_reason,omitempty"`
}

func (u QuestionUpdate) Type() reliantv1.ChatUpdateType {
	return reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_QUESTION
}

// EntityIDForQuestion is the entity id of a question update; the timestamp
// keeps each status change of a question a distinct entity.
func EntityIDForQuestion(questionID string) string {
	return "question-" + questionID + "-" + time.Now().Format("20060102150405.000000000")
}
