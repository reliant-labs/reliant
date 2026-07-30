// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/reliant-labs/reliant/internal/auth"
	cfg "github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/preset"
	"github.com/reliant-labs/reliant/internal/workflow"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"github.com/reliant-labs/reliant/internal/workflow/validation"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"

	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

func validateWorkflowParamStructure(params map[string]*structpb.Value) error {
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

// resolveDefaultWorkflow resolves the workflow to use when not explicitly provided.
// Priority: request > user setting > system default (builtin://agent)
func (s *ChatService) resolveDefaultWorkflow(ctx context.Context, userID string, reqWorkflow string) string {
	// Priority 1: Explicit request
	if reqWorkflow != "" {
		return reqWorkflow
	}

	// Priority 2: User default setting
	if setting, err := s.database.GetSetting(ctx, userID, nil, "config.default_workflow"); err == nil && setting.Value != "" {
		return setting.Value
	}

	// Priority 3: System default
	return workflow.DefaultWorkflow
}

// buildWorkflowInputs constructs the initial workflow input data by applying presets,
// user params, and workflow schema defaults. Order: presets (base), then user params (override),
// then apply workflow defaults for any missing inputs (e.g. model default from YAML).
func (s *ChatService) buildWorkflowInputs(
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
		loadPreset := s.createDBPresetLoaderFull(ctx, userID, projectID)
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
	// so validateWorkflowInputs can reject missing required params before a chat starts.
	protoInputs := s.loadWorkflowInputsForBuild(ctx, userID, workflowName, projectID)
	if len(protoInputs) > 0 {
		initialData = v2.ApplyDefaults(initialData, protoInputs)
	}

	// Normalize model inputs: convert any remaining string model values to {id: string} objects.
	// This is the ONE place where string-to-object conversion happens — at the gRPC ingestion boundary.
	// Everything downstream rejects strings.
	if len(protoInputs) > 0 {
		normalizeModelInputs(initialData, protoInputs)
	}

	logging.Info("[buildWorkflowInputs] Final resolved", "tools", initialData["tools"])

	// Add project_path to workflow inputs so spawned workflows can load presets
	// This flows through: workflow.go -> StepExecutor -> executeSpawnInline -> InlineWorkflowExecutor
	if projectPath != "" {
		initialData["project_path"] = projectPath
	}

	return initialData
}

// loadWorkflowForBuild loads the workflow definition for buildWorkflowInputs (defaults application).
// Uses same order as chat validation: DB by slug, then project files. Returns nil if not found.

func (s *ChatService) buildStateUpdateForActiveWorkflow(
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

	projectPath := s.getEffectiveWorkingPath(ctx, chat)
	return s.buildWorkflowInputs(ctx, userID, projectPath, chat.ProjectID, workflowName, effectivePresets, requestParams)
}

// loadWorkflowInputsForBuild loads workflow input schemas as proto types for ApplyDefaults.
// Uses the same resolution order as validation so builtin workflows also get defaults
// and boundary normalization for nested model selectors.
func (s *ChatService) loadWorkflowInputsForBuild(ctx context.Context, userID, workflowName, projectID string) map[string]*reliantv1.Input {
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
	draft, err := s.database.GetWorkflowDraftBySlug(ctx, userID, slug)
	if err == nil && draft != nil {
		wf, parseErr := wfyaml.ParseWorkflow([]byte(draft.Definition))
		if parseErr == nil {
			return wf.GetInputs()
		}
	}

	// Try stored project config (synced by daemon)
	projectWf, _, lookupErr := loadProjectWorkflowBySlugFromDB(s.database, ctx, projectID, slug)
	if lookupErr == nil && projectWf != nil {
		return projectWf.GetInputs()
	}
	return nil
}

// validateWorkflowInputs loads a workflow and validates the provided inputs against its schema.
// Returns validation errors if inputs are missing or invalid, nil if valid.
// This enables early validation (400 error) before starting the workflow.
func (s *ChatService) validateWorkflowInputs(ctx context.Context, workflowName, projectID string, inputs map[string]interface{}) []error {
	userID := auth.MustGetUserID(ctx)

	// Load workflow definition to get input schemas (builtin/project config first, then user draft from DB)
	wf, err := s.loadWorkflowForValidation(ctx, workflowName, projectID)
	if err != nil {
		// User workflows often exist only in DB; load by slug so we validate the same definition that will run
		slug := strings.ToLower(strings.ReplaceAll(workflowName, " ", "-"))
		draft, dbErr := s.database.GetWorkflowDraftBySlug(ctx, userID, slug)
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
	protoInputs := s.loadWorkflowInputsForBuild(ctx, userID, workflowName, projectID)
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
	// before validation. This mirrors the normalization in buildWorkflowInputs.
	if protoInputs != nil {
		normalizeModelInputs(filteredInputs, protoInputs)
		normalizeModelInputs(finalInputs, protoInputs)
	}

	// Validate inputs against schema
	var errs []error
	if result := validation.ValidateInputs(wf, finalInputs); result.HasErrors() {
		errs = append(errs, result.AsError())
	}

	// Validate model availability - check that any model inputs can be resolved
	// with the user's configured API keys. If the selected model isn't available,
	// reject it with a clear error instead of silently substituting.
	modelSelectors := extractModelSelectors(finalInputs, wf.GetInputs(), "")
	for inputPath, selector := range modelSelectors {
		if err := drivers.ValidateModelSelector(ctx, userID, selector); err != nil {
			errs = append(errs, fmt.Errorf("input '%s': %w", inputPath, err))
		}
	}

	return errs
}

// loadWorkflowForValidation loads a workflow by name for input validation.
// Searches builtin workflows first, then stored project workflows from DB.
func (s *ChatService) loadWorkflowForValidation(ctx context.Context, workflowName, projectID string) (*reliantv1.Workflow, error) {
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
	slug := normalizeWorkflowSlug(workflowName)
	projectWf, _, err := loadProjectWorkflowBySlugFromDB(s.database, ctx, projectID, slug)
	if err == nil && projectWf != nil {
		return projectWf, nil
	}

	return nil, fmt.Errorf("workflow not found: %s", workflowName)
}

func (s *ChatService) loadCreateChatWorkflowForValidation(ctx context.Context, userID, workflowName, projectID string) (*reliantv1.Workflow, error) {
	if strings.HasPrefix(workflowName, "builtin://") {
		return s.loadWorkflowForValidation(ctx, workflowName, projectID)
	}

	slug := normalizeWorkflowSlug(workflowName)
	draft, err := s.database.GetUsableWorkflowBySlug(ctx, userID, slug)
	if err != nil {
		return nil, fmt.Errorf("failed to look up workflow '%s': %w", workflowName, err)
	}
	if draft != nil {
		wf, parseErr := wfyaml.ParseWorkflow([]byte(draft.Definition))
		if parseErr != nil {
			return nil, fmt.Errorf("failed to parse workflow '%s': %w", workflowName, parseErr)
		}
		return wf, nil
	}

	wf, err := s.loadWorkflowForValidation(ctx, workflowName, projectID)
	if err != nil {
		return nil, fmt.Errorf("workflow '%s' not found", workflowName)
	}
	return wf, nil
}

func (s *ChatService) createChatWorkflowLoader(ctx context.Context, userID, projectID string) validation.WorkflowLoader {
	return func(workflowName string) (*reliantv1.Workflow, error) {
		return s.loadCreateChatWorkflowForValidation(ctx, userID, workflowName, projectID)
	}
}

func (s *ChatService) validateCreateChatWorkflowTree(ctx context.Context, userID, workflowName, projectID string) error {
	wf, err := s.loadCreateChatWorkflowForValidation(ctx, userID, workflowName, projectID)
	if err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}

	validationOpts := &validation.ValidationOptions{
		WorkflowLoader:       s.createChatWorkflowLoader(ctx, userID, projectID),
		CanonicalWorkflowRef: workflowName,
	}
	if projectID != "" {
		validationOpts.PresetLoader = s.createDBPresetLoader(ctx, projectID)
	}

	result := validation.StaticAnalysisWithOptions(wf, validationOpts)
	if result != nil && result.HasErrors() {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("workflow tree validation failed for '%s': %s", workflowName, result.Error()))
	}

	return nil
}

