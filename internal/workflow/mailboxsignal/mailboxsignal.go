// Copyright (c) 2025 Reliant Labs

// Package mailboxsignal defines the wire contract for the agent-mailbox
// doorbell: the signal a sender rings after queuing a row into
// agent_messages, so a recipient thread parked waiting on its background
// spawns wakes up and drains it.
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
package mailboxsignal

// SignalName is the Temporal signal a sender rings after queuing a mailbox
// row. Changing it breaks delivery silently — a signal to a name nothing
// listens on is not an error — so it is defined once and shared.
const SignalName = "agent_message_queued"

// Signal names the thread whose mailbox just gained a row.
//
// Deliberately carries no body. This is a doorbell, not a delivery mechanism:
// the message is already durable in agent_messages and the drain reads it from
// there, which keeps the database the single source of truth and makes a
// dropped or duplicated signal harmless.
type Signal struct {
	Thread string `json:"thread,omitempty"`
}
