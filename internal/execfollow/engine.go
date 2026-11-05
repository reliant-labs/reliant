// Copyright (c) 2025 Reliant Labs
//
// Leaf utility package: the exported surface is concrete helpers over the
// stdlib or the OS, with no collaborator to fake and no second implementation.
// An interface here would have exactly one implementor and one caller shape,
// which is indirection without a seam.
//
//forge:exclude-contract: `reliant workflow follow`: the CLI's flags-and-hooks layer over the SDK's follow engine (sdk/go/reliant/follow)

// Package execfollow is `reliant workflow follow`'s view of the follow engine.
//
// The engine itself (update mapping, false-green detection, gate reconciliation,
// exit semantics) lives in the SDK, sdk/go/reliant/follow, so every Go caller
// follows a run by the same rules. What stays here is what only the CLI has:
// the --hook flag and the shell hooks it runs, and the stdout/stderr defaults.
// Every other name is an alias of the SDK's, so a type crosses between the two
// without conversion.
package execfollow

import (
	"context"
	"io"
	"os"
	"time"

	"github.com/reliant-labs/reliant/sdk/go/reliant/follow"
)

// Exit codes returned by Engine.Run.
const (
	ExitSuccess = follow.ExitSuccess
	ExitFailed  = follow.ExitFailed
	ExitTimeout = follow.ExitTimeout
	ExitGate    = follow.ExitGate
)

// Gate kinds returned by Source.Pending.
const (
	GateQuestion = follow.GateQuestion
	GateApproval = follow.GateApproval
)

// DefaultInterval is the poll interval between update fetches.
const DefaultInterval = follow.DefaultInterval

// Event types emitted on the NDJSON stream.
const (
	EventNodeStarted       = follow.EventNodeStarted
	EventNodeCompleted     = follow.EventNodeCompleted
	EventNodeFailed        = follow.EventNodeFailed
	EventNodeSkipped       = follow.EventNodeSkipped
	EventWorkflowStarted   = follow.EventWorkflowStarted
	EventWorkflowCompleted = follow.EventWorkflowCompleted
	EventWorkflowFailed    = follow.EventWorkflowFailed
	EventWorkflowCancelled = follow.EventWorkflowCancelled
	EventQuestion          = follow.EventQuestion
	EventQuestionAnswered  = follow.EventQuestionAnswered
	EventApproval          = follow.EventApproval
	EventApprovalResolved  = follow.EventApprovalResolved
	EventFollowTimeout     = follow.EventFollowTimeout
)

// Run outcomes carried on a terminal workflow event.
const (
	OutcomeSuccess = follow.OutcomeSuccess
	OutcomeFailure = follow.OutcomeFailure
)

type (
	RawUpdate    = follow.RawUpdate
	RootState    = follow.RootState
	PendingGate  = follow.PendingGate
	Source       = follow.Source
	Event        = follow.Event
	SubQuestion  = follow.SubQuestion
	QuestionInfo = follow.QuestionInfo
	ApprovalInfo = follow.ApprovalInfo
)

var (
	RenderText  = follow.RenderText
	IsStuckStep = follow.IsStuckStep
	Succeeded   = follow.Succeeded
)

// Engine drives a follow session for the CLI: the SDK's engine, plus the
// --hook shell hooks and the CLI's output defaults (Out is stdout and Log is
// stderr when nil). Field for field it is the struct the CLI has always built.
type Engine struct {
	Source      Source
	ExecutionID string

	Out io.Writer // event stream (stdout)
	Log io.Writer // diagnostics (stderr)

	// Renderer formats an event for the Out stream. When nil the engine writes
	// the canonical NDJSON line; `workflow watch` sets RenderText. Hooks always
	// receive the NDJSON form regardless of Renderer, so hook stdin is stable
	// across both commands.
	Renderer func(Event) string

	Hooks      []Hook
	HookRunner *HookRunner

	Interval   time.Duration // poll interval; DefaultInterval when zero
	Timeout    time.Duration // overall deadline; 0 means none
	Tail       bool          // skip historical events
	ExitOnGate bool          // return ExitGate as soon as a gate is open

	inner *follow.Engine
}

func (e *Engine) log() io.Writer {
	if e.Log != nil {
		return e.Log
	}
	return os.Stderr
}

// Run follows the execution until terminal state, timeout, or context
// cancellation. It returns the process exit code.
func (e *Engine) Run(ctx context.Context) (int, error) {
	out := e.Out
	if out == nil {
		out = os.Stdout
	}
	runner := e.HookRunner
	if runner == nil {
		runner = &HookRunner{Stderr: e.log()}
	}
	hooks := e.Hooks
	e.inner = &follow.Engine{
		Source:      e.Source,
		ExecutionID: e.ExecutionID,
		Out:         out,
		Log:         e.log(),
		Renderer:    e.Renderer,
		Interval:    e.Interval,
		Timeout:     e.Timeout,
		Tail:        e.Tail,
		ExitOnGate:  e.ExitOnGate,
		OnEvent: func(ctx context.Context, ev Event, ndjson []byte) {
			runner.Run(ctx, hooks, ev, ndjson)
		},
	}
	return e.inner.Run(ctx)
}

// OpenGates returns the gate events open when Run returned ExitGate.
func (e *Engine) OpenGates() []Event {
	if e.inner == nil {
		return nil
	}
	return e.inner.OpenGates()
}

// TerminalStatus returns the normalized terminal status of the root workflow
// once Run has observed one, or "".
func (e *Engine) TerminalStatus() string {
	if e.inner == nil {
		return ""
	}
	return e.inner.TerminalStatus()
}

// TerminalOutcome returns the run's declared verdict, or "".
func (e *Engine) TerminalOutcome() string {
	if e.inner == nil {
		return ""
	}
	return e.inner.TerminalOutcome()
}
