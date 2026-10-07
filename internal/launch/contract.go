// Copyright (c) 2025 Reliant Labs

// Package launch is the one door through which a session's root run starts.
//
// Every source that starts work — an interactive chat's first send, a
// scheduled trigger, and later webhooks and buttons — produces an Event and a
// Spec, and Launch turns them into a running session: chat row, root workflow,
// thread, seed messages, the trigger_events row, and the Temporal start.
//
// Continuations are NOT launches. A message to a chat that has already started
// (live, paused, failed, completed) goes through ChatService.SendMessage, which
// never comes here.
//
// The package runs on both the api-server and the worker, so it never reads the
// caller from a request context — the owner is always explicit in the Spec —
// and it does not import connect: handlers map its errors to wire codes.
//
// See research/TRIGGERS.md.
package launch

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// Event is the uniform envelope every trigger source produces.
type Event struct {
	Kind       core.TriggerEventKind
	TriggerID  string         // stored trigger that fired; "" for ad hoc kinds
	DedupeKey  string         // unique within Kind
	OccurredAt time.Time      // when the source says it happened (scheduled time for a schedule)
	Payload    map[string]any // recorded verbatim on the event row
	// Sender is trigger.sender, as the source authenticated it. Nil for a
	// start a person made themselves.
	Sender *core.TriggerSender
}

// SeedMessage is one message written to the root thread before the run starts.
type SeedMessage struct {
	Role         reliantv1.MessageRole
	Content      string
	DisplayStyle *reliantv1.DisplayStyle
}

// Spec is what to launch.
type Spec struct {
	OwnerUserID string
	ProjectID   string
	WorktreeID  *string // nil → the project's main worktree

	// ChatID names an existing PENDING chat to start (a branched chat's first
	// send). Empty creates a new chat.
	ChatID string
	// NewChatID is the id for a created chat. Empty means random. Idempotent
	// sources derive it (a schedule uses a name-based UUID of its fire id) so a
	// retried fire cannot create a second chat.
	NewChatID string

	Title       *string
	Workflow    string // "" → the owner's default workflow
	Presets     map[string]string
	Params      map[string]*structpb.Value
	Mode        *string
	Messages    []SeedMessage
	Attachments []string

	// DaemonID is the daemon every tool call in this run executes on. It
	// becomes the chat's ActiveDaemonID, which the launcher injects as
	// inputs.session_daemon_id. Empty leaves daemon selection to the runtime.
	DaemonID string

	// NoMachine launches a run that has no machine by design (research/
	// DAEMONLESS_RUNS.md). It becomes the chat's NoMachine, which every
	// activity reads: the run is offered only tools that run without the
	// user's machine, and nothing in it resolves or wakes a daemon. A workflow
	// with a node that cannot run without one (a shell `run`, a
	// `create_worktree`, …) is refused here. Mutually exclusive with DaemonID.
	NoMachine bool

	// Unattended sets inputs.unattended: no human will answer questions or
	// approvals, so the run must not block on one, and it is withheld the
	// tools tools.UnattendedWithholding names. Launch sets it for every event
	// kind core.TriggerEventKind.Unattended reports, so a caller can add it
	// and never remove it.
	Unattended bool
	// UserJWT is the caller's JWT, carried on the run's execution context so a
	// cloud-daemon chat's first run resolves the control-plane daemon the same
	// way its later runs do. Interactive callers fill it; scheduled fires have
	// none and leave it empty.
	UserJWT string
	// GenerateTitle starts GenerateTitleWorkflow from the first user message.
	GenerateTitle bool
	// GreenfieldProbe marks this start as the chat's first turn of new work.
	// The run — not the launch — then asks the daemon whether the working
	// directory holds code before its first LLM call, and seeds the greenfield
	// guidance when it does not (WorkflowInput.GreenfieldProbe). Ignored for a
	// NoMachine run, which has no directory to ask about.
	GreenfieldProbe bool

	// Guard runs INSIDE the launch transaction, before anything is written, so
	// a check-then-launch decision (a schedule's overlap policy) is serialized
	// with the launch it protects instead of racing it. The ctx carries the
	// transaction: a guard that row-locks something holds the lock until the
	// launch commits or rolls back.
	//
	// A non-empty reason declines the launch: nothing is written and Launch
	// returns *DeclinedError. The guard may run more than once, because the
	// transaction retries on a serialization conflict. Applies to new chats
	// only; resuming an already-recorded launch never consults it.
	Guard func(ctx context.Context) (declineReason string, err error)
}

