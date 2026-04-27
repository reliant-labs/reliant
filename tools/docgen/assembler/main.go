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

## When to use

- Creating a new Reliant workflow from scratch
- Editing or extending an existing workflow
- Debugging workflow execution issues
- Writing scenario tests for workflows
- Understanding workflow patterns and best practices

## Approach

IMPORTANT: You are given a workflow draft ID in the system message. Use this ID for all workflow operations.

Follow the 6-step process:

1. **Setup** — Call ` + "`get_workflow(id=\"<draft_id>\")`" + ` to see current content
2. **Understand** — Ask clarifying questions about the user's goal
3. **Learn** — Use ` + "`list_workflows`" + ` to see examples and patterns
4. **Explore** — Read the user's codebase (test commands, code patterns). Note: references to "workflows" and "nodes" in user code are unlikely to be Reliant-specific.
5. **Build** — Use ` + "`edit_workflow`" + ` for small changes, ` + "`write_workflow`" + ` for larger rewrites
6. **Test** — Create and run scenarios (aim for 3+ covering positive, negative, and edge cases). Try to break your workflow. It's frustrating for users to run a workflow for an hour and hit a bug at the end—scenarios catch this early.`
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

Load these with ` + "`load_tool`" + ` as needed:

| Tool | Purpose |
|------|---------|
| ` + "`get_workflow`" + ` | Read current workflow content |
| ` + "`edit_workflow`" + ` | Make targeted edits to a workflow |
| ` + "`write_workflow`" + ` | Full workflow rewrite |
| ` + "`get_schema`" + ` | Get full field documentation for any node/input/shared type |
| ` + "`get_cel_reference`" + ` | Authoritative CEL reference (namespaces, functions, types) |
| ` + "`list_workflows`" + ` | Browse existing workflows for examples and patterns |
| ` + "`write_scenario`" + ` | Create a test scenario |
| ` + "`run_scenario`" + ` | Execute a scenario and check results |
| ` + "`list_scenarios`" + ` | List existing scenarios for a workflow |
| ` + "`view_scenario`" + ` | Read a scenario's content |
| ` + "`edit_scenario`" + ` | Modify an existing scenario |
| ` + "`delete_scenario`" + ` | Remove a scenario |
| ` + "`get_workflow_suggestions`" + ` | Get AI-powered suggestions for workflow improvements |`
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
		sb.WriteString(fmt.Sprintf("| `%s.*` | %s | %s |\n", tableCell(ns.Name), tableCell(ns.Description), tableCell(fields)))
	}
	sb.WriteString("\n")

	for _, ns := range namespaces {
		if len(ns.Fields) == 0 {
			continue
		}
		sb.WriteString(fmt.Sprintf("#### `%s` fields\n\n", ns.Name))
		sb.WriteString("| Field | Type | Description |\n")
		sb.WriteString("|-------|------|-------------|\n")
		fields := append([]reference.CELField(nil), ns.Fields...)
		sort.Slice(fields, func(i, j int) bool { return fields[i].Name < fields[j].Name })
		for _, field := range fields {
			sb.WriteString(fmt.Sprintf("| `%s.%s` | `%s` | %s |\n", tableCell(ns.Name), tableCell(field.Name), tableCell(field.Type), tableCell(field.Description)))
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
		sb.WriteString(fmt.Sprintf("| `%s` | %s | `%s` |\n", tableCell(signature), tableCell(fn.Description), tableCell(fn.Example)))
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
		sb.WriteString(fmt.Sprintf("| `%s` | %s |\n", tableCell(wf.Name), tableCell(wf.Description)))
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
		sb.WriteString(fmt.Sprintf("| `%s` | %s |\n", tableCell(typeName), tableCell(desc)))
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
		sb.WriteString(fmt.Sprintf("| `%s` | %s |\n", tableCell(typeName), tableCell(desc)))
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
		sb.WriteString(fmt.Sprintf("### %s\n\n", schema.Title))
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

	sb.WriteString(`### Example scenario

` + "```yaml" + `
name: agent_tool_usage
description: Agent calls a tool and completes
events:
  - node: agent_loop.call_llm
    type: llm_response
    tool_calls:
      - name: bash
        input: {command: ls}
  - node: agent_loop.execute_tools
    type: tool_result
    tool: bash
    tool_output: {result: "file.txt"}
  - node: agent_loop.call_llm
    type: llm_response
    text: "Found file.txt"
expect:
  outcome: completed
  reached: [agent_loop.call_llm, agent_loop.execute_tools]
` + "```" + `

### Best practices

- **Aim for 3+ scenarios per workflow** — cover the happy path, error cases, and edge cases
- **Test routing logic** — create scenarios that exercise each branch/case in your edges
- **Test loop termination** — verify loops exit correctly (both via ` + "`while`" + ` condition and max iterations)
- **Test skipped nodes** — verify conditional nodes skip correctly and downstream nodes handle it
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
