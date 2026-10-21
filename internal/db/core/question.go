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
}
