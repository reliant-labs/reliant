// Copyright (c) 2025 Reliant Labs
package tools

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	cfg "github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/simulator"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"gopkg.in/yaml.v3"
)

// =============================================================================
// SCENARIO SCHEMA
// =============================================================================
//
// There is exactly ONE definition of the scenario format:
// simulator.Scenario / Expectation / SimulatedEvent. The tools parse stored
// YAML straight into it.
//
// This file used to declare a private mirror (ScenarioParam / ExpectationParam
// / SimulatedEventParam) and copy it field-by-field onto the simulator types.
// The mirror drifted, and every field it forgot was silently DISCARDED —
// Expectation.Completed, Expectation.Skipped and Expectation.Outputs, plus the
// whole typed-event mode on SimulatedEvent. A scenario asserting
// `completed: [x]` for a node that was merely skipped therefore reported PASS
// through the tools while CI (which unmarshals into simulator.Scenario
// directly) enforced the assertion for real.
//
// The mirror existed only to hang jsonschema tags off, and nothing ever
// reflected over it: write_scenario takes the scenario as a raw YAML string
// (WriteScenarioParams.Content), so no tool's ParamSchema ever reached these
// types. Deleting it removes the drift mechanism outright rather than patching
// this round of dropped fields.
//
// =============================================================================
// LIST SCENARIOS TOOL
// =============================================================================

type ListScenariosParams struct {
	ID string `json:"id,omitempty" jsonschema:"description=Workflow UUID, slug, or name. Optional — defaults to the workflow this chat is editing."`
}

type listScenariosTool struct {
	repo db.Repository
}

const (
	ListScenariosToolName        = "list_scenarios"
	listScenariosToolDescription = `List all test scenarios for the current workflow.

Returns a summary of each scenario including name, description, and last run status.
Use this to see what scenarios exist and their current state.

No parameters are required. The workflow defaults to the one this chat is
editing. Pass id (a workflow UUID, slug, or name) only to list scenarios for a
different workflow.`
)

func NewListScenariosTool(repo db.Repository) Tool {
	tool := &listScenariosTool{repo: repo}
	return NewToolWrapper[ListScenariosParams, ToolResponse](tool)
}

func (t *listScenariosTool) Name() string {
	return ListScenariosToolName
}

func (t *listScenariosTool) Description() string {
	return listScenariosToolDescription
}

func (t *listScenariosTool) RequiresPermission(args ListScenariosParams) (bool, error) {
	return false, nil
}

func (t *listScenariosTool) Execute(ctx *rctx.ToolContext, args ListScenariosParams) (ToolResponse, error) {
	if t.repo == nil {
		return NewTextErrorResponse("This tool requires a database connection and is not available in daemon-only mode"), nil
	}

	draft, err := resolveWorkflowDraft(ctx, t.repo, args.ID)
	if err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}

	scenarios, err := t.repo.ListWorkflowScenariosByDraft(ctx, draft.ID)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to list scenarios: %v", err)), nil
	}

	if len(scenarios) == 0 {
		return NewTextResponse("No scenarios found for this workflow.\n\nUse `write_scenario` to add test scenarios."), nil
	}

	output := fmt.Sprintf("## Scenarios for %s\n\n", draft.Name)
	output += fmt.Sprintf("Found %d scenario(s):\n\n", len(scenarios))

	for _, s := range scenarios {
		status := "⏸ Not run"
		if s.LastRunStatus.Valid {
			switch s.LastRunStatus.String {
			case "passed":
				status = "✓ Passed"
			case "failed":
				status = "✗ Failed"
			case "error":
				status = "⚠ Error"
			}
		}

		output += fmt.Sprintf("### %s\n", s.Name)
		output += fmt.Sprintf("- **Status:** %s\n", status)
		if s.Description.Valid && s.Description.String != "" {
			output += fmt.Sprintf("- **Description:** %s\n", s.Description.String)
		}
		if s.LastRunAt.Valid {
			output += fmt.Sprintf("- **Last run:** %s\n", s.LastRunAt.Time.Format("2006-01-02 15:04:05"))
		}
		output += "\n"
	}

	return NewTextResponse(output), nil
}

// =============================================================================
// VIEW SCENARIO TOOL
// =============================================================================

type ViewScenarioParams struct {
	ID   string `json:"id,omitempty" jsonschema:"description=Workflow UUID, slug, or name. Optional — defaults to the workflow this chat is editing."`
	Name string `json:"name" jsonschema:"required,description=Name of the scenario to view"`
}

type viewScenarioTool struct {
	repo db.Repository
}

