// Copyright (c) 2025 Reliant Labs

// Package execfollow implements `reliant workflow follow`: it turns a chat's
// workflow-execution update feed into a stream of NDJSON lifecycle events
// (node id, old->new state, timestamps), fires user-supplied exec hooks on
// matching events, and decides the process exit code from the root
// workflow's terminal state.
package execfollow

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Event types emitted on the NDJSON stream.
const (
	EventNodeStarted   = "node_started"
	EventNodeCompleted = "node_completed"
	EventNodeFailed    = "node_failed"
	// EventNodeSkipped is a node whose condition evaluated false: it never ran.
	// Its own lifecycle event says "completed", because the activity that
	// records the skip completed — so without this a configured-off gate is
	// reported as "✓ node review completed", indistinguishable from a reviewer
	// that ran and passed. Skipped is neither passed nor failed.
	EventNodeSkipped       = "node_skipped"
	EventWorkflowStarted   = "workflow_started"
	EventWorkflowCompleted = "workflow_completed"
	EventWorkflowFailed    = "workflow_failed"
	EventWorkflowCancelled = "workflow_cancelled"
	// EventQuestion is emitted when a workflow raises a pending question
	// (awaiting human/agent input). It carries the question id and the
	// sub-question prompts + option labels so a supervisor can answer.
	EventQuestion = "question"
	// EventQuestionAnswered closes a question gate: the pending question was
	// resolved and the run is moving again. Without it a gate has an open and no
	// close, so the time a run spends parked is invisible on the stream — a run
	// that sat 22m44s on one question looks identical to one that answered
	// instantly, and phase timings silently include the human's think time.
	// Carries DurationMs (time in gate) whenever the open was seen in this
	// session; a follower that attached after the open reports the close alone.
	EventQuestionAnswered = "question_answered"
	// EventApproval is emitted when a workflow raises a pending approval gate
	// (awaiting approve/deny). It carries the approval id and title.
	EventApproval = "approval"
	// EventApprovalResolved closes an approval gate — the mirror of
	// EventQuestionAnswered, carrying the resolution and the time in gate.
	EventApprovalResolved = "approval_resolved"
	// EventFollowTimeout is emitted as the final line when --timeout elapses.
	EventFollowTimeout = "follow_timeout"
)

// Run outcomes carried on a terminal workflow event (Node.outcome from the
// workflow YAML). A run that routes to a failure-outcome terminal node is a
// COMPLETED Temporal execution whose work did not pass — the lifecycle status
// alone cannot say that, which is why the verdict rides on the event.
const (
	OutcomeSuccess = "success"
	OutcomeFailure = "failure"
)

// HookEventAny matches every event.
const HookEventAny = "any"

// hookableEvents is the set of event types a --hook may bind to (plus "any").
// Deliberately a subset of emitted events: workflow_started and
// workflow_cancelled are observable via "any" and the NDJSON stream.
var hookableEvents = map[string]bool{
	EventNodeStarted:       true,
	EventNodeCompleted:     true,
	EventNodeFailed:        true,
	EventNodeSkipped:       true,
	EventWorkflowCompleted: true,
	EventWorkflowFailed:    true,
	EventQuestion:          true,
	EventQuestionAnswered:  true,
	EventApproval:          true,
	EventApprovalResolved:  true,
	HookEventAny:           true,
}

// SubQuestion is one prompt within a question boundary, with its option labels.
type SubQuestion struct {
	Question      string   `json:"question"`
	Options       []string `json:"options,omitempty"`
	AllowMultiple bool     `json:"allow_multiple,omitempty"`
}

// QuestionInfo is the structured payload of an EventQuestion event: an ask_user
// question may bundle several sub-questions.
type QuestionInfo struct {
	QuestionID string `json:"question_id"`
	StepID     string `json:"step_id,omitempty"`
	// ThreadID names which of a run's threads is waiting. A fanned-out run has
	// many threads open at once and only some of them are gated, so the gate id
	// alone does not say where to look.
	ThreadID string `json:"thread_id,omitempty"`
	// Stuck marks a "stuck" escalation gate (the stuck_checkpoint node): the
	// workflow parked for human help because it hit a problem it could not
	// resolve on its own, as opposed to a routine review gate. Surfaced so a
	// supervisor is alerted rather than treating it like any other question.
	Stuck   bool          `json:"stuck,omitempty"`
	Prompts []SubQuestion `json:"prompts"`
}

