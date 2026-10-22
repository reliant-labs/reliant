// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/askuser"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// list_pending_questions and withdraw_question: an agent that asks without
// waiting (ask_user's default; blocking: true waits) keeps going, so it needs to see what it is
// still waiting on, and to retract a question its default has made moot —
// otherwise the user's inbox fills with stale ones. Both are scoped to the
// caller's thread subtree: its own questions and those of every sub-agent
// below it, never a sibling's (research/ASYNC_ASK_USER.md).

const (
	ListPendingQuestionsToolName = "list_pending_questions"
	WithdrawQuestionToolName     = "withdraw_question"

	listPendingQuestionsDescription = `List the questions you and your sub-agents have asked the user (ask_user) that are still unanswered.

Shows your own thread's questions and those of every sub-agent below you, recursively — never a sibling's. For each: question_id, who asked (you, or the sub-agent's agent_id and title), whether it is async (asked without waiting — nobody is blocked on it) or blocking (that agent is parked until the user answers), the question and its options, and how long it has been waiting.

When to call it:
- Before you finish a task: check for async questions still unanswered. If you went ahead on a default and the answer no longer matters, withdraw it with withdraw_question so it leaves the user's inbox. If it still matters, say so in your final answer.
- Before asking the user something: avoid asking a question that is already pending.

Answers to async questions arrive as messages from the user that name the question_id, so you can match an answer to the question it came from.`

	withdrawQuestionDescription = `Withdraw an async question (an ask_user call without blocking: true) that you or one of your sub-agents asked and that no longer needs an answer — typically because you proceeded on a default and the outcome is settled.

Withdrawing closes the question: it leaves the user's inbox and its card in the chat shows "Withdrawn: <reason>". The reason is shown to the user, so make it a short, plain sentence (e.g. "Went with Postgres; the schema is already migrated").

Only async questions can be withdrawn, and only those in your own thread or a sub-agent's below you. A blocking question cannot be: its agent is waiting on the user's answer, and only the user (or that agent finishing) can close it. Find question ids with list_pending_questions.`
)

// ListPendingQuestionsParams has no fields: the scope is the caller's
// subtree, by construction.
type ListPendingQuestionsParams struct{}

// PendingQuestionEntry is one listed question.
type PendingQuestionEntry struct {
	QuestionID string `json:"question_id"`
	// AgentID is the thread that asked: the caller's own, or a sub-agent's.
	AgentID string `json:"agent_id"`
	// Title is the asking sub-agent's title, when it has one.
	Title     string                 `json:"title,omitempty"`
	AskedBy   string                 `json:"asked_by"` // "you" or "sub-agent"
	Async     bool                   `json:"async"`
	Questions []PendingQuestionAsked `json:"questions"`
	CreatedAt string                 `json:"created_at"`
	WaitingMs int64                  `json:"waiting_ms"`
}

// PendingQuestionAsked is one sub-question of an ask_user call.
type PendingQuestionAsked struct {
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
}

// ListPendingQuestionsResponseMetadata is list_pending_questions' metadata.
type ListPendingQuestionsResponseMetadata struct {
	Questions []PendingQuestionEntry `json:"questions"`
}

type listPendingQuestionsTool struct {
	repo listPendingQuestionsStore
}

// NewListPendingQuestionsTool returns list_pending_questions.
func NewListPendingQuestionsTool(repo listPendingQuestionsStore) Tool {
	return NewToolWrapper[ListPendingQuestionsParams, ToolResponse](&listPendingQuestionsTool{repo: repo})
}

func (t *listPendingQuestionsTool) Name() string        { return ListPendingQuestionsToolName }
func (t *listPendingQuestionsTool) Description() string { return listPendingQuestionsDescription }
func (t *listPendingQuestionsTool) RequiresPermission(ListPendingQuestionsParams) (bool, error) {
	return false, nil
}

func (t *listPendingQuestionsTool) Execute(rc *rctx.ToolContext, _ ListPendingQuestionsParams) (ToolResponse, error) {
	if t.repo == nil {
		return NewTextErrorResponse("This tool requires a database connection and is not available in daemon-only mode"), nil
	}
	if rc.ChatID == "" || rc.Thread == "" {
		return NewTextErrorResponse("Thread context required"), nil
	}
	rows, err := t.repo.ListPendingQuestionsInThreadSubtree(rc.Context, rc.ChatID, rc.Thread)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to list pending questions: %v", err)), nil
	}
	if len(rows) == 0 {
		return WithResponseMetadata(NewTextResponse("No unanswered questions from you or your sub-agents."),
			ListPendingQuestionsResponseMetadata{Questions: []PendingQuestionEntry{}}), nil
	}

	now := time.Now()
	entries := make([]PendingQuestionEntry, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, pendingQuestionEntry(row, rc.Thread, now))
	}
	return WithResponseMetadata(NewTextResponse(formatPendingQuestions(entries)),
		ListPendingQuestionsResponseMetadata{Questions: entries}), nil
}