const (
	ViewScenarioToolName        = "view_scenario"
	viewScenarioToolDescription = `View a specific test scenario's full definition.

Returns the complete scenario YAML including events, expectations, and last run results.
Use this to examine a scenario's configuration or debug test failures.

The workflow defaults to the one this chat is editing. Pass id (a workflow UUID,
slug, or name) only to view a scenario on a different workflow.`
)

func NewViewScenarioTool(repo db.Repository) Tool {
	tool := &viewScenarioTool{repo: repo}
	return NewToolWrapper[ViewScenarioParams, ToolResponse](tool)
}

func (t *viewScenarioTool) Name() string {
	return ViewScenarioToolName
}

func (t *viewScenarioTool) Description() string {
	return viewScenarioToolDescription
}

func (t *viewScenarioTool) RequiresPermission(args ViewScenarioParams) (bool, error) {
	return false, nil
}

func (t *viewScenarioTool) Execute(ctx *rctx.ToolContext, args ViewScenarioParams) (ToolResponse, error) {
	if t.repo == nil {
		return NewTextErrorResponse("This tool requires a database connection and is not available in daemon-only mode"), nil
	}

	if args.Name == "" {
		return NewTextErrorResponse("name is required"), nil
	}

	draft, err := resolveWorkflowDraft(ctx, t.repo, args.ID)
	if err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}

	scenario, err := t.repo.GetWorkflowScenarioByName(ctx, draft.ID, args.Name)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to get scenario: %v", err)), nil
	}
	if scenario == nil {
		return NewTextErrorResponse(fmt.Sprintf("Scenario not found: %s\n\nUse list_scenarios to see available scenarios.", args.Name)), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "# Scenario: %s\n\n", scenario.Name)

	// Status
	status := "Not run"
	if scenario.LastRunStatus.Valid {
		switch scenario.LastRunStatus.String {
		case "passed":
			status = "✓ Passed"
		case "failed":
			status = "✗ Failed"
		case "error":
			status = "⚠ Error"
		}
	}
	fmt.Fprintf(&sb, "**Last Run Status:** %s\n", status)
	if scenario.LastRunAt.Valid {
		fmt.Fprintf(&sb, "**Last Run:** %s\n\n", scenario.LastRunAt.Time.Format("2006-01-02 15:04:05"))
	}

	sb.WriteString("```yaml\n")
	sb.WriteString(scenario.Events)
	if !strings.HasSuffix(scenario.Events, "\n") {
		sb.WriteString("\n")
	}
	sb.WriteString("```\n\n")

	sb.WriteString("---\n")
	fmt.Fprintf(&sb, "**Updated at:** `%s`\n", scenario.UpdatedAt.Format(time.RFC3339))

	// Last run result
	if scenario.LastRunResult.Valid && scenario.LastRunResult.String != "" {
		sb.WriteString("\n### Last Run Result\n```json\n")
		sb.WriteString(scenario.LastRunResult.String)
		sb.WriteString("\n```\n")
	}

	return NewTextResponse(sb.String()), nil
}

// =============================================================================
// EDIT SCENARIO TOOL
// =============================================================================

type EditScenarioParams struct {
	ID              string `json:"id,omitempty" jsonschema:"description=Workflow UUID, slug, or name. Optional — defaults to the workflow this chat is editing."`
	Name            string `json:"name" jsonschema:"required,description=Name of the scenario to edit"`
	OldString       string `json:"old_string" jsonschema:"required,description=The exact text to find and replace"`
	NewString       string `json:"new_string" jsonschema:"required,description=The replacement text"`
	ExpectedVersion *int64 `json:"expected_version,omitempty" jsonschema:"description=Optional version number for conflict detection"`
}

type editScenarioTool struct {
	repo db.Repository
}

const (
	EditScenarioToolName        = "edit_scenario"
	editScenarioToolDescription = `Make precise text replacements in a scenario's YAML definition.

Use this for small changes like updating expected values or modifying events.
The old_string must match exactly (including whitespace and indentation).

The workflow defaults to the one this chat is editing. Pass id (a workflow UUID,
slug, or name) only to edit a scenario on a different workflow.

**Example:**
{
  "name": "happy_path",
  "old_string": "outcome: completed",
  "new_string": "outcome: error"
}`
)

func NewEditScenarioTool(repo db.Repository) Tool {
	tool := &editScenarioTool{repo: repo}
	return NewToolWrapper[EditScenarioParams, ToolResponse](tool)
}

func (t *editScenarioTool) Name() string {
	return EditScenarioToolName
}

