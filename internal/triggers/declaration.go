// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"errors"
	"fmt"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/triggers/triggerspec"
)

// A workflow declares WHEN it runs in its `triggers:` block; a trigger row
// with WorkflowTrigger set ACTIVATES one of those declarations for one user
// (who the runs execute as, where, through which connection and daemon).
//
// The declaration, not the row, is the source of truth for what fires the
// trigger, what filters it and what inputs it passes. It is read from the
// workflow every time the trigger fires, so editing the workflow's YAML
// changes the behaviour of every activation — which is the point: the
// workflow's author decides when it runs, and the people who activated it
// decide only as whom and where.
//
// The row keeps a projection of the declaration's config and filter, because
// routing reads them in SQL (integration triggers by integration id) and the
// schedule syncer reads the cron; Reconcile keeps it current. A projection
// that disagrees with the declaration at fire time never fires on its own
// say-so: the fire path re-resolves and decides from the declaration.

// ErrNotAnActivation is returned for an ad hoc trigger, whose source is its
// own row rather than a declaration.
var ErrNotAnActivation = errors.New("not an activation of a declared trigger")

// DeclarationError reports an activation whose declaration cannot be used:
// the workflow is gone or not runnable, it no longer declares the name, or
// the declaration changed in a way this activation cannot follow. It is a
// verdict, not a transient failure: retrying will not fix it, and the
// trigger is BROKEN until the workflow is fixed or it is re-activated.
type DeclarationError struct {
	Workflow string
	Name     string
	Reason   string
}

func (e *DeclarationError) Error() string {
	return fmt.Sprintf("declared trigger %q of workflow %q: %s", e.Name, e.Workflow, e.Reason)
}

// Declaration is an activation's effective WHEN, read from its workflow.
type Declaration struct {
	Name   string
	Source *triggerspec.Source
	Filter string
	// Inputs maps workflow input names to templates over `trigger`.
	Inputs map[string]string
	// Prompt is the declaration's prompt template over `trigger`; empty when
	// every activation writes its own (see SeedPrompt).
	Prompt string
}

// WorkflowResolver loads the workflow a run of a ref executes, as run start
// does (launch.ResolveRunWorkflow). A store failure must be distinguishable
// from a verdict: return a *launch.WorkflowLookupError (or any error with a
// `Retryable() bool` that reports true) for the former.
type WorkflowResolver interface {
	ResolveRunWorkflow(ctx context.Context, userID, workflow, projectID string) (*reliantv1.Workflow, error)
}

// LaunchWorkflows is the WorkflowResolver over the launcher's store: the same
// resolution every run start uses.
type LaunchWorkflows struct{ Repo launch.WorkflowResolver }

// ResolveRunWorkflow implements WorkflowResolver.
func (w LaunchWorkflows) ResolveRunWorkflow(ctx context.Context, userID, workflow, projectID string) (*reliantv1.Workflow, error) {
	return launch.ResolveRunWorkflow(ctx, w.Repo, userID, workflow, projectID)
}

// ResolveDeclaration reads the declaration t activates from its workflow and
// checks t can still follow it. A non-nil error is ErrNotAnActivation, a
// *DeclarationError (the activation is broken), or a retryable failure to
// read the workflow.
func ResolveDeclaration(ctx context.Context, workflows WorkflowResolver, t *core.Trigger) (*Declaration, error) {
	if t == nil || t.WorkflowTrigger == nil || *t.WorkflowTrigger == "" {
		return nil, ErrNotAnActivation
	}
	wf, err := workflows.ResolveRunWorkflow(ctx, t.UserID, t.Workflow, t.ProjectID)
	if err != nil {
		if isRetryableLookup(err) {
			return nil, fmt.Errorf("load workflow %q for trigger %s: %w", t.Workflow, t.ID, err)
		}
		return nil, &DeclarationError{Workflow: t.Workflow, Name: *t.WorkflowTrigger,
			Reason: "its workflow does not resolve to a runnable workflow (" + err.Error() + ")"}
	}
	return DeclarationIn(wf, t)
}

