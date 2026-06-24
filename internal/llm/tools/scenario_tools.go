// Copyright (c) 2025 Reliant Labs
package tools

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	cfg "github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/simulator"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"gopkg.in/yaml.v3"
)

// =============================================================================
// SHARED TYPES FOR SCENARIOS
// =============================================================================
//
// These types mirror the simulator types but include jsonschema tags for LLM tool interfaces.
// The scenario system is type-agnostic - Output is passed through directly to nodes.
// =============================================================================

// SimulatedEventParam represents a single event in the simulation.
// The Output field contains the mock output that will be returned by the node,
// matching the activity's output structure directly (no translation needed).
type SimulatedEventParam struct {
	// Node targets a specific node (supports qualified IDs for inner loops and workflows).
	// Use dot-notation: "loop_id.inner_node_id" or "outer.inner.node_id".
	// If omitted, events are applied sequentially.
	Node string `json:"node,omitempty" yaml:"node,omitempty" jsonschema:"description=Target specific node. Use dot-notation for inner loops and workflows: loop_id.inner_node_id. If omitted events are applied sequentially."`

	// Output is the mock output returned by the node.
	// This should match the activity's output structure directly:
	//   - call_llm: {message: {role text} response_text tool_calls input_tokens ...}
	//   - execute_tools: {message tool_results thread_token_count response_data ...}
	//   - approval: {approval_id status action_taken data}
	//   - run: {exit_code stdout stderr}
	Output map[string]interface{} `json:"output" yaml:"output" jsonschema:"required,description=Mock output returned by the node. Must match the activity output structure (e.g. call_llm needs message/response_text/tool_calls)."`
}

// ExpectationParam defines expected outcomes for scenario validation.
type ExpectationParam struct {
	Outcome       string                            `json:"outcome,omitempty" yaml:"outcome,omitempty" jsonschema:"description=Expected outcome: completed or error"`
	Reached       []string                          `json:"reached,omitempty" yaml:"reached,omitempty" jsonschema:"description=Nodes that should be executed. Supports qualified IDs (e.g. loop_id.inner_node_id)"`
	NotReached    []string                          `json:"not_reached,omitempty" yaml:"not_reached,omitempty" jsonschema:"description=Nodes that should NOT be executed"`
	ErrorContains string                            `json:"error_contains,omitempty" yaml:"error_contains,omitempty" jsonschema:"description=Substring that should appear in error message"`
	ErrorNode     string                            `json:"error_node,omitempty" yaml:"error_node,omitempty" jsonschema:"description=Node where error should occur"`
	NodeOutputs   map[string]map[string]interface{} `json:"node_outputs,omitempty" yaml:"node_outputs,omitempty" jsonschema:"description=Expected output values for specific nodes"`
}

// ScenarioParam represents a scenario definition for testing workflows.
type ScenarioParam struct {
	Name        string                            `json:"name" yaml:"name" jsonschema:"required,description=Human-readable scenario name"`
	ApiVersion  string                            `json:"apiVersion,omitempty" yaml:"apiVersion,omitempty" jsonschema:"description=Schema version for the scenario format"`
	Description string                            `json:"description,omitempty" yaml:"description,omitempty" jsonschema:"description=What this scenario tests"`
	Events      []SimulatedEventParam             `json:"events" yaml:"events" jsonschema:"required,description=Sequence of simulated events with mock outputs"`
	Expect      *ExpectationParam                 `json:"expect,omitempty" yaml:"expect,omitempty" jsonschema:"description=Expected outcome and assertions"`
	Inputs      map[string]interface{}            `json:"inputs,omitempty" yaml:"inputs,omitempty" jsonschema:"description=Override workflow inputs for this scenario"`
	StartAt     string                            `json:"start_at,omitempty" yaml:"start_at,omitempty" jsonschema:"description=Start execution at a specific node (for partial testing)"`
	State       map[string]map[string]interface{} `json:"state,omitempty" yaml:"state,omitempty" jsonschema:"description=Pre-populate node outputs (for partial testing)"`
}

// =============================================================================
// LIST SCENARIOS TOOL
// =============================================================================

type ListScenariosParams struct {
	ID string `json:"id" jsonschema:"required,description=Workflow draft UUID"`
}

type listScenariosTool struct {
	repo db.Repository
}

const (
	ListScenariosToolName        = "list_scenarios"
	listScenariosToolDescription = `List all test scenarios for the current workflow.

Returns a summary of each scenario including name, description, and last run status.
Use this to see what scenarios exist and their current state.

No parameters needed - the workflow is determined from the current chat context.`
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

	draft, err := t.repo.GetWorkflowDraft(ctx, args.ID)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to get workflow draft: %v", err)), nil
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
	ID   string `json:"id" jsonschema:"required,description=Workflow draft UUID"`
	Name string `json:"name" jsonschema:"required,description=Name of the scenario to view"`
}

type viewScenarioTool struct {
	repo db.Repository
}