// createDBPresetLoader returns a PresetLoader that reads presets from project/builtin sources.
// Validation does not currently have user context, so DB-backed user presets are resolved at runtime only.
func (s *ChatService) createDBPresetLoader(ctx context.Context, projectID string) validation.PresetLoader {
	return func(presetName string) (map[string]interface{}, error) {
		// Pass empty userID so validation behavior stays scoped to project/builtin presets.
		p, err := s.loadPresetFromDB(ctx, "", projectID, presetName)
		if err != nil {
			return nil, err
		}
		return p.Params, nil
	}
}

// createDBPresetLoaderFull returns a function that loads full preset objects from all runtime sources.
// Used by buildWorkflowInputs where the full preset is needed for ApplyToInputs.
func (s *ChatService) createDBPresetLoaderFull(ctx context.Context, userID, projectID string) func(name string) (*preset.Preset, error) {
	return func(name string) (*preset.Preset, error) {
		return s.loadPresetFromDB(ctx, userID, projectID, name)
	}
}

// loadPresetFromDB loads a preset by name. Priority: user presets > stored project presets > builtins.
func (s *ChatService) loadPresetFromDB(ctx context.Context, userID, projectID, name string) (*preset.Preset, error) {
	slug := strings.ToLower(strings.ReplaceAll(name, " ", "-"))

	if s.database != nil {
		// Try project-scoped user preset first.
		if projectID != "" {
			dbPreset, err := s.database.GetPresetBySlugAndProject(ctx, userID, slug, projectID)
			if err == nil && dbPreset != nil {
				return dbPresetToRuntimePreset(dbPreset), nil
			}
		}

		// Fall back to global user preset.
		dbPreset, err := s.database.GetPresetBySlug(ctx, userID, slug)
		if err == nil && dbPreset != nil {
			return dbPresetToRuntimePreset(dbPreset), nil
		}

		// Try stored project presets from daemon config sync.
		if projectID != "" {
			record, err := s.database.GetProjectConfigRecord(ctx, projectID)
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

// UpdateWorkflowParams updates workflow parameters for a running chat
// This signals the running workflow to update its inputs (e.g., mode, temperature)
func (s *ChatService) UpdateWorkflowParams(
	ctx context.Context,
	req *connect.Request[reliantv1.UpdateWorkflowParamsRequest],
) (*connect.Response[reliantv1.UpdateWorkflowParamsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("chat_id is required"))
	}

	// Get chat to verify ownership
	chat, err := s.database.GetChat(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}
	if chat.UserID != userID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}

	// Always signal the root workflow. Child workflows are inline goroutines
	// with synthetic DB IDs that Temporal doesn't know about.
	// Thread-scoped updates use the __thread key in the signal payload.
	workflowID := ""
	runID := ""
	if chat.WorkflowID != nil && *chat.WorkflowID != "" {
		workflowID = *chat.WorkflowID
		if chat.RunID != nil {
			runID = *chat.RunID
		}
	} else {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no active workflow for chat"))
	}

	if err := validateWorkflowParamStructure(req.Msg.Params); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	// If targeting a specific thread, verify it exists and is running
	if req.Msg.ThreadId != nil && *req.Msg.ThreadId != "" {
		wf, wfErr := s.database.GetWorkflowByThread(ctx, req.Msg.ChatId, *req.Msg.ThreadId)
		if wfErr != nil || wf == nil {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no workflow found for thread"))
		}
		if wf.Status != db.WorkflowStatusRunning {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("thread workflow is not running"))
		}
	}

	// Build state update through the same path as SendMessage —
	// this ensures schema defaults and validation are applied.
	workflowName := ""
	if chat.WorkflowName != nil {
		workflowName = *chat.WorkflowName
	}
	stateUpdate := s.buildStateUpdateForActiveWorkflow(ctx, userID, chat, workflowName, nil, req.Msg.Params)

	// Validate model selectors in the updated params before signaling the workflow
	if validationErrors := s.validateWorkflowInputs(ctx, workflowName, chat.ProjectID, stateUpdate); len(validationErrors) > 0 {
		errMsgs := make([]string, len(validationErrors))
		for i, e := range validationErrors {
			errMsgs[i] = e.Error()
		}
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("workflow input validation failed: %s", strings.Join(errMsgs, "; ")))
	}

	// Add thread scope to signal payload so the handler updates the correct thread's inputs
	if req.Msg.ThreadId != nil && *req.Msg.ThreadId != "" {
		stateUpdate["__thread"] = *req.Msg.ThreadId
	}

	// Signal the root workflow with the parameter updates
	err = s.tempClient.SignalWorkflow(
		ctx,
		workflowID,
		runID,
		"update_workflow_state",
		stateUpdate,
	)
	if err != nil {
		logging.Error("Failed to signal workflow for param update",
			"chatID", req.Msg.ChatId,
			"workflowID", workflowID,
			"threadID", req.Msg.ThreadId,
			"error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update workflow params"))
	}

	logging.Debug("Updated workflow params",
		"chatID", req.Msg.ChatId,
		"workflowID", workflowID,
		"threadID", req.Msg.ThreadId,
		"params", stateUpdate)

	return connect.NewResponse(&reliantv1.UpdateWorkflowParamsResponse{
		Success: true,
		Message: "Workflow parameters updated",
	}), nil
}