func (t *editScenarioTool) Description() string {
	return editScenarioToolDescription
}

func (t *editScenarioTool) RequiresPermission(args EditScenarioParams) (bool, error) {
	return false, nil
}

func (t *editScenarioTool) Execute(ctx *rctx.ToolContext, args EditScenarioParams) (ToolResponse, error) {
	if t.repo == nil {
		return NewTextErrorResponse("This tool requires a database connection and is not available in daemon-only mode"), nil
	}

	if args.Name == "" {
		return NewTextErrorResponse("name is required"), nil
	}
	if args.OldString == "" {
		return NewTextErrorResponse("old_string is required"), nil
	}

	draft, err := resolveWorkflowDraft(ctx, t.repo, args.ID)
	if err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}

	scenario, err := t.repo.GetWorkflowScenarioByName(ctx, draft.ID, args.Name)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to get scenario: %v", err)), nil
	}
	if scenario == nil {
		return NewTextErrorResponse(fmt.Sprintf("Scenario not found: %s", args.Name)), nil
	}

	// Check for conflicts
	if args.ExpectedVersion != nil {
		if scenario.Version != *args.ExpectedVersion {
			return NewTextErrorResponse(fmt.Sprintf(
				"Scenario was modified since you last viewed it.\n\n"+
					"Your version: %d\n"+
					"Current version: %d\n\n"+
					"Please call view_scenario again.",
				*args.ExpectedVersion,
				scenario.Version,
			)), nil
		}
	}

	// Apply replacement on the stored YAML
	if !strings.Contains(scenario.Events, args.OldString) {
		return NewTextErrorResponse("old_string not found in scenario. Make sure it matches exactly (including whitespace)."), nil
	}

	count := strings.Count(scenario.Events, args.OldString)
	if count > 1 {
		return NewTextErrorResponse(fmt.Sprintf("old_string appears %d times. Please provide more context.", count)), nil
	}

	newContent := strings.Replace(scenario.Events, args.OldString, args.NewString, 1)

	// Try to extract description from the YAML
	var desc sql.NullString
	var basicDef struct {
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal([]byte(newContent), &basicDef); err == nil && basicDef.Description != "" {
		desc = sql.NullString{String: basicDef.Description, Valid: true}
	}

	// Store the raw edited content
	scenario.Events = newContent
	scenario.Expect = sql.NullString{}
	scenario.Description = desc
	scenario.UpdatedAt = time.Now().UTC()

	if err := t.repo.UpdateWorkflowScenario(ctx, scenario); err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to save scenario: %v", err)), nil
	}

	return NewTextResponse("Scenario updated successfully.\n\nUse `view_scenario` to see the result or `run_scenario` to test it."), nil
}

// =============================================================================
// WRITE SCENARIO TOOL
// =============================================================================

type WriteScenarioParams struct {
	ID              string `json:"id,omitempty" jsonschema:"description=Workflow UUID, slug, or name. Optional — defaults to the workflow this chat is editing."`
	Name            string `json:"name" jsonschema:"required,description=Name of the scenario to create/update"`
	Content         string `json:"content" jsonschema:"required,description=Complete scenario definition as YAML"`
	ExpectedVersion *int64 `json:"expected_version,omitempty" jsonschema:"description=Optional version number for conflict detection"`
}

type writeScenarioTool struct {
	repo db.Repository
}

const (
	WriteScenarioToolName        = "write_scenario"
	writeScenarioToolDescription = `Create or update a test scenario with YAML content.

Creates or updates a scenario with the given YAML definition and runs it.

**Scenario YAML structure:**
name: scenario_name
description: What this scenario tests
events:
  - node: node_id           # Optional: target specific node
    output:                  # Mock output for the node
      message:
        role: assistant
        text: "Hello!"
      response_text: "Hello!"
expect:
  outcome: completed         # or "error" / "failed"
  reached: ["node1", "node2"]
  not_reached: ["node3"]

**Events — typed mode (usually shorter than a raw output map):**
  - node: call_llm
    type: llm_response       # llm_response | tool_result | tool_error | llm_error | user_input
    text: "Hello!"
    tool_calls: [{name: bash, input: {command: ls}}]
  - node: execute_tools
    type: tool_result
    tool: bash
    tool_output: {result: "file.txt"}
    is_error: false          # marks the tool result as failed

**Assertions — all optional:**
- outcome: completed | error | failed
- reached / not_reached: whether a node was SCHEDULED. reached includes nodes
  that were skipped or errored, so it does NOT prove a node ran.
- completed: nodes that must have EXECUTED successfully. This is the assertion
  that excludes skipped nodes — use it, not reached, to prove a branch ran.
- skipped: nodes that must have been scheduled but skipped by a false condition.
- error_contains / error_node: for outcome: error
- node_outputs: {node_id: {field: expected}} — partial match on a node's output
- outputs: {name: expected} — the workflow's declared outputs. Keys support
  dotted paths (e.g. "response.choice": "complete").

**Targeting nodes:**
- Top-level nodes: node: "call_llm"
- Inner loop nodes: node: "agent_loop.call_llm" (dot-separated)
- Nested loops: node: "outer_loop.inner_loop.call_llm"

**Workflow selection:**
Defaults to the workflow this chat is editing. Pass id (a workflow UUID, slug,
or name) only to write a scenario on a different workflow.

**Example:**
{
  "name": "happy_path",
  "content": "name: happy_path\ndescription: Test happy path\nevents:\n  - output:\n      message:\n        role: assistant\n        text: Hello!\n      response_text: Hello!\nexpect:\n  outcome: completed"
}`
)

