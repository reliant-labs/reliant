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
// caller from a request context — the owner is always explicit in the Spec.
// Its errors carry their svcerr kind (each typed error unwraps to one, with the
// underlying cause withheld from the client), so a handler returns them as they
// are and the svcerr interceptor picks the wire code.
//
// See research/TRIGGERS.md.
//
// Built by hand, by internal/app's NewRunLauncher provider
// (providers_worker.go) and the chat handler: never a node compose.go
// constructs from Deps.
//
//forge:provided
package launch

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/types/known/structpb"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/chatselection"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/threads"
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
	// Origin is trigger.origin, resolved at intake. nil for a source with no
	// attribution.
	Origin *core.TriggerOrigin
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

	// ClientMessageID is the id of the first user message when the client
	// chose it (a canonical UUID). The message is written under it, which is
	// what lets a later send of the same message find it. Empty means a
	// generated id.
	ClientMessageID string

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

	// MachinePool records that a machine pool placed this run: DaemonID is
	// the member it chose (empty when resuming a launch whose chat is already
	// pinned). It becomes the chat's MachinePoolID, which is how the pool
	// counts its members' live runs, and the run's inputs.machine_pool, so a
	// workflow can address every member (sync-checkouts). Requires DaemonID
	// on a new chat; mutually exclusive with NoMachine.
	MachinePool *PoolPlacement

	// Unattended sets inputs.unattended: no human will answer questions or
	// approvals, so the run must not block on one, and it is withheld the
	// tools tools.UnattendedWithholding names. Launch sets it for every event
	// kind core.TriggerEventKind.Unattended reports, so a caller can add it
	// and never remove it.
	Unattended bool
	// ActingIdentity is who the integration writes of this run speak as
	// (core.ActingIdentityApp: the GitHub App's bot). Empty is the owner. Set
	// from the trigger row by the fire paths; it is recorded on the run's
	// inputs only for an unattended launch, because the bot is an automation's
	// identity and a person's own chat start must never act as it.
	ActingIdentity core.ActingIdentity
	// Automation names the trigger, for the footer on what the bot writes.
	Automation string
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

	// RecordExecution persists immutable admission identity after Temporal accepts
	// the idempotent start. A failed write is retried, never treated as free capacity.
	RecordExecution func(ctx context.Context, eventID, workflowID, firstRunID string) error
	// PrepareExecution records the immutable workflow ID in the SAME launch
	// transaction, before Temporal may accept it or the activation disappears.
	PrepareExecution func(ctx context.Context, eventID, workflowID string) error
}

// PoolPlacement is the pool a run was placed by, and its members as
// placement saw them.
type PoolPlacement struct {
	ID      string
	Name    string
	Members []PoolMember
}

