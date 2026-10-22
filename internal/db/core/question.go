package core

import "time"

// QuestionStatus values, stored as INTEGER in questions.status.
const (
	QuestionStatusPending  = 1 // QUESTION_STATUS_PENDING
	QuestionStatusResolved = 2 // QUESTION_STATUS_RESOLVED
)

// Question is a gate a workflow raised for the user to answer (the questions
// table). It lives in core, not internal/db, so internal/store can return it.
type Question struct {
	ID                 string
	ChatID             string
	WorkflowID         string
	TemporalWorkflowID string
	ThreadID           string
	StepID             string
	LoopNodeID         *string
	LoopIteration      *int
	Status             int
	Metadata           *string
	ResponseData       *string
	CreatedAt          time.Time
	ResolvedAt         *time.Time
	ToolCallID         *string
	// Async marks a question asked without waiting (ask_user async: true):
	// the asking thread carried on, and the answer is delivered to it later
	// as a message from the user. It is never a gate on the chat, and its
	// thread ending does not close it (research/ASYNC_ASK_USER.md).
	Async bool
	// WithdrawnReason is set when the agent withdrew the question
	// (withdraw_question) instead of the user answering it.
	WithdrawnReason *string
}
