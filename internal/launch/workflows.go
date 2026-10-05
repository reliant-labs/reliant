// Copyright (c) 2025 Reliant Labs

// Workflow and preset resolution for the start path.
//
// These were ChatService methods. They moved here because a scheduled trigger
// fires on the worker, which does not import internal/grpc/services — so the
// code that resolves, builds and validates a run's inputs has to live in a
// package both tiers can reach. The behavior is unchanged; the one real
// difference is that the owner's userID is now an explicit argument everywhere
// (validateWorkflowInputs used to read auth.MustGetUserID off the request
// context, which cannot work on the worker).
package launch

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	cfg "github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/preset"
	"github.com/reliant-labs/reliant/internal/workflow"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"github.com/reliant-labs/reliant/internal/workflow/validation"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"

	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// ValidateWorkflowParamStructure rejects dotted param keys, which silently
// never reach the workflow: inputs are nested objects, so "agent.model" is a
// top-level key named "agent.model" rather than a path into "agent".
func ValidateWorkflowParamStructure(params map[string]*structpb.Value) error {
	for key, value := range params {
		if strings.Contains(key, ".") {
			return fmt.Errorf("workflow_params contains dotted key %q; use nested objects instead (for example {\"agent\": {\"model\": ...}})", key)
		}
		if err := validateWorkflowParamKeyPath(key, value); err != nil {
			return err
		}
	}
	return nil
}

func validateWorkflowParamKeyPath(keyPath string, value *structpb.Value) error {
	if value == nil {
		return nil
	}
	structValue, ok := value.AsInterface().(map[string]interface{})
	if !ok {
		return nil
	}
	for nestedKey, nestedValue := range structValue {
		nestedPath := nestedKey
		if keyPath != "" {
			nestedPath = keyPath + "." + nestedKey
		}
		if strings.Contains(nestedKey, ".") {
			return fmt.Errorf("workflow_params contains dotted key %q; use nested objects instead (for example {\"agent\": {\"model\": ...}})", nestedPath)
		}
		nestedProtoValue, err := structpb.NewValue(nestedValue)
		if err != nil {
			continue
		}
		if err := validateWorkflowParamKeyPath(nestedPath, nestedProtoValue); err != nil {
			return err
		}
	}
	return nil
}

// ResolveDefaultWorkflow resolves the workflow to use when not explicitly provided.
// Priority: request > user setting > system default (builtin://agent)
func (l *Launcher) ResolveDefaultWorkflow(ctx context.Context, userID string, reqWorkflow string) string {
	// Priority 1: Explicit request
	if reqWorkflow != "" {
		return reqWorkflow
	}

	// Priority 2: User default setting
	if setting, err := l.repo.GetSetting(ctx, userID, nil, "config.default_workflow"); err == nil && setting.Value != "" {
		return setting.Value
	}

	// Priority 3: System default
	return workflow.DefaultWorkflow
}

