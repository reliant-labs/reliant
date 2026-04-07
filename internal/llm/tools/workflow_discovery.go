// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"gopkg.in/yaml.v3"
)

// =============================================================================
// LIST WORKFLOWS TOOL
// =============================================================================

type ListWorkflowsParams struct {
	// No user-facing parameters — always returns all workflows from all sources.
}

type listWorkflowsTool struct {
	repo db.Repository
}

const (
	ListWorkflowsToolName        = "list_workflows"
	listWorkflowsToolDescription = `Lists all available workflows (builtin, project, and user-created).

WHEN TO USE:
- To discover available workflows
- To find workflow patterns for common use cases
- Before using get_workflow to view details

RETURNS:
List of workflow names with descriptions and source (builtin, project, or user).`
)

func NewListWorkflowsTool(repo db.Repository) Tool {
	tool := &listWorkflowsTool{repo: repo}
	return NewToolWrapper[ListWorkflowsParams, ToolResponse](tool)
}

func (t *listWorkflowsTool) Name() string {
	return ListWorkflowsToolName
}

func (t *listWorkflowsTool) Description() string {
	return listWorkflowsToolDescription
}

func (t *listWorkflowsTool) RequiresPermission(args ListWorkflowsParams) (bool, error) {
	return false, nil
}

func (t *listWorkflowsTool) Execute(ctx *rctx.ToolContext, args ListWorkflowsParams) (ToolResponse, error) {
	source := "all"

	type workflowInfo struct {
		name        string
		description string
		source      string
		isValid     bool
	}
	var workflows []workflowInfo

	// Get builtin workflows
	if source == "all" || source == "builtin" {
		entries, err := builtin.BuiltinWorkflowsFS.ReadDir(".")
		if err == nil {
			for _, entry := range entries {
				if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
					continue
				}

				name := strings.TrimSuffix(entry.Name(), ".yaml")
				if builtin.IsInternalWorkflow(name) {
					continue
				}

				data, err := builtin.BuiltinWorkflowsFS.ReadFile(entry.Name())
				if err != nil {
					continue
				}

				var wf struct {
					Name        string `yaml:"name"`
					Description string `yaml:"description"`
				}
				if err := yaml.Unmarshal(data, &wf); err != nil {
					continue
				}

				desc := wf.Description
				if desc == "" {
					desc = "(no description)"
				}

				workflows = append(workflows, workflowInfo{
					name:        wf.Name,
					description: desc,
					source:      "builtin",
					isValid:     true,
				})
			}
		}
	}

	// Get project workflows (from .reliant/workflows/ synced via daemon)
	if (source == "all" || source == "project") && t.repo != nil {
		if ctx.Project != nil && ctx.Project.ID != "" {
			record, err := t.repo.GetProjectConfigRecord(ctx, ctx.Project.ID)
			if err == nil && record != nil {
				storedWorkflows, err := config.ParseStoredWorkflows(record.ProjectWorkflowsJSON)
				if err == nil {
					for _, sw := range storedWorkflows {
						var wf struct {
							Name        string `yaml:"name"`
							Description string `yaml:"description"`
						}
						if err := yaml.Unmarshal([]byte(sw.YAMLContent), &wf); err != nil {
							continue
						}

						desc := wf.Description
						if desc == "" {
							desc = "(no description)"
						}

						workflows = append(workflows, workflowInfo{
							name:        sw.Slug,
							description: desc,
							source:      "project",
							isValid:     true,
						})
					}
				}
			}
		}
	}

	// Get user's usable workflows (valid and not hidden)
	if (source == "all" || source == "user") && t.repo != nil {
		userID, ok := auth.GetUserIDFromContext(ctx)
		if ok && userID != "" {
			userWorkflows, err := t.repo.ListUsableWorkflowsByUser(ctx, userID)
			if err == nil {
				for _, wf := range userWorkflows {
					desc := "(no description)"
					if wf.Description != nil && *wf.Description != "" {
						desc = *wf.Description
					}

					workflows = append(workflows, workflowInfo{
						name:        wf.Slug,
						description: desc,
						source:      "user",
						isValid:     wf.IsValid,
					})
				}
			}
		}
	}

	// Sort by name
	sort.Slice(workflows, func(i, j int) bool {
		return workflows[i].name < workflows[j].name
	})

	var sb strings.Builder
	sb.WriteString("# Available Workflows\n\n")

	if len(workflows) == 0 {
		sb.WriteString("No workflows found.\n")
		return NewTextResponse(sb.String()), nil
	}

	sb.WriteString("| Workflow | Source | Valid | Description |\n")
	sb.WriteString("|----------|--------|-------|-------------|\n")
	for _, wf := range workflows {
		desc := wf.description
		if len(desc) > 200 {
			desc = desc[:197] + "..."
		}
		valid := "✓"
		if !wf.isValid {
			valid = "✗"
		}
		fmt.Fprintf(&sb, "| `%s` | %s | %s | %s |\n", wf.name, wf.source, valid, desc)
	}

	sb.WriteString("\nUse `get_workflow(name=\"...\")` to view the full workflow YAML.\n")

	return NewTextResponse(sb.String()), nil
}

