// Copyright (c) 2025 Reliant Labs
package serveraction

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/logging"
)

// SwitchName is the server setting that turns every server action on.
const SwitchName = "pipeline.enabled"

// Call is one action node's request to the dispatcher, as the activity
// received it.
type Call struct {
	// Uses is the node's `uses:` as the run rendered it.
	Uses string
	// Node is the node's definition.
	Node *reliantv1.Node
	// Params is the node's rendered `with:`.
	Params map[string]any
	// Invocation places the call in its run.
	Invocation Invocation
	// Run is what the runtime recorded about the run, for the Resolver.
	Run RunFacts
	// From is the node the call's `with: {from:}` names (Request.From).
	From *NodeRecord
}

// RunFacts is what the runtime hands an activity about its run, which the
// Resolver completes from the run's records.
type RunFacts struct {
	// WorkflowRef is the ref the run started with (RunTelemetry.WorkflowRef).
	WorkflowRef string
	// Trigger is what started it (RunTelemetry.Trigger).
	Trigger string
	// CatalogPin is the project catalog the run started with.
	CatalogPin string
	// ActingIdentity and Automation are the run's (RuntimeContext).
	ActingIdentity string
	Automation     string
}

// Resolver establishes who a run acts for and where its definition came
// from, from the run's own records. An error is a failure to read them; a
// definition it cannot place is SourceUnknown, not an error.
type Resolver interface {
	Resolve(ctx context.Context, inv Invocation, run RunFacts) (Principal, Provenance, error)
}

// Config is a Dispatcher's switch, catalog, handlers and resolver.
type Config struct {
	// Enabled is the pipeline.enabled server switch. Off, every server
	// action is refused before anything else is read.
	Enabled bool
	// Catalog is what exists; nil means Builtin().
	Catalog *Catalog
	// Handlers implement the catalog's actions, by canonical name. An
	// action with no handler is refused as unavailable on this server.
	Handlers map[string]Handler
	// Resolver completes each call's Principal and Provenance. Required for
	// any action to run: without it every call is refused.
	Resolver Resolver
}

// Dispatcher routes a server-action call to its handler. A nil *Dispatcher
// is a worker with no server actions configured, and refuses every call as
// switched off.
type Dispatcher struct {
	enabled  bool
	catalog  *Catalog
	handlers map[string]Handler
	resolver Resolver
}

// NewDispatcher builds a dispatcher, refusing a handler for an action the
// catalog does not declare.
func NewDispatcher(cfg Config) (*Dispatcher, error) {
	catalog := cfg.Catalog
	if catalog == nil {
		catalog = Builtin()
	}
	handlers := make(map[string]Handler, len(cfg.Handlers))
	for name, h := range cfg.Handlers {
		if h == nil {
			return nil, fmt.Errorf("server action %q: nil handler", name)
		}
		if _, err := catalog.Resolve(name); err != nil {
			return nil, fmt.Errorf("handler bound to %q: %w", name, err)
		}
		if parsed, _ := ParseName(name); parsed.String() != name {
			return nil, fmt.Errorf("handler bound to %q: bind it by its canonical name %q", name, parsed)
		}
		handlers[name] = h
	}
	return &Dispatcher{enabled: cfg.Enabled, catalog: catalog, handlers: handlers, resolver: cfg.Resolver}, nil
}

// Enabled reports the pipeline.enabled switch this dispatcher was built with.
func (d *Dispatcher) Enabled() bool { return d != nil && d.enabled }

// Outcome is a call's result: Result on success, Err on failure. Err's code
// and message are safe to record in the run's history.
type Outcome struct {
	Name   string
	Result *Result
	Err    *Error
}

// Dispatch runs one call. It never returns a Go error: every failure is an
// Outcome the graph can branch on, and an internal failure's cause is logged,
// never put in the Outcome.
func (d *Dispatcher) Dispatch(ctx context.Context, call Call) Outcome {
	name := strings.TrimSpace(call.Uses)
	fail := func(e *Error) Outcome { return Outcome{Name: name, Err: e} }
	if !d.Enabled() {
		return fail(Errorf(CodeFailedPrecondition, "%s is a Reliant server action, and server actions are switched off on this server (%s is false)", name, SwitchName))
	}
	spec, err := d.catalog.Resolve(call.Uses)
	if err != nil {
		return fail(Errorf(CodeInvalidArgument, "%v", err))
	}
	name = spec.Name
	handler, ok := d.handlers[spec.Name]
	if !ok {
		return fail(Errorf(CodeFailedPrecondition, "%s is not available on this server", spec.Name))
	}
	if err := d.catalog.CheckParams(spec.Name, call.Params); err != nil {
		return fail(Errorf(CodeInvalidArgument, "%v", err))
	}
	if d.resolver == nil {
		return fail(Errorf(CodeFailedPrecondition, "%s cannot run on this worker: it cannot establish who the run acts for", spec.Name))
	}
	principal, provenance, err := d.resolver.Resolve(ctx, call.Invocation, call.Run)
	if err != nil {
		logging.ErrorContext(ctx, "[ServerAction] could not resolve the run", "action", spec.Name,
			"run", call.Invocation.RunID, "node", call.Invocation.NodePath, "error", err)
		return fail(&Error{Code: CodeUnavailable, Message: fmt.Sprintf("%s could not read the run's records", spec.Name), Retryable: true})
	}
	if principal.UserID == "" {
		// A run the engine cannot attribute acts for nobody: fail closed.
		return fail(Errorf(CodeFailedPrecondition, "%s cannot run: the run has no recorded owner", spec.Name))
	}
	req := &Request{
		Name: spec.Name, Spec: spec, Invocation: call.Invocation, Node: call.Node,
		Principal: principal, Provenance: provenance, Params: call.Params, From: call.From,
	}
	result, err := invoke(ctx, handler, req)
	if err != nil {
		if public, ok := AsError(err); ok {
			return fail(public)
		}
		logging.ErrorContext(ctx, "[ServerAction] handler failed", "action", spec.Name,
			"run", call.Invocation.RunID, "node", call.Invocation.NodePath, "error", err)
		retryable := errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
		return fail(&Error{Code: CodeInternal, Message: fmt.Sprintf("%s failed; the server logged the cause", spec.Name), Retryable: retryable})
	}
	if result == nil {
		result = &Result{}
	}
	return Outcome{Name: spec.Name, Result: result}
}

// invoke calls the handler, turning a panic into an internal error so one
// broken handler fails its call, not the worker's activity slot.
func invoke(ctx context.Context, h Handler, req *Request) (result *Result, err error) {
	defer func() {
		if r := recover(); r != nil {
			result, err = nil, fmt.Errorf("handler panicked: %v\n%s", r, debug.Stack())
		}
	}()
	return h.Handle(ctx, req)
}