// BuildWorkflowInputs constructs the initial workflow input data by applying presets,
// user params, and workflow schema defaults. Order: presets (base), then user params (override),
// then apply workflow defaults for any missing inputs (e.g. model default from YAML).
func (l *Launcher) BuildWorkflowInputs(
	ctx context.Context,
	userID string,
	projectPath string,
	projectID string,
	workflowName string,
	selectedPresets map[string]string,
	userParams map[string]*structpb.Value,
) map[string]interface{} {
	initialData := make(map[string]interface{})

	// Apply selected presets first (preset params are the base, user params override)
	userParamKeys := make([]string, 0, len(userParams))
	for k := range userParams {
		userParamKeys = append(userParamKeys, k)
	}
	logging.Info("[buildWorkflowInputs] Starting", "workflow", workflowName, "selectedPresets", selectedPresets, "userParamKeys", userParamKeys)
	if len(selectedPresets) > 0 {
		loadPreset := l.createDBPresetLoaderFull(ctx, userID, projectID)
		for groupName, presetName := range selectedPresets {
			if presetName == "" {
				continue
			}
			p, err := loadPreset(presetName)
			if err != nil {
				logging.Warn("Failed to load preset", "preset", presetName, "group", groupName, "error", err)
				continue
			}
			initialData = preset.ApplyToInputs(p, initialData, groupName)
			logging.Info("[buildWorkflowInputs] Applied preset", "preset", presetName, "group", groupName, "tools_after_preset", initialData["tools"])
		}
	} else {
		logging.Info("[buildWorkflowInputs] No presets selected")
	}

	// User-provided params override preset values.
	// Params must use nested structure (e.g., {"agent": {"model": "..."}}).
	// Flat keys like "agent.model" are rejected by validation.
	for key, value := range userParams {
		// An engine-injected input is the engine's to set: validation skips
		// these keys precisely because the engine writes them, so taking one
		// from a request would let a client claim a session daemon, a trigger
		// event, or that nobody is watching its run.
		if workflow.RuntimeInjectedInputs[key] {
			logging.Warn("[buildWorkflowInputs] Ignoring a client param that names an engine-injected input", "key", key)
			continue
		}
		v := value.AsInterface()

		if key == "tools" {
			logging.Info("[buildWorkflowInputs] User param tools override", "value", v, "type", fmt.Sprintf("%T", v))
		}

		// If value is a map, merge it with existing group map.
		if mapVal, ok := v.(map[string]interface{}); ok {
			if existing, ok := initialData[key].(map[string]interface{}); ok {
				for nestedKey, nestedValue := range mapVal {
					existing[nestedKey] = nestedValue
				}
			} else {
				initialData[key] = mapVal
			}
		} else {
			initialData[key] = v
		}
	}

	logging.Info("[buildWorkflowInputs] After user params", "tools", initialData["tools"])

	// Apply workflow schema defaults (e.g. model: { id: gpt-4o }) so validation and execution
	// see the same inputs the workflow defines. Required inputs without defaults remain absent
	// so ValidateWorkflowInputs can reject missing required params before a chat starts.
	protoInputs := l.LoadWorkflowInputsForBuild(ctx, userID, workflowName, projectID)
	if len(protoInputs) > 0 {
		initialData = v2.ApplyDefaults(initialData, protoInputs)
	}

	// Normalize model inputs: convert any remaining string model values to {id: string} objects.
	// This is the ONE place where string-to-object conversion happens — at the gRPC ingestion boundary.
	// Everything downstream rejects strings.
	if len(protoInputs) > 0 {
		NormalizeModelInputs(initialData, protoInputs)
	}

	logging.Info("[buildWorkflowInputs] Final resolved", "tools", initialData["tools"])

	// Add project_path to workflow inputs so spawned workflows can load presets
	// This flows through: workflow.go -> StepExecutor -> executeSpawnInline -> InlineWorkflowExecutor
	if projectPath != "" {
		initialData["project_path"] = projectPath
	}

	return initialData
}

// BuildStateUpdateForActiveWorkflow builds the inputs to signal onto a chat's
// already-running workflow, merging the chat's stored preset selection with
// whatever the request supplied.
func (l *Launcher) BuildStateUpdateForActiveWorkflow(
	ctx context.Context,
	userID string,
	chat *db.Chat,
	workflowName string,
	requestPresets map[string]string,
	requestParams map[string]*structpb.Value,
) map[string]interface{} {
	if chat == nil {
		return map[string]interface{}{}
	}

	effectivePresets := make(map[string]string)
	for key, value := range chat.SelectedPresets {
		if value != "" {
			effectivePresets[key] = value
		}
	}
	for key, value := range requestPresets {
		if value != "" {
			effectivePresets[key] = value
		}
	}

	projectPath := l.GetEffectiveWorkingPath(ctx, chat)
	return l.BuildWorkflowInputs(ctx, userID, projectPath, chat.ProjectID, workflowName, effectivePresets, requestParams)
}