// GetWorkflowExecutions returns the workflow execution tree for a chat
func (s *ChatService) GetWorkflowExecutions(
	ctx context.Context,
	req *connect.Request[reliantv1.GetWorkflowExecutionsRequest],
) (*connect.Response[reliantv1.GetWorkflowExecutionsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("chat_id is required"))
	}

	// Verify ownership
	chat, err := s.database.GetChat(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}
	if chat.UserID != userID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}

	// Get all workflows for this chat
	workflows, err := s.database.ListWorkflowsByChat(ctx, req.Msg.ChatId)
	if err != nil {
		logging.Error("Failed to list workflows", "error", err, "chatID", req.Msg.ChatId)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list workflows"))
	}

	if len(workflows) == 0 {
		return connect.NewResponse(&reliantv1.GetWorkflowExecutionsResponse{}), nil
	}

	// Get step executions for all workflows
	stepsByWorkflow := make(map[string][]*db.StepExecution)
	for _, wf := range workflows {
		steps, err := s.database.GetStepExecutionsByWorkflow(ctx, wf.ID)
		if err != nil {
			logging.Warn("Failed to get step executions", "error", err, "workflowID", wf.ID)
			continue
		}
		stepsByWorkflow[wf.ID] = steps
	}

	// Build workflow map for tree construction
	workflowMap := make(map[string]*db.Workflow)
	for _, wf := range workflows {
		workflowMap[wf.ID] = wf
	}

	// Find root workflows (no parent or parent is chat itself)
	var roots []*db.Workflow
	for _, wf := range workflows {
		if wf.ParentID == nil {
			roots = append(roots, wf)
		}
	}

	if len(roots) == 0 {
		return connect.NewResponse(&reliantv1.GetWorkflowExecutionsResponse{}), nil
	}

	// Sort roots by created_at descending (newest first)
	sort.Slice(roots, func(i, j int) bool {
		return roots[i].CreatedAt.After(roots[j].CreatedAt)
	})

	// Build tree for each root workflow
	allRootProtos := make([]*reliantv1.WorkflowExecution, 0, len(roots))
	for _, root := range roots {
		rootProto := s.buildWorkflowExecutionTree(root, workflows, stepsByWorkflow)
		allRootProtos = append(allRootProtos, rootProto)
	}

	// The most recent root is first (for backwards compat)
	var latestRootProto *reliantv1.WorkflowExecution
	if len(allRootProtos) > 0 {
		latestRootProto = allRootProtos[0]
	}

	return connect.NewResponse(&reliantv1.GetWorkflowExecutionsResponse{
		RootWorkflow:     latestRootProto,
		AllRootWorkflows: allRootProtos,
	}), nil
}

