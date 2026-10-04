// Copyright (c) 2025 Reliant Labs

// Package threadcancel defines the wire contract for stopping ONE spawned
// thread without touching the rest of a run: the signal name and the payload
// that names which spawn to stop.
//
// It is its own package, holding nothing but a constant and a struct, for the
// same reason threadwake is: both ENDS of the signal need it and they sit on
// opposite sides of an import edge. The receiver is internal/workflow/runtime;
// the senders are internal/grpc/services (the UI cancel path) and, via
// internal/temporal, internal/llm/tools (the spawn_stop tool) — and runtime
// imports tools, so defining the contract in runtime makes a sender's import
// a cycle.
//
// A leaf package with no project imports can be depended on from anywhere, and
// keeps the signal name and payload in ONE place. Two spellings of a signal
// name that must match exactly is a silent-failure bug: the sender succeeds,
// the receiver never hears it, and the spawn the user asked to stop keeps
// running while every surface reports it cancelled. That exact failure has
// already happened here once (see ToolCallService.cancelChildWorkflowForToolCall).
package threadcancel

// SignalName is the Temporal signal a sender rings to stop one spawned thread.
//
// The value is load-bearing history, not a label: runtime has listened on
// "cancel_thread" since the spawn-cancel fix landed, and every live workflow
// execution's signal channel is already bound to that exact string. Changing
// it breaks delivery silently for every run in flight.
const SignalName = "cancel_thread"

// Signal names the spawn to stop. Either identifier may be set, and a sender
// that knows both should send both:
//
//   - Thread is the spawn's own thread id — what an agent holds as an
//     agent_id, and what the spawned loop checks at its step boundary.
//   - ToolCallID is the spawn tool call that created it — the only id a user
//     cancelling from the UI can name.
//
// They are not interchangeable: child_workflow_id on a tool call is not
// guaranteed to equal the child thread id, so a sender that guesses one from
// the other can address a spawn that nothing is listening for. The receiver
// records whichever ids arrive and the spawn matches on either.
type Signal struct {
	Thread     string `json:"thread,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
}

// SpawnRef names the THREE identities of one spawn. It lives beside the signal
// because the same import-edge argument applies: the senders (a tool, an RPC
// handler) and the code that delivers the stop sit on opposite sides of a
// cycle, and they need one agreed spelling of "which spawn".
//
// All three are carried because for a RESUMED spawn they are different values.
// A first spawn sets WorkflowID == ThreadID, which is why one "spawn id"
// parameter looked sufficient and was not. A resumption (spawn with agent_id)
// keeps the original ThreadID and derives a fresh WorkflowID per resumption
// from the NEW tool call — DeterministicWorkflowID(parent, toolCallID) — so:
//
//   - ThreadID addresses the running loop. The Signal names it, and the spawn's
//     step-boundary check matches on it.
//   - WorkflowID addresses the row recording THIS resumption's run. A status
//     reconcile must target that row; using ThreadID instead hits the original,
//     already-completed row and leaves the live one active after a stop that
//     reported success.
//   - ToolCallID is this resumption's call. The receiver matches either it or
//     ThreadID, and a user cancelling from the UI knows only this one.
//
// Naming them separately rather than letting a callee guess is the point: every
// bug on this path so far came from one id silently standing in for another.
type SpawnRef struct {
	ThreadID   string
	WorkflowID string
	ToolCallID string
}
