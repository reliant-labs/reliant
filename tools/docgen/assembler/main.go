// Copyright (c) 2025 Reliant Labs
//
// Workflow builder skill assembler - generates workflow-builder/SKILL.md.
//
// Auto-generates content from:
//   - internal/workflow/reference (populated from proto descriptors at init)
//   - internal/workflow/builtin/*.yaml (builtin workflow list)
//   - generated/docs-source/reference/workflow-schema.md (top-level schema)
//   - generated/docs-source/reference/scenario-schema.md (scenario testing schema)
//
// GENERATED FILE:
//   - internal/skills/catalog/builtin/workflow-builder/SKILL.md
//
// Usage: go run ./tools/docgen/assembler <docs_dir> <output_file>

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"github.com/reliant-labs/reliant/internal/workflow/reference"
	"gopkg.in/yaml.v3"
)

const skillFrontmatter = `---
name: workflow-builder
description: Build, edit, debug, and test Reliant workflows. Use when creating new workflows, modifying existing ones, writing scenario tests, or troubleshooting workflow execution issues.
compatibility: reliant
metadata:
  category: workflow
  owner: reliant
---`

// BuiltinWorkflow represents minimal workflow info for listing.
type BuiltinWorkflow struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "Usage: %s <docs_dir> <output_file>\n", os.Args[0])
		os.Exit(1)
	}

	docsDir := os.Args[1]
	outputFile := os.Args[2]

	skill, err := assembleSkill(docsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error assembling workflow-builder skill: %v\n", err)
		os.Exit(1)
	}

	if err := os.WriteFile(outputFile, []byte(skill), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing output: %v\n", err)
		os.Exit(1)
	}

	tokenEstimate := len(skill) / 4
	fmt.Printf("Generated workflow-builder skill: %s (~%d tokens)\n", outputFile, tokenEstimate)
}

func assembleSkill(docsDir string) (string, error) {
	sections := []string{
		generateHeader(),
		generateKeyConcepts(),
		generateSyntaxSugar(),
		generateGotchas(),
		generateAvailableTools(),
		generateCELQuickReference(),
	}

	builtins, err := generateBuiltinWorkflowsList()
	if err != nil {
		return "", fmt.Errorf("generating builtin workflows: %w", err)
	}
	sections = append(sections, builtins)

	sections = append(sections, generateNodeTypesSummary())
	sections = append(sections, generateInputTypesSummary())

	schemaPath, err := resolveReferenceFile(docsDir, "workflow-schema.md")
	if err != nil {
		return "", err
	}
	schema, err := extractTopLevelSchema(schemaPath)
	if err != nil {
		return "", fmt.Errorf("extracting schema: %w", err)
	}
	sections = append(sections, schema)

	scenarioPath, err := resolveReferenceFile(docsDir, "scenario-schema.md")
	if err != nil {
		return "", err
	}
	scenarios, err := extractScenarioEssentials(scenarioPath)
	if err != nil {
		return "", fmt.Errorf("extracting scenarios: %w", err)
	}
	sections = append(sections, scenarios)
	sections = append(sections, generateReferences())

	body := strings.Join(sections, "\n\n")
	return skillFrontmatter + "\n" + strings.TrimSpace(body) + "\n", nil
}

func generateHeader() string {
	return `# Workflow Builder

You build Reliant workflows. Your goal: create working workflows that solve the user's problem.

Threading, context, loops, parallelization, inputs, prompts, error handling and composition
have their own do's-and-don'ts reference: ` + "`skill(action=\"load\", path=\"workflow-builder/design-patterns\")`" + `.
Load it before designing a workflow's control flow.

## When to use

- Creating a new Reliant workflow from scratch
- Editing or extending an existing workflow
- Debugging workflow execution issues
- Writing scenario tests for workflows
- Understanding workflow patterns and best practices

## Approach

The workflow tools (` + "`get_workflow`" + `, ` + "`edit_workflow`" + `, ` + "`write_workflow`" + `, the scenario tools) all take an
optional ` + "`id`" + ` — a UUID, slug, or name. Omit it and they resolve to the workflow this chat is
editing; pass it only to target a different workflow. There is no draft ID injected into the
system message to look for.

Starting from scratch (no workflow exists yet)? Call ` + "`create_workflow`" + ` first — it returns the new
draft's ` + "`id`" + `, ` + "`name`" + `, and ` + "`slug`" + `. Omit both ` + "`name`" + ` and ` + "`content`" + ` to get a random name and the
default agent template; pass ` + "`content`" + ` with complete workflow YAML to start from a specific design.
After that, the chat is editing this draft, so ` + "`get_workflow`" + `/` + "`edit_workflow`" + `/` + "`write_workflow`" + ` need no
` + "`id`" + ` either.

Follow this process:

1. **Setup** — New workflow: call ` + "`create_workflow`" + `. Existing workflow: call ` + "`get_workflow()`" + ` (no id
   needed) to see current content.
2. **Understand** — Ask clarifying questions about the user's goal
3. **Learn** — Use ` + "`list_workflows`" + ` to see examples and patterns, ` + "`list_presets`" + `/` + "`get_preset`" + ` to see what
   agent presets exist before inventing a system prompt from scratch
4. **Explore** — Read the user's codebase (test commands, code patterns). Note: references to "workflows" and "nodes" in user code are unlikely to be Reliant-specific.
5. **Build** — Use ` + "`edit_workflow`" + ` for small changes, ` + "`write_workflow`" + ` for larger rewrites
6. **Test** — Create and run scenarios (aim for 3+ covering positive, negative, and edge cases). Try to break your workflow. It's frustrating for users to run a workflow for an hour and hit a bug at the end—scenarios catch this early.

Working in a chat that isn't running this preset? These tools are still reachable:
` + "`load_tool(query=\"workflow\")`" + ` loads them on demand.`
}

