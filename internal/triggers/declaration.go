// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/triggers/triggerspec"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
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

// ErrorClass implements svcerr.Classed. A broken declaration is the trigger
// owner's to fix, and they are told: the trigger reports BROKEN health and an
// inbox item (BrokenHealth). As a server fault it was also an ERROR record and
// an issue on every tick of the trigger.
func (e *DeclarationError) ErrorClass() svcerr.Class { return svcerr.ClassUser }

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
	// DedupeKey and DedupeWindow are a webhook declaration's dedupe settings
	// (see triggerspec.CompileDedupe); empty means the receiver's default key.
	DedupeKey, DedupeWindow string
	// OriginFilter is the declared origin_filter, "" for the default
	// (triggerspec.DefaultOriginFilter). It is evaluated before Filter.
	OriginFilter string
	// WorkflowInputs are the inputs the workflow declares: the types and
	// defaults activation.params is read through (see ActivationScope).
	WorkflowInputs map[string]*reliantv1.Input
}

// WorkflowResolver loads the workflow a run of a ref executes, as run start
// does (launch.ResolveRunWorkflow). A store failure must be distinguishable
// from a verdict: return a *launch.WorkflowLookupError (or any error with a
// `Retryable() bool` that reports true) for the former.
type WorkflowResolver interface {
	ResolveRunWorkflow(ctx context.Context, userID, workflow, projectID string) (*reliantv1.Workflow, error)
}

// MachineWorkflowResolver is a WorkflowResolver that can also resolve against
// what one machine synced. An activation targets a machine (Trigger.DaemonID)
// and its runs execute there, so its declaration is read from that machine's
// catalog, not from the union of every machine's: a workflow only another
// machine provides is a verdict naming that machine and the file's path.
type MachineWorkflowResolver interface {
	WorkflowResolver
	ResolveRunWorkflowOn(ctx context.Context, userID, workflow, projectID, daemonID string) (*reliantv1.Workflow, error)
}

// resolveTriggerWorkflow loads the workflow t's runs execute: on t's target
// machine when it has one and the resolver can read a machine's catalog.
//
// A pool-bound trigger has no machine of its own; it reads the pool's agreed
// catalog through the member bindPoolCatalog chose (CatalogDaemonID).
func resolveTriggerWorkflow(ctx context.Context, workflows WorkflowResolver, t *core.Trigger) (*reliantv1.Workflow, error) {
	machine := t.DaemonID
	if machine == "" {
		machine = t.CatalogDaemonID
	}
	if m, ok := workflows.(MachineWorkflowResolver); ok && machine != "" {
		return m.ResolveRunWorkflowOn(ctx, t.UserID, t.Workflow, t.ProjectID, machine)
	}
	return workflows.ResolveRunWorkflow(ctx, t.UserID, t.Workflow, t.ProjectID)
}

// LaunchWorkflows is the WorkflowResolver over the launcher's store: the same
// resolution every run start uses.
type LaunchWorkflows struct{ Repo launch.WorkflowResolver }

var _ MachineWorkflowResolver = LaunchWorkflows{}

// ResolveRunWorkflow implements WorkflowResolver.
func (w LaunchWorkflows) ResolveRunWorkflow(ctx context.Context, userID, workflow, projectID string) (*reliantv1.Workflow, error) {
	return launch.ResolveValidatedRunWorkflowOn(ctx, w.Repo, launch.AutomationScope(userID, projectID, ""), workflow)
}

// ResolveRunWorkflowOn implements MachineWorkflowResolver.
func (w LaunchWorkflows) ResolveRunWorkflowOn(ctx context.Context, userID, workflow, projectID, daemonID string) (*reliantv1.Workflow, error) {
	return launch.ResolveValidatedRunWorkflowOn(ctx, w.Repo, launch.AutomationScope(userID, projectID, daemonID), workflow)
}

