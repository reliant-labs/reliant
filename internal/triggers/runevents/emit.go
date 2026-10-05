// Copyright (c) 2025 Reliant Labs

// Package runevents writes the run-event outbox that workflow-event triggers
// fire from: one core.RunEvent per run transition another run may react to.
//
// Every write happens INSIDE the transaction of the state change it reports —
// the root workflow's terminal status, or the approval or question that
// blocked the run. That is the whole durability argument: the event commits
// exactly when the transition does, so a worker that dies after the commit
// still leaves the event for the relay, and one that dies before it leaves
// neither (and the activity's retry writes both). The (dedupe_key) constraint
// makes that retry a no-op instead of a second event.
//
// It is a leaf package (db + core only) because its callers sit on both sides
// of the runtime: the WorkflowStatus/ApprovalCreate/QuestionCreate activities
// and the reconciler, which repairs a run Temporal killed outright.
package runevents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// TriggerKind is the stored trigger kind that fires on run events, and
// TriggerEventKind the trigger_events kind its firings record (dedupe key
// "<trigger id>:<run event id>").
//
// Declared here rather than in core so this package does not collide with the
// trigger-receivers stream, which adds core.TriggerKindWorkflowEvent and
// core.TriggerEventKindWorkflowEvent with the same spelling. Once that is on
// main these become aliases of those constants.
const (
	TriggerKind      core.TriggerKind      = "workflow_event"
	TriggerEventKind core.TriggerEventKind = "workflow_event"
)

// MaxTextBytes bounds every free-text payload field (summary, error, blocked
// prompt). Payloads are templated into prompts and evaluated by CEL filters;
// an unbounded transcript in either place is a cost and an injection surface.
const MaxTextBytes = 4096

// Store is what the emitter reads and writes. *db.Repo satisfies it; every
// call honors the ambient RunTx transaction.
type Store interface {
	GetChat(ctx context.Context, id string) (*core.Chat, error)
	HasEnabledTriggerOfKind(ctx context.Context, userID string, kind core.TriggerKind) (bool, error)
	CreateRunEvent(ctx context.Context, ev *core.RunEvent) (bool, error)
	LockChatForRunEvent(ctx context.Context, chatID string) error
	CountOtherPendingBlockers(ctx context.Context, chatID, excludeID string) (int, error)
}

// Terminal describes a root run reaching a terminal state.
type Terminal struct {
	ChatID       string
	WorkflowID   string // the root workflow id (== chat id)
	WorkflowName string
	// RunID is the Temporal run id of the execution that ended. A chat's root
	// workflow is restarted under the same workflow id for every follow-up
	// turn, so the run id is what makes "turn 1 finished" and "turn 2
	// finished" two events rather than one.
	RunID   string
	Outcome core.RunEventOutcome // finished | failed
	// Summary is the run's final assistant text; Error the failure message.
	// Both are truncated to MaxTextBytes.
	Summary string
	Error   string
}

// Blocked describes a run that started waiting on a human.
type Blocked struct {
	ChatID       string
	WorkflowID   string
	WorkflowName string
	// BlockerID is the approval or question id; it is the dedupe key, since
	// each pending item is one transition into "blocked" at most.
	BlockerID   string
	BlockerKind string // "approval" | "question"
	Prompt      string // the approval title or question text
}

// EmitTerminal records a root run's terminal transition. It is a no-op —
// and a single indexed EXISTS — for an owner with no enabled workflow-event
// trigger. Call it inside the transaction that writes the terminal status.
func EmitTerminal(ctx context.Context, store Store, t Terminal, now time.Time) (bool, error) {
	if t.Outcome != core.RunEventFinished && t.Outcome != core.RunEventFailed {
		return false, fmt.Errorf("terminal run event outcome %q", t.Outcome)
	}
	chat, ok, err := ownerWantsEvents(ctx, store, t.ChatID)
	if err != nil || !ok {
		return false, err
	}
	runKey := t.RunID
	if runKey == "" {
		// No run id (a caller outside Temporal): fall back to the workflow id,
		// which still dedupes retries of the same report.
		runKey = t.WorkflowID
	}
	payload := basePayload(chat, t.WorkflowID, t.WorkflowName, t.Outcome)
	payload["temporal_run_id"] = t.RunID
	payload["summary"] = Truncate(t.Summary, MaxTextBytes)
	payload["error"] = Truncate(t.Error, MaxTextBytes)

	return store.CreateRunEvent(ctx, &core.RunEvent{
		ID:           uuid.NewString(),
		UserID:       chat.UserID,
		ChatID:       chat.ID,
		WorkflowName: t.WorkflowName,
		Outcome:      t.Outcome,
		DedupeKey:    fmt.Sprintf("%s:%s:%s", chat.ID, runKey, t.Outcome),
		Payload:      payload,
		OccurredAt:   now,
		CreatedAt:    now,
	})
}

