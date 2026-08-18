// Copyright (c) 2025 Reliant Labs
package execfollow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/askuser"
)

// Exit codes returned by Engine.Run.
const (
	ExitSuccess = 0 // root workflow reached a success terminal state
	ExitFailed  = 1 // root workflow failed / was cancelled / expired (or follow error)
	ExitTimeout = 2 // --timeout elapsed before a terminal state
	// ExitGate is returned only when ExitOnGate is set: a question/approval
	// gate opened, so a supervisor can block on the next gate without racing a
	// grep on the stream. The gate's event is emitted before the return.
	ExitGate = 3
)

// Gate kinds returned by Source.Pending.
const (
	GateQuestion = "question"
	GateApproval = "approval"
)

// DefaultInterval is the poll interval between update fetches.
const DefaultInterval = 2 * time.Second

// maxConsecutiveErrors bounds transient poll failures before giving up.
const maxConsecutiveErrors = 15

// RawUpdate is one row from the chat update feed (ChatService.GetChatUpdates).
type RawUpdate struct {
	Seq       int64
	Type      string // proto enum name, e.g. "CHAT_UPDATE_TYPE_NODE_EXECUTION"
	Data      []byte // JSON payload (see web/src/types/streaming.ts shapes)
	CreatedAt time.Time
}

// RootState is the root workflow's current status, normalized to lowercase
// ("pending", "running", "completed", "failed", "cancelled", "paused").
// Found is false while no root workflow exists yet.
type RootState struct {
	Found  bool
	Status string
	// Outcome is the run's verdict ("success"/"failure") declared by the
	// terminal node it reached, empty when the workflow declared none. A
	// follower that attaches AFTER the run ended never sees the terminal event,
	// so without this the fallback path would report a failure-outcome run as a
	// clean success.
	Outcome string
}

// PendingGate is one currently-open supervision gate (a pending question or
// approval) on the execution. The engine reconciles these every poll so a
// follower learns about a gate that was opened before it attached or whose
// create-edge update it never saw (--tail, reconnect, or a replay gap).
type PendingGate struct {
	Kind     string // GateQuestion or GateApproval
	ID       string // question id / approval id
	StepID   string // node/step that raised it (questions)
	ThreadID string // thread that raised it (questions)
	Title    string // human title (approvals)
	Metadata string // ask_user metadata JSON (questions)
}

// Source abstracts the server surface the engine polls. The production
// implementation wraps ChatService.GetChatUpdates + GetWorkflowExecutions +
// QuestionService/ApprovalService.
type Source interface {
	// Updates returns updates with sequence > sinceSeq plus the latest
	// sequence number (valid even when no updates are returned). The server
	// caps each call to a page of rows; the engine pages until caught up.
	Updates(ctx context.Context, sinceSeq int64) ([]RawUpdate, int64, error)
	// Root returns the root workflow execution's current status.
	Root(ctx context.Context) (RootState, error)
	// Pending returns the gates (questions/approvals) currently awaiting input.
	// The engine reconciles these against the events it has already emitted so
	// an open gate is surfaced exactly once even if its edge update was missed.
	Pending(ctx context.Context) ([]PendingGate, error)
}