// LoadWorkflowInputsForBuild loads workflow input schemas as proto types for ApplyDefaults.
// Uses the same resolution order as validation so builtin workflows also get defaults
// and boundary normalization for nested model selectors.
func (l *Launcher) LoadWorkflowInputsForBuild(ctx context.Context, userID, workflowName, projectID string) map[string]*reliantv1.Input {
	if strings.HasPrefix(workflowName, "builtin://") {
		name := strings.TrimPrefix(workflowName, "builtin://")
		data, err := builtin.BuiltinWorkflowsFS.ReadFile(name + ".yaml")
		if err != nil {
			logging.Warn("Could not load builtin workflow inputs for build", "workflow", workflowName, "error", err)
			return nil
		}
		wf, parseErr := wfyaml.ParseWorkflow(data)
		if parseErr != nil {
			logging.Warn("Could not parse builtin workflow inputs for build", "workflow", workflowName, "error", parseErr)
			return nil
		}
		return wf.GetInputs()
	}

	// Try DB draft first
	slug := strings.ToLower(strings.ReplaceAll(workflowName, " ", "-"))
	draft, err := l.repo.GetWorkflowDraftBySlug(ctx, userID, slug)
	if err == nil && draft != nil {
		wf, parseErr := wfyaml.ParseWorkflow([]byte(draft.Definition))
		if parseErr == nil {
			return wf.GetInputs()
		}
	}

	// Try stored project config (synced by daemon)
	projectWf, _, lookupErr := LoadProjectWorkflowBySlugFromDB(l.repo, ctx, projectID, slug)
	if lookupErr == nil && projectWf != nil {
		return projectWf.GetInputs()
	}
	return nil
}

// ValidateWorkflowInputs loads a workflow and validates the provided inputs against its schema.
// Returns validation errors if inputs are missing or invalid, nil if valid.
// This enables early validation (400 error) before starting the workflow.
//
// userID is the OWNER of the run, passed explicitly. It used to be read from
// the request context, which a scheduled fire on the worker does not have.
func (l *Launcher) ValidateWorkflowInputs(ctx context.Context, userID, workflowName, projectID string, inputs map[string]interface{}) []error {
	// Load workflow definition to get input schemas (builtin/project config first, then user draft from DB)
	wf, err := l.LoadWorkflowForValidation(ctx, workflowName, projectID)
	if err != nil {
		// User workflows often exist only in DB; load by slug so we validate the same definition that will run
		slug := strings.ToLower(strings.ReplaceAll(workflowName, " ", "-"))
		draft, dbErr := l.repo.GetWorkflowDraftBySlug(ctx, userID, slug)
		if dbErr != nil || draft == nil {
			logging.Warn("Could not load workflow for input validation", "workflow", workflowName, "error", err)
			return nil
		}
		wf, err = v2.ParseWorkflowProtoBytesNoValidation([]byte(draft.Definition))
		if err != nil {
			logging.Warn("Could not parse draft for input validation", "workflow", workflowName, "error", err)
			return nil
		}
	}

	// Filter out runtime-injected inputs before validation
	// These are internal values that shouldn't be validated against the workflow schema
	filteredInputs := make(map[string]interface{})
	for key, value := range inputs {
		if !workflow.RuntimeInjectedInputs[key] {
			filteredInputs[key] = value
		}
	}

	// Apply explicit defaults before validation so optional schema defaults are included.
	// Required inputs without defaults intentionally remain absent and are rejected below.
	protoInputs := l.LoadWorkflowInputsForBuild(ctx, userID, workflowName, projectID)
	inputsWithDefaults := v2.ApplyDefaults(filteredInputs, protoInputs)

	// Filter again after ApplyDefaults to ensure runtime-injected inputs aren't reintroduced
	// (ApplyDefaults should not add them, but this is a safety measure)
	finalInputs := make(map[string]interface{})
	for key, value := range inputsWithDefaults {
		if !workflow.RuntimeInjectedInputs[key] {
			finalInputs[key] = value
		}
	}

	// Normalize model inputs: convert any remaining string model values to {id: string} objects
	// before validation. This mirrors the normalization in BuildWorkflowInputs.
	if protoInputs != nil {
		NormalizeModelInputs(filteredInputs, protoInputs)
		NormalizeModelInputs(finalInputs, protoInputs)
	}

	// Validate inputs against schema
	var errs []error
	if result := validation.ValidateInputs(wf, finalInputs); result.HasErrors() {
		errs = append(errs, result.AsError())
	}

	// Validate model availability - check that any model inputs can be resolved
	// with the user's configured API keys. If the selected model isn't available,
	// reject it with a clear error instead of silently substituting.
	modelSelectors := ExtractModelSelectors(finalInputs, wf.GetInputs(), "")
	for inputPath, selector := range modelSelectors {
		if err := drivers.ValidateModelSelector(ctx, userID, selector); err != nil {
			errs = append(errs, fmt.Errorf("input '%s': %w", inputPath, err))
		}
	}

	return errs
}

