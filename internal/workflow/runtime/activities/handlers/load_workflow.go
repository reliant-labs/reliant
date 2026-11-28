// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/chatviews"

	"google.golang.org/protobuf/encoding/protojson"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	cfg "github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/preset"
	"github.com/reliant-labs/reliant/internal/telemetry"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
	"github.com/reliant-labs/reliant/internal/workflow/validation"
	"github.com/reliant-labs/reliant/internal/workflow/workflowref"
	"github.com/reliant-labs/reliant/internal/workflow/workflowsource"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// LoadWorkflowInput is defined in activities/types: this activity runs as a
// LOCAL activity (runtime.executeBookkeeping), whose input must already be
// the registered parameter type, so the workflow names it too.
type LoadWorkflowInput = types.LoadWorkflowInput

// LoadWorkflowOutput contains both the raw YAML and validated workflow
// The raw YAML is needed for runtime template resolution with actual inputs
type LoadWorkflowOutput struct {
	YAML         []byte `json:"yaml"`          // Raw YAML bytes for template resolution
	WorkflowJSON []byte `json:"workflow_json"` // Placeholder-resolved workflow JSON for validation
	HasTemplates bool   `json:"has_templates"` // Whether workflow contains unresolved templates
	// CatalogPin is the project catalog this load resolved against: the
	// input's, or the one it pinned. The runtime carries it to every later
	// load of the run.
	CatalogPin string `json:"catalog_pin,omitempty"`
	// ProjectDefinition attests to the original bytes from which this load
	// parsed the workflow, not the re-marshaled YAML used by the evaluator.
	ProjectDefinition *types.ProjectDefinition `json:"project_definition,omitempty"`
	// OrgID is the org of the run's chat, as the worker stamps it on every
	// span of the run (the run-scope interceptor's chat-owner lookup): the
	// workflow names its own span with it (runtime.nameRunOrg), which it
	// cannot look up itself. Telemetry only; empty when the chat has none,
	// or from a worker that predates it.
	OrgID string `json:"org_id,omitempty"`
}

// LoadWorkflowActivity loads a workflow definition from the filesystem
type LoadWorkflowActivity struct {
	repo LoadWorkflowStore
	rows ProjectWorktreeReader
}

// NewLoadWorkflowActivity creates a new LoadWorkflow activity
func NewLoadWorkflowActivity(repo LoadWorkflowStore, rows ProjectWorktreeReader) *LoadWorkflowActivity {
	return &LoadWorkflowActivity{repo: repo, rows: rows}
}

// Name returns the activity name
func (a *LoadWorkflowActivity) Name() string {
	return "ActivityLoadWorkflow"
}

// DisplayName returns human-readable name for UI
func (a *LoadWorkflowActivity) DisplayName() string {
	return "Load Workflow"
}

// Description returns what the activity does
func (a *LoadWorkflowActivity) Description() string {
	return "Load a workflow definition from the database or filesystem"
}

// Category returns the activity category for UI grouping
func (a *LoadWorkflowActivity) Category() schema.ActivityCategory {
	return schema.CategoryWorkflowManagement
}

// loadedWorkflow holds parsed workflow data with template metadata.
type loadedWorkflow struct {
	Workflow          *reliantv1.Workflow
	HasTemplates      bool
	ProjectDefinition *types.ProjectDefinition
}