// Engine drives a follow session: poll updates, emit NDJSON events, fire
// hooks, and stop with an exit code when the root workflow is terminal.
type Engine struct {
	Source      Source
	ExecutionID string

	Out io.Writer // event stream (stdout)
	Log io.Writer // diagnostics (stderr)

	// Renderer formats an event for the Out stream. When nil the engine writes
	// the canonical NDJSON line (the machine-facing `workflow follow` surface);
	// `workflow watch` sets RenderText for human-readable boundaries. Hooks
	// always receive the NDJSON form regardless of Renderer, so hook stdin is
	// stable across both commands.
	Renderer func(Event) string

	Hooks      []Hook
	HookRunner *HookRunner

	Interval time.Duration // poll interval; DefaultInterval when zero
	Timeout  time.Duration // overall deadline; 0 means none
	// Tail skips historical events: the cursor starts at the current latest
	// sequence instead of replaying from the beginning.
	Tail bool
	// ExitOnGate makes Run return ExitGate as soon as a question/approval gate is
	// OPEN (pending) — as reported by Source.Pending(), not merely because a gate
	// boundary replayed from history — so a supervisor can block until the next
	// gate that actually needs input without piping through grep. A historical
	// gate that has since been answered never triggers it, even though its
	// boundary still appears on the stream. The open gate's event is written
	// before Run returns; OpenGates reports the gate(s) it stopped on.
	ExitOnGate bool

	// nodeStates / wfStates remember the last observed state so events carry
	// old_state -> new_state transitions.
	nodeStates map[string]string
	wfStates   map[string]string
	// emittedQuestions / emittedApprovals dedupe gate boundaries across history
	// replay, per-poll reconciliation, and the Pending() safety net (a pending
	// gate's edge update replays every poll, and reconciliation re-observes it).
	emittedQuestions map[string]bool
	emittedApprovals map[string]bool
	// gateEvents records every gate (question/approval) boundary emitted this
	// session, keyed by gate id, so a currently-open gate can be reported with
	// its full payload even when its edge update — not the reconciler — was what
	// surfaced it.
	gateEvents map[string]Event
	// openGateEvents is the set of gate events that were OPEN at the last
	// successful Source.Pending() reconciliation, in the order Pending() reported
	// them. It — not "a gate boundary was emitted" — drives ExitOnGate, so a
	// historical gate that has since been answered (and is therefore absent from
	// Pending()) never trips the exit even though its boundary still replays on
	// the stream.
	openGateEvents []Event

	rootTerminal string // terminal state observed via root workflow events
	// rootOutcome is the run's VERDICT ("success"/"failure") as declared by the
	// terminal node the graph reached. Separate from rootTerminal on purpose: a
	// run that routes to its `failed` node has rootTerminal "completed" and
	// rootOutcome "failure", and the exit code has to key on both or a run that
	// failed every gate lane exits 0.
	rootOutcome      string
	emittedTerminal  bool
	consecutiveFails int
	// gateOpenedAt remembers when each gate's OPEN boundary was observed, so the
	// close event can report the time the run spent parked. A gate whose open
	// this session never saw simply reports no duration rather than a wrong one.
	gateOpenedAt map[string]time.Time
	// closedGates dedupes gate-close boundaries: a resolved question's update
	// row replays on every poll, exactly like the pending one does.
	closedGates map[string]bool
}

func (e *Engine) log() io.Writer {
	if e.Log != nil {
		return e.Log
	}
	return os.Stderr
}