// ResolveDeclaration reads the declaration t activates from its workflow and
// checks t can still follow it. A non-nil error is ErrNotAnActivation, a
// *DeclarationError (the activation is broken), or a retryable failure to
// read the workflow.
func ResolveDeclaration(ctx context.Context, workflows WorkflowResolver, t *core.Trigger) (*Declaration, error) {
	if t == nil || t.WorkflowTrigger == nil || *t.WorkflowTrigger == "" {
		return nil, ErrNotAnActivation
	}
	wf, err := resolveTriggerWorkflow(ctx, workflows, t)
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
	dedupeKey, dedupeWindow := strings.TrimSpace(wt.GetDedupeKey()), strings.TrimSpace(wt.GetDedupeWindow())
	if dedupeKey != "" || dedupeWindow != "" {
		if src.Kind != core.TriggerKindWebhook {
			return nil, broken("dedupe_key applies to webhook triggers only; " + kindNoun(src.Kind) + " triggers dedupe by their own event id")
		}
		if err := triggerspec.CompileDedupe(dedupeKey, dedupeWindow); err != nil {
			return nil, broken("the declaration's " + err.Error())
		}
	}

	originFilter := strings.TrimSpace(wt.GetOriginFilter())
	if originFilter != "" {
		if !src.Kind.IsEventDriven() {
			return nil, broken("origin_filter applies to event-driven triggers; a schedule has no event whose origin to read")
		}
		if _, err := triggerspec.CompileOriginFilter(originFilter); err != nil {
			return nil, broken("the declaration's " + err.Error())
		}
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
	return &Declaration{Name: name, Source: src, Filter: filter, Inputs: wt.GetInputs(), Prompt: prompt,
		DedupeKey: dedupeKey, DedupeWindow: dedupeWindow, OriginFilter: originFilter, WorkflowInputs: wf.GetInputs()}, nil
}

// ActivationScope is everything a trigger expression of t reads: the event's
// `trigger` root, with `trigger.activation` set under it. Both the intake filter and every
// fire path build it here, so a filter and an inputs mapping that name the
// same param read the same value.
//
// activation.params is what the activation's params will be when the run
// starts, as far as the trigger can know: the workflow's input defaults
// applied, every value coerced to its input's declared type, and an input
// the trigger maps from the event left out (the event sets it). An input
// with neither a param nor a default reads as its type's zero ([] for a
// list); a required input missing from the params makes the activation
// NEEDS_SETUP (see SetupIssues) rather than reading as empty.
// An ad hoc trigger (decl nil) has no declared inputs; its params are as
// stored.
func ActivationScope(ctx context.Context, repo any, t *core.Trigger, decl *Declaration, root map[string]any) triggerspec.Scope {
	params := t.Params
	if decl != nil {
		params = decl.activationParams(t.Params)
	}
	return triggerspec.Scope{Trigger: root, Activation: triggerspec.ActivationRoot(t.ID, OwnerOf(ctx, repo, t), params)}
}

// connectionReader and accessSubjectReader are what resolving an activation's
// owner needs of the store. Every repository the fire and intake paths run on
// (*db.Repo) has them (see ownerSource); a narrower fake that has neither
// simply yields an owner with no GitHub id.
type connectionReader interface {
	GetConnection(ctx context.Context, userID, id string) (*core.Connection, error)
}

type connectionStoreProvider interface {
	Connections() db.ConnectionStore
}

type accessSubjectReader interface {
	GetIntegrationAccessSubject(ctx context.Context, userID, integrationID string, freshAfter time.Time) (string, error)
}

// ownerSource is the production repository's side of owner resolution; the
// assertion keeps a refactor of *db.Repo from silently emptying every owner.
type ownerSource interface {
	connectionStoreProvider
	accessSubjectReader
}

var _ ownerSource = (*db.Repo)(nil)

// OwnerOf is who t's runs are for, as trigger.activation.owner. It is the one
// place every trigger path (intake filter, origin filter, dedupe, fire,
// replay, workflow events) resolves the owner, so a filter and an inputs
// mapping that name github_id read the same value.
//
// github_id is the owner's GitHub user id, in the form a GitHub event names
// its sender (trigger.sender.id), so a filter can say "only me" by comparing
// the two. It comes from, in order:
//
//  1. the GitHub connection t listens through, when it names one: the
//     account that connection acts as (Connection.SenderID). A named
//     connection DECIDES: runs act through it, so a connection that is not
//     the owner's GitHub one, or has no sender id, leaves github_id empty
//     rather than falling back to some other account of the owner's;
//  2. otherwise the owner's linked GitHub identity: the GitHub user id the
//     platform reads off the owner's own GitHub token (the one their runs
//     act with when no connection is named) every time it refreshes their
//     access, kept as the subject of their access snapshot.
//
// It is resolved at every evaluation and never stored on the trigger: the
// identity can change (the owner reconnects another GitHub account), and a
// copy taken when the trigger was declared would go on admitting the old
// account as "me". The snapshot is only trusted while fresh
// (core.IntegrationAccessFreshness), like routing trusts it.
//
// It is empty when neither source yields an id, and a failure to find the id
// must never widen who a filter admits: "" equals no verified sender's id.
func OwnerOf(ctx context.Context, repo any, t *core.Trigger) triggerspec.Owner {
	owner := triggerspec.Owner{UserID: t.UserID}
	if t.UserID == "" {
		return owner
	}
	if t.ConnectionID != nil && *t.ConnectionID != "" {
		owner.GitHubID = connectionSenderID(ctx, repo, t)
		return owner
	}
	reader, ok := repo.(accessSubjectReader)
	if !ok {
		return owner
	}
	id, err := reader.GetIntegrationAccessSubject(ctx, t.UserID, "github", time.Now().Add(-core.IntegrationAccessFreshness))
	if err != nil {
		logging.WarnContext(ctx, "could not read the owner's linked GitHub identity; treating it as unknown",
			"trigger_id", t.ID, "user_id", t.UserID, "error", err)
		return owner
	}
	owner.GitHubID = id
	return owner
}

// connectionSenderID is the GitHub user id of the connection t listens
// through, or "" when it is not the owner's GitHub connection or cannot be
// read.
func connectionSenderID(ctx context.Context, repo any, t *core.Trigger) string {
	var reader connectionReader
	switch r := repo.(type) {
	case connectionReader:
		reader = r
	case connectionStoreProvider:
		reader = r.Connections()
	default:
		return ""
	}
	conn, err := reader.GetConnection(ctx, t.UserID, *t.ConnectionID)
	if err != nil || conn == nil || conn.UserID != t.UserID || conn.IntegrationID != "github" || conn.SenderID == nil {
		return ""
	}
	return *conn.SenderID
}

func (d *Declaration) activationParams(stored map[string]any) map[string]any {
	return v2.ActivationParams(d.WorkflowInputs, d.Inputs, stored)
}

// SeedPrompt is the prompt a run t launches starts from: the activation's own
// message when it has one (an override), otherwise its declaration's prompt
// template rendered against the `trigger` root. An ad hoc trigger (decl nil)
// always has its own.
func SeedPrompt(t *core.Trigger, decl *Declaration, scope triggerspec.Scope) (string, error) {
	if own := strings.TrimSpace(t.Message); own != "" || decl == nil || decl.Prompt == "" {
		return t.Message, nil
	}
	rendered, err := triggerspec.RenderPrompt(decl.Prompt, scope)
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

// Projection is what an activation's row should cache of its declaration,
// given the params it holds: the source config with the typed scope resolved
// (see ProjectedConfig), and the filter.
func (d *Declaration) Projection(params map[string]any) core.TriggerProjection {
	return core.TriggerProjection{Config: d.ProjectedConfig(params), Filter: d.Filter}
}

// ProjectedConfig is the config to store for an activation holding params. It
// is the declaration's, plus, for an integration source scoped by
// `match: {attribute: {input: name}}`, MatchAny: the values each scoped
// input holds in the activation's params (defaults applied), which is what
// routing checks an event against before any row is written. It is a
// projection like the filter: it is rewritten whenever the params or the
// declaration change, and fire paths still read the declaration.
func (d *Declaration) ProjectedConfig(params map[string]any) json.RawMessage {
	if d.Source.Kind != core.TriggerKindIntegration {
		return d.Source.Config
	}
	var cfg core.IntegrationConfig
	if err := json.Unmarshal(d.Source.Config, &cfg); err != nil || len(cfg.MatchInput) == 0 {
		return d.Source.Config
	}
	resolved := d.activationParams(params)
	cfg.MatchAny = make(map[string][]string, len(cfg.MatchInput))
	for attr, input := range cfg.MatchInput {
		cfg.MatchAny[attr] = scopeValues(resolved[input])
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		// Without MatchAny the scope matches nothing, which is the safe side.
		return d.Source.Config
	}
	return raw
}

// scopeValues is the values an input holds, as the strings an attribute is
// compared with: a multi input's items, or a single input's value. Empty
// strings are no value.
func scopeValues(v any) []string {
	var items []any
	switch x := v.(type) {
	case nil:
	case []any:
		items = x
	case []string:
		for _, s := range x {
			items = append(items, s)
		}
	default:
		items = []any{x}
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// MergeDeclaredInputs evaluates a declaration's inputs against the event and
// its activation and merges them over the activation's params. A declared input WINS
// over a param of the same name: the declaration says where that input comes
// from, and a stored param silently overriding the event would make the
// mapping a lie. (Activation refuses such a collision up front; this rule
// also covers params stored before an edit added the mapping.)
func MergeDeclaredInputs(params map[string]any, inputs map[string]string, scope triggerspec.Scope) (map[string]any, error) {
	if len(inputs) == 0 {
		return params, nil
	}
	evaluated, err := triggerspec.EvaluateInputs(inputs, scope)
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
	cache map[[4]string]cachedWorkflow
}

var _ MachineWorkflowResolver = (*CachedWorkflows)(nil)

type cachedWorkflow struct {
	wf  *reliantv1.Workflow
	err error
}

// NewCachedWorkflows wraps inner for the life of one request.
func NewCachedWorkflows(inner WorkflowResolver) *CachedWorkflows {
	return &CachedWorkflows{inner: inner, cache: map[[4]string]cachedWorkflow{}}
}

// ResolveRunWorkflow implements WorkflowResolver.
func (c *CachedWorkflows) ResolveRunWorkflow(ctx context.Context, userID, workflow, projectID string) (*reliantv1.Workflow, error) {
	return c.resolve(ctx, userID, workflow, projectID, "")
}

// ResolveRunWorkflowOn implements MachineWorkflowResolver: on the inner
// resolver's machine catalog when it can read one, else as ResolveRunWorkflow.
func (c *CachedWorkflows) ResolveRunWorkflowOn(ctx context.Context, userID, workflow, projectID, daemonID string) (*reliantv1.Workflow, error) {
	if _, ok := c.inner.(MachineWorkflowResolver); !ok {
		daemonID = ""
	}
	return c.resolve(ctx, userID, workflow, projectID, daemonID)
}

func (c *CachedWorkflows) resolve(ctx context.Context, userID, workflow, projectID, daemonID string) (*reliantv1.Workflow, error) {
	key := [4]string{userID, workflow, projectID, daemonID}
	if hit, ok := c.cache[key]; ok {
		return hit.wf, hit.err
	}
	var (
		wf  *reliantv1.Workflow
		err error
	)
	if m, ok := c.inner.(MachineWorkflowResolver); ok && daemonID != "" {
		wf, err = m.ResolveRunWorkflowOn(ctx, userID, workflow, projectID, daemonID)
	} else {
		wf, err = c.inner.ResolveRunWorkflow(ctx, userID, workflow, projectID)
	}
	c.cache[key] = cachedWorkflow{wf: wf, err: err}
	return wf, err
}

func kindNoun(k core.TriggerKind) string {
	if k == core.TriggerKindWorkflowEvent {
		return "workflow-event"
	}
	return string(k)
}
