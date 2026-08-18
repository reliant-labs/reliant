// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
	"go.temporal.io/sdk/activity"
)

// errDrainBatchTaken signals that another writer claimed part of this drain's
// batch. It exists to force RunTx to ROLL BACK the partial claim -- returning
// nil would commit rows as delivered with no message behind them, and every
// read path filters on status = 1, so those messages could never be recovered.
var errDrainBatchTaken = errors.New("agent message batch already claimed")

// DrainAgentMessagesInput is the input for the DrainAgentMessages activity.
//
// A type ALIAS, not a defined type: the workflow dispatches this activity
// locally, and local-activity arguments reach the registered function by
// reflection rather than through the data converter, so the value the workflow
// passes must be the very same reflect.Type this function declares. See
// types/mailbox.go.
type DrainAgentMessagesInput = types.DrainAgentMessagesInput

// DrainAgentMessagesOutput reports what was delivered.
type DrainAgentMessagesOutput = types.DrainAgentMessagesOutput

// DrainAgentMessagesActivity folds any queued agent_messages rows for a
// thread into a single user-role message and marks them delivered.
//
// This is the delivery half of the mailbox described in
// specs/async-spawn-and-agent-messaging.md §5. A bare INSERT into `messages`
// is unsafe here (§5.2): an agent is mid-turn most of the time, and a message
// landing between an assistant-with-tool_calls row and its tool_results row
// deadlocks the provider. Queuing in agent_messages and draining only at the
// step boundary — after execute_tools has already saved its tool results —
// keeps history always consistent at the point this activity runs.
type DrainAgentMessagesActivity struct {
	repo    db.Repository
	threads *threads.Service
}

// NewDrainAgentMessagesActivity creates a new DrainAgentMessagesActivity.
func NewDrainAgentMessagesActivity(repo db.Repository, threadsSvc *threads.Service) *DrainAgentMessagesActivity {
	return &DrainAgentMessagesActivity{repo: repo, threads: threadsSvc}
}

func (a *DrainAgentMessagesActivity) Name() string        { return "DrainAgentMessages" }
func (a *DrainAgentMessagesActivity) DisplayName() string { return "Drain Agent Messages" }
func (a *DrainAgentMessagesActivity) Description() string {
	return "Deliver any queued agent mailbox messages into the thread's history"
}
func (a *DrainAgentMessagesActivity) Category() schema.ActivityCategory {
	return schema.CategoryMessageProcessing
}