func generateKeyConcepts() string {
	return `## Key concepts

### Workflows are DAGs

Every workflow must be a directed acyclic graph. You cannot create cycles in the graph structure. Use **loop nodes** (with ` + "`while`" + ` conditions) for iterative behavior.

### CEL expressions

Two syntax modes:

- **` + "`condition`" + ` / ` + "`while`" + ` fields**: Pure CEL — no ` + "`{{}}`" + ` wrapping
  ` + "```yaml" + `
  condition: "nodes.llm.stop_reason == 'tool_use'"
  while: "outputs.stop_reason != 'end_turn' && iter.iteration < 50"
  ` + "```" + `
- **All other fields**: Template interpolation with ` + "`{{}}`" + `
  ` + "```yaml" + `
  model: "{{inputs.model}}"
  system_prompt: "You are helping with {{inputs.task}}"
  ` + "```" + `

Always use ` + "`has()`" + ` or safe navigation (` + "`object.?field`" + `) before accessing optional fields.

### Threading

Modes: ` + "`inherit`" + ` (default), ` + "`new`" + `, ` + "`fork`" + `

- **inherit**: Reuse parent thread (default)
- **new**: Create a fresh thread
- **fork**: Copy parent thread into a new one

Key rules:
- Use ` + "`memo`" + ` in loops to reuse the same thread across iterations
- Use ` + "`inject`" + ` to prepend a message when entering a sub-workflow
- **Never** run parallel agents on the same thread simultaneously

### Edge routing

- Multiple ` + "`cases`" + ` on one edge = exactly 1 executes (first match wins)
- All cases require a ` + "`condition`" + ` — use ` + "`default`" + ` for the fallback
- For parallelism: create multiple edges from the same source node, OR use ` + "`default: [node-a, node-b]`" + ``
}

func generateSyntaxSugar() string {
	return `## Syntactic sugar

These shorthands compile to standard nodes/edges at parse time. They are **not** in the proto — purely YAML convenience.

### ` + "`sequence:`" + ` sugar

A top-level workflow field (alongside ` + "`name:`" + `, ` + "`nodes:`" + `, ` + "`edges:`" + `) that replaces the combination of ` + "`entry:`" + `, ` + "`nodes:`" + `, and sequential ` + "`edges:`" + ` for linear chains.

Rules:
- Cannot coexist with ` + "`entry:`" + ` (it implies entry from the first node)
- CAN coexist with ` + "`nodes:`" + ` and ` + "`edges:`" + ` for mixed patterns (e.g., linear main flow with extra branches)
- Compiles to standard ` + "`entry:`" + ` + ` + "`nodes:`" + ` + ` + "`edges:`" + ` at parse time

Before (explicit):
` + "```yaml" + `
entry: [research]
nodes:
  - id: research
    type: workflow
    ref: builtin://agent
  - id: implement
    type: workflow
    ref: builtin://agent
  - id: review
    type: workflow
    ref: builtin://structured-agent
edges:
  - from: research
    default: implement
  - from: implement
    default: review
` + "```" + `

After (sugar):
` + "```yaml" + `
sequence:
  - id: research
    type: workflow
    ref: builtin://agent
  - id: implement
    type: workflow
    ref: builtin://agent
  - id: review
    type: workflow
    ref: builtin://structured-agent
` + "```" + `

### ` + "`type: parallel`" + ` sugar

A node type used inside ` + "`nodes:`" + ` that desugars into branch nodes + a join node + fan-out/fan-in edges.

Rules:
- Must have ` + "`id:`" + ` and ` + "`branches:`" + ` fields
- ` + "`branches:`" + ` is a list of regular node definitions
- The parallel node's ` + "`id`" + ` becomes the join node's id (so downstream edges referencing it still work)
- Join uses ` + "`condition: all`" + ` (all branches must complete)
- Incoming edges targeting the parallel node get rewritten to fan-out to all branches

Before (explicit):
` + "```yaml" + `
nodes:
  - id: research
    type: workflow
    ref: builtin://agent
  - id: design
    type: workflow
    ref: builtin://agent
  - id: explore
    type: join
    condition: all
edges:
  - from: trigger
    default: [research, design]
  - from: research
    default: explore
  - from: design
    default: explore
` + "```" + `

After (sugar):
` + "```yaml" + `
nodes:
  - id: explore
    type: parallel
    branches:
      - id: research
        type: workflow
        ref: builtin://agent
      - id: design
        type: workflow
        ref: builtin://agent
edges:
  - from: trigger
    default: explore
` + "```" + ``
}