// Result is what Launch produced.
type Result struct {
	Chat       *db.Chat
	WorkflowID string
	RunID      string
	EventID    string
}

// ErrAlreadyLaunched reports that the (Kind, DedupeKey) — or the NewChatID —
// has already launched a run. Match with errors.Is; the concrete
// *AlreadyLaunchedError carries the existing chat id.
var ErrAlreadyLaunched = errors.New("already launched")

// AlreadyLaunchedError is the concrete ErrAlreadyLaunched.
type AlreadyLaunchedError struct {
	ChatID  string
	EventID string
}

func (e *AlreadyLaunchedError) Error() string {
	return fmt.Sprintf("already launched: chat %s", e.ChatID)
}

// Is makes errors.Is(err, ErrAlreadyLaunched) match.
func (e *AlreadyLaunchedError) Is(target error) bool { return target == ErrAlreadyLaunched }

// ErrDeclined reports that Spec.Guard declined the launch. Nothing was
// written. Match with errors.Is; *DeclinedError carries the reason.
var ErrDeclined = errors.New("launch declined")

// DeclinedError is the concrete ErrDeclined.
type DeclinedError struct {
	Reason string
}

func (e *DeclinedError) Error() string { return "launch declined: " + e.Reason }

// Is makes errors.Is(err, ErrDeclined) match.
func (e *DeclinedError) Is(target error) bool { return target == ErrDeclined }

// ErrNotPending reports that Spec.ChatID names a chat whose root run has
// already started; the caller should continue it with SendMessage instead.
var ErrNotPending = errors.New("chat has already started; use SendMessage")

// ValidationKind distinguishes "the request is malformed" from "the world is
// not in a state where this request can succeed". Both are validation failures
// that retrying will not fix, but they are different wire codes, and a handler
// must not have to pattern-match the message text to tell them apart.
type ValidationKind int

const (
	// ValidationInvalidArgument is a malformed Spec: a missing project id,
	// a dotted param key, inputs that do not satisfy the workflow schema.
	// Handlers map it to InvalidArgument.
	ValidationInvalidArgument ValidationKind = iota
	// ValidationFailedPrecondition is a well-formed Spec against a world that
	// cannot host it: a workflow whose tree does not validate, a project with
	// no main worktree. Handlers map it to FailedPrecondition.
	ValidationFailedPrecondition
)

// ValidationError reports a Spec that can never launch as written — an unknown
// workflow, a missing required input, an unavailable model. Retrying will not
// help. Handlers map it to InvalidArgument / FailedPrecondition per Kind; the
// schedule fire path records it as a failed event without retrying.
type ValidationError struct {
	Kind   ValidationKind
	Reason string
	Err    error
}

func (e *ValidationError) Error() string {
	if e.Err != nil {
		return e.Reason + ": " + e.Err.Error()
	}
	return e.Reason
}

func (e *ValidationError) Unwrap() error { return e.Err }

// ErrNotFound reports that the project, worktree or chat named in the Spec does
// not exist or does not belong to the owner.
var ErrNotFound = errors.New("not found")

// NotFoundError is the concrete ErrNotFound. Reason is the message a handler
// surfaces; it deliberately does not disclose whether the row is absent or
// merely owned by someone else.
type NotFoundError struct {
	Reason string
	Err    error
}

func (e *NotFoundError) Error() string {
	if e.Err != nil {
		return e.Reason + ": " + e.Err.Error()
	}
	return e.Reason
}

func (e *NotFoundError) Unwrap() error { return e.Err }

// Is makes errors.Is(err, ErrNotFound) match.
func (e *NotFoundError) Is(target error) bool { return target == ErrNotFound }

// ErrInternal reports that the launch failed for a reason the caller cannot
// act on — a failed transaction, a Temporal start that was refused, a chat that
// committed but could not be read back.
var ErrInternal = errors.New("internal")

// InternalError is the concrete ErrInternal. Reason is the message a handler
// surfaces; Err is the detail, which stays in the logs rather than on the wire.
type InternalError struct {
	Reason string
	Err    error
}

func (e *InternalError) Error() string {
	if e.Err != nil {
		return e.Reason + ": " + e.Err.Error()
	}
	return e.Reason
}

func (e *InternalError) Unwrap() error { return e.Err }

// Is makes errors.Is(err, ErrInternal) match.
func (e *InternalError) Is(target error) bool { return target == ErrInternal }
