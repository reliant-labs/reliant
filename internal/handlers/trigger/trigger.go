// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/rundiagnostics"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/daemonchoice"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/machinepool"
	"github.com/reliant-labs/reliant/internal/triggers"
	"github.com/reliant-labs/reliant/internal/workflow"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/validation"
)

// triggerConnections is the slice of the connection store an integration
// trigger's write path reads.
type triggerConnections interface {
	GetConnection(ctx context.Context, userID, id string) (*core.Connection, error)
	DefaultConnection(ctx context.Context, userID, integrationID string) (*core.Connection, error)
}

// connections returns the connection store, when the repository has one.
func (s *Service) connections() triggerConnections {
	if repo, ok := s.deps.Repo.(interface{ Connections() db.ConnectionStore }); ok {
		return repo.Connections()
	}
	return nil
}

// syncs reports whether a trigger is converged onto a Temporal Schedule:
// every schedule trigger, and integration triggers (whose syncer decides
// whether their integration is polled). A webhook or workflow-event trigger
// never is, so its writes need no schedule backend.
func syncs(kind core.TriggerKind) bool {
	return kind == core.TriggerKindSchedule || kind == core.TriggerKindIntegration
}

// sync converges the trigger's schedule when its kind has one. A schedule
// trigger without a backend was refused before it was stored; an
// integration trigger without one is a pushed-only deployment, where there
// is nothing to converge.
func (s *Service) sync(ctx context.Context, kind core.TriggerKind, id string) error {
	if !syncs(kind) || s.deps.Syncer == nil {
		return nil
	}
	if deferActivationEffect(ctx, func(ctx context.Context) error { return s.sync(ctx, kind, id) }) {
		return nil
	}
	return s.deps.Syncer.Sync(ctx, id)
}

// ownedTrigger loads a trigger and verifies the caller owns it.
//
// Another user's trigger is NotFound, not PermissionDenied: telling a caller
// that an id exists but is not theirs leaks the existence of other users'
// triggers for nothing.
func (s *Service) ownedTrigger(ctx context.Context, id string) (*core.Trigger, error) {
	if id == "" {
		return nil, svcerr.InvalidArgument("id is required")
	}
	userID := auth.MustGetUserID(ctx)

	trigger, err := s.deps.Repo.GetTrigger(ctx, id)
	if err != nil {
		// core.ErrTriggerNotFound is a NotFound kind, so it passes as itself.
		return nil, triggerDBError("get trigger", err)
	}
	if !authz.CanWriteTrigger(ctx, userID, trigger) {
		return nil, svcerr.NotFound("trigger")
	}
	return trigger, nil
}