// =============================================================================
// GET WORKFLOW TOOL
// =============================================================================

type GetWorkflowParams struct {
	ID string `json:"id" jsonschema:"required,description=Workflow draft UUID"`
}

type getWorkflowTool struct {
	repo db.Repository
}

const (
	GetWorkflowToolName        = "get_workflow"
	getWorkflowToolDescription = `Gets the full YAML definition of a workflow draft.

WHEN TO USE:
- To view the current state of the workflow you're editing
- Before making edits to understand the structure

PARAMETERS:
- id: (required) Workflow draft UUID

RETURNS:
The complete workflow YAML definition with validation status, version, and timestamps.
Use the version for conflict detection in edit_workflow/write_workflow.`
)

func NewGetWorkflowTool(repo db.Repository) Tool {
	tool := &getWorkflowTool{repo: repo}
	return NewToolWrapper[GetWorkflowParams, ToolResponse](tool)
}

func (t *getWorkflowTool) Name() string {
	return GetWorkflowToolName
}

func (t *getWorkflowTool) Description() string {
	return getWorkflowToolDescription
}

func (t *getWorkflowTool) RequiresPermission(args GetWorkflowParams) (bool, error) {
	return false, nil
}

func (t *getWorkflowTool) Execute(ctx *rctx.ToolContext, args GetWorkflowParams) (ToolResponse, error) {
	if t.repo == nil {
		return NewTextErrorResponse("This tool requires a database connection and is not available in daemon-only mode"), nil
	}

	if args.ID == "" {
		return NewTextErrorResponse("id parameter is required"), nil
	}

	draft, err := t.repo.GetWorkflowDraft(ctx, args.ID)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to get workflow draft: %v", err)), nil
	}
	if draft == nil {
		return NewTextErrorResponse(fmt.Sprintf("Workflow draft %s not found", args.ID)), nil
	}
	return formatWorkflowDraftResponse(draft)
}

func formatWorkflowDraftResponse(draft *db.WorkflowDraft) (ToolResponse, error) {
	var sb strings.Builder

	fmt.Fprintf(&sb, "# Workflow: %s\n\n", draft.Name)
	fmt.Fprintf(&sb, "**ID:** `%s`\n", draft.ID)
	if draft.IsValid {
		sb.WriteString("**Status:** valid\n")
	} else {
		sb.WriteString("**Status:** has errors\n")
	}
	if draft.IsHidden {
		sb.WriteString("**Visibility:** hidden\n")
	}
	if draft.Description != nil && *draft.Description != "" {
		fmt.Fprintf(&sb, "**Description:** %s\n", *draft.Description)
	}

	// Validate the workflow
	_, validationErr := v2.ParseWorkflowProtoBytes([]byte(draft.Definition))
	if validationErr != nil {
		fmt.Fprintf(&sb, "\n**⚠ Validation Errors:**\n```\n%s\n```\n", validationErr.Error())
	} else {
		sb.WriteString("**Validation:** ✓ Valid\n")
	}

	sb.WriteString("\n```yaml\n")
	sb.WriteString(draft.Definition)
	if !strings.HasSuffix(draft.Definition, "\n") {
		sb.WriteString("\n")
	}
	sb.WriteString("```\n\n")
	sb.WriteString("---\n")
	fmt.Fprintf(&sb, "**Updated at:** `%s`\n", draft.UpdatedAt.Format(time.RFC3339))
	sb.WriteString("\nUse `edit_workflow` for small changes or `write_workflow` to replace entirely.\n")

	return NewTextResponse(sb.String()), nil
}

// =============================================================================
// GET WORKFLOW SUGGESTIONS TOOL
// =============================================================================

type GetWorkflowSuggestionsParams struct {
	// No parameters - returns static suggestions
}

type getWorkflowSuggestionsTool struct{}

const (
	GetWorkflowSuggestionsToolName        = "get_workflow_suggestions"
	getWorkflowSuggestionsToolDescription = `Returns static design suggestions for building workflows.

WHEN TO USE:
- Before starting a new workflow design
- When encountering complexity or unexpected behavior
- To learn best practices for edges, joins, loops, and conditions

RETURNS:
Markdown document with categorized suggestions covering:
- Structure and organization
- Edge routing patterns
- Node vs edge conditions
- Join behavior with conditional sources
- Loop patterns and outputs
- Testing strategies

Note: These are static suggestions. Future calls yield the same results.`
)

func NewGetWorkflowSuggestionsTool() Tool {
	tool := &getWorkflowSuggestionsTool{}
	return NewToolWrapper[GetWorkflowSuggestionsParams, ToolResponse](tool)
}

func (t *getWorkflowSuggestionsTool) Name() string {
	return GetWorkflowSuggestionsToolName
}

func (t *getWorkflowSuggestionsTool) Description() string {
	return getWorkflowSuggestionsToolDescription
}