// DeclarationIn finds and validates the declaration t activates within an
// already-loaded workflow, for a caller that loaded it itself (a save that
// reconciles every activation of the workflow it just stored).
func DeclarationIn(wf *reliantv1.Workflow, t *core.Trigger) (*Declaration, error) {
	if t == nil || t.WorkflowTrigger == nil || *t.WorkflowTrigger == "" {
		return nil, ErrNotAnActivation
	}
	name := *t.WorkflowTrigger
	broken := func(reason string) error {
		return &DeclarationError{Workflow: t.Workflow, Name: name, Reason: reason}
	}

	wt := FindDeclaredTrigger(wf, name)
	if wt == nil {
		declared := DeclaredTriggerNames(wf)
		if len(declared) == 0 {
			return nil, broken(fmt.Sprintf("the workflow no longer declares a trigger named %q (it declares none)", name))
		}
		return nil, broken(fmt.Sprintf("the workflow no longer declares a trigger named %q (it declares: %s)",
			name, strings.Join(declared, ", ")))
	}
	src, err := triggerspec.FromWorkflowTrigger(wt)
	if err != nil {
		return nil, broken("the declaration is invalid: " + err.Error())
	}
	filter := strings.TrimSpace(wt.GetFilter())
	if filter != "" {
		if src.Kind == core.TriggerKindSchedule {
			return nil, broken("the declaration filters a schedule, which has no event to filter")
		}
		if _, err := triggerspec.CompileFilter(filter); err != nil {
			return nil, broken("the declaration's " + err.Error())
		}
	}
	if errs := triggerspec.CompileInputs(wt.GetInputs()); len(errs) > 0 {
		return nil, broken("the declaration's " + errs[0].Error())
	}
	prompt := strings.TrimSpace(wt.GetPrompt())
	if err := triggerspec.CompilePrompt(prompt); err != nil {
		return nil, broken("the declaration's " + err.Error())
	}

	// What this activation was created for and cannot follow: a webhook's
	// token, an integration's connection, a schedule's Temporal schedule all
	// belong to one kind (and a connection to one integration). Re-activate
	// to change them.
	if t.Kind != "" && src.Kind != t.Kind {
		return nil, broken(fmt.Sprintf("it is now a %s trigger, and this activation is a %s trigger; activate it again", kindNoun(src.Kind), kindNoun(t.Kind)))
	}
	if src.Kind == core.TriggerKindIntegration && len(t.Config) > 0 {
		if prev, err := IntegrationConfigFor(t); err == nil && prev.Integration != "" && prev.Integration != src.Integration {
			return nil, broken(fmt.Sprintf("it now listens to %s, and this activation's connection is for %s; activate it again", src.Integration, prev.Integration))
		}
	}
	return &Declaration{Name: name, Source: src, Filter: filter, Inputs: wt.GetInputs(), Prompt: prompt}, nil
}

// SeedPrompt is the prompt a run t launches starts from: the activation's own
// message when it has one (an override), otherwise its declaration's prompt
// template rendered against the `trigger` root. An ad hoc trigger (decl nil)
// always has its own.
func SeedPrompt(t *core.Trigger, decl *Declaration, root map[string]any) (string, error) {
	if own := strings.TrimSpace(t.Message); own != "" || decl == nil || decl.Prompt == "" {
		return t.Message, nil
	}
	rendered, err := triggerspec.RenderPrompt(decl.Prompt, root)
	if err != nil {
		return "", fmt.Errorf("the declared trigger's %w", err)
	}
	return rendered, nil
}

// FindDeclaredTrigger returns the trigger wf declares under name, or nil.
func FindDeclaredTrigger(wf *reliantv1.Workflow, name string) *reliantv1.WorkflowTrigger {
	for _, wt := range wf.GetTriggers() {
		if wt.GetName() == name {
			return wt
		}
	}
	return nil
}

// DeclaredTriggerNames lists the names wf declares, in declaration order.
func DeclaredTriggerNames(wf *reliantv1.Workflow) []string {
	names := make([]string, 0, len(wf.GetTriggers()))
	for _, wt := range wf.GetTriggers() {
		names = append(names, wt.GetName())
	}
	return names
}

// Projection is what t's row should cache of decl.
func (d *Declaration) Projection() core.TriggerProjection {
	return core.TriggerProjection{Config: d.Source.Config, Filter: d.Filter}
}

