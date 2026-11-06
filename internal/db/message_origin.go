// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/store"
)

// MessageOrigin attributes a user-role message that an agent, not the human,
// sent through a path built for the human's own input: send_to_run reaches
// another run through RunService.SignalRun, which is ChatService.SendMessage
// underneath. Without it that message is stored, and shown, as if the human
// had typed it.
//
// It travels on the context, not on the request, on purpose: the requests are
// public RPCs, and a sender a client could set is a sender a client could
// forge. Only in-process callers can put a value on a Go context.
type MessageOrigin struct {
	// ThreadID is the thread that sent the message (for a run, its main
	// thread). Stored as messages.sender_thread_id / agent_messages.from_thread_id.
	ThreadID string
	// Kind is the sender kind recorded on the message.
	Kind reliantv1.AgentMessageKind
	// Subject is the sender's one-line summary, "" when it gave none.
	Subject string
}

type messageOriginKey struct{}

// WithMessageOrigin returns ctx carrying origin: the user-role messages saved
// or queued under it are attributed to origin's thread. A zero ThreadID
// returns ctx unchanged.
func WithMessageOrigin(ctx context.Context, origin MessageOrigin) context.Context {
	if origin.ThreadID == "" {
		return ctx
	}
	if origin.Kind == reliantv1.AgentMessageKind_AGENT_MESSAGE_KIND_UNSPECIFIED {
		origin.Kind = reliantv1.AgentMessageKind_AGENT_MESSAGE_KIND_MESSAGE
	}
	return context.WithValue(ctx, messageOriginKey{}, origin)
}

// SenderTitles maps each sender thread id to the heading a message from it is
// shown under: the thread's own title, or, for a run's main thread (untitled,
// and sharing its chat's id), the run's title. Ids with neither are absent.
func (r *Repo) SenderTitles(ctx context.Context, threadIDs []string) (map[string]string, error) {
	return store.SenderTitlesByThreadIDs(ctx, r.ORM(), threadIDs)
}

// SenderTitle is SenderTitles for one thread, nil when it resolves to none.
// Best-effort: an untitled sender degrades a card's heading, never the
// transcript, so a failed lookup is no title rather than an error.
func (r *Repo) SenderTitle(ctx context.Context, threadID string) *string {
	titles, err := r.SenderTitles(ctx, []string{threadID})
	if err != nil {
		return nil
	}
	title, ok := titles[threadID]
	if !ok {
		return nil
	}
	return &title
}

// MessageOriginFrom reports the origin ctx carries, if any.
func MessageOriginFrom(ctx context.Context) (MessageOrigin, bool) {
	origin, ok := ctx.Value(messageOriginKey{}).(MessageOrigin)
	return origin, ok && origin.ThreadID != ""
}

type llmOnlyDeliveryKey struct{}

// WithLLMOnlyDelivery returns ctx under which the user-role message saved or
// queued is for the model only: it is stored HIDDEN, so it reaches the agent
// through its history but the transcript never draws it. The answer to an
// async ask_user question is one — the user gave it on the question's card,
// which shows it there ("Answered: …"); as a free-standing user message it
// would appear twice, the second time as words the user never typed.
//
// A context value for the reason MessageOrigin is: the send requests are
// public, and a hidden user message is not something a client may ask for.
func WithLLMOnlyDelivery(ctx context.Context) context.Context {
	return context.WithValue(ctx, llmOnlyDeliveryKey{}, true)
}

// LLMOnlyDelivery reports whether ctx is an LLM-only delivery.
func LLMOnlyDelivery(ctx context.Context) bool {
	v, _ := ctx.Value(llmOnlyDeliveryKey{}).(bool)
	return v
}

// QueuedDisplayStyle is the display style to queue a user-role mailbox row
// with under ctx: HIDDEN for an LLM-only delivery, nil otherwise.
func QueuedDisplayStyle(ctx context.Context) *int32 {
	if !LLMOnlyDelivery(ctx) {
		return nil
	}
	hidden := int32(reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN)
	return &hidden
}
