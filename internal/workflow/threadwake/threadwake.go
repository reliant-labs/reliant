// Copyright (c) 2025 Reliant Labs

// Package threadwake defines the wire contract for the thread-wake doorbell:
// the signal a sender rings when it has done something a parked agent thread
// needs to react to, so a thread waiting on its background spawns wakes up
// instead of sleeping until a sub-agent happens to finish.
//
// It is its own package, holding nothing but a constant and a struct, because
// both ENDS of the signal need it and they sit on opposite sides of an import
// edge. The receiver is internal/workflow/runtime; the senders are
// internal/grpc/services and (via internal/temporal) internal/llm/tools, which
// runtime itself imports. Defining the contract in runtime therefore creates a
// cycle as soon as a sender needs it — including a test-only cycle, which is
// how this was first caught.
//
// A leaf package with no project imports can be depended on from anywhere, and
// keeps the signal name and payload in ONE place. Two spellings of a signal
// name that must match exactly is a silent-failure bug: the sender succeeds,
// the receiver never hears it, and the message just arrives late.
//
// # Why this is not called mailboxsignal any more
//
// It was, and the name described the only sender it had: a row queued into
// agent_messages. But the gate it wakes has nothing to do with mailboxes — it
// is a loop that cannot take another turn — and a USER message saved to
// `messages` needs exactly the same wake for exactly the same reason. Under
// the old name a user message could only be delivered by re-purposing a signal
// whose contract said "a mailbox row exists", which was false. Naming the
// signal after the effect (wake this thread) rather than one of its causes is
// what lets both senders be honest; Reason records which cause it was.
package threadwake

// SignalName is the Temporal signal a sender rings to wake a parked thread.
// Changing it breaks delivery silently — a signal to a name nothing listens on
// is not an error — so it is defined once and shared.
const SignalName = "thread_wake"

// Reason records WHY a thread was woken. It is diagnostic, not dispatch: the
// receiver's behavior is identical for every reason (wake the thread; let it
// take a normal turn, which drains the mailbox and re-reads history). It
// exists so a log line, and anyone reading the gate, can tell the two senders
// apart instead of inferring it from which code path was live.
type Reason string

const (
	// ReasonMailbox is a row queued into agent_messages — spawn_send, or a
	// human addressing a sub-agent through SendAgentMessage. The message
	// body lives in the mailbox and is delivered by the drain.
	ReasonMailbox Reason = "mailbox"

	// ReasonUserMessage is a user message saved to `messages` on a thread
	// whose run is live. Nothing is queued in the mailbox for this one: the
	// message is already in history, and the wake is the whole point — a
	// spinning loop would have re-read history on its own, a parked one
	// never gets there.
	ReasonUserMessage Reason = "user_message"
)

// Signal names the thread to wake, and why.
//
// Deliberately carries no message body. This is a doorbell, not a delivery
// mechanism: whatever the thread needs to see is already durable (a mailbox
// row, or a saved message) and is read from the database when the woken thread
// takes its turn. That keeps the database the single source of truth and makes
// a dropped or duplicated signal harmless — a duplicate finds nothing new to
// do, and a drop degrades to the old next-boundary behavior rather than losing
// anything.
//
// Thread is the RECIPIENT and is load-bearing. Every thread in a chat is
// driven by one Temporal execution (a spawn is a goroutine inside its parent,
// not an execution of its own), so the signal is always addressed to the
// chat's workflow and this field is the only thing that selects which thread's
// gate actually wakes. A signal naming a sub-thread must not wake its parent.
type Signal struct {
	Thread string `json:"thread,omitempty"`
	Reason Reason `json:"reason,omitempty"`
}