// IsStuckStep reports whether a gate's node/step id marks it as a "stuck"
// escalation — a workflow parking for human help (the stuck_checkpoint gate) —
// rather than a routine review gate. Keyed on the node id so the classification
// is deterministic and shared by every supervision surface (watch,
// wait-for-gate, questions).
func IsStuckStep(stepID string) bool {
	return strings.Contains(strings.ToLower(stepID), "stuck")
}

// ApprovalInfo is the structured payload of an EventApproval event.
type ApprovalInfo struct {
	ApprovalID string `json:"approval_id"`
	Title      string `json:"title,omitempty"`
	ActivityID string `json:"activity_id,omitempty"`
	// Status is the resolution on an EventApprovalResolved event ("approved",
	// "denied", "cancelled", …). Empty while the gate is open.
	Status string `json:"status,omitempty"`
}

// Event is one NDJSON line on the follow stream.
type Event struct {
	Event        string `json:"event"`
	ExecutionID  string `json:"execution_id"`
	WorkflowID   string `json:"workflow_id,omitempty"`
	WorkflowName string `json:"workflow_name,omitempty"`
	NodeID       string `json:"node_id,omitempty"`
	OldState     string `json:"old_state,omitempty"`
	NewState     string `json:"new_state,omitempty"`
	Timestamp    string `json:"timestamp"` // RFC3339
	Sequence     int64  `json:"sequence,omitempty"`
	Error        string `json:"error,omitempty"`
	// ExitCode is the process exit code of a `run` node. A run step that exits
	// non-zero completes its ACTIVITY without error — the command ran, it just
	// failed — so the raw lifecycle event says "completed" while the lane is
	// red. The mapper reads this and emits EventNodeFailed instead, and the
	// code is carried so the boundary line can name it.
	ExitCode *int `json:"exit_code,omitempty"`
	// Outcome is the run's verdict on a terminal workflow event: "success" or
	// "failure" as declared by the terminal node the graph reached. Empty when
	// the workflow declared none — which means "it never said", not "failure".
	Outcome string `json:"outcome,omitempty"`
	// DurationMs is the time a gate stood open, set on gate-close events when
	// the open was observed in this session.
	DurationMs int64 `json:"duration_ms,omitempty"`
	// Question is set on EventQuestion and EventQuestionAnswered events.
	Question *QuestionInfo `json:"question,omitempty"`
	// Approval is set on EventApproval and EventApprovalResolved events.
	Approval *ApprovalInfo `json:"approval,omitempty"`
}

// Hook binds a shell command to an event type. On/Cmd match the config-file
// schema (cliconfig.HookSpec) and the --hook flag.
type Hook struct {
	On  string
	Cmd string
}

// Matches reports whether the hook fires for the given event type.
func (h Hook) Matches(eventType string) bool {
	return h.On == HookEventAny || h.On == eventType
}

// ValidateHook checks the event name and command of a hook (from flag or
// config file).
func ValidateHook(h Hook) error {
	if !hookableEvents[h.On] {
		return fmt.Errorf("invalid hook event %q — expected one of: node_started, node_completed, node_failed, workflow_completed, workflow_failed, question, question_answered, approval, approval_resolved, any", h.On)
	}
	if strings.TrimSpace(h.Cmd) == "" {
		return fmt.Errorf("hook command must not be empty")
	}
	return nil
}