func generateGotchas() string {
	return `## Important rules

### Parameters
- Set model as a param via a **tag** (flagship, moderate, cheap, etc.) — not a hardcoded model ID
- **NEVER** assume which models exist. Your training data may be stale, and users have different API keys.
- A workflow always runs in the context of a user thread. You never need to create input to consume the user's request.

### Agents and LLMs
- ` + "`CallLLM`" + ` is a single LLM call — it doesn't execute tools or loop
- Common patterns:
  - **Augmented agents**: Do additional work inside the agentic loop (e.g., auditing)
  - **Parallelization**: Multiple edges from the same source (not switch/case logic)
  - **Combining agents**: Multiple specialized agents with distinct tools/instructions
  - **Structured output**: Use response tools to produce output for conditional routing
- Combine patterns to create powerful workflows

### Loops
- ` + "`while`" + ` is **do-while**: the body always runs at least once
- ` + "`iter.iteration`" + ` is 0-indexed inside the loop body
- ` + "`outputs.*`" + ` in the ` + "`while`" + ` condition references the **current iteration's** outputs

### Response tools
- Force structured LLM output for routing/classification
- ` + "`builtin://agent`" + ` returns when no tool calls remain (use ` + "`ask: true`" + ` to prompt for user feedback first)
- ` + "`builtin://structured-agent`" + ` returns when the response tool is called (use ` + "`ask: true`" + ` to prompt for user feedback first)
- Access structured output via ` + "`nodes.<execute_tools_id>.response_data.<tool_name>`" + `

### Conditions on nodes
- Skipped nodes forward execution to the next edge target
- You **cannot** access outputs of skipped nodes
- Join nodes handle skipped inputs correctly`
}

func generateAvailableTools() string {
	return `## Available tools

Granted by ` + "`tag:workflow`" + ` — no ` + "`load_tool`" + ` needed in a workflow-building chat. All take the
optional ` + "`id`" + ` (UUID/slug/name) described above except ` + "`create_workflow`" + `, which mints one.

| Tool | Purpose |
|------|---------|
| ` + "`create_workflow`" + ` | Create a new workflow draft (returns id, name, slug) |
| ` + "`get_workflow`" + ` | Read current workflow content |
| ` + "`edit_workflow`" + ` | Make targeted edits to a workflow |
| ` + "`write_workflow`" + ` | Full workflow rewrite |
| ` + "`get_schema`" + ` | Get full field documentation for any node/input/shared type |
| ` + "`get_cel_reference`" + ` | Authoritative CEL reference (namespaces, functions, types) |
| ` + "`list_workflows`" + ` | Browse existing workflows for examples and patterns |
| ` + "`list_presets`" + ` | Discover available agent presets |
| ` + "`get_preset`" + ` | View a preset's full configuration |
| ` + "`write_scenario`" + ` | Create a test scenario |
| ` + "`run_scenario`" + ` | Execute a scenario and check results |
| ` + "`list_scenarios`" + ` | List existing scenarios for a workflow |
| ` + "`view_scenario`" + ` | Read a scenario's content |
| ` + "`edit_scenario`" + ` | Modify an existing scenario |
| ` + "`delete_scenario`" + ` | Remove a scenario |
| ` + "`get_workflow_suggestions`" + ` | Get AI-powered suggestions for workflow improvements |

Outside a workflow-building chat, these tools aren't preloaded, but ` + "`load_tool(query=\"workflow\")`" + `
is always available (it's in ` + "`tag:default`" + `) and loads the full set on demand.`
}

