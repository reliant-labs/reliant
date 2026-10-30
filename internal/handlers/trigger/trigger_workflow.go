// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/reliant-labs/forge/pkg/activity"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/triggers"
)

// A workflow declares WHEN it runs; the user turns that on. EnableWorkflow and
// DisableWorkflow are that act for a whole workflow at once: one set of
// choices (project, machine, connected accounts, params) applied to every
// trigger it declares. EnableWorkflow is also how those choices change later:
// an existing activation is brought to the request, so the workflow's setup is
// edited as one unit and its triggers cannot drift apart. Per-trigger
// activation (CreateTrigger with a workflow_trigger source) stays for the
// single-trigger case, and every path goes through the same validation.

// enableStep is what phase 2 writes for one declared trigger.
type enableStep struct {
	name     string
	existing *core.Trigger // the user's activation of it in this project, if any
	// def is the definition to write: a new activation's, or (with existing)
	// the changed settings of an existing one. Nil with existing means its
	// settings already match and it only needs resuming.
	def *reliantv1.TriggerDefinition
	// warnings are what reachability checking found worth telling the person
	// once the enable has succeeded.
	warnings []string
}

// EnableWorkflow enables every trigger a workflow declares, or none of them,
// creating the activations that are missing and bringing the existing ones to
// the request's settings.
func (s *Service) EnableWorkflow(
	ctx context.Context,
	req *connect.Request[reliantv1.EnableWorkflowRequest],
) (*connect.Response[reliantv1.EnableWorkflowResponse], error) {
	userID := auth.MustGetUserID(ctx)
	msg := req.Msg
	if strings.TrimSpace(msg.Workflow) == "" {
		return nil, svcerr.InvalidArgument("workflow is required")
	}
	if msg.ProjectId == "" {
		return nil, svcerr.InvalidArgument("project_id is required")
	}

	// Enabling activates the workflow's triggers: resolve it as they will.
	wf, err := launch.ResolveValidatedRunWorkflowOn(ctx, s.deps.Repo, launch.AutomationScope(userID, msg.ProjectId, msg.GetDaemonId()), msg.Workflow)
	if err != nil {
		var lookup *launch.WorkflowLookupError
		if errors.As(err, &lookup) {
			return nil, triggerDBError("resolve workflow", err)
		}
		return nil, unresolvedWorkflow(msg.Workflow, err)
	}
	declared := wf.GetTriggers()
	if len(declared) == 0 {
		return nil, svcerr.FailedPrecondition(fmt.Sprintf("workflow %q declares no triggers, so there is nothing to enable", msg.Workflow))
	}

	mine, err := s.deps.Repo.ListTriggers(ctx, core.TriggerFilters{UserID: userID, ProjectID: &msg.ProjectId})
	if err != nil {
		return nil, triggerDBError("list triggers", err)
	}
	activations := map[string]*core.Trigger{}
	taken := map[string]bool{}
	for _, t := range mine {
		taken[t.Name] = true
		if t.WorkflowTrigger != nil && sameWorkflowRef(t.Workflow, msg.Workflow) {
			activations[*t.WorkflowTrigger] = t
		}
	}

	overrides := map[string]*reliantv1.WorkflowTriggerOverride{}
	for _, o := range msg.Overrides {
		overrides[o.Trigger] = o
	}
	for name := range overrides {
		if triggers.FindDeclaredTrigger(wf, name) == nil {
			return nil, svcerr.InvalidArgument(fmt.Sprintf("override names %q, which workflow %q does not declare (it declares: %s)",
				name, msg.Workflow, strings.Join(triggers.DeclaredTriggerNames(wf), ", ")))
		}
	}

	prefix := s.activationPrefix(msg, declared, activations, overrides, taken)

	// Phase 1: validate everything and write nothing.
	results := make([]*reliantv1.WorkflowTriggerResult, len(declared))
	steps := make([]*enableStep, len(declared))
	failed := false
	for i, wt := range declared {
		name := wt.GetName()
		results[i] = &reliantv1.WorkflowTriggerResult{Trigger: name}
		if existing := activations[name]; existing != nil {
			step, err := s.existingActivationStep(ctx, userID, wf, wt, msg, existing, overrides[name], taken)
			if err != nil {
				results[i].Outcome = reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_FAILED
				results[i].Message = connectMessage(err)
				failed = true
				continue
			}
			if step == nil {
				results[i].Outcome = reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_ALREADY_ENABLED
				results[i].Activation = s.render(ctx, existing)
				continue
			}
			if step.def != nil && step.def.Name != existing.Name {
				delete(taken, existing.Name)
				taken[step.def.Name] = true
			}
			steps[i] = step
			continue
		}

		var warnings []string
		def, err := s.workflowActivationDefinition(wt, msg, prefix+" · "+name, overrides[name])
		if err == nil {
			if taken[def.Name] {
				// Only a name the caller chose for this trigger can still
				// collide: the default prefix was picked to be free.
				err = svcerr.AlreadyExists(fmt.Sprintf("you already have an automation named %q in this project; pick another name", def.Name))
			} else {
				var row *core.Trigger
				if row, err = s.triggerFromDefinition(ctx, userID, def, nil); err == nil {
					row.Enabled = true
					if err = s.refreshTriggerAccess(ctx, row); err == nil {
						warnings, err = s.checkReachability(ctx, userID, wf, wt, msg, row)
						warnings = append(warnings, s.ownerIdentityWarnings(ctx, wt, row)...)
					}
				}
			}
		}
		if err != nil {
			results[i].Outcome = reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_FAILED
			results[i].Message = connectMessage(err)
			failed = true
			continue
		}
		taken[def.Name] = true
		steps[i] = &enableStep{name: name, def: def, warnings: warnings}
	}
	if failed {
		for i, r := range results {
			if steps[i] != nil {
				r.Outcome = reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_NOT_ENABLED
				r.Message = "Not enabled because another trigger of this workflow could not be."
			}
		}
		// All or nothing, and it was nothing: no "enable" event.
		activity.Skip(ctx)
		return connect.NewResponse(&reliantv1.EnableWorkflowResponse{Results: results}), nil
	}

	// Phase 2: write, undoing everything on the first failure.
	var undo []func(context.Context) error
	for i, step := range steps {
		if step == nil {
			continue
		}
		if err := s.applyEnableStep(ctx, step, results[i], &undo); err != nil {
			results[i].Outcome = reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_FAILED
			results[i].Message = connectMessage(err)
			s.undoEnable(ctx, steps, results, undo)
			activity.Skip(ctx) // every write was undone: no "enable" event
			return connect.NewResponse(&reliantv1.EnableWorkflowResponse{Results: results}), nil
		}
	}
	for i, step := range steps {
		if step != nil {
			results[i].Warnings = step.warnings
		}
	}
	return connect.NewResponse(&reliantv1.EnableWorkflowResponse{Enabled: true, Results: results}), nil
}