func NewWriteScenarioTool(repo db.Repository) Tool {
	tool := &writeScenarioTool{repo: repo}
	return NewToolWrapper[WriteScenarioParams, ToolResponse](tool)
}

func (t *writeScenarioTool) Name() string {
	return WriteScenarioToolName
}

func (t *writeScenarioTool) Description() string {
	return writeScenarioToolDescription
}

func (t *writeScenarioTool) RequiresPermission(args WriteScenarioParams) (bool, error) {
	return false, nil
}

func (t *writeScenarioTool) Execute(ctx *rctx.ToolContext, args WriteScenarioParams) (ToolResponse, error) {
	if t.repo == nil {
		return NewTextErrorResponse("This tool requires a database connection and is not available in daemon-only mode"), nil
	}

	if args.Name == "" {
		return NewTextErrorResponse("name is required"), nil
	}
	if args.Content == "" {
		return NewTextErrorResponse("content is required"), nil
	}

	draft, err := resolveWorkflowDraft(ctx, t.repo, args.ID)
	if err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}

	// Check if scenario already exists
	existing, err := t.repo.GetWorkflowScenarioByName(ctx, draft.ID, args.Name)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to check existing scenario: %v", err)), nil
	}

	// Check for conflicts on existing scenarios
	if existing != nil && args.ExpectedVersion != nil {
		if existing.Version != *args.ExpectedVersion {
			return NewTextErrorResponse(fmt.Sprintf(
				"Scenario was modified since you last viewed it.\n\n"+
					"Your version: %d\n"+
					"Current version: %d\n\n"+
					"Please call view_scenario again.",
				*args.ExpectedVersion,
				existing.Version,
			)), nil
		}
	}

	// Check basic YAML syntax and try to extract description
	var desc sql.NullString
	var basicDef struct {
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal([]byte(args.Content), &basicDef); err == nil && basicDef.Description != "" {
		desc = sql.NullString{String: basicDef.Description, Valid: true}
	}

	now := time.Now().UTC()

	if existing != nil {
		// Update existing scenario
		existing.Events = args.Content
		existing.Expect = sql.NullString{}
		existing.Description = desc
		existing.UpdatedAt = now

		if err := t.repo.UpdateWorkflowScenario(ctx, existing); err != nil {
			return NewTextErrorResponse(fmt.Sprintf("Failed to update scenario: %v", err)), nil
		}
	} else {
		// Create new scenario
		chat, err := t.repo.GetChat(ctx, ctx.ChatID)
		if err != nil {
			return NewTextErrorResponse(fmt.Sprintf("Failed to get chat: %v", err)), nil
		}

		scenario := &db.WorkflowScenario{
			ID:              uuid.New().String(),
			WorkflowDraftID: sql.NullString{String: draft.ID, Valid: true},
			UserID:          chat.UserID,
			Name:            args.Name,
			Description:     desc,
			Events:          args.Content,
			CreatedAt:       now,
			UpdatedAt:       now,
		}

		if err := t.repo.CreateWorkflowScenario(ctx, scenario); err != nil {
			return NewTextErrorResponse(fmt.Sprintf("Failed to create scenario: %v", err)), nil
		}
	}

	return runScenarioByName(ctx, t.repo, draft, args.Name)
}

// =============================================================================
// DELETE SCENARIO TOOL
// =============================================================================