func generateCELQuickReference() string {
	var sb strings.Builder
	sb.WriteString("## CEL Reference\n\n")
	sb.WriteString("> Auto-generated from `internal/workflow/reference`. Use `get_cel_reference` for the complete authoritative reference.\n\n")

	sb.WriteString("### Syntax rules\n\n")
	sb.WriteString("| Context | Syntax | Example |\n")
	sb.WriteString("|---------|--------|--------|\n")
	sb.WriteString("| `condition`, `while` | Pure CEL (no `{{}}`) | `condition: \"nodes.llm.stop_reason == 'tool_use'\"` |\n")
	sb.WriteString("| All other fields | Template interpolation `{{}}` | `model: \"{{inputs.model}}\"` |\n\n")

	namespaces := append([]reference.CELNamespace(nil), reference.CELNamespaces...)
	sort.Slice(namespaces, func(i, j int) bool { return namespaces[i].Name < namespaces[j].Name })

	sb.WriteString("### Namespaces\n\n")
	sb.WriteString("| Namespace | Description | Fields |\n")
	sb.WriteString("|-----------|-------------|--------|\n")
	for _, ns := range namespaces {
		fields := "workflow-specific"
		if len(ns.Fields) > 0 {
			fieldNames := make([]string, 0, len(ns.Fields))
			for _, field := range ns.Fields {
				fieldNames = append(fieldNames, "`"+field.Name+"`")
			}
			fields = strings.Join(fieldNames, ", ")
		}
		fmt.Fprintf(&sb, "| `%s.*` | %s | %s |\n", tableCell(ns.Name), tableCell(ns.Description), tableCell(fields))
	}
	sb.WriteString("\n")

	for _, ns := range namespaces {
		if len(ns.Fields) == 0 {
			continue
		}
		fmt.Fprintf(&sb, "#### `%s` fields\n\n", ns.Name)
		sb.WriteString("| Field | Type | Description |\n")
		sb.WriteString("|-------|------|-------------|\n")
		fields := append([]reference.CELField(nil), ns.Fields...)
		sort.Slice(fields, func(i, j int) bool { return fields[i].Name < fields[j].Name })
		for _, field := range fields {
			fmt.Fprintf(&sb, "| `%s.%s` | `%s` | %s |\n", tableCell(ns.Name), tableCell(field.Name), tableCell(field.Type), tableCell(field.Description))
		}
		sb.WriteString("\n")
	}

	functions := append([]reference.CELFunction(nil), reference.CELFunctions...)
	sort.Slice(functions, func(i, j int) bool { return functions[i].Name < functions[j].Name })

	sb.WriteString("### Key functions\n\n")
	sb.WriteString("| Function | Description | Example |\n")
	sb.WriteString("|----------|-------------|--------|\n")
	for _, fn := range functions {
		signature := fn.Signature
		if signature == "" {
			signature = fn.Name
		}
		fmt.Fprintf(&sb, "| `%s` | %s | `%s` |\n", tableCell(signature), tableCell(fn.Description), tableCell(fn.Example))
	}
	sb.WriteString("\n")

	sb.WriteString(`### Common patterns

` + "```yaml" + `
# Check if LLM wants to use tools
condition: "nodes.llm.stop_reason == 'tool_use'"

# Agentic loop — keep going until LLM stops calling tools
while: "outputs.stop_reason != 'end_turn' && iter.iteration < 50"

# Dynamic model from input
model: "{{inputs.model}}"

# Safe field access with has()
condition: "has(nodes.classify) && nodes.classify.response_data.route.category == 'urgent'"

# Safe navigation with ?.
condition: "nodes.result.?error_message == null"

# Parse JSON from command output in an interpolated field
result: "{{parseJson(nodes.run.stdout)}}"

# Coalesce to provide defaults
name: "{{coalesce(inputs.name, 'anonymous')}}"
` + "```" + `

### Null safety

Always guard optional field access:

` + "```yaml" + `
# Good — use has()
condition: "has(nodes.check) && nodes.check.exit_code == 0"

# Good — safe navigation
condition: "nodes.result.?data != null"

# Bad — errors if node was skipped or field doesn't exist
condition: "nodes.check.exit_code == 0"
` + "```")

	return sb.String()
}

func generateBuiltinWorkflowsList() (string, error) {
	var sb strings.Builder
	sb.WriteString("## Builtin Workflows\n\n")
	sb.WriteString("> Auto-generated from `internal/workflow/builtin/*.yaml`. Reference via `builtin://<name>` in workflow nodes.\n\n")
	sb.WriteString("| Name | Description |\n")
	sb.WriteString("|------|-------------|\n")

	entries, err := builtin.BuiltinWorkflowsFS.ReadDir(".")
	if err != nil {
		return "", err
	}

	var workflows []BuiltinWorkflow
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}

		data, err := builtin.BuiltinWorkflowsFS.ReadFile(entry.Name())
		if err != nil {
			continue
		}

		var wf BuiltinWorkflow
		if err := yaml.Unmarshal(data, &wf); err != nil {
			continue
		}

		if wf.Name == "" {
			continue
		}
		wf.Description = firstParagraph(wf.Description)
		workflows = append(workflows, wf)
	}

	sort.Slice(workflows, func(i, j int) bool {
		return workflows[i].Name < workflows[j].Name
	})

	for _, wf := range workflows {
		fmt.Fprintf(&sb, "| `%s` | %s |\n", tableCell(wf.Name), tableCell(wf.Description))
	}

	return sb.String(), nil
}