const (
	ViewScenarioToolName        = "view_scenario"
	viewScenarioToolDescription = `View a specific test scenario's full definition.

Returns the complete scenario YAML including events, expectations, and last run results.
Use this to examine a scenario's configuration or debug test failures.`
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

	draft, err := t.repo.GetWorkflowDraft(ctx, args.ID)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to get workflow draft: %v", err)), nil
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
	ID              string `json:"id" jsonschema:"required,description=Workflow draft UUID"`
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

	draft, err := t.repo.GetWorkflowDraft(ctx, args.ID)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to get workflow draft: %v", err)), nil
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
	ID              string `json:"id" jsonschema:"required,description=Workflow draft UUID"`
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
  outcome: completed         # or "error"
  reached: ["node1", "node2"]
  not_reached: ["node3"]

**Targeting nodes:**
- Top-level nodes: node: "call_llm"
- Inner loop nodes: node: "agent_loop.call_llm" (dot-separated)
- Nested loops: node: "outer_loop.inner_loop.call_llm"

**Example:**
{
  "id": "workflow-uuid",
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

	draft, err := t.repo.GetWorkflowDraft(ctx, args.ID)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to get workflow draft: %v", err)), nil
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
	ID   string `json:"id" jsonschema:"required,description=Workflow draft UUID"`
	Name string `json:"name" jsonschema:"required,description=Name of the scenario to delete"`
}

type deleteScenarioTool struct {
	repo db.Repository
}

const (
	DeleteScenarioToolName        = "delete_scenario"
	deleteScenarioToolDescription = `Delete a test scenario.

Permanently removes the scenario from the workflow.`
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

	draft, err := t.repo.GetWorkflowDraft(ctx, args.ID)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to get workflow draft: %v", err)), nil
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
	ID   string `json:"id" jsonschema:"required,description=Workflow draft UUID"`
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

Use list_scenarios to see available scenario names.`
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

	draft, err := t.repo.GetWorkflowDraft(ctx, args.ID)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to get workflow draft: %v", err)), nil
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

		if strings.HasPrefix(workflowRef, "project://") {
			workflowRef = strings.TrimPrefix(workflowRef, "project://")
		}

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

// dbScenarioToSimulatorInternal converts a database scenario to simulator types
func dbScenarioToSimulatorInternal(s *db.WorkflowScenario) (*simulator.Scenario, error) {
	var scenarioParam ScenarioParam
	if err := yaml.Unmarshal([]byte(s.Events), &scenarioParam); err != nil {
		return nil, fmt.Errorf("failed to parse scenario YAML: %w", err)
	}
	return convertScenarioParam(&scenarioParam), nil
}

// formatScenarioResultInternal formats the result for display
func formatScenarioResultInternal(result *simulator.ScenarioResult) string {
	var status string
	switch result.Status {
	case simulator.StatusPassed:
		status = "✓ PASSED"
	case simulator.StatusFailed:
		status = "✗ FAILED"
	case simulator.StatusError:
		status = "⚠ ERROR"
	}

	output := fmt.Sprintf("## Scenario: %s\n\n", result.Scenario)
	output += fmt.Sprintf("**Status:** %s\n\n", status)

	output += "### Execution\n"
	output += fmt.Sprintf("- **Outcome:** %s\n", result.Execution.Outcome)
	output += fmt.Sprintf("- **Nodes reached:** %v\n", result.Execution.NodesReached)
	output += fmt.Sprintf("- **Duration:** %dms\n", result.Execution.DurationMs)

	if result.Execution.Error != nil {
		output += "\n### Error\n"
		output += fmt.Sprintf("- **Node:** %s\n", result.Execution.Error.Node)
		if result.Execution.Error.Step != "" {
			output += fmt.Sprintf("- **Step:** %s\n", result.Execution.Error.Step)
		}
		output += fmt.Sprintf("- **Message:** %s\n", result.Execution.Error.Message)
		if result.Execution.Error.Expression != "" {
			output += fmt.Sprintf("- **Expression:** `%s`\n", result.Execution.Error.Expression)
		}
	}

	if len(result.Mismatches) > 0 {
		output += "\n### Expectation Mismatches\n"
		for _, m := range result.Mismatches {
			output += fmt.Sprintf("- %s\n", m)
		}
	}

	return output
}

// convertScenarioParam converts tool params to simulator types.
// The conversion is straightforward since both use the same structure.
func convertScenarioParam(p *ScenarioParam) *simulator.Scenario {
	scenario := &simulator.Scenario{
		Name:        p.Name,
		ApiVersion:  p.ApiVersion,
		Description: p.Description,
		Inputs:      p.Inputs,
		StartAt:     p.StartAt,
		State:       p.State,
	}

	// Convert events (types match, just copy)
	for _, e := range p.Events {
		scenario.Events = append(scenario.Events, simulator.SimulatedEvent{
			Node:   e.Node,
			Output: e.Output,
		})
	}

	// Convert expectations
	if p.Expect != nil {
		scenario.Expect = &simulator.Expectation{
			Outcome:       simulator.ExpectedOutcome(p.Expect.Outcome),
			Reached:       p.Expect.Reached,
			NotReached:    p.Expect.NotReached,
			ErrorContains: p.Expect.ErrorContains,
			ErrorNode:     p.Expect.ErrorNode,
			NodeOutputs:   p.Expect.NodeOutputs,
		}
	}

	return scenario
}