type DeleteScenarioParams struct {
	ID   string `json:"id,omitempty" jsonschema:"description=Workflow UUID, slug, or name. Optional — defaults to the workflow this chat is editing."`
	Name string `json:"name" jsonschema:"required,description=Name of the scenario to delete"`
}

type deleteScenarioTool struct {
	repo db.Repository
}

const (
	DeleteScenarioToolName        = "delete_scenario"
	deleteScenarioToolDescription = `Delete a test scenario.

Permanently removes the scenario from the workflow.

The workflow defaults to the one this chat is editing. Pass id (a workflow UUID,
slug, or name) only to delete a scenario on a different workflow.`
)

func NewDeleteScenarioTool(repo db.Repository) Tool {
	tool := &deleteScenarioTool{repo: repo}
	return NewToolWrapper[DeleteScenarioParams, ToolResponse](tool)
}

func (t *deleteScenarioTool) Name() string {
	return DeleteScenarioToolName
}

func (t *deleteScenarioTool) Description() string {
	return deleteScenarioToolDescription
}

func (t *deleteScenarioTool) RequiresPermission(args DeleteScenarioParams) (bool, error) {
	return false, nil
}

func (t *deleteScenarioTool) Execute(ctx *rctx.ToolContext, args DeleteScenarioParams) (ToolResponse, error) {
	if t.repo == nil {
		return NewTextErrorResponse("This tool requires a database connection and is not available in daemon-only mode"), nil
	}

	if args.Name == "" {
		return NewTextErrorResponse("name is required"), nil
	}

	draft, err := resolveWorkflowDraft(ctx, t.repo, args.ID)
	if err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}

	scenario, err := t.repo.GetWorkflowScenarioByName(ctx, draft.ID, args.Name)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to get scenario: %v", err)), nil
	}
	if scenario == nil {
		return NewTextErrorResponse(fmt.Sprintf("Scenario not found: %s", args.Name)), nil
	}

	if err := t.repo.DeleteWorkflowScenario(ctx, scenario.ID); err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to delete scenario: %v", err)), nil
	}

	return NewTextResponse(fmt.Sprintf("Scenario '%s' deleted successfully.", args.Name)), nil
}

// =============================================================================
// RUN SCENARIO TOOL
// =============================================================================

type RunScenarioParams struct {
	ID   string `json:"id,omitempty" jsonschema:"description=Workflow UUID, slug, or name. Optional — defaults to the workflow this chat is editing."`
	Name string `json:"name" jsonschema:"required,description=Name of the scenario to run"`
}

type runScenarioTool struct {
	repo db.Repository
}

const (
	RunScenarioToolName        = "run_scenario"
	runScenarioToolDescription = `Run an existing test scenario by name.

Executes the scenario against the current workflow and returns the results.
Use this after making changes to verify scenarios still pass.

Use list_scenarios to see available scenario names.

The workflow defaults to the one this chat is editing. Pass id (a workflow UUID,
slug, or name) only to run a scenario on a different workflow.`
)

func NewRunScenarioTool(repo db.Repository) Tool {
	tool := &runScenarioTool{repo: repo}
	return NewToolWrapper[RunScenarioParams, ToolResponse](tool)
}

func (t *runScenarioTool) Name() string {
	return RunScenarioToolName
}

func (t *runScenarioTool) Description() string {
	return runScenarioToolDescription
}

func (t *runScenarioTool) RequiresPermission(args RunScenarioParams) (bool, error) {
	return false, nil
}

func (t *runScenarioTool) Execute(ctx *rctx.ToolContext, args RunScenarioParams) (ToolResponse, error) {
	if t.repo == nil {
		return NewTextErrorResponse("This tool requires a database connection and is not available in daemon-only mode"), nil
	}

	if args.Name == "" {
		return NewTextErrorResponse("name is required"), nil
	}

	draft, err := resolveWorkflowDraft(ctx, t.repo, args.ID)
	if err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}

	return runScenarioByName(ctx, t.repo, draft, args.Name)
}

// =============================================================================
// HELPER FUNCTIONS
// =============================================================================