func generateNodeTypesSummary() string {
	var sb strings.Builder
	sb.WriteString("## Node Types\n\n")
	sb.WriteString("> Auto-generated from `reference.ListNodeTypes()` / `reference.GetNodeType()`. Use `get_schema(name=\"<type>\")` for full field documentation.\n\n")

	sb.WriteString("### Common Fields (all nodes)\n\n")
	sb.WriteString("| Field | Type | Description |\n")
	sb.WriteString("|-------|------|-------------|\n")
	sb.WriteString("| `type` | string | Node type (required) |\n")
	sb.WriteString("| `id` | string | Unique node ID (required) |\n")
	sb.WriteString("| `condition` | CEL | Skip if false; on join nodes: `all` or `any` |\n")
	sb.WriteString("| `thread` | ThreadConfig | Thread mode: `inherit`, `new`, `fork` |\n")
	sb.WriteString("| `timeout` | string | Override timeout (e.g., `5m`) |\n")
	sb.WriteString("| `save_message` | SaveMessageConfig | Auto-save message after completion |\n\n")

	sb.WriteString("### Available Types\n\n")
	sb.WriteString("| Type | Description |\n")
	sb.WriteString("|------|-------------|\n")

	nodeTypes := reference.ListNodeTypes()
	sort.Strings(nodeTypes)
	for _, typeName := range nodeTypes {
		info, ok := reference.GetNodeType(typeName)
		if !ok {
			continue
		}
		desc := summaryForName(info.Name, info.Summary, info.Description)
		fmt.Fprintf(&sb, "| `%s` | %s |\n", tableCell(typeName), tableCell(desc))
	}

	return sb.String()
}

func generateInputTypesSummary() string {
	var sb strings.Builder
	sb.WriteString("## Input Types\n\n")
	sb.WriteString("> Auto-generated from `reference.ListInputTypes()` / `reference.GetInputType()`. Use `get_schema(name=\"<type>\")` for full documentation.\n\n")
	sb.WriteString("| Type | Description |\n")
	sb.WriteString("|------|-------------|\n")

	inputTypes := reference.ListInputTypes()
	sort.Strings(inputTypes)
	for _, typeName := range inputTypes {
		info, ok := reference.GetInputType(typeName)
		if !ok {
			continue
		}
		desc := summaryForName(info.Name, info.Summary, info.Description)
		fmt.Fprintf(&sb, "| `%s` | %s |\n", tableCell(typeName), tableCell(desc))
	}

	return sb.String()
}

func extractTopLevelSchema(schemaPath string) (string, error) {
	content, err := os.ReadFile(schemaPath)
	if err != nil {
		return "", fmt.Errorf("reading workflow schema: %w (run 'make generate-schema' first)", err)
	}

	text := cleanMarkdown(string(content))

	var sb strings.Builder
	sb.WriteString("## Workflow Structure\n\n")
	sb.WriteString("> Auto-generated from `generated/docs-source/reference/workflow-schema.md`.\n\n")

	for index, name := range []string{"Workflow", "Edge", "EdgeCase"} {
		section := extractSection(text, "## "+name, "\n---")
		if section == "" {
			continue
		}
		if index > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(strings.TrimSpace(section))
		sb.WriteString("\n")
	}

	return strings.TrimSpace(sb.String()), nil
}