// Execute loads a workflow definition from DB (or embedded builtins) and returns both raw YAML and validated JSON.
func (a *LoadWorkflowActivity) Execute(ctx context.Context, input LoadWorkflowInput) (*LoadWorkflowOutput, error) {
	if input.WorkflowName == "" {
		return nil, fmt.Errorf("workflow name cannot be empty - check that the workflow name is being set correctly when creating the workflow")
	}

	wfCtx, err := a.resolveWorkflowContext(ctx, input.ChatID)
	if err != nil {
		wfCtx = &workflowContext{}
	}
	if err := a.pinCatalog(ctx, wfCtx, input.CatalogPin); err != nil {
		return nil, err
	}

	yamlData, loaded, err := a.loadWorkflowByNameWithRaw(ctx, input.WorkflowName, wfCtx)
	if err != nil {
		return nil, err
	}

	// Validate workflow tree
	wfLoader := a.createWorkflowLoader(ctx, wfCtx)

	validateOpts := &validation.ValidationOptions{
		WorkflowLoader:       wfLoader,
		CanonicalWorkflowRef: input.WorkflowName,
	}
	if wfCtx.projectID != "" {
		validateOpts.PresetLoader = a.createPresetLoader(ctx, wfCtx)
	}

	validationResult := validation.StaticAnalysisWithOptions(loaded.Workflow, validateOpts)
	if validationResult != nil && validationResult.HasErrors() {
		// A child or preset the loaders could not READ is not one the tree
		// lacks: retry the load rather than fail the run as invalid.
		if unreadable := wfCtx.readFailure(ctx); unreadable != nil {
			return nil, fmt.Errorf("workflow %s: %w", input.WorkflowName, unreadable)
		}
		return nil, fmt.Errorf("workflow tree validation failed for %s:\n%s", input.WorkflowName, validationResult.Error())
	}

	// Convert proto to JSON
	jsonData, err := protojson.Marshal(loaded.Workflow)
	if err != nil {
		// Fall back to standard JSON if protojson fails
		jsonData, err = json.Marshal(loaded.Workflow)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal workflow to JSON: %w", err)
		}
	}

	return &LoadWorkflowOutput{
		YAML:              yamlData,
		WorkflowJSON:      jsonData,
		HasTemplates:      loaded.HasTemplates,
		CatalogPin:        wfCtx.catalogPin,
		ProjectDefinition: loaded.ProjectDefinition,
		OrgID:             telemetry.RunScopeFromContext(ctx).OrgID,
	}, nil
}

// pinCatalog sets the project catalog this load resolves against: pin, the
// run's, when it has one; else the catalog as it is now on the run's
// machine, stored so the rest of the run reads the same one.
func (a *LoadWorkflowActivity) pinCatalog(ctx context.Context, wfCtx *workflowContext, pin string) error {
	if pin != "" || wfCtx.projectID == "" || a.repo == nil {
		wfCtx.catalogPin = pin
		return nil
	}
	pinned, err := a.repo.PinProjectCatalog(ctx, db.CatalogPinScope{
		ProjectID: wfCtx.projectID, DaemonID: wfCtx.daemonID, UserID: wfCtx.userID,
		DraftRoot: workflowref.ProjectSlug(wfCtx.draftRoot),
	})
	if err != nil {
		return fmt.Errorf("pin the project's workflow catalog for this run: %w", err)
	}
	wfCtx.catalogPin = pinned
	return nil
}

// workflowContext holds the resolved context needed for workflow loading
type workflowContext struct {
	userID    string
	projectID string
	// draftRoot is the one workflow that may be loaded from an unfinished
	// draft: the root of a builder test run. Empty for every other chat.
	draftRoot string
	// daemonID is the machine the run executes on (the chat's pin). Its
	// workflows and presets are that machine's own synced catalog, the one
	// a trigger was admitted against, not the union of every machine's.
	daemonID string
	// projectFirst is an automation's precedence (launch.AutomationPrecedence):
	// a run its trigger started resolves the project's workflow before the
	// owner's own of the same name, as its activation was admitted.
	projectFirst bool
	// catalogPin is the project catalog the run started with
	// (workflowsource.Options.CatalogPin).
	catalogPin string
	// unreadable is the first child workflow or preset tree validation
	// could not read (a store error, not an absent one): Execute retries
	// such a load instead of reporting the tree invalid.
	unreadable error
}

// recordUnreadable keeps err when it is a failure to read the catalog
// (workflowsource.StoreError), or the activity's context ended under it.
func (c *workflowContext) recordUnreadable(ctx context.Context, err error) {
	if c == nil || c.unreadable != nil || err == nil {
		return
	}
	var storeErr *workflowsource.StoreError
	if errors.As(err, &storeErr) || ctx.Err() != nil {
		c.unreadable = err
	}
}

// readFailure is why this load could not read what tree validation needed,
// as a retryable error, or nil when every read answered. A context that
// ended counts even when no loader saw it: a read it cut short may have
// been reported as an absence.
func (c *workflowContext) readFailure(ctx context.Context) error {
	switch {
	case c != nil && c.unreadable != nil:
		return &catalogUnreadableError{cause: c.unreadable}
	case ctx.Err() != nil:
		return &catalogUnreadableError{cause: context.Cause(ctx)}
	}
	return nil
}

// catalogUnreadableErrType is the Temporal error type of a workflow load
// that could not read a child workflow or preset its tree names.
const catalogUnreadableErrType = "WorkflowCatalogUnreadable"