// GetThreadWorkflowInputs returns the workflow inputs for a specific thread.
// It looks up the workflow record for the thread, queries Temporal for current inputs,
// and falls back to empty inputs for completed workflows.
func (s *ChatService) GetThreadWorkflowInputs(
	ctx context.Context,
	req *connect.Request[reliantv1.GetThreadWorkflowInputsRequest],
) (*connect.Response[reliantv1.GetThreadWorkflowInputsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" || req.Msg.ThreadId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("chat_id and thread_id are required"))
	}

	// Verify chat ownership
	chat, err := s.database.GetChat(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}
	if chat.UserID != userID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}

	// Look up the workflow record for this thread
	wf, err := s.database.GetWorkflowByThread(ctx, req.Msg.ChatId, req.Msg.ThreadId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no workflow found for thread"))
	}
	if wf == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no workflow found for thread"))
	}

	isRunning := wf.Status == db.WorkflowStatusRunning

	// Resolve to root workflow ID for Temporal queries.
	// Child workflows are inline goroutines with synthetic DB IDs that Temporal doesn't know.
	// All queries must target the root workflow (the only real Temporal workflow).
	rootWorkflowID := ""
	rootRunID := ""
	if chat.WorkflowID != nil && *chat.WorkflowID != "" {
		rootWorkflowID = *chat.WorkflowID
		if chat.RunID != nil {
			rootRunID = *chat.RunID
		}
	}

	// Try to query Temporal for thread-specific inputs (only works for running workflows)
	inputsMap := make(map[string]*structpb.Value)
	if isRunning && rootWorkflowID != "" {
		// Use get_thread_inputs query which returns the thread's subInputs map
		queryResp, err := s.tempClient.QueryWorkflow(ctx, rootWorkflowID, rootRunID, "get_thread_inputs", req.Msg.ThreadId)
		if err == nil {
			var currentInputs map[string]interface{}
			if err := queryResp.Get(&currentInputs); err == nil {
				// Convert to protobuf Values, filtering out runtime-injected inputs
				for key, value := range currentInputs {
					if workflow.RuntimeInjectedInputs[key] {
						continue
					}
					pbVal, err := structpb.NewValue(value)
					if err == nil {
						inputsMap[key] = pbVal
					}
				}
			}
		} else {
			logging.Debug("Failed to query thread inputs", "error", err, "rootWorkflowID", rootWorkflowID, "thread", req.Msg.ThreadId)
		}
	}

	return connect.NewResponse(&reliantv1.GetThreadWorkflowInputsResponse{
		WorkflowName: wf.WorkflowName,
		Inputs:       inputsMap,
		IsRunning:    isRunning,
	}), nil
}