// existingActivationStep decides what enabling does to an activation the user
// already has: nothing (nil, when it is on with exactly the request's
// settings), a resume, or an update to the request's settings. The update is
// validated here, in phase 1, exactly as a new activation is, so a setting
// that is no longer valid refuses the whole enable before anything is written.
func (s *Service) existingActivationStep(
	ctx context.Context,
	userID string,
	wf *reliantv1.Workflow,
	wt *reliantv1.WorkflowTrigger,
	msg *reliantv1.EnableWorkflowRequest,
	existing *core.Trigger,
	override *reliantv1.WorkflowTriggerOverride,
	taken map[string]bool,
) (*enableStep, error) {
	name := wt.GetName()
	def, err := s.workflowActivationDefinition(wt, msg, existing.Name, override)
	if err != nil {
		return nil, err
	}
	// A connection the request does not name stays the one this activation
	// listens through, rather than falling back to the default.
	if def.ConnectionId == nil && existing.ConnectionID != nil {
		id := *existing.ConnectionID
		def.ConnectionId = &id
	}
	if def.Name != existing.Name && taken[def.Name] {
		return nil, svcerr.AlreadyExists(fmt.Sprintf("you already have an automation named %q in this project; pick another name", def.Name))
	}
	row, err := s.triggerFromDefinition(ctx, userID, def, existing)
	if err != nil {
		return nil, err
	}
	row.Enabled = true
	if err := s.refreshTriggerAccess(ctx, row); err != nil {
		return nil, err
	}
	if sameActivationSettings(row, existing) && existing.Enabled {
		return nil, nil
	}
	warnings, err := s.checkReachability(ctx, userID, wf, wt, msg, row)
	if err != nil {
		return nil, err
	}
	warnings = append(warnings, s.ownerIdentityWarnings(ctx, wt, row)...)
	if sameActivationSettings(row, existing) {
		return &enableStep{name: name, existing: existing, warnings: warnings}, nil
	}
	return &enableStep{name: name, existing: existing, def: def, warnings: warnings}, nil
}

