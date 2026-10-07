// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	cfg "github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/preset"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
	"github.com/reliant-labs/reliant/internal/workflow/validation"
	"github.com/reliant-labs/reliant/internal/workflow/workflowref"
	"github.com/reliant-labs/reliant/internal/workflow/workflowsource"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"google.golang.org/protobuf/encoding/protojson"
)

// LoadWorkflowInput contains the input for loading a workflow
type LoadWorkflowInput struct {
	ChatID       string `json:"chat_id" reliant:"-"` // Chat ID to resolve project path
	WorkflowName string `json:"workflow_name"`       // Workflow name or path
}

// LoadWorkflowOutput contains both the raw YAML and validated workflow
// The raw YAML is needed for runtime template resolution with actual inputs
type LoadWorkflowOutput struct {
	YAML         []byte `json:"yaml"`          // Raw YAML bytes for template resolution
	WorkflowJSON []byte `json:"workflow_json"` // Placeholder-resolved workflow JSON for validation
	HasTemplates bool   `json:"has_templates"` // Whether workflow contains unresolved templates
}

// LoadWorkflowActivity loads a workflow definition from the filesystem
type LoadWorkflowActivity struct {
	repo db.Repository
}

// NewLoadWorkflowActivity creates a new LoadWorkflow activity
func NewLoadWorkflowActivity(repo db.Repository) *LoadWorkflowActivity {
	return &LoadWorkflowActivity{repo: repo}
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
	Workflow     *reliantv1.Workflow
	HasTemplates bool
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
		validateOpts.PresetLoader = a.createPresetLoader(ctx, wfCtx.projectID)
	}

	validationResult := validation.StaticAnalysisWithOptions(loaded.Workflow, validateOpts)
	if validationResult != nil && validationResult.HasErrors() {
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
		YAML:         yamlData,
		WorkflowJSON: jsonData,
		HasTemplates: loaded.HasTemplates,
	}, nil
}

// workflowContext holds the resolved context needed for workflow loading
type workflowContext struct {
	userID       string
	projectID    string
	projectPath  string
	worktreePath string
	// draftRoot is the one workflow that may be loaded from an unfinished
	// draft: the root of a builder test run. Empty for every other chat.
	draftRoot string
}

// sourceOptions is whose workflows, in which project, a ref resolves against.
func (c *workflowContext) sourceOptions() workflowsource.Options {
	if c == nil {
		return workflowsource.Options{}
	}
	return workflowsource.Options{UserID: c.userID, ProjectID: c.projectID, DraftRoot: c.draftRoot}
}

// resolveWorkflowContext resolves user, project, and path from a chat ID
func (a *LoadWorkflowActivity) resolveWorkflowContext(ctx context.Context, chatID string) (*workflowContext, error) {
	if chatID == "" {
		return nil, fmt.Errorf("chat ID is empty")
	}

	if a.repo == nil {
		return nil, fmt.Errorf("repository not available")
	}

	chat, err := a.repo.GetChat(ctx, chatID)
	if err != nil {
		return nil, fmt.Errorf("failed to get chat: %w", err)
	}

	wfCtx := &workflowContext{
		userID:    chat.UserID,
		projectID: chat.ProjectID,
	}
	if chat.LaunchKind == string(core.TriggerEventKindBuilderTest) && chat.WorkflowName != nil {
		wfCtx.draftRoot = *chat.WorkflowName
	}

	if chat.ProjectID == "" {
		return wfCtx, nil
	}

	project, err := a.repo.GetProject(ctx, chat.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("failed to get project: %w", err)
	}

	if project != nil {
		wfCtx.projectPath = project.Path
	}

	if chat.WorktreeID != nil && *chat.WorktreeID != "" {
		worktree, wtErr := a.repo.GetWorktree(ctx, *chat.WorktreeID)
		if wtErr == nil && worktree != nil {
			wfCtx.worktreePath = worktree.Path
		}
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
	return yamlData, &loadedWorkflow{Workflow: resolved.Workflow, HasTemplates: hasTemplates}, nil
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
		return a.loadWorkflowByName(ctx, workflowName, wfCtx)
	}
}

// createPresetLoader creates a PresetLoader function for validation.
// Loads from stored project config (synced by daemon), then falls back to builtins.
func (a *LoadWorkflowActivity) createPresetLoader(ctx context.Context, projectID string) validation.PresetLoader {
	return func(presetName string) (map[string]interface{}, error) {
		// Try stored project presets from daemon config sync
		if projectID != "" && a.repo != nil {
			record, err := a.repo.GetProjectConfigRecord(ctx, projectID)
			if err == nil {
				presets, err := cfg.ParseStoredPresets(record.ProjectPresetsJSON)
				if err == nil {
					sp := cfg.FindStoredPresetByName(presets, presetName)
					if sp != nil {
						p, err := preset.ParsePreset([]byte(sp.YAMLContent), presetName)
						if err == nil {
							return p.Params, nil
						}
					}
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