func (a *DrainAgentMessagesActivity) Execute(ctx context.Context, input DrainAgentMessagesInput) (DrainAgentMessagesOutput, error) {
	if input.Thread == "" {
		return DrainAgentMessagesOutput{}, fmt.Errorf("thread is required")
	}

	queued, err := a.repo.ListQueuedAgentMessagesForThread(ctx, input.Thread)
	if err != nil {
		return DrainAgentMessagesOutput{}, fmt.Errorf("failed to list queued agent messages: %w", err)
	}
	if len(queued) == 0 {
		return DrainAgentMessagesOutput{}, nil
	}

	logger := activity.GetLogger(ctx)
	logger.Info("[DrainAgentMessages] Delivering queued mailbox messages",
		"chatID", input.ChatID,
		"thread", input.Thread,
		"count", len(queued),
	)

	ids := make([]string, len(queued))
	for i, m := range queued {
		ids[i] = m.ID
	}

	// One transaction for the envelope, every delivered body, and the mailbox
	// bookkeeping. RunTx is re-entrant (internal/db/repo.go), so the RunTx
	// inside each SaveMessage joins this one rather than opening its own —
	// partial delivery (envelope written, bodies lost, rows still queued) is
	// not a state any reader has to cope with.
	var envelopeMessageID string
	err = a.repo.RunTx(ctx, func(txCtx context.Context) error {
		// Claim the rows BEFORE writing anything they would produce.
		//
		// The list above is a plain read outside this transaction, so two drains
		// racing on one thread can select the same queued rows. The claim is the
		// conditional UPDATE (status = 1 only) and it returns the ids it actually
		// moved, so exactly one drain can win a given row. The loser writes
		// nothing at all — which is the whole point of doing this first. Doing it
		// last, as this used to, meant both drains had already inserted their
		// envelope and bodies by the time either discovered it had lost, and the
		// user saw the same queued message twice.
		//
		// delivered_message_id is backfilled once the envelope exists; the claim
		// only needs to establish ownership.
		claimed, err := a.repo.MarkAgentMessagesDelivered(txCtx, ids, time.Now().UTC(), "")
		if err != nil {
			return fmt.Errorf("failed to claim agent messages for delivery: %w", err)
		}
		if len(claimed) != len(ids) {
			// Someone else took part of this batch. Abandon the whole delivery
			// rather than deliver the remainder: whoever took those rows is
			// writing them, and a partial second delivery duplicates exactly the
			// messages they already wrote.
			//
			// Returning an ERROR, not nil, is load-bearing. RunTx commits on a nil
			// return, and the claim above is part of this transaction — so
			// abandoning with nil would leave the rows we DID claim marked
			// delivered with no message written and no way back: every read path
			// filters on status = 1, so nothing would ever find them again and the
			// user's message would be silently, permanently gone. The error rolls
			// the claim back so the rows stay queued and the next drain re-reads
			// them. The caller recognises this sentinel and reports "nothing
			// delivered" rather than a failure.
			//
			// A partial claim is not only a competing drain: "Send now"
			// (ClaimQueuedAgentMessagesForThread) and cancel both DELETE queued
			// rows, and either can land between the unguarded list above and this
			// claim.
			return errDrainBatchTaken
		}

		// The envelope is LLM-only framing, so it is written as its own
		// message marked HIDDEN: it reaches the model through the ordinary
		// history load, and the two transcript filters
		// (proto_converters.go, InterleavedTimeline.tsx) keep it out of the
		// UI. Folding it into the user's text — as this activity used to —
		// meant the transcript rendered the <system> preamble and the
		// <message> wrappers verbatim in a chat bubble.
		//
		// USER role, not SYSTEM, and deliberately: role and visibility are
		// separate axes here. HIDDEN is what hides it; USER is what makes it
		// survive every provider.
		envelopeResult, err := a.threads.SaveMessage(txCtx, threads.SaveMessageOpts{
			ChatID:       input.ChatID,
			Thread:       input.Thread,
			Role:         int32(reliantv1.MessageRole_MESSAGE_ROLE_USER),
			Content:      a.buildEnvelope(txCtx, queued),
			DisplayStyle: int32(reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN),
		})
		if err != nil {
			return fmt.Errorf("failed to save mailbox envelope: %w", err)
		}
		envelopeMessageID = envelopeResult.MessageID

		// Each queued body then lands as its own message. A HUMAN message
		// (the user typing into a running thread) is visible in the
		// transcript exactly as the sender wrote it. Attachments carry
		// through the same way: SaveMessage builds the same image /
		// file_reference / document content blocks a normal SendMessage
		// would, so the LLM sees a queued screenshot exactly as it would
		// have if the turn had been sent directly instead of queued.
		//
		// A sub-agent COMPLETION / CANCELLATION / FAILURE report is machine
		// output, not something the human wrote — the raw
		// <agent_result agent_id="..." status="..."> body must still reach
		// the model (that is how the orchestrator learns what its
		// sub-agent produced), but rendering it as an ordinary user bubble
		// showed the human that envelope text verbatim. So the body is
		// saved HIDDEN (same LLM-visible/UI-invisible contract as the
		// envelope above), and a short SYSTEM+INFO notification is saved
		// alongside it for the human to see instead.
		for _, m := range queued {
			if isAgentResultKind(m.Kind) {
				if _, err := a.threads.SaveMessage(txCtx, threads.SaveMessageOpts{
					ChatID:       input.ChatID,
					Thread:       input.Thread,
					Role:         int32(reliantv1.MessageRole_MESSAGE_ROLE_USER),
					Content:      m.Body,
					Attachments:  m.Attachments,
					DisplayStyle: int32(reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN),
				}); err != nil {
					return fmt.Errorf("failed to save drained agent message %s: %w", m.ID, err)
				}
				// SYSTEM role, not USER, and this one is forced rather than
				// stylistic: InterleavedTimeline.tsx short-circuits every
				// USER-role message to ChatMessage BEFORE it consults
				// displayStyle, so a USER+INFO notification would render as
				// an ordinary chat bubble and reintroduce the exact bug this
				// fixes. SYSTEM falls through to SystemNotificationMessage,
				// which is what draws the INFO surface.
				if _, err := a.threads.SaveMessage(txCtx, threads.SaveMessageOpts{
					ChatID:       input.ChatID,
					Thread:       input.Thread,
					Role:         int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM),
					Content:      a.spawnNotification(txCtx, m),
					DisplayStyle: int32(reliantv1.DisplayStyle_DISPLAY_STYLE_INFO),
				}); err != nil {
					return fmt.Errorf("failed to save spawn notification for agent message %s: %w", m.ID, err)
				}
				continue
			}

			if _, err := a.threads.SaveMessage(txCtx, threads.SaveMessageOpts{
				ChatID:      input.ChatID,
				Thread:      input.Thread,
				Role:        int32(reliantv1.MessageRole_MESSAGE_ROLE_USER),
				Content:     m.Body,
				Attachments: m.Attachments,
			}); err != nil {
				return fmt.Errorf("failed to save drained agent message %s: %w", m.ID, err)
			}
		}

		// delivered_message_id takes the ENVELOPE's id: it is the one message
		// guaranteed to exist for any drain (a batch may be all completion
		// notices, which carry no body message of their own), and it is the
		// row whose ordinal marks where this delivery landed in the thread.
		//
		// The rows were already claimed at the top of this transaction, so this
		// only backfills that pointer. It deliberately does not re-check the
		// claim: these ids are ours, and re-running the guarded update would
		// match nothing now that their status is 2.
		if err := a.repo.SetAgentMessagesDeliveredMessageID(txCtx, ids, envelopeMessageID); err != nil {
			return fmt.Errorf("failed to record delivery message id: %w", err)
		}

		// Tell the client which mailbox rows these messages came from, in
		// this same transaction. The pending-queue strip polls
		// ListQueuedAgentMessages, so without this a drained message sits in
		// the strip until the next poll happens to omit it -- visible in the
		// transcript and in the strip at once, which is what the user sees as
		// the same message twice.
		//
		// Inside the transaction, and returning the error rather than logging
		// it, on purpose: the announcement and the messages it announces must
		// become visible together. Emitting after the commit would leave a
		// window where a failure loses the announcement and strands the rows
		// in the strip; emitting before would clear the strip against a
		// transcript that has not shown them yet. Rolling back is safe --
		// the rows stay queued and the next boundary re-drains them.
		if err := a.repo.EmitAgentMessagesDrainedUpdate(txCtx, input.ChatID, db.AgentMessagesDrainedUpdate{
			Thread:     input.Thread,
			MessageIDs: ids,
		}); err != nil {
			return fmt.Errorf("failed to announce drained agent messages: %w", err)
		}
		return nil
	})
	if errors.Is(err, errDrainBatchTaken) {
		// Rolled back cleanly: the rows are queued again and whoever took them is
		// delivering them. "Nothing delivered" is the honest answer for THIS call.
		logging.Info("[DrainAgentMessages] Batch was claimed by another writer; rolled back and left it to them",
			"chatID", input.ChatID, "thread", input.Thread, "count", len(queued))
		return DrainAgentMessagesOutput{}, nil
	}
	if err != nil {
		return DrainAgentMessagesOutput{}, err
	}

	return DrainAgentMessagesOutput{Count: len(queued), HasMessages: true}, nil
}