func pendingQuestionEntry(row db.SubtreeQuestion, caller string, now time.Time) PendingQuestionEntry {
	e := PendingQuestionEntry{
		QuestionID: row.ID,
		AgentID:    row.ThreadID,
		AskedBy:    "sub-agent",
		Async:      row.Async,
		Questions:  []PendingQuestionAsked{},
		CreatedAt:  row.CreatedAt.UTC().Format(time.RFC3339),
		WaitingMs:  now.Sub(row.CreatedAt).Milliseconds(),
	}
	if row.ThreadID == caller {
		e.AskedBy = "you"
	}
	if row.ThreadTitle != nil {
		e.Title = *row.ThreadTitle
	}
	if row.Metadata != nil {
		if md, ok := askuser.ParseMetadata(*row.Metadata); ok {
			for _, q := range md.Questions {
				e.Questions = append(e.Questions, PendingQuestionAsked{Question: q.Question, Options: q.OptionLabels()})
			}
		}
	}
	return e
}

func formatPendingQuestions(entries []PendingQuestionEntry) string {
	var b strings.Builder
	noun := "questions"
	if len(entries) == 1 {
		noun = "question"
	}
	fmt.Fprintf(&b, "%d unanswered %s from you and your sub-agents:\n", len(entries), noun)
	for i, e := range entries {
		who := "asked by you"
		if e.AskedBy != "you" {
			who = fmt.Sprintf("asked by sub-agent %s", e.AgentID)
			if e.Title != "" {
				who += fmt.Sprintf(" (%q)", e.Title)
			}
		}
		kind := "async — nobody is waiting on it"
		if !e.Async {
			kind = "BLOCKING — that agent is waiting on the user's answer"
		}
		fmt.Fprintf(&b, "\n%d. question_id: %s · %s · %s · waiting %s\n", i+1, e.QuestionID, kind, who,
			formatDuration(time.Duration(e.WaitingMs)*time.Millisecond))
		for _, q := range e.Questions {
			fmt.Fprintf(&b, "   Q: %s\n", q.Question)
			if len(q.Options) > 0 {
				fmt.Fprintf(&b, "      options: %s\n", strings.Join(q.Options, " | "))
			}
		}
	}
	b.WriteString("\nWithdraw an async question that no longer matters with withdraw_question(question_id, reason). " +
		"Blocking questions cannot be withdrawn. Answers to async questions arrive as messages from the user naming their question_id.")
	return b.String()
}

// WithdrawQuestionParams is withdraw_question's input.
type WithdrawQuestionParams struct {
	QuestionID string `json:"question_id" jsonschema:"required,description=The question_id to withdraw (from list_pending_questions, or the ask_user result)."`
	Reason     string `json:"reason" jsonschema:"required,description=Why it no longer needs an answer. Shown to the user on the question's card, so keep it short and plain."`
}

type withdrawQuestionTool struct {
	repo withdrawQuestionStore
}

// NewWithdrawQuestionTool returns withdraw_question.
func NewWithdrawQuestionTool(repo withdrawQuestionStore) Tool {
	return NewToolWrapper[WithdrawQuestionParams, ToolResponse](&withdrawQuestionTool{repo: repo})
}

func (t *withdrawQuestionTool) Name() string        { return WithdrawQuestionToolName }
func (t *withdrawQuestionTool) Description() string { return withdrawQuestionDescription }
func (t *withdrawQuestionTool) RequiresPermission(WithdrawQuestionParams) (bool, error) {
	return false, nil
}

func (t *withdrawQuestionTool) Execute(rc *rctx.ToolContext, params WithdrawQuestionParams) (ToolResponse, error) {
	if t.repo == nil {
		return NewTextErrorResponse("This tool requires a database connection and is not available in daemon-only mode"), nil
	}
	if rc.ChatID == "" || rc.Thread == "" {
		return NewTextErrorResponse("Thread context required"), nil
	}
	id := strings.TrimSpace(params.QuestionID)
	reason := strings.TrimSpace(params.Reason)
	if id == "" {
		return NewTextErrorResponse("question_id is required"), nil
	}
	if reason == "" {
		return NewTextErrorResponse("reason is required: the user sees it on the question's card"), nil
	}

	outcome, err := t.repo.WithdrawAsyncQuestion(rc.Context, rc.ChatID, rc.Thread, id, reason)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to withdraw question %s: %v", id, err)), nil
	}
	switch outcome {
	case db.QuestionWithdrawn:
		return NewTextResponse(fmt.Sprintf("Withdrew question %s. It has left the user's inbox, and its card shows: Withdrawn: %s", id, reason)), nil
	case db.QuestionWithdrawBlocking:
		return NewTextErrorResponse(fmt.Sprintf("Question %s is blocking: the agent that asked it is waiting on the user's answer, "+
			"so it cannot be withdrawn. Only the user's answer, or that agent finishing, closes it.", id)), nil
	default:
		// Same answer for "no such question", "a sibling's", and "already
		// answered": the caller learns nothing about questions outside its
		// subtree.
		return NewTextErrorResponse(fmt.Sprintf("No unanswered question %s from you or your sub-agents — it may already be answered. "+
			"Call list_pending_questions to see the ones that are.", id)), nil
	}
}
