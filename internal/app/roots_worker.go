// Copyright (c) 2025 Reliant Labs

package app

import (
	"github.com/reliant-labs/reliant/internal/llm/accountlimits"
	"github.com/reliant-labs/reliant/internal/triggers/workflowevent"
	"github.com/reliant-labs/reliant/internal/workersetup"
)

// The worker root's field order is its STOP order: the first field stops
// first, and a field's closure is built just before it. That order is
// load-bearing (TestWorkerStopsTakingWorkFirst):
//
//  1. temporal_worker: stops taking work first. Its Drain stops polling,
//     gives in-flight activities a short grace, then cancels the ones still
//     running and waits for them to report — releasing them, so Temporal
//     re-dispatches them to a live replica rather than waiting out their
//     heartbeat timeout (workersetup.Worker). It is also the node whose
//     Ready ("polling") holds the process's readiness until it polls.
//  2. workflow_event_relay, account_usage_poller, write_ledger_prune: the
//     background loops, which take no Temporal tasks; each ends with its
//     context.
//  3. account_recorder: after every loop, because the activities record into
//     it; its Close flushes the rate-limit state they recorded. It is a
//     field of its own for that: reached only through the Temporal worker,
//     it would be built just before it and stop right after it, ahead of
//     the loops.
//
// Then the infrastructure, each after everything that uses it — the
// streaming hub, the Temporal client, the update hubs and NATS, the
// diagnostics listener, the database — which is why each is a field too:
// reached first through the Temporal worker or a loop, it would stop between
// them. The probe listener (HEALTH_PORT) answers for the whole of boot and
// drain: the readiness paths say "draining" from the first moment of the
// stop.
//
// The last fields are the process-wide state, so they are built FIRST:
// Settings (config only) before any other node, Runtime (the database-backed
// globals) right after the database.
//
// The worker never migrates: the api-server owns the schema, and the
// database provider waits for it (migrate=wait).

// Worker's doc comment is `reliant server worker`'s help (roots_gen.go): the
// first paragraph its Short, the rest its Long.

// Run the Temporal workflow worker
//
// Starts a Temporal worker that processes workflow executions. Connects to
// an external Temporal server and executes workflow activities (LLM inference,
// tool execution routing, etc.).
//
// Designed to run as N replicas for horizontal scaling.
//
//forge:root worker command=server,worker migrate=wait
type Worker struct {
	TemporalWorker *workersetup.Worker
	Relay          *workflowevent.Relay
	UsagePoller    *accountlimits.Poller
	LedgerPrune    *WriteLedgerPrune
	Recorder       *AccountRecorder
	Streaming      *StreamingHub
	Temporal       *TemporalClient
	// Feed publishes the repository's user and chat updates; it has no
	// lifecycle, and orders the hubs after the Temporal client. NATS starts
	// before the client, which publishes stop nudges on it (stopnudge).
	Feed *RepoUpdateFeed
	// Debug is off with PPROF_PORT=0; it stops after NATS, so a profile can
	// be taken through the drain.
	Debug    *DebugServer
	Runtime  *ProcessRuntime
	Settings *ProcessSettings
}