// Run follows the execution until terminal state, timeout, or context
// cancellation. It returns the process exit code.
func (e *Engine) Run(ctx context.Context) (int, error) {
	if e.Source == nil {
		return ExitFailed, errors.New("execfollow: Source is required")
	}
	if e.Out == nil {
		e.Out = os.Stdout
	}
	if e.HookRunner == nil {
		e.HookRunner = &HookRunner{Stderr: e.log()}
	}
	e.nodeStates = map[string]string{}
	e.wfStates = map[string]string{}
	e.emittedQuestions = map[string]bool{}
	e.emittedApprovals = map[string]bool{}
	e.gateEvents = map[string]Event{}
	e.openGateEvents = nil
	e.gateOpenedAt = map[string]time.Time{}
	e.closedGates = map[string]bool{}

	interval := e.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}

	var cancel context.CancelFunc
	if e.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, e.Timeout)
		defer cancel()
	}

	cursor := int64(0)
	if e.Tail {
		// Fetch only the latest sequence so history is skipped.
		if _, latest, err := e.Source.Updates(ctx, math.MaxInt64); err == nil {
			cursor = latest
		}
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		var err error
		cursor, err = e.drain(ctx, cursor)
		if err != nil {
			e.consecutiveFails++
			fmt.Fprintf(e.log(), "reliant: fetching updates failed (%d/%d): %v\n", e.consecutiveFails, maxConsecutiveErrors, err)
			if e.consecutiveFails >= maxConsecutiveErrors {
				return ExitFailed, fmt.Errorf("giving up after %d consecutive poll failures: %w", e.consecutiveFails, err)
			}
		} else {
			e.consecutiveFails = 0
		}

		// Reconcile currently-open gates against what we've already emitted.
		// This is the safety net that makes a gate impossible to miss: even if
		// the edge update was skipped (--tail, reconnect, or a burst that pushed
		// it past a page boundary), the open gate is surfaced here within one
		// poll interval. The edge event remains primary (it carries the real
		// sequence); reconciliation only fires for gates not yet on the stream.
		e.reconcilePending(ctx)

		// Stop only for a gate that is actually OPEN right now. openGateEvents is
		// rebuilt each poll from Source.Pending(), so a historical gate whose
		// boundary just replayed from history — but which has since been answered
		// — never trips the exit. The open gate's event is already on the stream.
		if e.ExitOnGate && len(e.openGateEvents) > 0 {
			return ExitGate, nil
		}

		if e.rootTerminal != "" {
			return exitCodeFor(e.rootTerminal, e.rootOutcome), nil
		}

		// Fallback terminal detection: covers attaching after the workflow
		// finished and event feeds that never carried a root terminal event.
		if root, rerr := e.Source.Root(ctx); rerr == nil && root.Found && isTerminalStatus(root.Status) {
			// One final drain in case the terminal events landed between the
			// two calls above. Called for its emit side effects only — every
			// path below returns, so the advanced cursor has no reader.
			_, _ = e.drain(ctx, cursor)
			if e.rootTerminal != "" {
				return exitCodeFor(e.rootTerminal, e.rootOutcome), nil
			}
			e.emitSyntheticRootTerminal(ctx, root.Status, root.Outcome)
			return exitCodeFor(root.Status, root.Outcome), nil
		}

		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				e.emit(ctx, Event{
					Event:       EventFollowTimeout,
					ExecutionID: e.ExecutionID,
					Timestamp:   time.Now().UTC().Format(time.RFC3339),
				})
				return ExitTimeout, nil
			}
			return ExitFailed, ctx.Err()
		case <-ticker.C:
		}
	}
}

// drain fetches and processes ALL pending updates, paging until caught up, and
// returns the new cursor.
//
// The server caps each Updates call to a page of rows. The cursor must advance
// only to the highest sequence actually consumed — never jump to `latest` after
// a single page, or every row between the page boundary and `latest` is
// silently skipped. That skip is exactly how a question/approval gate goes
// missing: a burst of node/message/tool updates pushes the gate's row past the
// first page, the cursor leaps to `latest`, and the gate is never fetched. So
// we loop, advancing by the max consumed sequence, until the page reaches
// `latest` (or comes back empty).
func (e *Engine) drain(ctx context.Context, cursor int64) (int64, error) {
	for {
		updates, latest, err := e.Source.Updates(ctx, cursor)
		if err != nil {
			return cursor, err
		}

		var maxSeq int64
		for _, u := range updates {
			if ev, ok := e.mapUpdate(u); ok {
				e.emit(ctx, ev)
			}
			if u.Seq > maxSeq {
				maxSeq = u.Seq
			}
		}

		if len(updates) == 0 {
			// Caught up: no rows past the cursor. Advance to latest so the next
			// poll doesn't rescan the same empty range.
			if latest > cursor {
				cursor = latest
			}
			return cursor, nil
		}

		// Advance only to what we actually consumed (returned rows always have
		// seq > cursor, so this makes progress and the loop terminates).
		cursor = maxSeq

		// If the page reached the newest row, we're caught up. Otherwise the
		// server capped the batch and more rows remain — keep paging so a burst
		// larger than one page can never skip a gate event.
		if maxSeq >= latest {
			return cursor, nil
		}
	}
}

// emit renders the event to Out (NDJSON by default, or via Renderer) and fires
// matching hooks with the canonical NDJSON form on stdin.
func (e *Engine) emit(ctx context.Context, ev Event) {
	line, err := marshalEvent(ev)
	if err != nil {
		fmt.Fprintf(e.log(), "reliant: failed to encode event: %v\n", err)
		return
	}
	if e.Renderer != nil {
		fmt.Fprintf(e.Out, "%s\n", e.Renderer(ev))
	} else {
		fmt.Fprintf(e.Out, "%s\n", line)
	}
	e.recordGateEvent(ev)
	e.HookRunner.Run(ctx, e.Hooks, ev, line)
}

