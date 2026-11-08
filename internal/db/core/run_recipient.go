// Copyright (c) 2025 Reliant Labs
package core

import "errors"

// Input addressed to a run (SignalRun, send_to_run, the composer on a run's
// main view) goes to a thread an agent of the run is reading. These are the
// two ways that has no single answer; callers match them with errors.Is.
var (
	// ErrRunNoListeningAgent: the run is live, but no agent loop is running
	// in it right now, so nothing would read the message.
	ErrRunNoListeningAgent = errors.New("no agent is running in this run")
	// ErrRunRecipientAmbiguous: several agents are running in parallel and
	// the sender named none of them. The run never guesses.
	ErrRunRecipientAmbiguous = errors.New("several agents are running in this run")
)