// buildWorkflowExecutionTree recursively builds the workflow execution tree
func (s *ChatService) buildWorkflowExecutionTree(
	wf *db.Workflow,
	allWorkflows []*db.Workflow,
	stepsByWorkflow map[string][]*db.StepExecution,
) *reliantv1.WorkflowExecution {
	proto := &reliantv1.WorkflowExecution{
		Id:           wf.ID,
		WorkflowName: wf.WorkflowName,
		Thread:       wf.Thread,
		Status:       wf.Status,
		CreatedAt:    wf.CreatedAt.Format(time.RFC3339),
		MessageCount: 0, // TODO: count messages by thread
	}

	if wf.ParentID != nil {
		proto.ParentId = wf.ParentID
	}
	if wf.SpawnedByNodeID != nil {
		proto.SpawnedByNodeId = wf.SpawnedByNodeID
	}
	// The run's verdict, beside its lifecycle status: a run that ran to its
	// `failed` terminal node is Status=COMPLETED, Outcome=failure, and a
	// supervisor must be able to tell that from a run that built the app.
	if wf.Outcome != nil && *wf.Outcome != "" {
		proto.Outcome = wf.Outcome
	}
	// Populate Origin, ForkedFromThread, ParentThread, and ThreadTitle from the
	// Thread table (single source of truth for thread identity).
	if thread, err := s.database.GetThread(context.Background(), wf.Thread); err == nil && thread != nil {
		if thread.ParentThreadID != nil {
			// ForkedFromThread: only for actual forks (have fork metadata)
			if thread.ForkAtOrdinal != nil {
				proto.ForkedFromThread = thread.ParentThreadID
			}
			// ParentThread: always set when parent exists (both fork and new)
			proto.ParentThread = thread.ParentThreadID
		}
		if thread.Title != nil {
			proto.ThreadTitle = thread.Title
		}
		proto.Origin = thread.Origin
		proto.OriginNodeId = thread.OriginNodeID
	}
	if wf.LoopIteration != nil {
		iteration := int32(*wf.LoopIteration)
		proto.Iteration = &iteration
	}
	if wf.CompletedAt != nil {
		completedAt := wf.CompletedAt.Format(time.RFC3339)
		proto.CompletedAt = &completedAt
	}

	// Add step executions (omit output_json to reduce response size — can be 8-14MB with full outputs)
	if steps, ok := stepsByWorkflow[wf.ID]; ok {
		proto.Steps = make([]*reliantv1.StepExecution, len(steps))
		for i, step := range steps {
			proto.Steps[i] = &reliantv1.StepExecution{
				Id:           step.ID,
				WorkflowId:   step.WorkflowID,
				StepId:       step.StepID,
				ActivityName: step.ActivityName,
				CreatedAt:    step.CreatedAt.Format(time.RFC3339),
			}
			// Only include a minimal output_json for save steps (need message_id for timeline)
			// Skip full output_json to avoid sending megabytes of step execution output
			if step.OutputJSON.Valid && strings.HasSuffix(step.StepID, "-save") {
				proto.Steps[i].OutputJson = step.OutputJSON.String
			}
			if step.ExitCode.Valid {
				exitCode := int32(step.ExitCode.Int64)
				proto.Steps[i].ExitCode = &exitCode
			}
			if step.Success.Valid {
				proto.Steps[i].Success = &step.Success.Bool
			}
			if step.DurationMs.Valid {
				proto.Steps[i].DurationMs = &step.DurationMs.Int64
			}
			if step.LoopNodeID.Valid {
				proto.Steps[i].LoopNodeId = &step.LoopNodeID.String
			}
			if step.LoopIteration.Valid {
				loopIter := int32(step.LoopIteration.Int64)
				proto.Steps[i].LoopIteration = &loopIter
			}
		}
	}

	// Find and add children
	for _, child := range allWorkflows {
		if child.ParentID != nil && *child.ParentID == wf.ID {
			childProto := s.buildWorkflowExecutionTree(child, allWorkflows, stepsByWorkflow)
			proto.Children = append(proto.Children, childProto)
		}
	}

	return proto
}