// LoadWorkflowForValidation loads a workflow by name for input validation.
// Searches builtin workflows first, then stored project workflows from DB.
func (l *Launcher) LoadWorkflowForValidation(ctx context.Context, workflowName, projectID string) (*reliantv1.Workflow, error) {
	return loadBuiltinOrProjectWorkflow(ctx, l.repo, workflowName, projectID)
}

func loadBuiltinOrProjectWorkflow(ctx context.Context, repo ProjectWorkflowLoader, workflowName, projectID string) (*reliantv1.Workflow, error) {
	// Handle builtin:// protocol
	if strings.HasPrefix(workflowName, "builtin://") {
		name := strings.TrimPrefix(workflowName, "builtin://")
		data, err := builtin.BuiltinWorkflowsFS.ReadFile(name + ".yaml")
		if err != nil {
			return nil, fmt.Errorf("builtin workflow not found: %s", workflowName)
		}
		return v2.ParseWorkflowProtoBytesNoValidation(data)
	}

	// Load from stored project config (synced by daemon)
	// Normalize slug the same way as generateWorkflowSlug in load_workflow.go.
	slug := NormalizeWorkflowSlug(workflowName)
	projectWf, _, err := LoadProjectWorkflowBySlugFromDB(repo, ctx, projectID, slug)
	if err == nil && projectWf != nil {
		return projectWf, nil
	}

	return nil, fmt.Errorf("workflow not found: %s", workflowName)
}

// WorkflowLookupError marks a failure to READ a workflow, as opposed to a
// workflow that is absent or malformed. The first is a store problem and
// retryable; only the second can never launch.
type WorkflowLookupError struct{ Err error }

func (e *WorkflowLookupError) Error() string { return e.Err.Error() }
func (e *WorkflowLookupError) Unwrap() error { return e.Err }

// draftRootFor names the workflow whose saved draft may run without being
// marked complete: the root of a builder test run, and nothing else. A test run
// runs what the builder just saved, which is the point of Run; every other
// launch, and every workflow nested under the root, still needs a complete one.
func draftRootFor(ev Event, workflowName string) string {
	if ev.Kind == core.TriggerEventKindBuilderTest {
		return workflowName
	}
	return ""
}

// ResolveRunWorkflow loads the workflow a run of workflowName by userID in
// projectID executes, exactly as run start resolves it: builtin:// from the
// embedded catalog, else the owner's COMPLETE workflow of that slug, else the
// project's synced workflow. A draft that is not marked complete is a
// *db.WorkflowDraftNotRunnableError; a store failure is a
// *WorkflowLookupError (retryable); anything else means the workflow does
// not resolve.
//
// It is what a trigger reads its declaration from, so an activation fires
// against the same definition the run it starts will execute.
func ResolveRunWorkflow(ctx context.Context, repo WorkflowResolver, userID, workflowName, projectID string) (*reliantv1.Workflow, error) {
	return resolveRunWorkflow(ctx, repo, userID, workflowName, projectID, "")
}

func (l *Launcher) loadCreateChatWorkflowForValidation(ctx context.Context, userID, workflowName, projectID, draftRoot string) (*reliantv1.Workflow, error) {
	return resolveRunWorkflow(ctx, l.repo, userID, workflowName, projectID, draftRoot)
}

func resolveRunWorkflow(ctx context.Context, repo WorkflowResolver, userID, workflowName, projectID, draftRoot string) (*reliantv1.Workflow, error) {
	if strings.HasPrefix(workflowName, "builtin://") {
		return loadBuiltinOrProjectWorkflow(ctx, repo, workflowName, projectID)
	}

	slug := NormalizeWorkflowSlug(workflowName)
	var draft *db.WorkflowDraft
	var err error
	if draftRoot != "" && slug == NormalizeWorkflowSlug(draftRoot) {
		draft, err = repo.GetWorkflowDraftBySlug(ctx, userID, slug)
		if err == nil && (draft == nil || draft.IsHidden) {
			return nil, fmt.Errorf("workflow '%s' not found", workflowName)
		}
	} else {
		draft, err = repo.GetUsableWorkflowBySlug(ctx, userID, slug)
	}
	if err != nil {
		var notRunnable *db.WorkflowDraftNotRunnableError
		if errors.As(err, &notRunnable) {
			// A verdict about the draft (it is not marked complete), not a
			// store failure: final, and the message names the remedy.
			return nil, err
		}
		return nil, &WorkflowLookupError{Err: fmt.Errorf("failed to look up workflow '%s': %w", workflowName, err)}
	}
	if draft != nil {
		wf, parseErr := wfyaml.ParseWorkflow([]byte(draft.Definition))
		if parseErr != nil {
			return nil, fmt.Errorf("failed to parse workflow '%s': %w", workflowName, parseErr)
		}
		return wf, nil
	}

	wf, err := loadBuiltinOrProjectWorkflow(ctx, repo, workflowName, projectID)
	if err != nil {
		return nil, fmt.Errorf("workflow '%s' not found", workflowName)
	}
	return wf, nil
}