// buildEnvelope builds the LLM-only preamble for a drain: the <system> notice
// that these arrived after the recipient's last action, followed by one
// attribution line per queued row in the order their bodies follow.
//
// The bodies are NOT in here. They are saved as their own visible messages
// immediately after this one, in this same order, so the model reads
// attribution-then-bodies while the transcript shows only the bodies. Carrying
// them in both places would show the model every message twice.
func (a *DrainAgentMessagesActivity) buildEnvelope(ctx context.Context, queued []*core.AgentMessage) string {
	var b strings.Builder

	noun, verb, they := "message", "is", "It"
	if len(queued) != 1 {
		noun, verb, they = "messages", "are", "They"
	}
	fmt.Fprintf(&b, "<system>\n%d %s queued while you were working and %s delivered as the next\n"+
		"%d %s in this conversation, in the order listed below.\n"+
		"%s arrived AFTER your last action — treat them as new instructions that may\n"+
		"supersede your current plan, not as context you have already accounted for.\n",
		len(queued), noun, verb, len(queued), noun, they)

	for i, m := range queued {
		queuedAt := m.CreatedAt.UTC().Format(time.RFC3339)

		switch m.Kind {
		case core.AgentMessageKindCompletion:
			fmt.Fprintf(&b, "\n%d. <agent_result agent_id=%q status=\"completed\">", i+1, m.FromThreadID)
		case core.AgentMessageKindCancelled:
			fmt.Fprintf(&b, "\n%d. <agent_result agent_id=%q status=\"cancelled\">", i+1, m.FromThreadID)
		case core.AgentMessageKindFailed:
			fmt.Fprintf(&b, "\n%d. <agent_result agent_id=%q status=\"failed\">", i+1, m.FromThreadID)
		case core.AgentMessageKindHumanMessage:
			fmt.Fprintf(&b, "\n%d. <message from=\"user\" queued_at=%q>", i+1, queuedAt)
		default:
			from := a.senderLabel(ctx, m.FromThreadID)
			fmt.Fprintf(&b, "\n%d. <message from=%q queued_at=%q>", i+1, from, queuedAt)
		}
	}

	b.WriteString("\n</system>\n")
	return b.String()
}