// runScenarioByName runs a scenario and returns the formatted result
func runScenarioByName(ctx *rctx.ToolContext, repo db.Repository, draft *db.WorkflowDraft, name string) (ToolResponse, error) {
	scenario, err := repo.GetWorkflowScenarioByName(ctx, draft.ID, name)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to find scenario: %v", err)), nil
	}
	if scenario == nil {
		return NewTextErrorResponse(fmt.Sprintf("Scenario not found: %s\n\nUse list_scenarios to see available scenarios.", name)), nil
	}

	// Parse the workflow
	wf, err := v2.ParseWorkflowProtoBytes([]byte(draft.Definition))
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to parse workflow: %v", err)), nil
	}

	// Convert stored scenario to simulator types
	simScenario, err := dbScenarioToSimulatorInternal(scenario)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to parse scenario: %v", err)), nil
	}

	// Run the simulation
	workflowLoader := createScenarioWorkflowLoader(ctx, repo)
	engine := simulator.NewEngineWithLoader(wf, workflowLoader)
	result := engine.RunScenario(simScenario)

	// Update the result in the database
	resultJSON, err := result.ToJSON()
	if err == nil {
		if updateErr := repo.UpdateWorkflowScenarioResult(ctx, scenario.ID, string(result.Status), resultJSON); updateErr != nil {
			logging.Warn("Failed to update scenario result", "error", updateErr)
		}
	}

	return NewTextResponse(formatScenarioResultInternal(result)), nil
}

func createScenarioWorkflowLoader(ctx *rctx.ToolContext, repo db.Repository) func(string) (*reliantv1.Workflow, error) {
	projectID := ""
	if ctx != nil && ctx.Project != nil {
		projectID = ctx.Project.ID
	}

	userID := ""
	if ctx != nil && ctx.ChatID != "" {
		chat, err := repo.GetChat(ctx, ctx.ChatID)
		if err == nil && chat != nil {
			userID = chat.UserID
			if projectID == "" {
				projectID = chat.ProjectID
			}
		} else if err != nil {
			logging.Warn("Failed to load chat for scenario workflow loader", "error", err, "chat_id", ctx.ChatID)
		}
	}

	return func(ref string) (*reliantv1.Workflow, error) {
		workflowRef := strings.TrimSpace(ref)
		if workflowRef == "" {
			return nil, fmt.Errorf("workflow ref is empty")
		}

		if strings.HasPrefix(workflowRef, "builtin://") {
			return loadScenarioBuiltinWorkflow(workflowRef)
		}

		workflowRef = strings.TrimPrefix(workflowRef, "project://")

		if userID != "" {
			slug := cfg.NormalizeSlug(workflowRef)
			if slug != "" {
				draft, err := repo.GetUsableWorkflowBySlug(ctx, userID, slug)
				if err != nil {
					return nil, fmt.Errorf("failed to look up workflow %q: %w", ref, err)
				}
				if draft != nil {
					wf, err := wfyaml.ParseWorkflow([]byte(draft.Definition))
					if err != nil {
						return nil, fmt.Errorf("failed to parse workflow %q: %w", ref, err)
					}
					return wf, nil
				}
			}
		}

		if projectID != "" {
			wf, err := loadScenarioProjectWorkflow(ctx, repo, projectID, workflowRef)
			if err != nil {
				return nil, err
			}
			if wf != nil {
				return wf, nil
			}
		}

		return nil, fmt.Errorf("workflow not found: %s", ref)
	}
}

func loadScenarioBuiltinWorkflow(ref string) (*reliantv1.Workflow, error) {
	name := strings.TrimPrefix(ref, "builtin://")
	if yamlData := builtin.GetInternalWorkflowYAML(name); yamlData != nil {
		wf, err := wfyaml.ParseWorkflow(yamlData)
		if err != nil {
			return nil, fmt.Errorf("failed to parse internal workflow %q: %w", ref, err)
		}
		return wf, nil
	}

	data, err := builtin.BuiltinWorkflowsFS.ReadFile(name + ".yaml")
	if err != nil {
		return nil, fmt.Errorf("builtin workflow not found: %s", ref)
	}
	wf, err := wfyaml.ParseWorkflow(data)
	if err != nil {
		return nil, fmt.Errorf("failed to parse builtin workflow %q: %w", ref, err)
	}
	return wf, nil
}

func loadScenarioProjectWorkflow(ctx *rctx.ToolContext, repo db.Repository, projectID, workflowRef string) (*reliantv1.Workflow, error) {
	record, err := repo.GetProjectConfigRecord(ctx, projectID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to load project config for workflow %q: %w", workflowRef, err)
	}

	workflows, err := cfg.ParseStoredWorkflows(record.ProjectWorkflowsJSON)
	if err != nil {
		return nil, fmt.Errorf("failed to parse stored project workflows: %w", err)
	}

	slug := cfg.NormalizeSlug(workflowRef)
	storedWorkflow := cfg.FindStoredWorkflowBySlug(workflows, slug)
	if storedWorkflow == nil {
		return nil, nil
	}

	wf, err := wfyaml.ParseWorkflow([]byte(storedWorkflow.YAMLContent))
	if err != nil {
		return nil, fmt.Errorf("failed to parse project workflow %q: %w", workflowRef, err)
	}
	return wf, nil
}