// PoolMember is one member of a PoolPlacement.
type PoolMember struct {
	ID    string
	Name  string
	State string
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

// Unwrap makes it an AlreadyExists, with its own text as the client message.
func (e *AlreadyLaunchedError) Unwrap() error { return svcerr.AlreadyExists(e.Error()) }

// ErrClientMessageReused reports that Spec.ClientMessageID already names a
// different message: the same id with other text or attachments. A repeat of
// the same message is not an error; it is answered with the chat it started.
// Its text is what the client reads.
var ErrClientMessageReused = svcerr.AlreadyExists("client_message_id was already used for a different message; use a new id for a new message")

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

// TxOutcome marks a decline as the launch transaction's intended rollback,
// not a failure: the repo logs it quietly (a trigger waiting for workflow
// capacity is declined on every poll, and each one was an ERROR and an issue).
func (e *DeclinedError) TxOutcome() {}

var _ db.TxOutcome = (*DeclinedError)(nil)

// ErrorClass implements svcerr.Classed: a guard declining a launch ("waiting
// for workflow capacity") is the system working, never a fault, wherever the
// error surfaces. TxOutcome only quiets the transaction ladder's own log.
func (e *DeclinedError) ErrorClass() svcerr.Class { return svcerr.ClassUser }

// ErrNotPending reports that Spec.ChatID names a chat whose root run has
// already started; the caller should continue it with SendMessage instead.
var ErrNotPending = svcerr.FailedPrecondition("chat has already started; use SendMessage")

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
// help. It is an InvalidArgument or a FailedPrecondition per Kind, reading
// Reason; the schedule fire path records it as a failed event without
// retrying.
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

// Unwrap is the svcerr kind the client reads, Reason as its message; Err is
// the withheld cause, still reachable by errors.Is and errors.As.
func (e *ValidationError) Unwrap() error {
	kind := svcerr.InvalidArgument(e.Reason)
	if e.Kind == ValidationFailedPrecondition {
		kind = svcerr.FailedPrecondition(e.Reason)
	}
	return svcerr.WithCause(kind, e.Err)
}

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

// Unwrap is a NotFound reading Reason; Err is the withheld cause.
func (e *NotFoundError) Unwrap() error {
	return svcerr.WithCause(svcerr.WithMessage(svcerr.ErrNotFound, e.Reason), e.Err)
}

// Is makes errors.Is(err, ErrNotFound) match.
func (e *NotFoundError) Is(target error) bool { return target == ErrNotFound }

// ErrInternal reports that the launch failed for a reason the caller cannot
// act on — a failed transaction, a Temporal start that was refused, a chat that
// committed but could not be read back.
var ErrInternal = errors.New("internal")

// InternalError is the concrete ErrInternal. Reason is the message the client
// reads; Err is the detail, which stays in the logs rather than on the wire.
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

// Unwrap is an Internal reading Reason; Err is the withheld cause, which the
// observe interceptor logs.
func (e *InternalError) Unwrap() error { return svcerr.WithCause(svcerr.Internal(e.Reason), e.Err) }

// Is makes errors.Is(err, ErrInternal) match.
func (e *InternalError) Is(target error) bool { return target == ErrInternal }

// The Launcher reads and writes through these role interfaces instead of the
// whole *db.Repo, so each dependency of a launch is visible here and a
// fake implements only what a test exercises.

// ChatWriter creates and updates the chat a launch materializes.
type ChatWriter interface {
	CreateChat(ctx context.Context, chat *db.Chat) error
	GetChat(ctx context.Context, id string) (*db.Chat, error)
	UpdateChat(ctx context.Context, chat *db.Chat) error
	UpdateChatActiveDaemon(ctx context.Context, chatID string, daemonID *string) error
	CountMessagesInChat(ctx context.Context, chatID string) (int, error)
	SaveMessageToThread(ctx context.Context, chatID, thread string, role int32, content string, workflowID *string, attachmentIDs []string, displayStyle *int32) (*db.Message, error)
	SaveMessageToThreadOnce(ctx context.Context, chatID, thread string, role int32, content string, workflowID *string, attachmentIDs []string, displayStyle *int32, messageID string) (msg *db.Message, created bool, err error)
	CreateUserUpdate(ctx context.Context, update *db.UserUpdate) error
}

// TriggerEventStore is the trigger_events ledger a launch dedupes against.
type TriggerEventStore interface {
	CreateTriggerEvent(ctx context.Context, ev *core.TriggerEvent) (created bool, err error)
	GetTriggerEventByDedupe(ctx context.Context, kind core.TriggerEventKind, dedupeKey string) (*core.TriggerEvent, error)
	UpdateTriggerEventOutcome(ctx context.Context, id string, outcome core.TriggerEventOutcome, detail string, chatID *string) error
	UpdateTriggerEventPayload(ctx context.Context, id string, payload map[string]any) error
	// ClaimPendingTriggerEvent adopts an inbound event a receiver recorded
	// as pending; claimed=false means another launch got it first.
	ClaimPendingTriggerEvent(ctx context.Context, id string, payload map[string]any) (claimed bool, err error)
}

// WorkflowStore is the root workflow row a launch starts.
type WorkflowStore interface {
	GetWorkflow(ctx context.Context, id string) (*db.Workflow, error)
	CompareAndSwapWorkflowStatus(ctx context.Context, id string, newStatus, expectedStatus db.WorkflowStatus) (bool, error)
	UpdateWorkflowName(ctx context.Context, id string, workflowName string) error
	// MarkWorkflowStartFailed records a failed Temporal start on a PENDING
	// root; ClaimWorkflowStart clears it before a retry, and false means the
	// root left PENDING, so the start is no longer the caller's to make.
	MarkWorkflowStartFailed(ctx context.Context, id string) (bool, error)
	ClaimWorkflowStart(ctx context.Context, id string) (bool, error)
}

// WorkflowResolver looks up the workflow definition and presets a spec names.
type WorkflowResolver interface {
	GetWorkflowDraftBySlug(ctx context.Context, userID, slug string) (*db.WorkflowDraft, error)
	GetUsableWorkflowBySlug(ctx context.Context, userID, slug string) (*db.WorkflowDraft, error)
	GetPresetBySlug(ctx context.Context, userID, slug string) (*db.Preset, error)
	GetPresetBySlugAndProject(ctx context.Context, userID, slug, projectID string) (*db.Preset, error)
	// One column each, never the whole project config record: see
	// db.Repo.GetProjectWorkflowsJSON.
	GetProjectWorkflowsJSON(ctx context.Context, projectID string) (*string, error)
	GetProjectPresetsJSON(ctx context.Context, projectID string) (*string, error)
	// One machine's own synced catalog (workflowsource.MachineStore), which a
	// trigger's run on that machine resolves against; synced is false when
	// the machine has never synced the project.
	GetProjectMachineWorkflowsJSON(ctx context.Context, projectID, daemonID string) (workflows *string, synced bool, err error)
	GetProjectMachinePresetsJSON(ctx context.Context, projectID, daemonID string) (presets *string, synced bool, err error)
	// WorkflowSourceDiagnostic (workflowsource.SourceDiagnostics) says, on a
	// miss, which machine provides the workflow and at which path.
	WorkflowSourceDiagnostic(ctx context.Context, projectID, daemonID, ref string) (string, error)
}

// ProjectLookup resolves the project, worktrees and settings a launch runs in.
type ProjectLookup interface {
	GetProject(ctx context.Context, id string) (*db.Project, error)
	GetProjectWithUserCheck(ctx context.Context, id string, userID string) (*db.Project, error)
	GetWorktree(ctx context.Context, id string) (*db.Worktree, error)
	ListWorktrees(ctx context.Context, filters db.WorktreeFilters) ([]*db.Worktree, error)
	GetSetting(ctx context.Context, userID string, projectID *string, key string) (*db.Setting, error)
}

// Transactor runs f in one transaction; repository calls made with the ctx f
// receives join it.
type Transactor interface {
	RunTx(ctx context.Context, policy orm.TxPolicy, f func(ctx context.Context) error) error
}

// ChatSelections is the chat selection store: the layer between a chat's
// presets and a send's params, and where a start records its first choice.
type ChatSelections interface {
	chatselection.Store
}

// Store is everything a Launcher needs from the database. *db.Repo satisfies it.
type Store interface {
	ChatWriter
	ChatSelections
	TriggerEventStore
	WorkflowStore
	WorkflowResolver
	ProjectLookup
	Transactor
}

// ThreadCreator creates the root workflow and its thread inside the launch
// transaction. Satisfied by *threads.Service.
type ThreadCreator interface {
	CreateWorkflowWithThread(ctx context.Context, opts threads.CreateWorkflowWithThreadOpts) (*db.Workflow, *db.Thread, *db.ContextWindow, error)
}

var (
	_ Store         = (*db.Repo)(nil)
	_ ThreadCreator = (*threads.Service)(nil)
)
