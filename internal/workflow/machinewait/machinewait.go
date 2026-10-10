// Copyright (c) 2025 Reliant Labs

// Package machinewait defines the wire contract for the signal that reaches a
// run parked in preflight waiting for its machine.
//
// A run whose machine is still starting does not poll for it: after its first
// wait slice it sleeps on a durable Temporal timer, and this signal is what
// cuts the sleep short. The api-server sends it when one of the user's
// machines connects or the user sends another message (so the run re-checks
// at once instead of at its next recheck), and when the user gives up on the
// machine ("Continue without machine"), which ends the wait.
//
// It is a leaf package for the same reason internal/workflow/threadwake is:
// the receiver is internal/workflow/runtime and the senders sit on the other
// side of its import edge, so the name and payload live in one place both can
// import. A misspelled signal name is not an error — the sender succeeds and
// nothing hears it.
//
// Only a run in its preflight machine wait listens. Anywhere else the signal
// sits unread in the run's history, so a sender need not know exactly where
// the run is.
package machinewait

// SignalName is the Temporal signal that reaches a run waiting for its
// machine.
const SignalName = "machine_wait"

// Signal is the payload of SignalName.
type Signal struct {
	// Abandon ends the wait: the user chose to continue without the machine
	// (in a no-machine branch), so this run must not wait for it, nor deliver
	// the message later. The run ends cancelled, with nothing queued.
	// Otherwise the signal only asks the run to check its machine now.
	Abandon bool `json:"abandon,omitempty"`
	// Reason is diagnostic: which event sent the signal.
	Reason string `json:"reason,omitempty"`
}

// Reasons a signal was sent; diagnostic only.
const (
	// ReasonMachineConnected: one of the user's machines connected.
	ReasonMachineConnected = "machine_connected"
	// ReasonUserMessage: the user sent another message, which woke the
	// machine again.
	ReasonUserMessage = "user_message"
	// ReasonContinuedWithoutMachine: the user branched the chat into one with
	// no machine.
	ReasonContinuedWithoutMachine = "continued_without_machine"
)