func extractScenarioEssentials(scenarioPath string) (string, error) {
	content, err := os.ReadFile(scenarioPath)
	if err != nil {
		return "", fmt.Errorf("reading scenario schema: %w (run 'make generate-scenario-schema' first)", err)
	}

	text := cleanMarkdown(string(content))

	var sb strings.Builder
	sb.WriteString("## Scenario Testing\n\n")
	sb.WriteString("Test workflows by simulating LLM and tool responses without making real API calls.\n\n")
	sb.WriteString("> Auto-generated schema details from `generated/docs-source/reference/scenario-schema.md`.\n\n")

	for _, schema := range []struct {
		Name  string
		Title string
	}{
		{Name: "Scenario", Title: "Scenario fields"},
		{Name: "SimulatedEvent", Title: "Event fields"},
		{Name: "Expectation", Title: "Expectation fields"},
	} {
		section := extractSection(text, "## "+schema.Name, "\n---")
		if section == "" {
			continue
		}
		summary := extractSectionSummary(section)
		fieldsTable := extractFieldsTable(section)
		if fieldsTable == "" {
			continue
		}
		fmt.Fprintf(&sb, "### %s\n\n", schema.Title)
		if summary != "" {
			sb.WriteString(summary)
			sb.WriteString("\n\n")
		}
		sb.WriteString(fieldsTable)
		sb.WriteString("\n\n")
	}

	if targeting := extractSection(text, "## Targeting Nodes", "\n---"); targeting != "" {
		sb.WriteString(demoteHeading(strings.TrimSpace(targeting)))
		sb.WriteString("\n\n")
	}

	if events := extractSection(text, "## Event Types", "\n---"); events != "" {
		sb.WriteString(demoteHeading(strings.TrimSpace(events)))
		sb.WriteString("\n\n")
	}

	sb.WriteString(`### Use the raw ` + "`output:`" + ` form, not ` + "`type:`" + `

Every event needs either ` + "`output:`" + ` (a raw activity output map) or ` + "`type:`" + ` (a convenience wrapper
like ` + "`llm_response`" + `/` + "`tool_result`" + ` that gets converted into one). **Write ` + "`output:`" + ` directly.** It is
the form every scenario in ` + "`internal/workflow/builtin/testdata/`" + ` uses (the CI-gating corpus), and it is
the only form that exposes real activity field names — ` + "`response_data`" + `, ` + "`thread_token_count`" + `,
` + "`has_feedback`" + `, ` + "`tool_results`" + ` — which you need for routing and compaction scenarios and which the
` + "`type:`" + ` wrapper hides.

### ` + "`completed:`" + ` proves a node ran — ` + "`reached:`" + ` does not

` + "`reached:`" + ` passes for anything **scheduled**, including a node skipped by a false condition — per
the field doc above, it covers nodes "completed, skipped, or errored." So ` + "`reached: [handler]`" + `
is satisfied even when ` + "`handler`" + ` never actually executed. **If a scenario is meant to prove a
branch executed, assert ` + "`completed:`" + `, not ` + "`reached:`" + `.** This is the single most common way a
scenario passes without testing what its author intended.

A node can legitimately land in both ` + "`completed:`" + ` and ` + "`skipped:`" + ` across one run — e.g. a
loop-body node that runs on iteration 0 and is skipped on iteration 1. Real example, from
` + "`internal/workflow/builtin/scenarios/get-it-right/stuck_feedback_rereviews_without_reimplementing.yaml`" + `:

` + "```yaml" + `
expect:
  outcome: completed
  reached:
    - attempt.implement   # scheduled at least once — true either way, proves nothing about iteration 1
  skipped:
    - attempt.implement   # the assertion that actually proves iteration 1 skipped it
` + "```" + `

Node state is a set of facts about the whole run, not a single verdict per node.

### Example scenario

Verbatim from ` + "`internal/workflow/builtin/testdata/agent_scenarios.yaml`" + `, run against ` + "`builtin://agent`" + `.
It exercises a tool call whose output crosses the compaction token threshold, so the ` + "`compact`" + `
node runs before the loop's next iteration:

` + "```yaml" + `
name: compaction_triggered
description: Token count exceeds the model's compaction threshold after tool execution, compact node runs
inputs:
  mode: auto
events:
  # LLM requests a tool that returns lots of data
  - node: agent_loop.call_llm
    output:
      message:
        role: assistant
        text: "Let me read a large file."
      response_text: "Let me read a large file."
      tool_calls:
        - id: call_0
          name: view
          input:
            file_path: "/path/to/large/file.go"
  # Tool returns with token count above the 185000 default threshold
  - node: agent_loop.execute_tools
    output:
      message:
        role: tool
        text: ""
      tool_results:
        - tool_call_id: call_0
          content: "Very large file content..."
      thread_token_count: 200000
  # Compact node runs due to high token count
  - node: agent_loop.compact
    output:
      message:
        role: assistant
        text: "Context compacted successfully."
  # Next iteration: LLM provides final response
  - node: agent_loop.call_llm
    output:
      message:
        role: assistant
        text: "Here's the analysis of the large file."
      response_text: "Here's the analysis of the large file."
      tool_calls: []
expect:
  outcome: completed
  reached:
    - agent_loop
    - agent_loop.call_llm
    - agent_loop.execute_tools
    - agent_loop.compact
  not_reached:
    - agent_loop.approval
` + "```" + `

Notes on this example, all load-bearing:

- ` + "`inputs:`" + ` overrides workflow inputs for just this scenario (here, selecting ` + "`mode: auto`" + ` to skip
  the approval node).
- ` + "`not_reached:`" + ` is the primary way to prove a conditional path was actually SKIPPED, not just that
  the scenario didn't happen to visit it — assert it on every branch you didn't take.
- The loop node itself (` + "`agent_loop`" + `) belongs in ` + "`reached`" + ` alongside its children — a loop that never
  entered its body wouldn't reach ` + "`agent_loop.call_llm`" + ` either.
- Field names on ` + "`output:`" + ` are the real activity output fields for that node type: ` + "`call_llm`" + ` gives
  ` + "`message`" + `, ` + "`response_text`" + `, ` + "`tool_calls`" + `, ` + "`token_count`" + `; ` + "`execute_tools`" + ` gives ` + "`tool_results`" + `,
  ` + "`response_data`" + ` (structured output keyed by response-tool name), ` + "`thread_token_count`" + `;
  ` + "`ask_question`" + ` gives ` + "`has_feedback`" + `. Use ` + "`get_schema(name=\"<node_type>\")`" + ` for the full field list of
  any node type before guessing at output shapes.

### Asserting workflow outputs with ` + "`outputs:`" + `

` + "`outputs:`" + ` checks the workflow's declared outputs (as opposed to ` + "`node_outputs:`" + `, which checks a
specific node's raw activity output). Keys support dotted paths into structured values, and matching
is partial — only the paths you list are checked. Verbatim from
` + "`internal/workflow/builtin/scenarios/structured-agent/happy_path.yaml`" + `:

` + "```yaml" + `
expect:
  outcome: completed
  reached:
    - agent_loop
    - agent_loop.call_llm
    - agent_loop.execute_tools
  outputs:
    response.choice: "complete"
    response.value: "Task completed successfully. The analysis shows positive results."
    completed: true
` + "```" + `

### Multi-scenario files

A scenario file holds one scenario per YAML document, separated by ` + "`---`" + `. Put related scenarios for
one workflow in a single file (e.g. ` + "`agent_scenarios.yaml`" + ` holds 9+ scenarios) rather than one file per
scenario — this is the convention every builtin workflow follows.

### Reading ` + "`run_scenario`" + ` output

` + "`run_scenario`" + ` reports, in order:

- **Status and outcome** — ` + "`✓ PASSED`" + `/` + "`✗ FAILED`" + `/` + "`⚠ ERROR`" + `, plus actual vs. expected outcome when they
  differ.
- **` + "`completed:`" + ` and, if any, ` + "`skipped:`" + ` and ` + "`errored:`" + `** — every node broken out by its actual
  execution state, not merged into one "reached" list. A node showing under ` + "`skipped:`" + ` when you
  expected it to run, or absent from ` + "`completed:`" + ` entirely, tells you which fix to make: "never
  scheduled" means a routing/edge problem, "skipped" means the node's own condition was false.
- **Error details** — for a workflow-level error, the failing node, step, message, and (if the failure
  was a condition evaluation) the CEL expression itself.
- **Warnings**, under "Warnings — this run may not have tested what you think" — printed even on a
  PASS. See the next section; do not skip a warning just because the run went green.
- **Failed expectations** — on a FAIL, each mismatch (e.g. a node in ` + "`not_reached`" + ` was actually
  scheduled) annotated with that node's actual state, e.g. ` + "`[handle_question was skipped]`" + `, so you
  don't have to cross-reference the completed/skipped lists yourself.
- **Workflow outputs and node outputs** — the actual values, truncated, so you can see what to assert
  against without a second run.

### Warnings: a PASS can still test nothing

Two warnings print even when the scenario passes, because a passing assertion can coexist with a run
that never exercised what its name claims to test:

- **Black-boxed sub-workflow** — ` + "`type: workflow`" + ` with a ` + "`ref:`" + ` (not ` + "`inline:`" + `) executes as one
  opaque unit unless the scenario targets its internals with qualified node ids. If nothing in
  ` + "`events:`" + ` or ` + "`expect:`" + ` uses ` + "`<node_id>.<inner_id>`" + `, the sub-workflow's output came straight from
  your scenario's mock and none of its own logic ran. Fix: mock its internals directly, e.g.
  ` + "`node: research.search`" + `, or accept that this scenario only tests the caller's wiring and say so
  in the description.
- **Unmocked node router defaulted to ` + "`candidates[0]`" + `** — a ` + "`type: router`" + ` node in node-routing mode
  (` + "`router.nodes`" + ` set) that never receives a mocked ` + "`selected_node`" + ` silently falls back to its
  first candidate. A scenario can pass while asserting nothing about which branch was chosen. **If
  you are testing routing, you must mock the router explicitly** — otherwise the scenario proves
  nothing about the routing decision, no matter what its name says. Fix, verbatim from
  ` + "`internal/workflow/builtin/scenarios/pitch-deck/start_from_research.yaml`" + `:

  ` + "```yaml" + `
  events:
    - node: classify
      output:
        selected_node: research_competitors
        reasoning: "Website analysis already exists — run the research phase."
  ` + "```" + `

  A corpus survey found 96 of 159 scenarios (60%) black-box at least one sub-workflow, and 27 (17%)
  have a silently-defaulted router — including every ` + "`one-ring`" + ` scenario, where names like
  ` + "`implement_only`" + ` and ` + "`plan_only`" + ` assert a routing outcome ` + "`classify`" + ` never actually made.
  Don't let this happen to your scenario: if the point is "task X gets classified as Y," mock ` + "`classify`" + `.

### Best practices

- **Aim for 3+ scenarios per workflow** — cover the happy path, error cases, and edge cases
- **` + "`outcome:`" + ` accepts three values** — ` + "`completed`" + `, ` + "`error`" + ` (workflow terminated with an error;
  pair with ` + "`error_contains`" + `/` + "`error_node`" + `), and ` + "`failed`" + ` (e.g. validation failure, distinct from a
  runtime error)
- **Use ` + "`completed:`" + ` to prove a branch ran, not ` + "`reached:`" + `** — see above; ` + "`reached:`" + ` also passes
  for skipped nodes
- **Test routing logic by mocking the router** — ` + "`reached:`" + `/` + "`completed:`" + ` on the destination node
  proves nothing if the router itself defaulted; read the warnings section
- **Test loop termination** — verify loops exit correctly (both via ` + "`while`" + ` condition and max iterations)
- **Test skipped nodes with ` + "`skipped:`" + `**, not ` + "`not_reached:`" + ` — a node can be scheduled and skipped
  (` + "`reached`" + `/` + "`skipped`" + `) or never scheduled at all (` + "`not_reached`" + `); they are different failure modes
- **Use ` + "`start_at`" + `** — to test specific sections of complex workflows without simulating the entire flow
- **Use ` + "`state`" + `** — to pre-populate node outputs when testing downstream logic
- **Name scenarios descriptively** — e.g., ` + "`error_handling_api_failure`" + ` not ` + "`test_3`" + `
- **Try to break it** — simulate unexpected LLM responses, tool failures, and edge cases. Finding bugs in scenarios is much cheaper than finding them during a real 1-hour workflow run.`)

	return strings.TrimSpace(sb.String()), nil
}