// catalogUnreadableError is a workflow load whose tree validation could not
// READ a child workflow or preset (the store failed, or the worker shut down
// mid-read). It says nothing about whether the tree is valid, so it must
// retry, never fail the run as "preset not found". It is a selfSchedulingRetry
// of the runtime's (on the step's own backoff), which also keeps its text,
// whatever the store's error said, away from the terminal-pattern match.
type catalogUnreadableError struct{ cause error }

func (e *catalogUnreadableError) Error() string {
	return fmt.Sprintf("could not read the workflows and presets it uses (temporary, retrying): %v", e.cause)
}

func (e *catalogUnreadableError) Unwrap() error { return e.cause }

// RetryAfter retries on the step's own retry policy (a zero delay).
func (e *catalogUnreadableError) RetryAfter() (time.Duration, bool) { return 0, true }

func (e *catalogUnreadableError) TemporalErrorType() string { return catalogUnreadableErrType }

// sourceOptions is whose workflows, in which project, a ref resolves against.
func (c *workflowContext) sourceOptions() workflowsource.Options {
	if c == nil {
		return workflowsource.Options{}
	}
	return workflowsource.Options{UserID: c.userID, ProjectID: c.projectID, DaemonID: c.daemonID, ProjectFirst: c.projectFirst, DraftRoot: c.draftRoot, CatalogPin: c.catalogPin}
}

// resolveWorkflowContext resolves whose workflows, in which project and on
// which machine, a chat's refs resolve against. It resolves no directory: the
// run's working directory is the project's checkout on the machine that runs
// each call, resolved at dispatch.
func (a *LoadWorkflowActivity) resolveWorkflowContext(ctx context.Context, chatID string) (*workflowContext, error) {
	if chatID == "" {
		return nil, fmt.Errorf("chat ID is empty")
	}

	if a.repo == nil {
		return nil, fmt.Errorf("repository not available")
	}

	chat, err := chatviews.Of(a.repo).GetChat(ctx, chatID)
	if err != nil {
		return nil, fmt.Errorf("failed to get chat: %w", err)
	}

	wfCtx := &workflowContext{
		userID:    chat.UserID,
		projectID: chat.ProjectID,
		// The chat's origin, not its trigger id: deleting the trigger clears
		// the id, and must not switch a running automation to account-first.
		projectFirst: chat.Origin == core.ChatOriginAutomation,
	}
	if chat.LaunchKind == string(core.TriggerEventKindBuilderTest) && chat.WorkflowName != nil {
		wfCtx.draftRoot = *chat.WorkflowName
	}
	if !chat.NoMachine && chat.ActiveDaemonID != nil {
		wfCtx.daemonID = *chat.ActiveDaemonID
	}

	return wfCtx, nil
}

// loadWorkflowByName loads a workflow by name from either builtin FS or DB.
func (a *LoadWorkflowActivity) loadWorkflowByName(ctx context.Context, workflowName string, wfCtx *workflowContext) (*reliantv1.Workflow, error) {
	_, loaded, err := a.loadWorkflowByNameWithRaw(ctx, workflowName, wfCtx)
	if err != nil {
		return nil, err
	}
	return loaded.Workflow, nil
}

// loadWorkflowByNameWithRaw loads a workflow and returns both raw YAML and
// parsed proto. It resolves the name by the one rule every surface shares
// (workflowref, fed by workflowsource): builtin:// from the embedded
// builtins; project:// or a bare name from the user's own workflows, then the
// project's synced .reliant/workflows, by name:.
func (a *LoadWorkflowActivity) loadWorkflowByNameWithRaw(ctx context.Context, workflowName string, wfCtx *workflowContext) ([]byte, *loadedWorkflow, error) {
	var store workflowsource.Store
	if a.repo != nil {
		store = a.repo
	}
	resolved, err := workflowsource.Resolve(ctx, store, wfCtx.sourceOptions(), workflowName)
	if err != nil {
		return nil, nil, fmt.Errorf("workflow %q: %w", workflowName, err)
	}

	hasTemplates := bytes.Contains(resolved.YAML, []byte("{{"))
	if resolved.Source == workflowref.SourceBuiltin {
		return resolved.YAML, &loadedWorkflow{Workflow: resolved.Workflow, HasTemplates: hasTemplates}, nil
	}

	// Re-marshal to YAML for template resolution
	yamlData, err := wfyaml.MarshalWorkflow(resolved.Workflow)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal workflow to YAML: %w", err)
	}
	loaded := &loadedWorkflow{Workflow: resolved.Workflow, HasTemplates: hasTemplates}
	if resolved.Source == workflowref.SourceProject {
		loaded.ProjectDefinition = &types.ProjectDefinition{
			Path: workflowref.ProjectRelative(resolved.Path), ContentHash: cfg.DefinitionHash(resolved.YAML),
		}
	}
	return yamlData, loaded, nil
}

