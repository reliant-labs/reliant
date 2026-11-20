// Copyright (c) 2025 Reliant Labs
package question

import (
	"encoding/json"
	"strings"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
)

// questionsToProto is questions as access reads them. A viewer gets every
// question's identity and status, but a workflow step's question (no
// tool_call_id) without its metadata or answer: the engine resolved that
// prompt, and a resolved value can carry an earlier step's or tool's result
// (D2). An agent's question (ask_user) is the model's own words, which the
// viewer already reads as the tool call's input, so it stays whole.
func questionsToProto(questions []*db.Question, access authz.Access) []*reliantv1.QuestionInfo {
	out := make([]*reliantv1.QuestionInfo, 0, len(questions))
	for _, q := range questions {
		info := questionToProto(q)
		if access.Viewer() && info.GetToolCallId() == "" {
			info.Metadata = nil
			info.ResponseData = nil
		}
		out = append(out, info)
	}
	return out
}

// ============================================================================
// HELPERS
// ============================================================================

// questionToProto converts a db.Question to proto QuestionInfo
func questionToProto(q *db.Question) *reliantv1.QuestionInfo {
	info := &reliantv1.QuestionInfo{
		QuestionId: q.ID,
		ChatId:     q.ChatID,
		WorkflowId: q.WorkflowID,
		ThreadId:   q.ThreadID,
		StepId:     q.StepID,
		Status:     questionStatusString(q.Status),
		CreatedAt:  q.CreatedAt.Format(time.RFC3339),
	}
	if q.Metadata != nil {
		info.Metadata = q.Metadata
	}
	info.Async = q.Async
	if q.ToolCallID != nil {
		info.ToolCallId = *q.ToolCallID
	}
	if q.ResponseData != nil {
		info.ResponseData = q.ResponseData
	}
	if q.WithdrawnReason != nil {
		info.WithdrawnReason = q.WithdrawnReason
	}
	return info
}

// questionStatusString converts an int status to string
func questionStatusString(status int) string {
	switch status {
	case db.QuestionStatusPending:
		return "pending"
	case db.QuestionStatusResolved:
		return "resolved"
	default:
		return "unknown"
	}
}

// formatAnswersReply extracts a user-visible reply from the ask_user answers
// payload. Mirrors parseQuestionResponse's feedback semantics: a bare
// "Continue" selection with no freetext is not feedback and yields "" (no
// message saved — the workflow exits its loop without another LLM call).
// Unlike the workflow (which only consumes the first answer), all answered
// questions are preserved in the saved message.
func formatAnswersReply(responseData string) string {
	var payload struct {
		Answers []struct {
			Question string   `json:"question"`
			Selected []string `json:"selected"`
			Freetext string   `json:"freetext"`
		} `json:"answers"`
	}
	if err := json.Unmarshal([]byte(responseData), &payload); err != nil || len(payload.Answers) == 0 {
		return ""
	}

	var parts []string
	for _, a := range payload.Answers {
		answer := a.Freetext
		if answer == "" {
			selected := make([]string, 0, len(a.Selected))
			for _, s := range a.Selected {
				if s != "Continue" {
					selected = append(selected, s)
				}
			}
			answer = strings.Join(selected, ", ")
		}
		if answer == "" {
			continue
		}
		if a.Question != "" && len(payload.Answers) > 1 {
			parts = append(parts, a.Question+": "+answer)
		} else {
			parts = append(parts, answer)
		}
	}
	return strings.Join(parts, "\n")
}