func generateReferences() string {
	return `## References

- Use ` + "`get_cel_reference`" + ` for the complete, auto-generated CEL reference
- Use ` + "`get_schema(name=\"<type>\")`" + ` for full field docs on any node type, input type, or shared type
- Use ` + "`list_workflows`" + ` to find real-world examples and established patterns`
}

func resolveReferenceFile(docsDir, filename string) (string, error) {
	candidates := []string{
		filepath.Join(docsDir, "reference", filename),
		filepath.Join(docsDir, filename),
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("could not find %s under %s or %s (run 'make generate-schema generate-scenario-schema' first)", filename, candidates[0], candidates[1])
}

func extractSection(text, startMarker, endMarker string) string {
	startIdx := strings.Index(text, startMarker)
	if startIdx == -1 {
		return ""
	}

	rest := text[startIdx+len(startMarker):]
	endIdx := strings.Index(rest, endMarker)
	if endIdx == -1 {
		return startMarker + rest
	}

	return startMarker + rest[:endIdx]
}

// stripFieldsTable removes ### Fields sections to reduce verbosity when a caller
// wants section prose without duplicating schema tables.
func stripFieldsTable(text string) string {
	fieldsIdx := strings.Index(text, "### Fields")
	if fieldsIdx == -1 {
		return text
	}

	before := text[:fieldsIdx]
	after := text[fieldsIdx:]
	restIdx := strings.Index(after[len("### Fields"):], "###")
	if restIdx == -1 {
		return strings.TrimSpace(before)
	}

	return strings.TrimSpace(before + after[len("### Fields")+restIdx:])
}

func extractFieldsTable(section string) string {
	fieldsIdx := strings.Index(section, "### Fields")
	if fieldsIdx == -1 {
		return ""
	}

	lines := strings.Split(section[fieldsIdx:], "\n")
	var table []string
	inTable := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "| Field |") {
			inTable = true
		}
		if !inTable {
			continue
		}
		if !strings.HasPrefix(trimmed, "|") {
			break
		}
		table = append(table, trimmed)
	}

	return strings.Join(table, "\n")
}

