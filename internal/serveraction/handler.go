// Copyright (c) 2025 Reliant Labs
package serveraction

import (
	"context"
	"errors"
	"fmt"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// Handler implements one server action. It runs on the worker, inside the
// action node's activity, after the dispatcher has checked the switch, the
// name and the params and established the run's Principal and Provenance.
//
// A Handler is retried as a node is: Temporal may run the same scheduled
// call more than once (a worker lost mid-call), so a handler that writes
// must be idempotent on Request.Invocation.ExecutionKey.
//
// Return an *Error for an answer the workflow's author should read (a
// refused verdict, a stale head): its code and message are recorded in the
// run. Any other error is internal: it is logged here and the run records
// only that the action failed, never the error's text.
type Handler interface {
	Handle(ctx context.Context, req *Request) (*Result, error)
}

// HandlerFunc adapts a function to a Handler.
type HandlerFunc func(ctx context.Context, req *Request) (*Result, error)

// Handle calls f.
func (f HandlerFunc) Handle(ctx context.Context, req *Request) (*Result, error) { return f(ctx, req) }

// Request is one call of a server action.
type Request struct {
	// Name is the action called, canonical ("reliant/pr.next@1").
	Name string
	// Spec is its declaration.
	Spec Spec
	// Invocation is where in which run the call was made.
	Invocation Invocation
	// Node is the action node's definition as the run executes it (its `with`
	// rendered). Handlers read node-level metadata from it, such as a
	// declared role. Read it, never write it.
	Node *reliantv1.Node
	// Principal is who the run acts for.
	Principal Principal
	// Provenance is where the run's workflow definition came from.
	Provenance Provenance
	// Params is the node's rendered `with:`, already checked against
	// Spec.Params.
	Params map[string]any
	// From is the node the call's `with: {from: <id>}` names, as the engine
	// recorded it, or nil (no from, or no such node in the action's scope).
	// An action that judges another node's work reads it here, never from
	// Params: the author renders Params, the engine records From.
	From *NodeRecord
}

// NodeRecord is a node of the run's graph as the engine recorded it.
type NodeRecord struct {
	// NodeID is the node's id in the action's scope.
	NodeID string
	// Type is the node's type as the run executes it ("run"; a review node
	// runs as a "workflow" node).
	Type string
	// Role is the engine role its definition declares ("reviewer").
	Role string
	// Review is true only for an engine-owned review node: its output is the
	// checked verdict, with the reviewer session it ran in.
	Review bool
	// Ran is true when the node has recorded an output.
	Ran bool
	// Output is that output, JSON-shaped.
	Output map[string]any
}

// Invocation places a call in its run. Every field is the runtime's own
// record; none comes from the workflow's author.
type Invocation struct {
	// RunID is the workflow id of the run the node belongs to (the
	// workflows row).
	RunID string
	// ChatID is the run's chat, "" for none.
	ChatID string
	// ThreadID is the thread the node runs on.
	ThreadID string
	// NodeID is the node's id; NodePath its dotted position through every
	// nesting boundary ("loop.attempt.decide"), or NodeID at the top level.
	NodeID   string
	NodePath string
	// LoopNodeID and LoopIteration name the innermost loop iteration the
	// node runs in, "" and 0 outside a loop.
	LoopNodeID    string
	LoopIteration int
	// TemporalRunID and ActivityID identify the scheduled execution:
	// together unique in the run's history, and the same on every Temporal
	// retry of it. Empty outside an activity (a test).
	TemporalRunID string
	ActivityID    string
	// Attempt is the Temporal attempt of this execution, 1 on the first.
	Attempt int32
	// PositionKey is the call's place in the graph: the run, the node path
	// and the loop iteration. It is the same on a retry, a resume and a
	// replay, and it repeats when one iteration visits the node again.
	PositionKey string
	// ExecutionKey is the scheduled execution: PositionKey plus the
	// TemporalRunID and ActivityID. It is the same on every retry of one
	// execution and different for every other execution of the node, so it
	// is the key a writing handler is idempotent on. It falls back to
	// PositionKey outside an activity.
	ExecutionKey string
}

// Principal is who a run acts for, as the run's own records say.
type Principal struct {
	// UserID is the run's owner: its recorded owner, else its chat's user.
	UserID string
	// OrgID is the run's org: its recorded org, else its chat's ("" for
	// none).
	OrgID string
	// ProjectID is the project of the run's chat.
	ProjectID string
	// ActingIdentity is who the run's integration writes speak as: "app"
	// for the GitHub App's bot, "" for the owner (runtime.ActingIdentity).
	ActingIdentity string
	// Automation is the trigger the run belongs to, by name, "" for a run a
	// person started.
	Automation string
	// TriggerID is the activation (the triggers row) whose firing launched
	// the run's chat, by id, from its launch event: "" for a run a person
	// started, a sub-agent's run, or a run whose chat predates launch
	// events. Capability grants are made to it (internal/pipeline/grants).
	TriggerID string
	// DaemonID is the machine the run's chat executes on
	// (chats.active_daemon_id), "" for a run on no machine.
	DaemonID string
}

// Source is where a run's workflow definition was resolved from.
type Source string

const (
	// SourceUnknown is a definition the dispatcher could not place: a
	// project or account workflow of a run with no pinned catalog (it may
	// have changed since the run started), or a run whose records are
	// incomplete. A handler that authorizes on provenance refuses it.
	SourceUnknown Source = ""
	// SourceBuiltin is a workflow embedded in Reliant (builtin://).
	SourceBuiltin Source = "builtin"
	// SourceProject is a .reliant/workflows file of the run's project.
	SourceProject Source = "project"
	// SourceUser is the run owner's account workflow.
	SourceUser Source = "user"
)

// Provenance is where the definition a run executes came from.
//
// It describes the run's ROOT workflow: the ref the run was started with,
// resolved as the run resolved it. A node inside a nested `ref:` workflow
// reports its root's provenance, as does a spawned sub-agent's run its own
// target's (it is a run of its own).
type Provenance struct {
	// WorkflowRef is the ref the run was started with ("builtin://pr", a
	// project workflow's name). "" from a run that predates the record.
	WorkflowRef string
	// Source is where WorkflowRef resolved.
	Source Source
	// Path is the project file it resolved to, for SourceProject: relative
	// to the root repo's workflows directory for the root's own files, and
	// project-relative for a nested repo's ("api/.reliant/workflows/x.yaml").
	Path string
	// CatalogPin is the snapshot of the project's workflows the run started
	// with, "" for a run with none.
	CatalogPin string
	// Repo is the git state the pinned project file was synced from, nil
	// when not SourceProject or the machine reported none.
	Repo *RepoState
	// Trigger is what started the run: a trigger kind, "manual" for a
	// person, "spawn" for a sub-agent.
	Trigger string
}

// RepoState is a repository's git state as the machine synced it.
type RepoState struct {
	// Repo is its path relative to the project root, "" for the root.
	Repo string
	// Branch is the checked-out branch, "" when HEAD is detached.
	Branch string
	// Commit is HEAD's SHA.
	Commit string
	// Dirty is true when .reliant had uncommitted changes.
	Dirty bool
	// DefaultBranch is origin's default branch, "" when unknown.
	DefaultBranch string
}

// Result is a handler's answer, recorded as the node's output.
type Result struct {
	// Data is the structured output (nodes.<id>.data), JSON-shaped.
	Data map[string]any
	// Content is a one-line human summary (nodes.<id>.content).
	Content string
}

// Error codes an action's failure may carry, the integration actions' set.
const (
	CodeFailedPrecondition = "failed_precondition"
	CodeInvalidArgument    = "invalid_argument"
	CodePermissionDenied   = "permission_denied"
	CodeNotFound           = "not_found"
	CodeAlreadyExists      = "already_exists"
	CodeAborted            = "aborted"
	CodeUnavailable        = "unavailable"
	CodeInternal           = "internal"
)

// Error is a failure whose code and message are written for the workflow's
// author and are recorded in the run as they are.
type Error struct {
	Code    string
	Message string
	// Retryable marks a failure a later attempt may not repeat (a conflict,
	// a dependency that was down).
	Retryable bool
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Errorf is an *Error with a formatted message.
func Errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// AsError returns err's *Error, if it has one.
func AsError(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) && e != nil {
		return e, true
	}
	return nil, false
}
