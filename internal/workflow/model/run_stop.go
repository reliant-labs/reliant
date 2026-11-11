// Copyright (c) 2025 Reliant Labs
package model

// RunStopQuery is the query a run answers with RunStopState: whether it has
// stopped executing its graph and is ending on its own.
//
// It is what lets a cancel be cooperative and still bounded. The run service
// asks Temporal to CANCEL the run — which runs the run's own completion code:
// its activities are cancelled (and their commands killed), its cleanup runs,
// and it writes its own terminal status — and then waits a short grace for the
// run to say it is ending. A run that never says so (parked somewhere that
// does not observe cancellation, no worker to answer) is terminated instead,
// exactly as every cancel used to be.
//
// Declared here, not in the runtime package, because the run service reads it
// and must not import the engine.
const RunStopQuery = "get_run_stop"

// RunStopState is the RunStopQuery result.
type RunStopState struct {
	// Ending is true once the run has left its graph and is running its
	// completion code (terminal handlers, bookkeeping). From here it closes by
	// itself, bounded by its handlers' own timeouts, and must not be
	// terminated: terminating it now would cut off the cleanup the cancel was
	// cooperative for.
	Ending bool `json:"ending"`
}
