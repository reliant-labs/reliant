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

	// Unattended sets inputs.unattended: no human will answer questions or
	// approvals, so the run must not block on one.
	Unattended bool
	// UserJWT is the caller's JWT, carried on the run's execution context so a
	// cloud-daemon chat's first run resolves the control-plane daemon the same
	// way its later runs do. Interactive callers fill it; scheduled fires have
	// none and leave it empty.
	UserJWT string
	// GenerateTitle starts GenerateTitleWorkflow from the first user message.
	GenerateTitle bool
	// GreenfieldProbe asks the daemon whether the working directory holds code
	// and, when it does not, prepends the greenfield guidance message.
	GreenfieldProbe bool
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