// sameActivationSettings reports whether two rows of one activation hold the
// same user choices. The source projection (kind, config, filter) is the
// declaration's, re-read at every fire, so it is not a choice and not compared.
func sameActivationSettings(a, b *core.Trigger) bool {
	return a.Name == b.Name &&
		a.ProjectID == b.ProjectID &&
		ptrValue(a.WorktreeID) == ptrValue(b.WorktreeID) &&
		a.Message == b.Message &&
		a.DaemonID == b.DaemonID &&
		a.NoMachine == b.NoMachine &&
		a.NotifyOnComplete == b.NotifyOnComplete &&
		a.ActingIdentity.OrOwner() == b.ActingIdentity.OrOwner() &&
		ptrValue(a.ConnectionID) == ptrValue(b.ConnectionID) &&
		sameJSON(a.Presets, b.Presets) &&
		sameJSON(a.Params, b.Params)
}

func ptrValue(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// sameJSON compares two maps as they are stored: params decoded from jsonb and
// params decoded from the wire differ in Go types (json.Number, float64) while
// meaning the same value. An empty map and a nil one are the same setting.
func sameJSON[M ~map[string]V, V any](a, b M) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(ja, jb)
}

func (s *Service) applyEnableStep(ctx context.Context, step *enableStep, result *reliantv1.WorkflowTriggerResult, undo *[]func(context.Context) error) error {
	if step.existing != nil && step.def != nil {
		return s.applyActivationUpdate(ctx, step, result, undo)
	}
	if step.existing != nil {
		resp, err := s.SetTriggerEnabled(ctx, connect.NewRequest(&reliantv1.SetTriggerEnabledRequest{Id: step.existing.ID, Enabled: true}))
		if err != nil {
			// SetTriggerEnabled writes the row before it converges, so a
			// failure can leave it on; put it back.
			_, _ = s.SetTriggerEnabled(ctx, connect.NewRequest(&reliantv1.SetTriggerEnabledRequest{Id: step.existing.ID, Enabled: false}))
			return err
		}
		id := step.existing.ID
		*undo = append(*undo, func(ctx context.Context) error {
			_, err := s.SetTriggerEnabled(ctx, connect.NewRequest(&reliantv1.SetTriggerEnabledRequest{Id: id, Enabled: false}))
			return err
		})
		result.Outcome = reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_RESUMED
		result.Activation = resp.Msg.GetTrigger()
		return nil
	}
	resp, err := s.CreateTrigger(ctx, connect.NewRequest(&reliantv1.CreateTriggerRequest{Trigger: step.def}))
	if err != nil {
		return err
	}
	id := resp.Msg.GetTrigger().GetId()
	*undo = append(*undo, func(ctx context.Context) error {
		_, err := s.DeleteTrigger(ctx, connect.NewRequest(&reliantv1.DeleteTriggerRequest{Id: id}))
		return err
	})
	result.Outcome = reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_ENABLED
	result.Activation = resp.Msg.GetTrigger()
	result.Webhook = resp.Msg.GetWebhook()
	return nil
}