// recordGateEvent remembers a question/approval boundary by its gate id so the
// Pending() reconciler can report the currently-open gate's full event even
// when its edge update (not the reconciler) was what emitted it. Recording a
// gate here does NOT by itself trigger ExitOnGate — only presence in the
// authoritative Source.Pending() set (openGateEvents) does — so a historical
// answered gate replayed from history is recorded but never trips the exit.
func (e *Engine) recordGateEvent(ev Event) {
	switch ev.Event {
	case EventQuestion:
		if ev.Question != nil && ev.Question.QuestionID != "" {
			e.gateEvents[ev.Question.QuestionID] = ev
		}
	case EventApproval:
		if ev.Approval != nil && ev.Approval.ApprovalID != "" {
			e.gateEvents[ev.Approval.ApprovalID] = ev
		}
	}
}

// reconcilePending emits a synthetic gate boundary for every currently-open
// question/approval the follower has not already surfaced. It runs every poll
// as a safety net (the mirror of the Root() terminal fallback): it guarantees a
// follower cannot sit at a gate with no signal, regardless of --tail, a
// reconnect, or a replay gap. A gate whose edge update was already emitted is
// deduped via emittedQuestions/emittedApprovals and skipped here.
func (e *Engine) reconcilePending(ctx context.Context) {
	gates, err := e.Source.Pending(ctx)
	if err != nil {
		// Non-fatal: the edge event and the next poll still cover us. Leave
		// openGateEvents untouched — a stale value cannot cause a false
		// ExitOnGate trigger (had a gate been open last poll under ExitOnGate,
		// Run would already have returned ExitGate).
		fmt.Fprintf(e.log(), "reliant: reconciling open gates failed: %v\n", err)
		return
	}
	nowT := time.Now().UTC()
	now := nowT.Format(time.RFC3339)
	for _, g := range gates {
		switch g.Kind {
		case GateQuestion:
			if g.ID == "" || e.emittedQuestions[g.ID] {
				continue
			}
			e.emittedQuestions[g.ID] = true
			e.markGateOpened(g.ID, nowT)
			e.emit(ctx, Event{
				Event:       EventQuestion,
				ExecutionID: e.ExecutionID,
				NodeID:      g.StepID,
				Timestamp:   now,
				Question:    questionInfo(g.ID, g.StepID, g.ThreadID, g.Metadata),
			})
		case GateApproval:
			if g.ID == "" || e.emittedApprovals[g.ID] {
				continue
			}
			e.emittedApprovals[g.ID] = true
			e.markGateOpened(g.ID, nowT)
			e.emit(ctx, Event{
				Event:       EventApproval,
				ExecutionID: e.ExecutionID,
				Timestamp:   now,
				Approval:    &ApprovalInfo{ApprovalID: g.ID, Title: g.Title},
			})
		}
	}

	// Rebuild the open-gate set from the authoritative pending list. Every gate
	// here is open right now, and its boundary is on the stream (emitted just
	// above, or earlier by its edge update), so ExitOnGate keys on live openness
	// rather than on a possibly-historical replayed boundary. Gates the mapper
	// couldn't render (empty id) are simply absent from gateEvents and skipped.
	open := make([]Event, 0, len(gates))
	for _, g := range gates {
		if ev, ok := e.gateEvents[g.ID]; ok {
			open = append(open, ev)
		}
	}
	e.openGateEvents = open
}

func (e *Engine) emitSyntheticRootTerminal(ctx context.Context, status, outcome string) {
	if e.emittedTerminal {
		return
	}
	eventType := EventWorkflowCompleted
	switch status {
	case "failed":
		eventType = EventWorkflowFailed
	case "cancelled":
		eventType = EventWorkflowCancelled
	}
	e.emittedTerminal = true
	e.rootTerminal = status
	e.rootOutcome = outcome
	e.emit(ctx, Event{
		Event:       eventType,
		ExecutionID: e.ExecutionID,
		OldState:    "running",
		NewState:    status,
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
		Outcome:     outcome,
	})
}

// OpenGates returns the gate events (questions/approvals) that were open when
// Run returned ExitGate — each a fully-rendered boundary (id, node, prompts for
// questions; id + title for approvals). It is empty when Run returned for any
// other reason. wait-for-gate reads this to print the gate it stopped on.
func (e *Engine) OpenGates() []Event {
	return e.openGateEvents
}