// triggerFromDefinition validates a wire definition into a storable row.
// existing is the current row on update and nil on create.
func (s *Service) triggerFromDefinition(
	ctx context.Context,
	userID string,
	def *reliantv1.TriggerDefinition,
	existing *core.Trigger,
) (*core.Trigger, error) {
	if def.Name == "" {
		return nil, svcerr.InvalidArgument("name is required")
	}
	// A trigger with no prompt would launch a run with nothing to do, and the
	// agent would have no way to ask what was wanted — nobody is watching.
	// An activation may leave it empty when its declaration has a prompt
	// template; that is checked once the declaration is read.
	if def.Message == "" && def.GetWorkflowTrigger() == "" {
		return nil, svcerr.InvalidArgument("message is required")
	}
	if def.ProjectId == "" {
		return nil, svcerr.InvalidArgument("project_id is required")
	}

	// The machine is an explicit choice: a daemon, or no_machine. An empty
	// daemon_id alone is still the mistake it always was, so a client that
	// omits the field cannot silently create a run with no machine.
	// Or a machine pool: exactly one of the three.
	switch {
	case def.MachinePoolId != "" && (def.NoMachine || def.DaemonId != ""):
		return nil, svcerr.InvalidArgument("machine_pool_id, daemon_id and no_machine are mutually exclusive: runs execute on a pool's members, one machine, or none")
	case def.MachinePoolId != "":
	case def.NoMachine && def.DaemonId != "":
		return nil, svcerr.InvalidArgument("no_machine and daemon_id are mutually exclusive: runs either have a machine or they do not")
	case !def.NoMachine && def.DaemonId == "":
		return nil, svcerr.InvalidArgument("daemon_id is required: a trigger must name the daemon its runs execute on, a machine_pool_id, or no_machine")
	}

	if _, err := s.deps.Projects.GetProjectWithUserCheck(ctx, def.ProjectId, userID); svcerr.IsNotFound(err) {
		return nil, tools.RefuseActivation("project not found", svcerr.NotFound("project"))
	} else if err != nil {
		return nil, triggerDBError("get project", err)
	}

	if def.MachinePoolId != "" {
		if err := s.validateTriggerPool(ctx, userID, def); err != nil {
			return nil, err
		}
	} else if def.NoMachine {
		if err := s.validateNoMachineWorkflow(ctx, userID, def); err != nil {
			return nil, err
		}
	} else if err := s.validateTriggerDaemon(ctx, userID, def.ProjectId, def.DaemonId); err != nil {
		return nil, tools.RefuseActivation("machine is not usable for this project (not found, not yours, or the project is not on it)", err)
	}

	if def.WorktreeId != nil && *def.WorktreeId != "" {
		worktree, err := s.deps.Repo.GetWorktree(ctx, *def.WorktreeId)
		if err != nil && !svcerr.IsNotFound(err) {
			return nil, triggerDBError("get worktree", err)
		}
		if err != nil || worktree == nil {
			return nil, svcerr.NotFound("worktree")
		}
		// The worktree has to belong to the project the runs execute in;
		// otherwise the trigger would launch runs against a checkout of a
		// different project.
		if worktree.ProjectID != def.ProjectId {
			return nil, svcerr.InvalidArgument("worktree does not belong to the project")
		}
	}

	// An activation's source, filter and inputs come from the workflow's
	// declaration; an ad hoc trigger writes its source inline.
	var (
		src  *triggers.Source
		decl *triggers.Declaration
		err  error
	)
	if name := def.GetWorkflowTrigger(); name != "" {
		decl, err = s.resolveActivation(ctx, userID, def, name)
		if err != nil {
			return nil, err
		}
		src = decl.Source
	} else {
		// The source arm is what determines the kind, so an absent arm is
		// not a defaultable field — there is no kind to store. Validated
		// before storing: the Temporal server would reject a bad cron later
		// as an opaque RPC error, by which point the row exists with no
		// working schedule behind it.
		src, err = triggers.SourceFromDefinition(def)
		if err != nil {
			var cfgErr *triggers.ConfigError
			if errors.As(err, &cfgErr) {
				return nil, svcerr.InvalidArgument(cfgErr.Error())
			}
			return nil, svcerr.WrapOr(err, svcerr.Internal("could not read the trigger's source"))
		}
	}
	if existing != nil {
		wasActivation := existing.WorkflowTrigger != nil
		if wasActivation != (decl != nil) {
			return nil, svcerr.InvalidArgument("a trigger cannot change between activating a workflow's declared trigger and writing its source inline; delete it and create the other")
		}
		if existing.Kind != src.Kind {
			return nil, svcerr.InvalidArgument("a trigger's kind is not updatable")
		}
	}
	if src.Kind == core.TriggerKindSchedule && strings.TrimSpace(def.Filter) != "" {
		return nil, svcerr.InvalidArgument("filter: a schedule fires on time, not on an event, so there is nothing to filter")
	}

	identity, err := s.actingIdentityFor(def, existing, src)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	trigger := &core.Trigger{
		UserID:           userID,
		ProjectID:        def.ProjectId,
		WorktreeID:       def.WorktreeId,
		Name:             def.Name,
		Kind:             src.Kind,
		Workflow:         def.Workflow,
		Presets:          def.Presets,
		Params:           triggers.ParamsFromProto(def.Params),
		Message:          def.Message,
		DaemonID:         def.DaemonId,
		NoMachine:        def.NoMachine,
		MachinePoolID:    def.MachinePoolId,
		ActingIdentity:   identity,
		NotifyOnComplete: def.NotifyOnComplete,
		Config:           src.Config,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if trigger.WorktreeID != nil && *trigger.WorktreeID == "" {
		trigger.WorktreeID = nil
	}
	if src.Kind.IsEventDriven() {
		if err := s.applyInboundDefinition(ctx, userID, def, src, trigger, existing); err != nil {
			return nil, err
		}
	}
	if decl != nil {
		name := decl.Name
		trigger.WorkflowTrigger = &name
		// The row's filter is the declaration's, as of now: a projection the
		// fire path re-reads from the workflow.
		trigger.Filter = decl.Filter
		// ...and so is the typed scope, resolved from the params just validated.
		trigger.Config = decl.ProjectedConfig(trigger.Params)
	}
	return trigger, nil
}

// actingIdentityFor decides who a trigger's integration writes speak as.
//
// Unspecified means app (the Reliant GitHub bot) for every new activation,
// whatever its source: an automation's GitHub writes are the bot's unless its
// owner explicitly chooses owner. On update it means "unchanged": never
// reinterpret a stored identity using the creation default.
//
// A deployment with no control plane has no bot. There a new GitHub
// integration trigger is refused (its source says it acts on GitHub, so the
// bot is what it would have meant), while other sources default to the owner.
// Missing bot infrastructure never silently turns a chosen bot into the owner.
func (s *Service) actingIdentityFor(def *reliantv1.TriggerDefinition, existing *core.Trigger, src *triggers.Source) (core.ActingIdentity, error) {
	identity, chosen := triggers.ActingIdentityFromProto(def.ActingIdentity)
	if !chosen {
		if existing != nil {
			return existing.ActingIdentity.OrOwner(), nil
		}
		identity = core.ActingIdentityApp
		if s.deps.ControlPlane == nil && (src.Kind != core.TriggerKindIntegration || src.Integration != "github") {
			identity = core.ActingIdentityOwner
		}
	}
	if identity == core.ActingIdentityApp && s.deps.ControlPlane == nil {
		return "", tools.RefuseActivation("acting_identity app needs this server's control plane (the GitHub App); choose owner to write under your own name",
			svcerr.FailedPrecondition("acting_identity: app needs a Reliant deployment with a control plane, which holds the GitHub App; "+
				"configure the App, or explicitly choose owner to write under your own name"))
	}
	return identity, nil
}

// resolveActivation reads the declaration a definition activates and checks
// what the activation itself may set. The returned declaration's source is
// then validated and stored like an inline one.
func (s *Service) resolveActivation(ctx context.Context, userID string, def *reliantv1.TriggerDefinition, name string) (*triggers.Declaration, error) {
	if strings.TrimSpace(def.Workflow) == "" {
		return nil, svcerr.InvalidArgument("workflow is required: a declared trigger is activated from the workflow that declares it")
	}
	if strings.TrimSpace(def.Filter) != "" {
		return nil, svcerr.InvalidArgument("filter: an activation's filter is its declaration's; edit the workflow's triggers: block to change it")
	}
	// On the machine the activation targets: its runs execute there, so a
	// workflow only another machine provides is refused here, naming both.
	// A pool's runs execute on its agreed catalog; before any member has
	// synced there is none, and the union is all there is to validate
	// against (fires then wait until a member syncs).
	catalogMachine := def.DaemonId
	if def.MachinePoolId != "" {
		if member, _, err := machinepool.AgreedCatalog(ctx, s.deps.Repo, userID, def.MachinePoolId, def.ProjectId); err == nil {
			catalogMachine = member
		}
	}
	wf, err := launch.ResolveValidatedRunWorkflowOn(ctx, s.deps.Repo,
		launch.AutomationScope(userID, def.ProjectId, catalogMachine), def.Workflow)
	if err != nil {
		var lookup *launch.WorkflowLookupError
		if errors.As(err, &lookup) {
			return nil, triggerDBError("resolve workflow", err)
		}
		return nil, tools.RefuseActivation("workflow not found on the trigger's machine, or it does not validate", unresolvedWorkflow(def.Workflow, err))
	}
	declared := triggers.FindDeclaredTrigger(wf, name)
	if declared == nil {
		declared := triggers.DeclaredTriggerNames(wf)
		msg := fmt.Sprintf("workflow %q does not declare a trigger named %q", def.Workflow, name)
		if len(declared) > 0 {
			msg += " (it declares: " + strings.Join(declared, ", ") + ")"
		}
		return nil, tools.RefuseActivation(msg, svcerr.WithMessage(svcerr.ErrNotFound, msg))
	}
	// The activation's message overrides the declaration's prompt template;
	// with neither, an agent would start with nothing to do. A workflow that
	// runs no agent needs no prompt: its runs start from
	// triggers.DefaultSeedPrompt.
	if strings.TrimSpace(def.Message) == "" && strings.TrimSpace(declared.GetPrompt()) == "" && triggers.WorkflowRunsAgent(wf) {
		return nil, svcerr.InvalidArgument(fmt.Sprintf(
			"message is required: the declared trigger %q has no prompt and the workflow runs an agent, so the activation must write one", name))
	}
	// Activation has no previous kind to hold the declaration to.
	decl, err := triggers.DeclarationIn(wf, &core.Trigger{Workflow: def.Workflow, WorkflowTrigger: &name})
	if err != nil {
		var declErr *triggers.DeclarationError
		if errors.As(err, &declErr) {
			return nil, svcerr.FailedPrecondition(declErr.Error())
		}
		return nil, svcerr.WrapOr(err, svcerr.Internal("could not read the declared trigger"))
	}
	// A param the declaration's inputs also set would be silently replaced
	// by the event's value at every fire; refuse it rather than store a
	// setting that does nothing.
	for input := range decl.Inputs {
		if _, ok := def.Params[input]; ok {
			return nil, tools.RefuseActivation("params."+input+" is set by the declared trigger from each event; remove it", svcerr.InvalidArgument(fmt.Sprintf(
				"params.%s: the declared trigger %q sets this input from each event; remove it from params", input, name)))
		}
	}
	// Params are what filters, inputs and prompts read as activation.params,
	// so a misspelt or mistyped one would match nothing on every event, and a
	// required input nobody supplied would fail every run it launched. Every
	// write of an activation passes through here, so none can store either.
	if issues := validation.ValidateActivationParams(wf.GetInputs(), decl.Inputs, triggers.ParamsFromProto(def.Params)); len(issues) > 0 {
		names := make([]string, len(issues))
		for i, issue := range issues {
			names[i] = issue.Input
		}
		return nil, tools.RefuseActivation("params invalid: "+strings.Join(names, ", "), svcerr.InvalidArgument(fmt.Sprintf(
			"params: %s", joinIssues(issues))))
	}
	return decl, nil
}

// validateTriggerDaemon checks that the daemon exists, is the caller's, and can
// host the project.
//
// Another user's daemon is NotFound, identical to a missing one, so a caller
// cannot probe which daemon ids exist.
func (s *Service) validateTriggerDaemon(ctx context.Context, userID, projectID, daemonID string) error {
	return daemonchoice.Validate(ctx, s.deps.Repo, userID, projectID, daemonID)
}

// validateTriggerPool refuses a pool that is not the caller's. A pool with no
// member that has the project is accepted (its runs wait, saying why), so a
// pool can be wired before its machines finish cloning.
func (s *Service) validateTriggerPool(ctx context.Context, userID string, def *reliantv1.TriggerDefinition) error {
	if _, err := s.deps.Repo.GetMachinePoolForUser(ctx, userID, def.MachinePoolId); errors.Is(err, core.ErrMachinePoolNotFound) {
		return tools.RefuseActivation("machine pool not found", svcerr.NotFound("machine pool"))
	} else if err != nil {
		return triggerDBError("get machine pool", err)
	}
	return nil
}

// ensureGrants mints the trigger's automation (daemon:resume) grant for every
// machine its runs may wake: its daemon, or each current member of its pool.
// A member that joins the pool later gets one the next time the trigger is
// saved while its owner is signed in; until then placement passes over it
// only when it is asleep (a wake needs the grant).
func (s *Service) ensureGrants(ctx context.Context, t *core.Trigger) {
	if t.MachinePoolID == "" {
		s.grants.ensure(ctx, t.UserID, t.DaemonID)
		return
	}
	pool, err := s.deps.Repo.GetMachinePoolForUser(ctx, t.UserID, t.MachinePoolID)
	if err != nil {
		logging.WarnContext(ctx, "automation access not granted to pool members: could not read the pool",
			"trigger_id", t.ID, "pool_id", t.MachinePoolID, "error", err)
		return
	}
	members, err := s.deps.Repo.ListMachinePoolMembers(ctx, pool, "")
	if err != nil {
		logging.WarnContext(ctx, "automation access not granted to pool members: could not list them",
			"trigger_id", t.ID, "pool_id", t.MachinePoolID, "error", err)
		return
	}
	for _, m := range members {
		s.grants.ensure(ctx, t.UserID, m.DaemonID)
	}
}

// validateNoMachineWorkflow refuses a no-machine trigger whose workflow will not
// work without a machine, so the author learns it while writing the trigger
// rather than from a run nobody is watching. Stricter than a no-machine chat
// launch: an agent GIVEN machine tools is refused too, because for an
// unattended run "it quietly had fewer tools than you configured" is a silent
// downgrade. The trigger's params are what the run will start with, so a
// builtin agent with tools: ["tag:web"] passes. See research/DAEMONLESS_RUNS.md.
func (s *Service) validateNoMachineWorkflow(ctx context.Context, userID string, def *reliantv1.TriggerDefinition) error {
	workflowName := def.Workflow
	if workflowName == "" {
		workflowName = workflow.DefaultWorkflow
	}
	scope := launch.AutomationScope(userID, def.ProjectId, def.DaemonId)
	wf, err := launch.ResolveRunWorkflowOn(ctx, s.deps.Repo, scope, workflowName)
	if err != nil {
		var lookup *launch.WorkflowLookupError
		if errors.As(err, &lookup) {
			return triggerDBError("resolve workflow", err)
		}
		// An unresolvable workflow is reported where it is resolved for the
		// launch itself; nothing about machines can be said of it here.
		return nil
	}
	loader := func(ref string) (*reliantv1.Workflow, error) {
		return launch.ResolveRunWorkflowOn(ctx, s.deps.Repo, scope, ref)
	}
	req := v2.MachineRequirements(wf, triggers.ParamsFromProto(def.Params), loader, tools.PreflightConfig())
	if req.None() {
		return nil
	}
	return svcerr.FailedPrecondition(fmt.Sprintf(
		"this workflow needs a machine: %s. Pick a machine for this automation, or give it only tools "+
			"that run without one (for example tools: [\"tag:web\"])", req.Summary()))
}

// render builds the wire trigger, resolving the read-only projections.
//
// Neither projection is allowed to fail the response: next_fire_at comes from
// Temporal and last_event from a second query, and a trigger the caller cannot
// see at all is a worse outcome than one whose next fire time is momentarily
// absent.
func (s *Service) render(ctx context.Context, t *core.Trigger) *reliantv1.Trigger {
	// The write paths hand over the row they just stored, which has no joined
	// names; re-read it so every response carries them.
	if fresh, err := s.deps.Repo.GetTrigger(ctx, t.ID); err == nil {
		t = fresh
	}
	recent, err := s.deps.Repo.RecentTriggerFirings(ctx, t.UserID, []string{t.ID}, triggers.HealthWindow)
	if err != nil {
		logging.WarnContext(ctx, "could not resolve a trigger's recent firings", "trigger_id", t.ID, "error", err)
	}
	regs, err := s.deps.Repo.ListTriggerRegistrations(ctx, t.UserID, []string{t.ID})
	if err != nil {
		logging.WarnContext(ctx, "could not resolve a trigger's source state", "trigger_id", t.ID, "error", err)
	}
	return s.renderWithWorkflows(ctx, t, s.nextFireAt(ctx, t), recent[t.ID], regs[t.ID], triggers.LaunchWorkflows{Repo: s.deps.Repo}, s.deps.Repo)
}

// nextFireBudget bounds the wait for next fire times: one trigger's in
// render, every listed trigger's together in ListTriggers. Each is a Temporal
// schedule Describe, and a slow Temporal used to hold a list for the SDK's
// deadline once per trigger, in turn (prod, 2026-11-20: ListTriggers p95
// 38.6s, p99 246s). A var so tests can shrink it.
var nextFireBudget = 3 * time.Second

// nextFireWorkers is how many Describes ListTriggers has in flight at once.
const nextFireWorkers = 8

// nextFireAt is t's next fire time, nil when it has none or it could not be
// read within nextFireBudget: an absent next_fire_at never fails a response.
func (s *Service) nextFireAt(ctx context.Context, t *core.Trigger) *time.Time {
	if s.deps.Syncer == nil || !syncs(t.Kind) {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, nextFireBudget)
	defer cancel()
	next, err := s.deps.Syncer.NextFireAt(ctx, t.ID)
	if err != nil {
		logging.WarnContext(ctx, "could not resolve a trigger's next fire time", "trigger_id", t.ID, "error", err)
		return nil
	}
	return next
}

// nextFireTimes resolves the next fire time of every syncing trigger in
// stored, nextFireWorkers at a time, all under one nextFireBudget: the list
// waits for Temporal at most that long whatever its length. A trigger whose
// lookup failed or ran out of budget is absent from the map, and the
// failures are logged once for the list.
func (s *Service) nextFireTimes(ctx context.Context, stored []*core.Trigger) map[string]*time.Time {
	if s.deps.Syncer == nil {
		return nil
	}
	var ids []string
	for _, t := range stored {
		if syncs(t.Kind) {
			ids = append(ids, t.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	ctx, span := otel.Tracer("reliant.trigger").Start(ctx, "trigger.nextFireTimes",
		trace.WithAttributes(attribute.Int("trigger.schedules", len(ids))))
	defer span.End()
	budget, cancel := context.WithTimeout(ctx, nextFireBudget)
	defer cancel()

	var (
		mu       sync.Mutex
		out      = make(map[string]*time.Time, len(ids))
		failed   int
		firstErr error
	)
	fail := func(n int, err error) {
		mu.Lock()
		defer mu.Unlock()
		failed += n
		if firstErr == nil {
			firstErr = err
		}
	}
	// A fixed pool of workers drains the ids; the feeder hands them over only
	// while the budget lasts, so an expired budget stops the submission too
	// and the request never walks the rest of a long list.
	work := make(chan string)
	var wg sync.WaitGroup
	for range min(nextFireWorkers, len(ids)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range work {
				next, err := s.deps.Syncer.NextFireAt(budget, id)
				if err != nil {
					fail(1, err)
					continue
				}
				mu.Lock()
				out[id] = next
				mu.Unlock()
			}
		}()
	}
feed:
	for i, id := range ids {
		select {
		case work <- id:
		case <-budget.Done():
			// Past the budget, the rest are not asked at all.
			fail(len(ids)-i, budget.Err())
			break feed
		}
	}
	close(work)
	wg.Wait()
	span.SetAttributes(attribute.Int("trigger.next_fire_failures", failed))
	if failed > 0 {
		logging.WarnContext(ctx, "could not resolve some triggers' next fire times",
			"failed", failed, "triggers", len(ids), "error", firstErr)
	}
	return out
}

// renderWithWorkflows renders a trigger, resolving an activation's
// declaration through workflows so that a broken one reports BROKEN health.
// It is computed on read, from the workflow as it is now: a stored verdict
// would go stale the moment someone fixed the YAML.
func (s *Service) renderWithWorkflows(ctx context.Context, t *core.Trigger, nextFireAt *time.Time, firings []*core.TriggerEventWithRun, reg *core.TriggerRegistration, workflows triggers.WorkflowResolver, sources launch.WorkflowResolver) *reliantv1.Trigger {
	proto := s.renderFirings(ctx, t, nextFireAt, firings, reg)
	if t.WorkflowTrigger == nil {
		return proto
	}
	decl, err := triggers.ResolveDeclaration(ctx, workflows, t)
	if err != nil {
		var declErr *triggers.DeclarationError
		if errors.As(err, &declErr) {
			proto.Health = triggers.BrokenHealth(declErr)
		} else {
			// The workflow could not be read right now; that says nothing
			// about the trigger, so its firings still describe it.
			logging.WarnContext(ctx, "could not resolve a trigger's declaration", "trigger_id", t.ID, "error", err)
		}
		return proto
	}
	proto.DefinitionSource = definitionSource(ctx, sources, t)
	// Broken wins over this (a broken declaration cannot be set up); this wins
	// over everything the firings say, because nothing launches until the
	// activation is finished.
	if needsSetup := triggers.NeedsSetupHealth(decl, t); needsSetup != nil {
		proto.Health = needsSetup
	}
	return proto
}

// definitionSource says where an activation's runs read its workflow from,
// resolved exactly as they resolve it (on its machine, project first), and
// what about that choice the owner should know. Nil when it does not resolve
// right now: the declaration's health already says so. It reads through
// sources: a list passes its request resolver.
func definitionSource(ctx context.Context, sources launch.WorkflowResolver, t *core.Trigger) *reliantv1.TriggerDefinitionSource {
	desc, err := launch.DescribeRunWorkflow(ctx, sources, launch.AutomationScope(t.UserID, t.ProjectID, t.DaemonID), t.Workflow)
	if err != nil {
		return nil
	}
	return triggers.DefinitionSource(desc, t)
}

// requireSetupComplete refuses to turn on or run an activation whose params do
// not satisfy its workflow as it is now (triggers.SetupIssues). An ad hoc
// trigger, an activation whose declaration is broken, and a workflow that
// cannot be read right now are not a setup matter and pass: they are reported
// where they are (BROKEN health, the launch).
func (s *Service) requireSetupComplete(ctx context.Context, t *core.Trigger) error {
	if t.WorkflowTrigger == nil {
		return nil
	}
	decl, err := triggers.ResolveDeclaration(ctx, triggers.LaunchWorkflows{Repo: s.deps.Repo}, t)
	if err != nil {
		var broken *triggers.DeclarationError
		if errors.As(err, &broken) {
			return svcerr.FailedPrecondition(broken.Error())
		}
		return triggerDBError("resolve workflow", err)
	}
	issues := triggers.SetupIssues(decl, t)
	if len(issues) == 0 {
		return nil
	}
	return svcerr.FailedPrecondition(fmt.Sprintf(
		"%s. Finish setting up %q before turning it on or running it", triggers.SetupDetail(issues), t.Name))
}

// joinIssues is issues as one sentence list: "repos is required; team ...".
func joinIssues(issues []validation.ParamIssue) string {
	msgs := make([]string, len(issues))
	for i, issue := range issues {
		msgs[i] = issue.Message
	}
	return strings.Join(msgs, "; ")
}

// renderFirings renders a trigger from its row, its next fire time (nil when
// it has none or it could not be read), recent firings and (for a polled
// trigger) its source state.
func (s *Service) renderFirings(ctx context.Context, t *core.Trigger, nextFireAt *time.Time, firings []*core.TriggerEventWithRun, reg *core.TriggerRegistration) *reliantv1.Trigger {
	proto, err := renderTrigger(t, nextFireAt, firings, reg)
	if err != nil {
		// Params that will not round-trip through structpb. Report the trigger
		// without them rather than failing the whole call.
		logging.ErrorContext(ctx, "could not fully render a trigger", "trigger_id", t.ID, "error", err)
		return &reliantv1.Trigger{Id: t.ID, Name: t.Name, ProjectId: t.ProjectID, Enabled: t.Enabled}
	}
	switch t.Kind {
	case core.TriggerKindWebhook:
		webhookURL := triggers.WebhookURL(s.publicURL, t.ID)
		proto.WebhookUrl = &webhookURL
		proto.WebhookManaged = s.managed(t)
	case core.TriggerKindIntegration:
		// A provider whose webhook each user sets on their own resources
		// (Twilio's per-number "A message comes in" URL) needs the user to
		// know it; an operator-registered app webhook does not.
		if uc, ok := s.deps.Catalog.(userConfiguredCatalog); ok {
			if cfg, err := triggers.IntegrationConfigFor(t); err == nil && uc.UserConfiguredURL(cfg.Integration) {
				eventsURL := triggers.IntegrationEventsURL(s.publicURL, cfg.Integration)
				proto.WebhookUrl = &eventsURL
			}
		}
	}
	// Older firings predate the terminal detail write. Diagnose the FIRST
	// root workflow (the one launch created), never the chat's current root:
	// replying later must not change what failed in this activation.
	if proto.Health != nil && (proto.Health.Status == reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_FAILING || proto.Health.Status == reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_DEGRADED) {
		for _, f := range firings {
			failed := f.Event.RunStatus == core.TriggerRunFailed || f.Event.RunStatus == "" && f.Run != nil && f.Run.DisplayState == core.RunDisplayFailed
			if f.Event.Outcome != core.TriggerEventLaunched || !failed {
				continue
			}
			if f.Event.OutcomeDetail == "" && f.Event.ChatID != nil {
				// Launch allocates the chat id as its first root workflow id.
				if rows, e := s.deps.Repo.ListRunNodeExecutions(ctx, *f.Event.ChatID, rundiagnostics.RecordLimit); e == nil {
					if detail := rundiagnostics.Failure(rows); detail != "" {
						proto.Health.LastFailureDetail = detail
					}
				}
			}
			break
		}
	}
	return proto
}

// triggerDBError is a store failure as the caller reads it: a classified
// error (a store's NotFound, a deadline) keeps its code, anything else is
// "database error", with op and the store's text kept for the log.
func triggerDBError(op string, err error) error {
	return svcerr.WrapOr(fmt.Errorf("%s: %w", op, err), svcerr.Internal("database error"))
}

// triggerSyncError is a schedule-backend failure. A config the backend
// rejects is the caller's problem; anything else is the backend being
// unreachable, which is retryable — and its text stays server-side.
func triggerSyncError(err error) error {
	var cfgErr *triggers.ConfigError
	if errors.As(err, &cfgErr) {
		return svcerr.InvalidArgument(cfgErr.Error())
	}
	return svcerr.WrapOr(err, svcerr.Unavailable("the schedule backend is unavailable"))
}

// unresolvedWorkflow is a workflow ref the caller cannot run. The reason is
// the resolution's own (no such workflow, a definition that does not parse),
// about the caller's workflow; a store failure is a WorkflowLookupError,
// which the callers report as triggerDBError instead.
func unresolvedWorkflow(ref string, err error) error {
	return svcerr.WithCause(svcerr.WithMessage(svcerr.ErrNotFound,
		fmt.Sprintf("workflow %q does not resolve to a runnable workflow: %v", ref, err)), err)
}