// dbScenarioToSimulatorInternal parses a stored scenario into the simulator's
// own type. This is the same unmarshal the builtin-scenario CI suite performs
// (internal/workflow/builtin/scenarios_test.go), so the tools and CI cannot
// disagree about what a scenario file means.
func dbScenarioToSimulatorInternal(s *db.WorkflowScenario) (*simulator.Scenario, error) {
	var scenario simulator.Scenario
	if err := yaml.Unmarshal([]byte(s.Events), &scenario); err != nil {
		return nil, fmt.Errorf("failed to parse scenario YAML: %w", err)
	}
	return &scenario, nil
}

// Budgets for the scenario result text. This string is fed straight into a
// model's context window alongside the workflow YAML the model is trying to
// fix, so a verbose result is its own failure mode: it evicts the very thing
// being debugged. A passing run therefore prints a few lines, and detail is
// spent only where something actually diverged.
const (
	scenarioNodeOutputBudget  = 220 // per-node output, characters
	scenarioTotalOutputBudget = 8   // number of nodes whose outputs are printed
)

// formatScenarioResultInternal renders a simulation result for an LLM.
//
// The simulator already knows far more than "which nodes were reached" — it
// distinguishes completed from skipped nodes, records each node's actual
// output, and evaluates the workflow's declared outputs. Without that, a failed
// routing assertion is undebuggable: "handle_question was not completed" reads
// identically whether the node was skipped by a false condition or never
// scheduled at all, and those have completely different fixes.
//
// This only changes the text returned to the model. The persisted JSON
// (result.ToJSON, read by the UI) is untouched.
func formatScenarioResultInternal(result *simulator.ScenarioResult) string {
	var sb strings.Builder
	exec := &result.Execution

	status := string(result.Status)
	switch result.Status {
	case simulator.StatusPassed:
		status = "✓ PASSED"
	case simulator.StatusFailed:
		status = "✗ FAILED"
	case simulator.StatusError:
		status = "⚠ ERROR"
	}

	fmt.Fprintf(&sb, "## Scenario: %s — %s\n\n", result.Scenario, status)

	// Outcome, expected vs actual. The expectation is only worth printing when
	// it disagrees with what happened.
	expectedOutcome := ""
	if result.Expected != nil {
		expectedOutcome = string(result.Expected.Outcome)
	}
	if expectedOutcome != "" && expectedOutcome != exec.Outcome {
		fmt.Fprintf(&sb, "outcome: %s (expected %s) · %dms\n", exec.Outcome, expectedOutcome, exec.DurationMs)
	} else {
		fmt.Fprintf(&sb, "outcome: %s · %dms\n", exec.Outcome, exec.DurationMs)
	}

	// The single most useful missing signal: which nodes ran vs which were
	// scheduled and skipped by a false condition.
	fmt.Fprintf(&sb, "completed: %s\n", joinNodesOrNone(exec.NodesCompleted))
	if len(exec.NodesSkipped) > 0 {
		fmt.Fprintf(&sb, "skipped:   %s\n", joinNodesOrNone(exec.NodesSkipped))
	}
	if errored := scenarioNodesInState(exec, simulator.StateError); len(errored) > 0 {
		fmt.Fprintf(&sb, "errored:   %s\n", joinNodesOrNone(errored))
	}
	// Anything scheduled but classified as neither completed nor skipped would
	// otherwise vanish from the summary, which is exactly the ambiguity this
	// formatter exists to remove.
	if other := scenarioUnaccountedNodes(exec); len(other) > 0 {
		fmt.Fprintf(&sb, "reached:   %s\n", joinNodesOrNone(other))
	}

	if exec.Error != nil {
		fmt.Fprintf(&sb, "\n### Error at %s\n", exec.Error.Node)
		if exec.Error.Step != "" {
			fmt.Fprintf(&sb, "step: %s\n", exec.Error.Step)
		}
		fmt.Fprintf(&sb, "%s\n", exec.Error.Message)
		if exec.Error.Expression != "" {
			fmt.Fprintf(&sb, "expression: `%s`\n", exec.Error.Expression)
		}
	}

	// Warnings print even on a PASS. A run that passed while black-boxing a
	// sub-workflow or defaulting a router is precisely the case where the
	// author needs telling, and by definition there are no mismatches to carry
	// the message.
	if len(result.Warnings) > 0 {
		sb.WriteString("\n### Warnings — this run may not have tested what you think\n")
		for _, w := range result.Warnings {
			fmt.Fprintf(&sb, "- %s\n", w)
		}
	}

	// On success there is nothing left to say — stop here and leave the
	// context window to the workflow itself.
	if len(result.Mismatches) == 0 && exec.Error == nil {
		return sb.String()
	}

	if len(result.Mismatches) > 0 {
		sb.WriteString("\n### Failed expectations\n")
		for _, m := range result.Mismatches {
			fmt.Fprintf(&sb, "- %s\n", annotateMismatchWithNodeState(m, exec))
		}
	}

	if len(exec.WorkflowOutputs) > 0 {
		sb.WriteString("\n### Workflow outputs\n")
		for _, key := range sortedKeys(exec.WorkflowOutputs) {
			fmt.Fprintf(&sb, "- %s: %s\n", key, truncateScenarioValue(exec.WorkflowOutputs[key]))
		}
	}

	if len(exec.NodeOutputs) > 0 {
		sb.WriteString("\n### Node outputs\n")
		nodes := sortedNodeOutputKeys(exec.NodeOutputs)
		shown := nodes
		if len(shown) > scenarioTotalOutputBudget {
			shown = shown[:scenarioTotalOutputBudget]
		}
		for _, node := range shown {
			fmt.Fprintf(&sb, "- %s: %s\n", node, truncateScenarioValue(exec.NodeOutputs[node]))
		}
		if len(nodes) > len(shown) {
			fmt.Fprintf(&sb, "- … %d more node(s) truncated\n", len(nodes)-len(shown))
		}
	}

	return sb.String()
}