func (t *getWorkflowSuggestionsTool) RequiresPermission(args GetWorkflowSuggestionsParams) (bool, error) {
	return false, nil
}

func (t *getWorkflowSuggestionsTool) Execute(ctx *rctx.ToolContext, args GetWorkflowSuggestionsParams) (ToolResponse, error) {
	suggestions := `# Workflow Design Suggestions

> **Note:** These are static suggestions. Future calls to this tool will yield the same results.

---

## Structure & Organization

### Break Up Large Workflows
- **Large inline workflows (>8 nodes)** become hard to test and reason about
- Extract complex sections into separate sub-workflows

### Sub-Workflow Options
| Approach | Pros | Cons |
|----------|------|------|
| **Inline** (` + "`inline:`" + `) | Access root ` + "`inputs.*`" + ` directly, tightly coupled | Harder to test in isolation |
| **Referenced** (` + "`ref:`" + `) | Reusable, testable, explicit contract | Must plumb args explicitly |

- Workflows can **nest arbitrarily deep** - extract complexity into layers
- Use **presets** when multiple nodes share the same agent configuration

---

## Edge Routing

### Parallel Execution
` + "```yaml" + `
# ✅ CORRECT: Separate edge blocks for parallel
- from: fanout
  default: task_a
- from: fanout
  default: task_b
- from: fanout
  default: task_c

# ❌ WRONG: Multiple cases = sequential (first-match-wins)
- from: fanout
  cases:
    - to: task_a  # Only this one triggers!
    - to: task_b
    - to: task_c
` + "```" + `

### Key Principles
- Edge cases use **first-match-wins** (like a switch statement)
- Edge ` + "`default`" + ` fires when **NO case matches** - be explicit
- Prefer **node ` + "`condition`" + `** over complex edge routing for skip logic

---

## Node Conditions vs Edge Conditions

| Use | For |
|-----|-----|
| **Node ` + "`condition`" + `** | "Should this step execute?" - simpler, self-contained |
| **Edge ` + "`condition`" + `** | "Where should we route based on output?" - true branching |

` + "```yaml" + `
# Node condition - step decides if it runs
- id: lint
  type: run
  condition: "'lint' in inputs.steps"  # Self-filtering
  command: npm run lint

# Edge condition - route based on output
- from: validate
  cases:
    - to: success
      condition: "nodes.validate.exit_code == 0"
  default: failure
` + "```" + `

---

## Joins

### Critical: Conditional Sources

` + "```yaml" + `
# ✅ CORRECT: Use node condition for optional join sources
- id: lint
  type: run
  condition: "'lint' in inputs.steps"  # Skipped nodes auto-satisfy join

- id: join_checks
  type: join
  condition: all  # Works correctly - skipped lint counts as satisfied
` + "```" + `

` + "```yaml" + `
# ❌ WRONG: Edge conditions for optional join sources
- from: fanout
  cases:
    - to: lint
      condition: "'lint' in inputs.steps"  # If false, join waits forever!
` + "```" + `

### Join Conditions
- ` + "`condition: all`" + ` - Wait for all sources (skipped via node condition = satisfied)
- ` + "`condition: any`" + ` - Fire when first source completes

---

## Loops

### Define Explicit Outputs
The ` + "`while`" + ` condition evaluates the inline workflow's ` + "`outputs`" + `:

` + "```yaml" + `
- id: retry_loop
  type: loop
  while: "outputs.failed == true && iter.iteration < inputs.max_retries"
  inline:
    outputs:
      failed: "{{nodes.verify.exit_code != 0}}"  # Explicit output for while
    nodes:
      - id: attempt
        ...
      - id: verify
        type: run
        command: npm test
` + "```" + `

### Tips
- ` + "`iter.iteration`" + ` is **0-indexed** - use ` + "`iter.iteration + 1`" + ` for display
- Complex retry logic? Extract the retry target to a **referenced workflow**
- Test loop termination explicitly - what outputs make ` + "`while`" + ` become false?

---

## Testing with Scenarios

### Coverage Strategy
- Test **each conditional path combination**
- Parallel execution **multiplies test cases** - test with different enabled steps
- Test **loop termination** explicitly

### Scenario Tips
- Use ` + "`node:`" + ` field to target specific nodes (including inside loops: ` + "`loop_id.node_id`" + `)
- Mock validation nodes with ` + "`exit_code: 0`" + ` or ` + "`exit_code: 1`" + `
- Check ` + "`reached`" + ` and ` + "`not_reached`" + ` to verify routing

---

## Quick Reference

| Problem | Solution |
|---------|----------|
| Nodes share same agent config | Use presets |
| Large inline workflow | Extract to referenced sub-workflow |
| Need parallel execution | Separate edge blocks from same source |
| Optional step in a join | Use node ` + "`condition`" + `, not edge condition |
| Loop doesn't terminate | Define explicit ` + "`outputs`" + ` for ` + "`while`" + ` |
| Complex skip logic | Prefer node ` + "`condition`" + ` over edge routing |
`

	return NewTextResponse(suggestions), nil
}