// EmitBlocked records that the run became blocked on b — but only when b is
// the run's FIRST pending approval or question. A run that already waits on
// a human is not newly blocked by a second item, and firing again would turn
// one stuck run into a burst of triggered runs.
//
// Call it inside the transaction that inserts the approval or question row.
// It row-locks the chat so two concurrent creations cannot both see the other
// as absent.
func EmitBlocked(ctx context.Context, store Store, b Blocked, now time.Time) (bool, error) {
	chat, ok, err := ownerWantsEvents(ctx, store, b.ChatID)
	if err != nil || !ok {
		return false, err
	}
	if err := store.LockChatForRunEvent(ctx, chat.ID); err != nil {
		return false, err
	}
	others, err := store.CountOtherPendingBlockers(ctx, chat.ID, b.BlockerID)
	if err != nil {
		return false, err
	}
	if others > 0 {
		return false, nil
	}

	workflowID := b.WorkflowID
	if workflowID == "" {
		workflowID = chat.ID
	}
	payload := basePayload(chat, workflowID, b.WorkflowName, core.RunEventBlocked)
	payload["blocked_on"] = b.BlockerKind
	payload["blocker_id"] = b.BlockerID
	payload["prompt"] = Truncate(b.Prompt, MaxTextBytes)

	return store.CreateRunEvent(ctx, &core.RunEvent{
		ID:           uuid.NewString(),
		UserID:       chat.UserID,
		ChatID:       chat.ID,
		WorkflowName: b.WorkflowName,
		Outcome:      core.RunEventBlocked,
		DedupeKey:    fmt.Sprintf("%s:blocked:%s", chat.ID, b.BlockerID),
		Payload:      payload,
		OccurredAt:   now,
		CreatedAt:    now,
	})
}

// ownerWantsEvents loads the chat and reports whether its owner has any
// enabled workflow-event trigger. The check is the cost gate: without it every
// run of every user would write an outbox row nobody reads.
func ownerWantsEvents(ctx context.Context, store Store, chatID string) (*core.Chat, bool, error) {
	if chatID == "" {
		return nil, false, nil
	}
	chat, err := store.GetChat(ctx, chatID)
	if errors.Is(err, core.ErrChatNotFound) {
		// A chat deleted under a finishing run has nobody to fire for, and
		// must not fail the status write the event rides on.
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("load chat %s for run event: %w", chatID, err)
	}
	if chat == nil || chat.UserID == "" {
		return nil, false, nil
	}
	ok, err := store.HasEnabledTriggerOfKind(ctx, chat.UserID, TriggerKind)
	if err != nil {
		return nil, false, err
	}
	return chat, ok, nil
}

// basePayload is the part of every run event's payload that describes the
// source run. Keys are always present (empty when unknown) so a CEL filter
// over trigger.payload never fails with "no such key".
func basePayload(chat *core.Chat, workflowID, workflowName string, outcome core.RunEventOutcome) map[string]any {
	name := workflowName
	if name == "" && chat.WorkflowName != nil {
		name = *chat.WorkflowName
	}
	return map[string]any{
		"run_id":        chat.ID,
		"chat_id":       chat.ID,
		"workflow_id":   workflowID,
		"workflow_name": name,
		"workflow":      WorkflowRef(name),
		"project_id":    chat.ProjectID,
		"chat_title":    chat.Title,
		"outcome":       string(outcome),
		"summary":       "",
		"error":         "",
		"blocked_on":    "",
		"prompt":        "",
	}
}

// WorkflowRef is the comparable reference of a workflow name: builtin
// references are kept verbatim, everything else is the normalized slug the
// launcher resolves (lowercase, hyphenated). A source filter is compared
// through the same function, so "Code Review" and "code-review" match.
func WorkflowRef(name string) string {
	name = strings.TrimSpace(name)
	if strings.HasPrefix(name, "builtin://") {
		return name
	}
	slug := strings.ToLower(name)
	slug = strings.ReplaceAll(slug, " ", "-")
	return strings.ReplaceAll(slug, "_", "-")
}

// QuestionPrompt extracts the human-readable question text from a question
// row's metadata JSON: the "questions" array's "question" fields (ask_user and
// the ask_question node both use that shape), else a top-level "question" or
// "title". Unrecognized metadata yields "".
func QuestionPrompt(metadata *string) string {
	if metadata == nil || *metadata == "" {
		return ""
	}
	var meta struct {
		Question  string `json:"question"`
		Title     string `json:"title"`
		Questions []struct {
			Question string `json:"question"`
		} `json:"questions"`
	}
	if err := json.Unmarshal([]byte(*metadata), &meta); err != nil {
		return ""
	}
	var parts []string
	for _, q := range meta.Questions {
		if q.Question != "" {
			parts = append(parts, q.Question)
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n")
	}
	if meta.Question != "" {
		return meta.Question
	}
	return meta.Title
}

// Truncate cuts s to at most max bytes on a rune boundary, marking the cut.
func Truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	const marker = "…"
	cut := max - len(marker)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + marker
}