// applyActivationUpdate writes an existing activation's new settings and turns
// it on. Its undo writes back the row exactly as it was, not through
// UpdateTrigger: the old settings were valid when chosen and are the user's,
// and a restore that re-validated could refuse them and leave the new ones.
func (s *Service) applyActivationUpdate(ctx context.Context, step *enableStep, result *reliantv1.WorkflowTriggerResult, undo *[]func(context.Context) error) error {
	prev := *step.existing
	enabled := true
	def := step.def
	def.Enabled = &enabled
	resp, err := s.UpdateTrigger(ctx, connect.NewRequest(&reliantv1.UpdateTriggerRequest{Id: prev.ID, Trigger: def}))
	if err != nil {
		// UpdateTrigger writes the row before it converges, so a failure
		// can leave the new settings stored; put the old ones back.
		_ = s.restoreActivation(ctx, &prev)
		return err
	}
	*undo = append(*undo, func(ctx context.Context) error { return s.restoreActivation(ctx, &prev) })
	result.Outcome = reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_UPDATED
	if !prev.Enabled {
		result.Outcome = reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_RESUMED
	}
	result.Activation = resp.Msg.GetTrigger()
	return nil
}

// restoreActivation writes a row back as it was before an update, reconverges
// its schedule, and moves the machine grant back with it.
func (s *Service) restoreActivation(ctx context.Context, prev *core.Trigger) error {
	current, err := s.deps.Repo.GetTrigger(ctx, prev.ID)
	if err != nil {
		return err
	}
	if err := s.deps.Repo.UpdateTrigger(ctx, prev); err != nil {
		return err
	}
	if err := s.sync(ctx, prev.Kind, prev.ID); err != nil {
		return err
	}
	if prev.Enabled {
		s.grants.ensure(ctx, prev.UserID, prev.DaemonID)
	}
	if current.DaemonID != prev.DaemonID || !prev.Enabled {
		s.grants.releaseIfUnused(ctx, prev.UserID, current.DaemonID)
	}
	return nil
}

// undoEnable reverses what phase 2 wrote, newest first, and records on each
// result what became of it. An undo that fails leaves that trigger running and
// says so: that is the one outcome the user must act on.
func (s *Service) undoEnable(ctx context.Context, steps []*enableStep, results []*reliantv1.WorkflowTriggerResult, undo []func(context.Context) error) {
	written := make([]int, 0, len(undo))
	for i, r := range results {
		switch r.Outcome {
		case reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_ENABLED,
			reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_RESUMED,
			reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_UPDATED:
			written = append(written, i)
		}
	}
	for n := len(undo) - 1; n >= 0; n-- {
		r := results[written[n]]
		updated := r.Outcome == reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_UPDATED
		if err := undo[n](ctx); err != nil {
			logging.ErrorContext(ctx, "could not undo a workflow enable; the trigger is still running",
				"trigger", r.Trigger, "error", err)
			r.Outcome = reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_LEFT_ENABLED
			if updated {
				r.Message = "Saving failed and this trigger's previous settings could not be put back: " + connectMessage(err) + " Check its settings."
			} else {
				r.Message = "Enabling failed and this trigger could not be switched back off: " + connectMessage(err) + " Disable it from Automations."
			}
			continue
		}
		r.Outcome = reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_NOT_ENABLED
		r.Message = "Undone because another trigger of this workflow could not be enabled."
		if updated {
			r.Message = "Undone because another trigger of this workflow could not be saved; it keeps its previous settings."
		}
		r.Activation, r.Webhook = nil, nil
	}
	for i, step := range steps {
		r := results[i]
		if step != nil && r.Outcome == reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_UNSPECIFIED {
			r.Outcome = reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_NOT_ENABLED
			r.Message = "Not enabled because another trigger of this workflow could not be."
		}
	}
}

