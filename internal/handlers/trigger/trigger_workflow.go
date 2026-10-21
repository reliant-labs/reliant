// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

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
// trigger it declares. Per-trigger activation (CreateTrigger with a
// workflow_trigger source) stays for the single-trigger case and for
// overrides, and both go through the same validation.

type enableStep struct {
	name     string
	existing *core.Trigger // the user's activation of it in this project, if any
	def      *reliantv1.TriggerDefinition
}

// EnableWorkflow enables every trigger a workflow declares, or none of them.
func (s *Service) EnableWorkflow(
	ctx context.Context,
	req *connect.Request[reliantv1.EnableWorkflowRequest],
) (*connect.Response[reliantv1.EnableWorkflowResponse], error) {
	userID := auth.MustGetUserID(ctx)
	msg := req.Msg
	if strings.TrimSpace(msg.Workflow) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("workflow is required"))
	}
	if msg.ProjectId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("project_id is required"))
	}

	wf, err := launch.ResolveRunWorkflow(ctx, s.deps.Repo, userID, msg.Workflow, msg.ProjectId)
	if err != nil {
		var lookup *launch.WorkflowLookupError
		if errors.As(err, &lookup) {
			return nil, triggerDBError("resolve workflow", err)
		}
		return nil, connect.NewError(connect.CodeNotFound,
			fmt.Errorf("workflow %q does not resolve to a runnable workflow: %w", msg.Workflow, err))
	}
	declared := wf.GetTriggers()
	if len(declared) == 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("workflow %q declares no triggers, so there is nothing to enable", msg.Workflow))
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
			return nil, connect.NewError(connect.CodeInvalidArgument,
				fmt.Errorf("override names %q, which workflow %q does not declare (it declares: %s)",
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
			if existing.Enabled {
				results[i].Outcome = reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_ALREADY_ENABLED
				results[i].Activation = s.render(ctx, existing)
				continue
			}
			resuming := *existing
			resuming.Enabled = true
			if err := s.refreshTriggerAccess(ctx, &resuming); err != nil {
				results[i].Outcome = reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_FAILED
				results[i].Message = connectMessage(err)
				failed = true
				continue
			}
			steps[i] = &enableStep{name: name, existing: existing}
			continue
		}

		def, err := s.workflowActivationDefinition(wt, msg, prefix, overrides[name])
		if err == nil {
			if taken[def.Name] {
				// Only a name the caller chose for this trigger can still
				// collide: the default prefix was picked to be free.
				err = connect.NewError(connect.CodeAlreadyExists,
					fmt.Errorf("you already have an automation named %q in this project; pick another name", def.Name))
			} else {
				var row *core.Trigger
				if row, err = s.triggerFromDefinition(ctx, userID, def, nil); err == nil {
					row.Enabled = true
					err = s.refreshTriggerAccess(ctx, row)
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
		steps[i] = &enableStep{name: name, def: def}
	}
	if failed {
		for i, r := range results {
			if steps[i] != nil {
				r.Outcome = reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_NOT_ENABLED
				r.Message = "Not enabled because another trigger of this workflow could not be."
			}
		}
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
			return connect.NewResponse(&reliantv1.EnableWorkflowResponse{Results: results}), nil
		}
	}
	return connect.NewResponse(&reliantv1.EnableWorkflowResponse{Enabled: true, Results: results}), nil
}

func (s *Service) applyEnableStep(ctx context.Context, step *enableStep, result *reliantv1.WorkflowTriggerResult, undo *[]func(context.Context) error) error {
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

// undoEnable reverses what phase 2 wrote, newest first, and records on each
// result what became of it. An undo that fails leaves that trigger running and
// says so: that is the one outcome the user must act on.
func (s *Service) undoEnable(ctx context.Context, steps []*enableStep, results []*reliantv1.WorkflowTriggerResult, undo []func(context.Context) error) {
	written := make([]int, 0, len(undo))
	for i, r := range results {
		switch r.Outcome {
		case reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_ENABLED,
			reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_RESUMED:
			written = append(written, i)
		}
	}
	for n := len(undo) - 1; n >= 0; n-- {
		r := results[written[n]]
		if err := undo[n](ctx); err != nil {
			logging.ErrorContext(ctx, "could not undo a workflow enable; the trigger is still running",
				"trigger", r.Trigger, "error", err)
			r.Outcome = reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_LEFT_ENABLED
			r.Message = "Enabling failed and this trigger could not be switched back off: " + connectMessage(err) + " Disable it from Automations."
			continue
		}
		r.Outcome = reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_NOT_ENABLED
		r.Message = "Undone because another trigger of this workflow could not be enabled."
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
// activation: the request's shared choices, then the trigger's override.
func (s *Service) workflowActivationDefinition(
	wt *reliantv1.WorkflowTrigger,
	msg *reliantv1.EnableWorkflowRequest,
	prefix string,
	override *reliantv1.WorkflowTriggerOverride,
) (*reliantv1.TriggerDefinition, error) {
	enabled := true
	def := &reliantv1.TriggerDefinition{
		Name:             prefix + " · " + wt.GetName(),
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
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("workflow is required"))
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

// connectMessage is the text of a handler's error as a person reads it.
func connectMessage(err error) string {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce.Message()
	}
	return err.Error()
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
		h := holder{t.UserID, t.DaemonID}
		if t.DaemonID != "" && !released[h] {
			released[h] = true
			s.grants.releaseIfUnused(ctx, t.UserID, t.DaemonID)
		}
	}
}