// TerminalStatus returns the normalized terminal status of the root workflow
// ("completed", "failed", "cancelled") once Run has observed one, or
// "" if Run stopped at a gate or timeout before the root reached a terminal
// state. This is the LIFECYCLE — pair it with TerminalOutcome before calling a
// run successful.
func (e *Engine) TerminalStatus() string {
	return e.rootTerminal
}

// TerminalOutcome returns the run's VERDICT ("success"/"failure") as declared by
// the terminal node the graph reached, or "" when the workflow declared none.
// A run can be TerminalStatus "completed" and TerminalOutcome "failure": it ran
// to the end of its graph and the work did not pass.
func (e *Engine) TerminalOutcome() string {
	return e.rootOutcome
}

// Succeeded reports whether the run both finished its lifecycle cleanly AND
// passed. It is the single place the two facts are combined, so every caller
// answers "did this run succeed" the same way.
func Succeeded(status, outcome string) bool {
	return status == "completed" && outcome != OutcomeFailure
}

// ---- update mapping ----

// nodeExecutionPayload mirrors the node_execution data JSON emitted by the
// workflow runtime (see web/src/types/streaming.ts NodeExecutionUpdate).
type nodeExecutionPayload struct {
	UpdateType       string          `json:"update_type"`
	EventType        json.RawMessage `json:"event_type"`
	NodeID           string          `json:"node_id"`
	WorkflowID       string          `json:"workflow_id"`
	ErrorMessage     string          `json:"error_message"`
	ParentNodeID     string          `json:"parent_node_id"`
	ProgressMessage  string          `json:"progress_message"`
	SequenceOverride int64           `json:"sequence_number"`
	// ExitCode is set for `run` nodes. The runtime emits a "completed" lifecycle
	// event for a command that exited non-zero — the activity succeeded, the
	// command did not — so this is the only thing on the event that says the
	// lane was red.
	ExitCode *int `json:"exit_code"`
	// Status is the node's VERDICT, beside event_type's lifecycle. A node whose
	// condition was false also arrives as event_type "completed" — the activity
	// that recorded the skip finished fine — and status is the only thing on
	// the event that says the node never ran.
	Status json.RawMessage `json:"status"`
}

// workflowStatusPayload mirrors the workflow_status data JSON written by
// activities/handlers/workflow_status.go — the only producer of workflow
// lifecycle rows.
type workflowStatusPayload struct {
	UpdateType       string          `json:"update_type"`
	Status           json.RawMessage `json:"status"`
	WorkflowID       string          `json:"workflow_id"`
	WorkflowName     string          `json:"workflow_name"`
	ParentWorkflowID string          `json:"parent_workflow_id"`
	// Outcome is the run's verdict, carried on the terminal event. Status says
	// the Temporal execution finished; outcome says whether the work passed.
	Outcome string `json:"outcome"`
}

// mapUpdate converts a raw chat update into a follow event. Returns
// ok == false for update types the follower does not surface.
//
// This stream is a CONTROL-FLOW view, not a transcript, and the filtering is
// deliberate: a follower answers "where is this run and is it stuck", so it
// carries structure (node and workflow boundaries) and gates (questions,
// approvals). Everything else on chat_updates is conversation content —
// MESSAGE, TOOL_CALL, STREAMING_DELTA, STREAM_FINALIZED, THREAD, RUN_OUTPUT,
// EXECUTION_LOG, INFO, WARNING, ERROR, CHAT, REFETCH, SKILL_INVOCATION — and
// those are the overwhelming bulk of the rows. A run with ~500 sequences
// surfacing ~20 events is that ratio, not lost events: `drain` pages to the
// latest sequence and advances the cursor only by what it consumed, and
// nothing in this path buffers or drops.
//
// Use `reliant-dev workflow node` / `analyze` to read the content this omits.
//
// WORKFLOW_STATUS, not WORKFLOW_EXECUTION, is the workflow lifecycle feed.
// CHAT_UPDATE_TYPE_WORKFLOW_EXECUTION has no producer anywhere in the runtime;
// matching on it meant every workflow start/finish was dropped, so the only
// lifecycle event a follower ever saw was the synthetic terminal the engine
// emits for itself.
func (e *Engine) mapUpdate(u RawUpdate) (Event, bool) {
	switch {
	case strings.Contains(u.Type, "NODE_EXECUTION"):
		return e.mapNodeUpdate(u)
	case strings.Contains(u.Type, "WORKFLOW_STATUS"):
		return e.mapWorkflowUpdate(u)
	case strings.Contains(u.Type, "QUESTION"):
		return e.mapQuestionUpdate(u)
	case strings.Contains(u.Type, "APPROVAL"):
		return e.mapApprovalUpdate(u)
	default:
		return Event{}, false
	}
}