// ParseHookFlag parses a repeatable --hook value of the form:
//
//	on=<event> cmd=<shell command>
//
// Everything after "cmd=" (including spaces and '=') is the command.
func ParseHookFlag(raw string) (Hook, error) {
	s := strings.TrimSpace(raw)
	if !strings.HasPrefix(s, "on=") {
		return Hook{}, fmt.Errorf("invalid --hook %q — expected 'on=<event> cmd=<shell>'", raw)
	}
	rest := s[len("on="):]
	idx := strings.IndexAny(rest, " \t")
	if idx < 0 {
		return Hook{}, fmt.Errorf("invalid --hook %q — missing 'cmd=' part", raw)
	}
	event := rest[:idx]
	cmdPart := strings.TrimLeft(rest[idx:], " \t")
	if !strings.HasPrefix(cmdPart, "cmd=") {
		return Hook{}, fmt.Errorf("invalid --hook %q — expected 'cmd=' after the event", raw)
	}
	h := Hook{On: event, Cmd: cmdPart[len("cmd="):]}
	if err := ValidateHook(h); err != nil {
		return Hook{}, err
	}
	return h, nil
}

// marshalEvent renders the single-line JSON form of an event.
func marshalEvent(ev Event) ([]byte, error) {
	return json.Marshal(ev)
}

// RenderText renders an event as a human-readable supervision line for
// `workflow watch`. It surfaces only meaningful boundaries: node lifecycle,
// workflow lifecycle, questions (with prompts + option labels), and timeout.
func RenderText(ev Event) string {
	ts := shortTime(ev.Timestamp)
	switch ev.Event {
	case EventNodeStarted:
		return fmt.Sprintf("%s  ▶ node %s started", ts, ev.NodeID)
	case EventNodeCompleted:
		return fmt.Sprintf("%s  ✓ node %s completed", ts, ev.NodeID)
	case EventNodeSkipped:
		// Deliberately not ✓ and not ✗: a check that never ran neither passed
		// nor failed, and the one thing a supervisor must not read here is a
		// green tick.
		return fmt.Sprintf("%s  ⊘ node %s skipped (condition false — it did not run)", ts, ev.NodeID)
	case EventNodeFailed:
		line := fmt.Sprintf("%s  ✗ node %s failed", ts, ev.NodeID)
		// A red gate lane's exit code IS the finding. Printing "failed" without
		// it sends a supervisor to the logs for the one number the event already
		// has.
		if ev.ExitCode != nil {
			line += fmt.Sprintf(" (exit %d)", *ev.ExitCode)
		}
		if ev.Error != "" {
			line += ": " + ev.Error
		}
		return line
	case EventWorkflowStarted:
		return fmt.Sprintf("%s  ▶ workflow started (%s)", ts, workflowLabel(ev))
	case EventWorkflowCompleted:
		// Ran to the end of the graph ≠ the work passed. A run that routed to
		// its `failed` terminal node completes normally, and printing ✓ over
		// that is the difference between a supervisor seeing a finished build
		// and seeing a run that failed every lane.
		if ev.Outcome == OutcomeFailure {
			return fmt.Sprintf("%s  ✗ workflow ended WITHOUT PASSING (ran to completion, outcome: failure)", ts)
		}
		return fmt.Sprintf("%s  ✓ workflow completed", ts)
	case EventWorkflowFailed:
		line := fmt.Sprintf("%s  ✗ workflow failed", ts)
		if ev.Error != "" {
			line += ": " + ev.Error
		}
		return line
	case EventWorkflowCancelled:
		return fmt.Sprintf("%s  ⊘ workflow cancelled", ts)
	case EventQuestion:
		return renderQuestion(ts, ev)
	case EventQuestionAnswered:
		return renderQuestionAnswered(ts, ev)
	case EventApproval:
		return renderApproval(ts, ev)
	case EventApprovalResolved:
		return renderApprovalResolved(ts, ev)
	case EventFollowTimeout:
		return fmt.Sprintf("%s  ⏱ watch timed out", ts)
	default:
		return fmt.Sprintf("%s  %s", ts, ev.Event)
	}
}