// MergeDeclaredInputs evaluates a declaration's inputs against the `trigger`
// root and merges them over the activation's params. A declared input WINS
// over a param of the same name: the declaration says where that input comes
// from, and a stored param silently overriding the event would make the
// mapping a lie. (Activation refuses such a collision up front; this rule
// also covers params stored before an edit added the mapping.)
func MergeDeclaredInputs(params map[string]any, inputs map[string]string, root map[string]any) (map[string]any, error) {
	if len(inputs) == 0 {
		return params, nil
	}
	evaluated, err := triggerspec.EvaluateInputs(inputs, root)
	if err != nil {
		return nil, err
	}
	merged := make(map[string]any, len(params)+len(evaluated))
	for k, v := range params {
		merged[k] = v
	}
	for k, v := range evaluated {
		merged[k] = v
	}
	return merged, nil
}

// isRetryableLookup reports a failure to READ the workflow, as opposed to a
// verdict about it.
func isRetryableLookup(err error) bool {
	var lookup *launch.WorkflowLookupError
	if errors.As(err, &lookup) {
		return true
	}
	// A draft that is not marked complete (db.WorkflowDraftNotRunnableError)
	// is a verdict, and so is anything else without this marker.
	var retryable interface{ Retryable() bool }
	return errors.As(err, &retryable) && retryable.Retryable()
}

// errNoResolver is an activation fired on a server given no way to read its
// declaration. Firing from the row's projection instead would mean an edit
// to the workflow silently does nothing; refusing is the honest answer.
var errNoResolver = errors.New("this server was not given a workflow resolver, so it cannot read the trigger's declaration")

// activationFor is the fire paths' one rule for an activation: nil for an ad
// hoc trigger (its row is its definition); otherwise its declaration, read
// now. A *DeclarationError (or errNoResolver) is a verdict to record; any
// other error is a failure to read the workflow, worth retrying.
func activationFor(ctx context.Context, workflows WorkflowResolver, t *core.Trigger) (*Declaration, error) {
	if t.WorkflowTrigger == nil || *t.WorkflowTrigger == "" {
		return nil, nil
	}
	if workflows == nil {
		return nil, errNoResolver
	}
	return ResolveDeclaration(ctx, workflows, t)
}

// isVerdict reports a declaration error a retry cannot fix.
func isVerdict(err error) bool {
	var declErr *DeclarationError
	return errors.As(err, &declErr) || errors.Is(err, errNoResolver)
}

// BrokenHealth is the health of an activation whose declaration cannot be
// used: BROKEN, with the reason. It replaces the firing-derived status,
// because no firing of a broken activation can launch.
func BrokenHealth(err *DeclarationError) *reliantv1.TriggerHealth {
	return &reliantv1.TriggerHealth{
		Status:            reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_BROKEN,
		LastFailureDetail: err.Error(),
	}
}

// CachedWorkflows memoizes a WorkflowResolver for one request: a list of
// fifty activations of one workflow reads and parses it once.
type CachedWorkflows struct {
	inner WorkflowResolver
	cache map[[3]string]cachedWorkflow
}

type cachedWorkflow struct {
	wf  *reliantv1.Workflow
	err error
}

// NewCachedWorkflows wraps inner for the life of one request.
func NewCachedWorkflows(inner WorkflowResolver) *CachedWorkflows {
	return &CachedWorkflows{inner: inner, cache: map[[3]string]cachedWorkflow{}}
}

// ResolveRunWorkflow implements WorkflowResolver.
func (c *CachedWorkflows) ResolveRunWorkflow(ctx context.Context, userID, workflow, projectID string) (*reliantv1.Workflow, error) {
	key := [3]string{userID, workflow, projectID}
	if hit, ok := c.cache[key]; ok {
		return hit.wf, hit.err
	}
	wf, err := c.inner.ResolveRunWorkflow(ctx, userID, workflow, projectID)
	c.cache[key] = cachedWorkflow{wf: wf, err: err}
	return wf, err
}

func kindNoun(k core.TriggerKind) string {
	if k == core.TriggerKindWorkflowEvent {
		return "workflow-event"
	}
	return string(k)
}