// questionPayload mirrors the CHAT_UPDATE_TYPE_QUESTION data JSON emitted by
// the workflow runtime (db.QuestionUpdate).
type questionPayload struct {
	QuestionID string `json:"question_id"`
	ThreadID   string `json:"thread_id"`
	StepID     string `json:"step_id"`
	Status     string `json:"status"`
	Metadata   string `json:"metadata"`
}

// mapQuestionUpdate turns a pending-question update into a question boundary
// event, parsing the ask_user metadata for its sub-question prompts + options.
// Resolved-question updates and already-emitted questions are skipped so the
// boundary fires exactly once even though the pending update replays on every
// poll.
func (e *Engine) mapQuestionUpdate(u RawUpdate) (Event, bool) {
	var p questionPayload
	if err := json.Unmarshal(u.Data, &p); err != nil || p.QuestionID == "" {
		return Event{}, false
	}
	// A non-pending question update CLOSES the gate. Dropping it was why a run
	// could show "❓ question raised" at 02:05 and nothing until 02:29 — the
	// 22m44s parked was invisible, and every phase timing that spans it silently
	// includes the wait.
	if p.Status != "" && p.Status != "pending" {
		if e.closedGates[p.QuestionID] {
			return Event{}, false
		}
		e.closedGates[p.QuestionID] = true
		return Event{
			Event:       EventQuestionAnswered,
			ExecutionID: e.ExecutionID,
			NodeID:      p.StepID,
			Timestamp:   u.CreatedAt.UTC().Format(time.RFC3339),
			Sequence:    u.Seq,
			DurationMs:  e.timeInGate(p.QuestionID, u.CreatedAt),
			Question:    questionInfo(p.QuestionID, p.StepID, p.ThreadID, p.Metadata),
		}, true
	}
	if e.emittedQuestions[p.QuestionID] {
		return Event{}, false
	}
	e.emittedQuestions[p.QuestionID] = true
	e.markGateOpened(p.QuestionID, u.CreatedAt)

	return Event{
		Event:       EventQuestion,
		ExecutionID: e.ExecutionID,
		NodeID:      p.StepID,
		Timestamp:   u.CreatedAt.UTC().Format(time.RFC3339),
		Sequence:    u.Seq,
		Question:    questionInfo(p.QuestionID, p.StepID, p.ThreadID, p.Metadata),
	}, true
}

// markGateOpened records when a gate's open boundary was observed. First
// observation wins: the pending row replays on every poll, and the ORIGINAL
// timestamp is what makes the close's duration the real time in gate.
func (e *Engine) markGateOpened(gateID string, at time.Time) {
	if gateID == "" || at.IsZero() {
		return
	}
	if _, seen := e.gateOpenedAt[gateID]; !seen {
		e.gateOpenedAt[gateID] = at
	}
}

// timeInGate returns how long a gate stood open, or 0 when this session never
// saw it open (attached late, or --tail past the boundary). Zero means "not
// measured here" — never a claim that the gate closed instantly.
func (e *Engine) timeInGate(gateID string, closedAt time.Time) int64 {
	openedAt, ok := e.gateOpenedAt[gateID]
	if !ok || closedAt.IsZero() || !closedAt.After(openedAt) {
		return 0
	}
	return closedAt.Sub(openedAt).Milliseconds()
}