// activationPrefix is the "<prefix> · <trigger>" prefix the new activations are
// named by: the request's (or the workflow ref), made free in this project.
// A name taken by an automation of ANOTHER workflow — a user workflow of the
// same title as a built-in, or a leftover of a workflow that was deleted —
// never fails the enable. The prefix is qualified by this workflow's source
// ("(built-in)" / "(yours)") first, then numbered, so one enable names all of
// its activations consistently. A name the caller chose for one trigger
// (override.Name) is theirs and is not rewritten.
func (s *Service) activationPrefix(
	msg *reliantv1.EnableWorkflowRequest,
	declared []*reliantv1.WorkflowTrigger,
	activations map[string]*core.Trigger,
	overrides map[string]*reliantv1.WorkflowTriggerOverride,
	taken map[string]bool,
) string {
	base := strings.TrimSpace(msg.NamePrefix)
	if base == "" {
		base = msg.Workflow
	}
	var created []string
	for _, wt := range declared {
		if activations[wt.GetName()] != nil {
			continue
		}
		if o := overrides[wt.GetName()]; o != nil && o.Name != nil {
			continue
		}
		created = append(created, wt.GetName())
	}
	free := func(prefix string) bool {
		for _, name := range created {
			if taken[prefix+" · "+name] {
				return false
			}
		}
		return true
	}
	if free(base) {
		return base
	}
	qualifier := "yours"
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(msg.Workflow)), "builtin://") {
		qualifier = "built-in"
	}
	if candidate := base + " (" + qualifier + ")"; free(candidate) {
		return candidate
	}
	for n := 2; ; n++ {
		if candidate := fmt.Sprintf("%s (%d)", base, n); free(candidate) {
			return candidate
		}
	}
}

// workflowActivationDefinition is the definition of one declared trigger's
// activation, named name: the request's shared choices, then the trigger's
// override.
func (s *Service) workflowActivationDefinition(
	wt *reliantv1.WorkflowTrigger,
	msg *reliantv1.EnableWorkflowRequest,
	name string,
	override *reliantv1.WorkflowTriggerOverride,
) (*reliantv1.TriggerDefinition, error) {
	enabled := true
	def := &reliantv1.TriggerDefinition{
		Name:             name,
		ActingIdentity:   msg.ActingIdentity,
		ProjectId:        msg.ProjectId,
		WorktreeId:       msg.WorktreeId,
		Enabled:          &enabled,
		Workflow:         msg.Workflow,
		Presets:          msg.Presets,
		Message:          msg.Message,
		DaemonId:         msg.DaemonId,
		NoMachine:        msg.NoMachine,
		NotifyOnComplete: msg.NotifyOnComplete,
		Source:           &reliantv1.TriggerDefinition_WorkflowTrigger{WorkflowTrigger: wt.GetName()},
	}

	// The shared prompt is for triggers whose declaration has none: a message
	// on an activation overrides the declaration's prompt, and one box must
	// not rewrite what each trigger's author wrote.
	if strings.TrimSpace(wt.GetPrompt()) != "" {
		def.Message = ""
	}

	params := map[string]*structpb.Value{}
	for k, v := range msg.Params {
		params[k] = v
	}
	if integ := wt.GetIntegration(); integ != nil {
		if id := msg.ConnectionIds[integ.GetIntegration()]; id != "" {
			def.ConnectionId = &id
		}
	}
	if override != nil {
		if override.Name != nil {
			def.Name = *override.Name
		}
		if override.NoMachine != nil || override.DaemonId != nil {
			def.NoMachine = override.GetNoMachine()
			def.DaemonId = override.GetDaemonId()
		}
		if override.ConnectionId != nil {
			def.ConnectionId = override.ConnectionId
		}
		if override.Message != nil {
			def.Message = *override.Message
		}
		for k, v := range override.Params {
			params[k] = v
		}
	}
	// What the declaration maps from each event wins over a shared param of
	// that name, and an activation may not set it: drop it for this trigger
	// rather than refuse the whole enable over a setting that does nothing.
	for input := range wt.GetInputs() {
		delete(params, input)
	}
	if len(params) > 0 {
		def.Params = params
	}
	return def, nil
}