func (l *Launcher) createChatWorkflowLoader(ctx context.Context, userID, projectID, draftRoot string) validation.WorkflowLoader {
	return func(workflowName string) (*reliantv1.Workflow, error) {
		return l.loadCreateChatWorkflowForValidation(ctx, userID, workflowName, projectID, draftRoot)
	}
}

// ValidateCreateChatWorkflowTree statically analyses the whole workflow tree
// before a chat is created, so a graph that cannot run fails the request rather
// than leaving an orphaned chat behind.
//
// A workflow that cannot be LOADED is an invalid argument; one that loads but
// does not validate is a failed precondition.
func (l *Launcher) ValidateCreateChatWorkflowTree(ctx context.Context, userID, workflowName, projectID string) error {
	return l.validateWorkflowTree(ctx, userID, workflowName, projectID, "")
}

// validateWorkflowTree is ValidateCreateChatWorkflowTree with the one workflow
// (draftRoot) that may be a draft; see draftRootFor.
func (l *Launcher) validateWorkflowTree(ctx context.Context, userID, workflowName, projectID, draftRoot string) error {
	wf, err := l.loadCreateChatWorkflowForValidation(ctx, userID, workflowName, projectID, draftRoot)
	if err != nil {
		var lookupErr *WorkflowLookupError
		if errors.As(err, &lookupErr) {
			// The store failed; the workflow may well exist. Retryable.
			return &InternalError{Reason: "failed to look up workflow", Err: err}
		}
		return &ValidationError{Kind: ValidationInvalidArgument, Reason: err.Error()}
	}

	validationOpts := &validation.ValidationOptions{
		WorkflowLoader:       l.createChatWorkflowLoader(ctx, userID, projectID, draftRoot),
		CanonicalWorkflowRef: workflowName,
	}
	if projectID != "" {
		validationOpts.PresetLoader = l.createDBPresetLoader(ctx, projectID)
	}

	result := validation.StaticAnalysisWithOptions(wf, validationOpts)
	if result != nil && result.HasErrors() {
		return &ValidationError{
			Kind:   ValidationFailedPrecondition,
			Reason: fmt.Sprintf("workflow tree validation failed for '%s': %s", workflowName, result.Error()),
		}
	}

	return nil
}

// validateNoMachine refuses a no-machine launch of a workflow with a node that
// cannot run without one. Only HARD reasons refuse here: an attended chat
// whose agent was given tag:coding:default still starts, and is simply offered
// the subset that runs without a machine. The trigger write path is stricter
// (see TriggerService), because an unattended run's silent downgrade has no one
// watching it.
func (l *Launcher) validateNoMachine(ctx context.Context, userID, workflowName, projectID, draftRoot string) error {
	wf, err := l.loadCreateChatWorkflowForValidation(ctx, userID, workflowName, projectID, draftRoot)
	if err != nil {
		// validateWorkflowTree already reported a load failure; nothing new.
		return nil
	}
	loader := l.createChatWorkflowLoader(ctx, userID, projectID, draftRoot)
	req := v2.MachineRequirements(wf, nil, v2.WorkflowRefLoader(loader), tools.PreflightConfig())
	if len(req.Hard) > 0 {
		return &ValidationError{
			Kind:   ValidationFailedPrecondition,
			Reason: fmt.Sprintf("workflow '%s' needs a machine (%s), so it cannot run with no machine", workflowName, strings.Join(req.Hard, "; ")),
		}
	}
	return nil
}