// questionInfo builds the structured EventQuestion payload from ask_user
// metadata. Shared by the edge mapper and the Pending() reconciler so both
// paths render identically.
func questionInfo(questionID, stepID, threadID, metadata string) *QuestionInfo {
	info := &QuestionInfo{QuestionID: questionID, StepID: stepID, ThreadID: threadID, Stuck: IsStuckStep(stepID)}
	if md, ok := askuser.ParseMetadata(metadata); ok {
		for _, q := range md.Questions {
			info.Prompts = append(info.Prompts, SubQuestion{
				Question:      q.Question,
				Options:       q.OptionLabels(),
				AllowMultiple: q.AllowMultiple,
			})
		}
	}
	return info
}

// approvalPayload mirrors the CHAT_UPDATE_TYPE_APPROVAL data JSON emitted by
// the approval activity/service (see approval.go).
type approvalPayload struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	Title      string `json:"title"`
	ActivityID string `json:"activity_id"`
}

// mapApprovalUpdate turns a pending-approval update into an approval boundary
// event. Resolved-approval updates and already-emitted approvals are skipped so
// the boundary fires exactly once.
func (e *Engine) mapApprovalUpdate(u RawUpdate) (Event, bool) {
	var p approvalPayload
	if err := json.Unmarshal(u.Data, &p); err != nil || p.ID == "" {
		return Event{}, false
	}
	// Resolution closes the gate — same reasoning as questions.
	if p.Status != "" && p.Status != "pending" {
		if e.closedGates[p.ID] {
			return Event{}, false
		}
		e.closedGates[p.ID] = true
		return Event{
			Event:       EventApprovalResolved,
			ExecutionID: e.ExecutionID,
			NodeID:      p.ActivityID,
			Timestamp:   u.CreatedAt.UTC().Format(time.RFC3339),
			Sequence:    u.Seq,
			DurationMs:  e.timeInGate(p.ID, u.CreatedAt),
			Approval:    &ApprovalInfo{ApprovalID: p.ID, Title: p.Title, ActivityID: p.ActivityID, Status: p.Status},
		}, true
	}
	if e.emittedApprovals[p.ID] {
		return Event{}, false
	}
	e.emittedApprovals[p.ID] = true
	e.markGateOpened(p.ID, u.CreatedAt)

	return Event{
		Event:       EventApproval,
		ExecutionID: e.ExecutionID,
		NodeID:      p.ActivityID,
		Timestamp:   u.CreatedAt.UTC().Format(time.RFC3339),
		Sequence:    u.Seq,
		Approval:    &ApprovalInfo{ApprovalID: p.ID, Title: p.Title, ActivityID: p.ActivityID},
	}, true
}

func (e *Engine) mapNodeUpdate(u RawUpdate) (Event, bool) {
	var p nodeExecutionPayload
	if err := json.Unmarshal(u.Data, &p); err != nil || p.NodeID == "" {
		return Event{}, false
	}

	var eventType, newState string
	switch decodeEnum(p.EventType) {
	case 1, enumStarted:
		eventType, newState = EventNodeStarted, "running"
	case 3, enumCompleted:
		eventType, newState = EventNodeCompleted, "completed"
		switch {
		// A node whose condition was false never ran, and reaches here as
		// "completed" because the activity that RECORDED the skip completed.
		// Reporting that as ✓ is how a run printed "✓ node review completed"
		// for a reviewer that was configured off. The status is the truth on
		// the event — read it.
		case decodeEnum(p.Status) == statusSkipped:
			eventType, newState = EventNodeSkipped, "skipped"
		// A `run` node that exits non-zero reaches here as "completed": the
		// activity did its job, the command failed. Reporting that as ✓ is how a
		// run whose build/test/lint lanes were red five times in a row printed
		// three green boundary lines per iteration. The exit code is the truth
		// on the event — read it.
		case p.ExitCode != nil && *p.ExitCode != 0:
			eventType, newState = EventNodeFailed, "failed"
		}
	case 4, enumFailed:
		eventType, newState = EventNodeFailed, "failed"
	default:
		// PROGRESS (2) and unknown event types are not surfaced.
		return Event{}, false
	}

	key := p.WorkflowID + "/" + p.NodeID
	oldState := e.nodeStates[key]
	if oldState == "" {
		oldState = "pending"
	}
	e.nodeStates[key] = newState

	return Event{
		Event:       eventType,
		ExecutionID: e.ExecutionID,
		WorkflowID:  p.WorkflowID,
		NodeID:      p.NodeID,
		OldState:    oldState,
		NewState:    newState,
		Timestamp:   u.CreatedAt.UTC().Format(time.RFC3339),
		Sequence:    u.Seq,
		Error:       p.ErrorMessage,
		ExitCode:    p.ExitCode,
	}, true
}