func renderQuestion(ts string, ev Event) string {
	var b strings.Builder
	q := ev.Question
	if q == nil {
		return fmt.Sprintf("%s  ❓ question raised", ts)
	}
	// A stuck escalation is surfaced as a first-class alert, not a routine
	// "question raised", so a watching operator is unmistakably notified that the
	// workflow needs help (it still trips the same exit-on-gate / exit-3 path).
	if q.Stuck {
		fmt.Fprintf(&b, "%s  ⚠ STUCK — workflow escalated for help (id %s", ts, q.QuestionID)
	} else {
		fmt.Fprintf(&b, "%s  ❓ question raised (id %s", ts, q.QuestionID)
	}
	if q.StepID != "" {
		fmt.Fprintf(&b, ", node %s", q.StepID)
	}
	// Name the thread: a fanned-out run has many threads open at once and only
	// this one is waiting, so "a gate opened" is not actionable without it.
	if q.ThreadID != "" {
		fmt.Fprintf(&b, ", thread %s", shortThread(q.ThreadID))
	}
	b.WriteString(")")
	for i, sq := range q.Prompts {
		fmt.Fprintf(&b, "\n     %d. %s", i+1, sq.Question)
		for _, opt := range sq.Options {
			fmt.Fprintf(&b, "\n        - %s", opt)
		}
	}
	fmt.Fprintf(&b, "\n     answer with: reliant workflow answer %s --select \"<option>\"", ev.ExecutionID)
	return b.String()
}

// renderQuestionAnswered closes the gate on the stream and names the time the
// run spent parked. That number is what a measured run subtracts from its phase
// timings — without it, human think time is silently attributed to the phase.
func renderQuestionAnswered(ts string, ev Event) string {
	q := ev.Question
	line := fmt.Sprintf("%s  ▶ question answered", ts)
	if q != nil {
		line += fmt.Sprintf(" (id %s", q.QuestionID)
		if q.StepID != "" {
			line += fmt.Sprintf(", node %s", q.StepID)
		}
		line += ")"
	}
	if ev.DurationMs > 0 {
		line += fmt.Sprintf(" — %s in gate", shortDuration(ev.DurationMs))
	}
	return line
}

func renderApprovalResolved(ts string, ev Event) string {
	a := ev.Approval
	line := fmt.Sprintf("%s  ▶ approval resolved", ts)
	if a != nil {
		if a.Status != "" {
			line = fmt.Sprintf("%s  ▶ approval %s", ts, a.Status)
		}
		line += fmt.Sprintf(" (id %s)", a.ApprovalID)
	}
	if ev.DurationMs > 0 {
		line += fmt.Sprintf(" — %s in gate", shortDuration(ev.DurationMs))
	}
	return line
}

// shortDuration renders a gate's time-in-state compactly (22m44s, 1h3m2s, 8s).
func shortDuration(ms int64) string {
	d := (time.Duration(ms) * time.Millisecond).Round(time.Second)
	if d < time.Second {
		return fmt.Sprintf("%dms", ms)
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh%dm%ds", h, m, s)
	case m > 0:
		return fmt.Sprintf("%dm%ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

func renderApproval(ts string, ev Event) string {
	a := ev.Approval
	if a == nil {
		return fmt.Sprintf("%s  ⏸ approval required", ts)
	}
	line := fmt.Sprintf("%s  ⏸ approval required (id %s", ts, a.ApprovalID)
	if a.ActivityID != "" {
		line += fmt.Sprintf(", activity %s", a.ActivityID)
	}
	line += ")"
	if a.Title != "" {
		line += "\n     " + a.Title
	}
	return line
}

func workflowLabel(ev Event) string {
	if ev.WorkflowName != "" {
		return ev.WorkflowName
	}
	return ev.WorkflowID
}

// shortTime trims an RFC3339 timestamp to HH:MM:SS for compact display,
// falling back to the raw value.
func shortTime(rfc3339 string) string {
	if len(rfc3339) >= 19 && rfc3339[10] == 'T' {
		return rfc3339[11:19]
	}
	return rfc3339
}

// shortThread abbreviates a thread UUID to its leading segment, which is enough
// to tell a run's concurrent threads apart in a supervision line.
func shortThread(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