// DisableWorkflow pauses every activation of a workflow.
func (s *Service) DisableWorkflow(
	ctx context.Context,
	req *connect.Request[reliantv1.DisableWorkflowRequest],
) (*connect.Response[reliantv1.DisableWorkflowResponse], error) {
	userID := auth.MustGetUserID(ctx)
	if strings.TrimSpace(req.Msg.Workflow) == "" {
		return nil, svcerr.InvalidArgument("workflow is required")
	}
	mine, err := s.deps.Repo.ListTriggers(ctx, core.TriggerFilters{UserID: userID, ProjectID: req.Msg.ProjectId})
	if err != nil {
		return nil, triggerDBError("list triggers", err)
	}
	resp := &reliantv1.DisableWorkflowResponse{}
	for _, t := range mine {
		if t.WorkflowTrigger == nil || !sameWorkflowRef(t.Workflow, req.Msg.Workflow) {
			continue
		}
		if !t.Enabled {
			resp.Paused = append(resp.Paused, s.render(ctx, t))
			continue
		}
		paused, err := s.SetTriggerEnabled(ctx, connect.NewRequest(&reliantv1.SetTriggerEnabledRequest{Id: t.ID, Enabled: false}))
		if err != nil {
			resp.Failed = append(resp.Failed, &reliantv1.TriggerFailure{TriggerId: t.ID, Name: t.Name, Message: connectMessage(err)})
			continue
		}
		resp.Paused = append(resp.Paused, paused.Msg.GetTrigger())
	}
	return connect.NewResponse(resp), nil
}

// sameWorkflowRef reports whether two refs name one workflow: a builtin by its
// ref, anything else by the slug every surface resolves refs by.
func sameWorkflowRef(a, b string) bool {
	key := func(ref string) string {
		ref = strings.TrimSpace(ref)
		if strings.HasPrefix(ref, "builtin://") {
			return strings.ToLower(ref)
		}
		return launch.NormalizeWorkflowSlug(ref)
	}
	ka, kb := key(a), key(b)
	return ka != "" && ka == kb
}

// connectMessage is the text of a handler's error as a person reads it: what
// the svcerr interceptor would send, so an unclassified error's own text stays
// withheld here too.
func connectMessage(err error) string {
	return svcerr.ToConnect(err).Message()
}

// RetireDeletedTriggers stops what outlives the rows of triggers already
// deleted: their Temporal Schedules and the automation access nothing else
// holds any more. It is best effort, because the rows are the truth: a
// schedule left behind finds no row to fire and SyncAll collects it.
func (s *Service) RetireDeletedTriggers(ctx context.Context, deleted []*core.Trigger) {
	type holder struct{ user, daemon string }
	released := map[holder]bool{}
	for _, t := range deleted {
		if s.deps.Syncer != nil && syncs(t.Kind) {
			if err := s.deps.Syncer.Delete(ctx, t.ID); err != nil {
				logging.WarnContext(ctx, "could not delete the schedule of a deleted trigger; the next sync collects it",
					"trigger_id", t.ID, "error", err)
			}
		}
		if s.managed(t) {
			s.retireAlerting(ctx, t.ID)
		}
		h := holder{t.UserID, t.DaemonID}
		if t.DaemonID != "" && !released[h] {
			released[h] = true
			s.grants.releaseIfUnused(ctx, t.UserID, t.DaemonID)
		}
	}
}