func (e *Engine) mapWorkflowUpdate(u RawUpdate) (Event, bool) {
	var p workflowStatusPayload
	if err := json.Unmarshal(u.Data, &p); err != nil || p.WorkflowID == "" {
		return Event{}, false
	}

	var eventType, newState string
	switch decodeEnum(p.Status) {
	case enumStarted:
		eventType, newState = EventWorkflowStarted, "running"
	case enumCompleted:
		eventType, newState = EventWorkflowCompleted, "completed"
	case enumFailed:
		eventType, newState = EventWorkflowFailed, "failed"
	case enumCancelled:
		eventType, newState = EventWorkflowCancelled, "cancelled"
	default:
		// "paused" is the other status the producer can write. It is not a
		// lifecycle transition this stream models: a
		// follower learns about a park from the gate events (question /
		// approval), which carry what is actually being waited on. Anything
		// else is unrecognized and deliberately not guessed at.
		return Event{}, false
	}

	oldState := e.wfStates[p.WorkflowID]
	if oldState == "" {
		oldState = "pending"
	}
	e.wfStates[p.WorkflowID] = newState

	// Root terminal detection: a terminal event for a workflow with no parent
	// ends the follow. The verdict is recorded alongside the lifecycle state —
	// they are separate facts, and the exit code needs both.
	if p.ParentWorkflowID == "" && eventType != EventWorkflowStarted {
		e.rootTerminal = newState
		e.rootOutcome = p.Outcome
		e.emittedTerminal = true
	}

	return Event{
		Event:        eventType,
		ExecutionID:  e.ExecutionID,
		WorkflowID:   p.WorkflowID,
		WorkflowName: p.WorkflowName,
		OldState:     oldState,
		NewState:     newState,
		Timestamp:    u.CreatedAt.UTC().Format(time.RFC3339),
		Sequence:     u.Seq,
		Outcome:      p.Outcome,
	}, true
}

// ---- enum decoding ----

// Symbolic results for string enums so the switch above can match either the
// numeric proto value or a string name.
const (
	enumStarted   = -1
	enumCompleted = -3
	enumFailed    = -4
	enumCancelled = -5
)

// statusSkipped is NodeExecutionStatus SKIPPED as it arrives on a node event.
// Taken from the proto rather than written as a literal so a renumbering moves
// both ends at once. It is read off `status`, which is the node STATE enum —
// a different enum from event_type, and 6 is not a value event_type can take.
var statusSkipped = int(reliantv1.NodeExecutionStatus_NODE_EXECUTION_STATUS_SKIPPED)

// decodeEnum accepts proto enums serialized either as JSON numbers (the Go
// runtime marshals enum ints) or as string names, and normalizes string
// forms onto symbolic values.
func decodeEnum(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		return n
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return 0
	}
	ls := strings.ToLower(s)
	switch {
	case strings.HasSuffix(ls, "started"):
		return enumStarted
	case strings.HasSuffix(ls, "completed"):
		return enumCompleted
	case strings.HasSuffix(ls, "failed"):
		return enumFailed
	case strings.HasSuffix(ls, "cancelled"):
		return enumCancelled
	case strings.HasSuffix(ls, "skipped"):
		return statusSkipped
	default:
		return 0
	}
}

// ---- terminal status helpers ----

func isTerminalStatus(status string) bool {
	switch status {
	case "completed", "failed", "cancelled":
		return true
	default:
		return false
	}
}

// exitCodeFor keys on BOTH facts. A run that reached its `failed` terminal node
// is a completed Temporal execution, so status alone exits 0 — which is exactly
// how a run that built nothing and failed every lane came back green to a
// scripted supervisor.
func exitCodeFor(status, outcome string) int {
	if Succeeded(status, outcome) {
		return ExitSuccess
	}
	return ExitFailed
}