// annotateMismatchWithNodeState appends the actual execution state of any node
// the mismatch names. The engine's messages quote node ids, so a mismatch about
// a node that was skipped gets "(handle_question was skipped)" attached —
// turning "it wasn't completed" into an actionable statement about which
// condition to look at.
func annotateMismatchWithNodeState(mismatch string, exec *simulator.ExecutionDetails) string {
	if len(exec.NodeStates) == 0 {
		return mismatch
	}

	var notes []string
	seen := make(map[string]bool)
	for _, node := range sortedStateKeys(exec.NodeStates) {
		if seen[node] || !strings.Contains(mismatch, `"`+node+`"`) {
			continue
		}
		seen[node] = true
		notes = append(notes, fmt.Sprintf("%s was %s", node, exec.NodeStates[node]))
	}
	if len(notes) == 0 {
		return mismatch
	}
	return fmt.Sprintf("%s [%s]", mismatch, strings.Join(notes, "; "))
}

// scenarioNodesInState collects nodes sitting in a given execution state.
func scenarioNodesInState(exec *simulator.ExecutionDetails, want simulator.NodeExecutionState) []string {
	var nodes []string
	for _, node := range sortedStateKeys(exec.NodeStates) {
		if exec.NodeStates[node] == want {
			nodes = append(nodes, node)
		}
	}
	return nodes
}

// scenarioUnaccountedNodes returns nodes that were scheduled but appear in
// neither the completed nor the skipped list.
func scenarioUnaccountedNodes(exec *simulator.ExecutionDetails) []string {
	accounted := make(map[string]bool, len(exec.NodesCompleted)+len(exec.NodesSkipped))
	for _, node := range exec.NodesCompleted {
		accounted[node] = true
	}
	for _, node := range exec.NodesSkipped {
		accounted[node] = true
	}
	for node, state := range exec.NodeStates {
		if state == simulator.StateError {
			accounted[node] = true
		}
	}

	var rest []string
	for _, node := range exec.NodesReached {
		if !accounted[node] {
			accounted[node] = true
			rest = append(rest, node)
		}
	}
	return rest
}

// truncateScenarioValue renders a value as compact JSON, clipped to a budget
// with an explicit marker so the model knows the value continues rather than
// concluding the field is genuinely short.
func truncateScenarioValue(value interface{}) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		encoded = []byte(fmt.Sprintf("%v", value))
	}
	text := string(encoded)
	if len(text) <= scenarioNodeOutputBudget {
		return text
	}
	return fmt.Sprintf("%s… (truncated, %d chars total)", text[:scenarioNodeOutputBudget], len(text))
}

func joinNodesOrNone(nodes []string) string {
	if len(nodes) == 0 {
		return "(none)"
	}
	return strings.Join(nodes, ", ")
}

func sortedKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedNodeOutputKeys(m map[string]map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedStateKeys(m map[string]simulator.NodeExecutionState) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