// senderLabel resolves a human-readable label for the sending thread. Falls
// back to the raw thread id when the thread has no title or cannot be
// loaded — this is best-effort context for the model, not a correctness
// requirement.
func (a *DrainAgentMessagesActivity) senderLabel(ctx context.Context, fromThreadID string) string {
	thread, err := a.repo.GetThread(ctx, fromThreadID)
	if err != nil || thread == nil || thread.Title == nil || *thread.Title == "" {
		return fromThreadID
	}
	return *thread.Title
}

// isAgentResultKind reports whether kind is a sub-agent's report of its own
// outcome (completed / cancelled / failed) rather than free-form text a
// human or peer agent chose to send.
func isAgentResultKind(kind core.AgentMessageKind) bool {
	switch kind {
	case core.AgentMessageKindCompletion, core.AgentMessageKindCancelled, core.AgentMessageKindFailed:
		return true
	default:
		return false
	}
}

// spawnNotification builds the short human-visible line that stands in for
// an agent-result body in the transcript, e.g. `spawn "probe-A" completed`.
// Reuses senderLabel so the title resolution (spawn's title, falling back to
// the thread id) matches what the envelope's <agent_result> attribution
// already uses.
func (a *DrainAgentMessagesActivity) spawnNotification(ctx context.Context, m *core.AgentMessage) string {
	label := a.senderLabel(ctx, m.FromThreadID)

	verb := "completed"
	switch m.Kind {
	case core.AgentMessageKindCancelled:
		verb = "cancelled"
	case core.AgentMessageKindFailed:
		verb = "failed"
	}

	return fmt.Sprintf("spawn %q %s", label, verb)
}