// createDBPresetLoader returns a PresetLoader that reads presets from project/builtin sources.
// Validation does not currently have user context, so DB-backed user presets are resolved at runtime only.
func (l *Launcher) createDBPresetLoader(ctx context.Context, projectID string) validation.PresetLoader {
	return func(presetName string) (map[string]interface{}, error) {
		// Pass empty userID so validation behavior stays scoped to project/builtin presets.
		p, err := l.LoadPresetFromDB(ctx, "", projectID, presetName)
		if err != nil {
			return nil, err
		}
		return p.Params, nil
	}
}

// createDBPresetLoaderFull returns a function that loads full preset objects from all runtime sources.
// Used by BuildWorkflowInputs where the full preset is needed for ApplyToInputs.
func (l *Launcher) createDBPresetLoaderFull(ctx context.Context, userID, projectID string) func(name string) (*preset.Preset, error) {
	return func(name string) (*preset.Preset, error) {
		return l.LoadPresetFromDB(ctx, userID, projectID, name)
	}
}

// LoadPresetFromDB loads a preset by name. Priority: user presets > stored project presets > builtins.
func (l *Launcher) LoadPresetFromDB(ctx context.Context, userID, projectID, name string) (*preset.Preset, error) {
	slug := strings.ToLower(strings.ReplaceAll(name, " ", "-"))

	if l.repo != nil {
		// Try project-scoped user preset first.
		if projectID != "" {
			dbPreset, err := l.repo.GetPresetBySlugAndProject(ctx, userID, slug, projectID)
			if err == nil && dbPreset != nil {
				return dbPresetToRuntimePreset(dbPreset), nil
			}
		}

		// Fall back to global user preset.
		dbPreset, err := l.repo.GetPresetBySlug(ctx, userID, slug)
		if err == nil && dbPreset != nil {
			return dbPresetToRuntimePreset(dbPreset), nil
		}

		// Try stored project presets from daemon config sync.
		if projectID != "" {
			record, err := l.repo.GetProjectConfigRecord(ctx, projectID)
			if err == nil {
				presets, err := cfg.ParseStoredPresets(record.ProjectPresetsJSON)
				if err == nil {
					sp := cfg.FindStoredPresetByName(presets, name)
					if sp != nil {
						p, err := preset.ParsePreset([]byte(sp.YAMLContent), name)
						if err == nil {
							p.Source = "project"
							return p, nil
						}
					}
				}
			}
		}
	}

	// Fall back to builtin presets.
	builtinPath := "presets/" + name + ".yaml"
	data, err := builtin.BuiltinPresetsFS.ReadFile(builtinPath)
	if err == nil {
		p, err := preset.ParsePreset(data, name)
		if err == nil {
			p.Source = "builtin"
			return p, nil
		}
	}

	return nil, fmt.Errorf("preset not found: %s", name)
}

// ProjectWorkflowLoader is the one repository read LoadProjectWorkflowBySlugFromDB
// needs. Declared at the consumer so both ChatService and WorkflowService can
// call the function with their own *db.Repo.
type ProjectWorkflowLoader interface {
	GetProjectConfigRecord(ctx context.Context, projectID string) (*db.ProjectConfigRecord, error)
}

// LoadProjectWorkflowBySlugFromDB loads a project workflow by slug from the stored config record.
// Returns the workflow, its YAML content, and error. Returns nil, "", nil if not found.
func LoadProjectWorkflowBySlugFromDB(repo ProjectWorkflowLoader, ctx context.Context, projectID string, slug string) (*reliantv1.Workflow, string, error) {
	if projectID == "" || slug == "" {
		return nil, "", nil
	}

	record, err := repo.GetProjectConfigRecord(ctx, projectID)
	if err != nil {
		return nil, "", nil
	}

	workflows, err := cfg.ParseStoredWorkflows(record.ProjectWorkflowsJSON)
	if err != nil {
		return nil, "", fmt.Errorf("failed to parse stored workflows: %w", err)
	}

	sw := cfg.FindStoredWorkflowBySlug(workflows, slug)
	if sw == nil {
		return nil, "", nil // Not found
	}

	protoWf, err := wfyaml.ParseWorkflow([]byte(sw.YAMLContent))
	if err != nil {
		return nil, "", fmt.Errorf("failed to parse project workflow %s: %w", slug, err)
	}

	return protoWf, sw.YAMLContent, nil
}