// loadBuiltinWorkflowWithRaw loads a builtin workflow and returns raw YAML + parsed proto.
func loadBuiltinWorkflowWithRaw(name string) ([]byte, *loadedWorkflow, error) {
	if !strings.HasPrefix(name, workflowref.BuiltinScheme) {
		name = workflowref.BuiltinScheme + name
	}
	return (&LoadWorkflowActivity{}).loadWorkflowByNameWithRaw(context.Background(), name, nil)
}

// createWorkflowLoader creates a validation.WorkflowLoader for tree validation.
func (a *LoadWorkflowActivity) createWorkflowLoader(ctx context.Context, wfCtx *workflowContext) validation.WorkflowLoader {
	return func(workflowName string) (*reliantv1.Workflow, error) {
		wf, err := a.loadWorkflowByName(ctx, workflowName, wfCtx)
		wfCtx.recordUnreadable(ctx, err)
		return wf, err
	}
}

// createPresetLoader creates a PresetLoader function for validation.
// Loads from the presets the run's machine synced (the union when it has
// never synced the project; see workflowsource.ProjectPresets), then falls
// back to builtins.
func (a *LoadWorkflowActivity) createPresetLoader(ctx context.Context, wfCtx *workflowContext) validation.PresetLoader {
	return func(presetName string) (map[string]interface{}, error) {
		// One column, not the whole config row (see GetProjectPresetsJSON).
		if wfCtx.projectID != "" && a.repo != nil {
			presets, err := projectPresets(ctx, a.repo, wfCtx.projectID, wfCtx.daemonID, wfCtx.catalogPin)
			// A preset the store could not read is not a missing one:
			// report the read, never "preset not found", and let Execute
			// retry the load. Any other error (a pin that is gone, a
			// catalog that does not parse) falls back to builtins as before.
			var storeErr *workflowsource.StoreError
			if err != nil && (errors.As(err, &storeErr) || ctx.Err() != nil) {
				err = fmt.Errorf("read preset %s of project %s: %w", presetName, wfCtx.projectID, err)
				wfCtx.recordUnreadable(ctx, err)
				return nil, err
			}
			if err == nil {
				if sp := cfg.FindStoredPresetByName(presets, presetName); sp != nil {
					p, err := preset.ParsePreset([]byte(sp.YAMLContent), presetName)
					if err != nil {
						// The run's machine's preset is broken: fail closed
						// rather than run a builtin of the same name.
						return nil, fmt.Errorf("preset %s (%s): %w", presetName, sp.RelativePath, err)
					}
					return p.Params, nil
				}
			}
		}

		// Fall back to builtin presets
		builtinPath := "presets/" + presetName + ".yaml"
		data, err := builtin.BuiltinPresetsFS.ReadFile(builtinPath)
		if err == nil {
			p, err := preset.ParsePreset(data, presetName)
			if err == nil {
				return p.Params, nil
			}
		}

		return nil, fmt.Errorf("preset not found: %s", presetName)
	}
}

// loadBuiltinWorkflow is a convenience wrapper for tests.
func loadBuiltinWorkflow(name string) (*reliantv1.Workflow, error) {
	_, loaded, err := loadBuiltinWorkflowWithRaw("builtin://" + name)
	if err != nil {
		return nil, err
	}
	return loaded.Workflow, nil
}

// projectPresets reads a run's project presets: from its pinned catalog when
// it has one, else its machine's (workflowsource.ProjectPresets).
func projectPresets(ctx context.Context, repo interface {
	GetProjectPresetsJSON(ctx context.Context, projectID string) (*string, error)
}, projectID, daemonID, pin string) ([]cfg.StoredPreset, error) {
	if pin != "" {
		return workflowsource.PinnedPresets(ctx, repo, pin)
	}
	return workflowsource.ProjectPresets(ctx, repo, projectID, daemonID)
}
