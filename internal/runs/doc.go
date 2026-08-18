// Copyright (c) 2025 Reliant Labs
//
// Package runs owns the lifecycle of a chat's run: making it stop, and making
// it execute again.
//
// It exists because that decision used to be inlined in gRPC handlers — twice,
// in ResumeChat and in SendMessage, already drifting apart. A handler is now
// authenticate -> call this service -> map the outcome to proto; everything
// about HOW a run stops or restarts lives here.
//
// # What this service knows, and nothing outside it does
//
//   - the chat/workflow rows and the workflow status values that gate a
//     lifecycle decision (db.Failed(), db.Paused())
//   - how to ask Temporal for execution state, and how a Temporal status maps
//     onto ours
//   - what "stuck" means: the DB says Failed while Temporal says the execution
//     is still running. Such a run cannot be resumed; the user must branch.
//   - that a resume may RESET the execution and mint a NEW run id, so the run
//     id has to be re-read from Temporal afterward and written back to the chat
//   - how PauseService's error sentinels classify into a caller-visible outcome
//   - how a hard operator terminate compensates for Temporal TerminateWorkflow
//     skipping the workflow completion handler: checkpoint deletion, CAS root
//     status repair, pending-question resolution, descendant cascade, and thread
//     cascade
//
// # No durable position record
//
// Resuming needs no stored position, and this package deliberately writes none.
// Position lives in Temporal, in two forms: while the process is alive it IS
// the goroutine stack (executors block in place on CanceledError and re-dispatch
// the cancelled step), and when the process is dead it IS Temporal history
// (reset-and-replay rebuilds the nested engine stack, and continueAsNew carries
// ResumeInput forward in the continuation's own input).
//
// A per-frame position table was built and reverted for exactly this reason.
// The flat workflow_checkpoints row survives only as the coarse hint for the one
// path that can re-enter a top-level node. If a change here appears to need a
// new table or column, that is the signal the design has gone wrong again.
// See specs/pause-and-resume.md.
//
// # Relationship to workflow.PauseService
//
// This service WRAPS PauseService rather than absorbing it. PauseService retains
// one job that is not run lifecycle: SignalWithRecovery delivers an arbitrary
// domain signal (an approval decision, a question answer) to a possibly-dead
// workflow, addressed by Temporal workflow id. Approval and question handlers
// use it against SUB-workflow ids that no chat lifecycle call could name.
//
// The lifecycle trio — PauseWorkflow, ResumeWorkflow, ResumeInterruptedWorkflow —
// is reachable only through this package. ChatService holds no PauseService
// handle at all, so there is exactly one door.
package runs