func extractSectionSummary(section string) string {
	section = stripFieldsTable(section)
	lines := strings.Split(section, "\n")
	var summary []string
	for _, line := range lines[1:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if len(summary) > 0 {
				break
			}
			continue
		}
		if strings.HasPrefix(trimmed, "Example") || strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "###") {
			break
		}
		summary = append(summary, trimmed)
	}
	return strings.Join(summary, "\n")
}

func demoteHeading(section string) string {
	lines := strings.Split(section, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "## ") {
			lines[i] = "#" + line
		} else if strings.HasPrefix(line, "### ") {
			lines[i] = "#" + line
		}
	}
	return strings.Join(lines, "\n")
}

// cleanMarkdown removes frontmatter and HTML comments.
func cleanMarkdown(text string) string {
	commentRe := regexp.MustCompile(`(?s)<!--.*?-->`)
	text = commentRe.ReplaceAllString(text, "")

	text = strings.TrimSpace(text)

	if strings.HasPrefix(text, "---") {
		rest := text[3:]
		idx := strings.Index(rest, "---")
		if idx != -1 {
			text = strings.TrimSpace(rest[idx+3:])
		}
	}

	blankRe := regexp.MustCompile(`\n{3,}`)
	text = blankRe.ReplaceAllString(text, "\n\n")

	return strings.TrimSpace(text)
}

func summaryForName(name, summary, description string) string {
	result := strings.TrimSpace(summary)
	if result == "" || result == "-" {
		result = strings.TrimSpace(description)
	}
	result = firstParagraph(result)

	prefix := name + " "
	if strings.HasPrefix(result, prefix) {
		result = strings.TrimPrefix(result, prefix)
		if result != "" {
			result = strings.ToUpper(result[:1]) + result[1:]
		}
	}
	if result == "" {
		return "-"
	}
	return result
}

func firstParagraph(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return "-"
	}

	paragraphs := regexp.MustCompile(`\n\s*\n`).Split(text, 2)
	text = paragraphs[0]
	return strings.Join(strings.Fields(text), " ")
}

func tableCell(text string) string {
	text = strings.TrimSpace(text)
	text = strings.ReplaceAll(text, "\n", " ")
	text = strings.ReplaceAll(text, "|", `\|`)
	if text == "" {
		return "-"
	}
	return text
}
