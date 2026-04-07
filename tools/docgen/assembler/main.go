// Copyright (c) 2025 Reliant Labs
//
// Workflow builder prompt assembler - generates workflow_builder.yaml.
//
// Auto-generates content from:
//   - internal/workflow/reference (populated from proto descriptors at init)
//   - internal/workflow/builtin/*.yaml (builtin workflow list)
//   - generated/docs-source/reference/workflow-schema.md (top-level schema only)
//
// GENERATED FILE:
//   - internal/workflow/builtin/presets/workflow_builder.yaml
//
// Usage: go run ./tools/docgen/assembler <docs_dir> <output_file>

package main

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"github.com/reliant-labs/reliant/internal/workflow/reference"
	"gopkg.in/yaml.v3"
)

// Preset represents the YAML structure
type Preset struct {
	Name        string                 `yaml:"name"`
	Description string                 `yaml:"description"`
	Tag         string                 `yaml:"tag"`
	Params      map[string]interface{} `yaml:"params"`
}

// BuiltinWorkflow represents minimal workflow info for listing
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

	prompt, err := assemblePrompt(docsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error assembling prompt: %v\n", err)
		os.Exit(1)
	}

	preset := Preset{
		Name:        "workflow_builder",
		Description: "Specialized assistant for building and modifying Reliant workflows",
		Tag:         "agent",
		Params: map[string]interface{}{
			"model": map[string]string{
				"id": "claude-4.6-opus",
			},
			"temperature":    1.0,
			"thinking_level": "high",
			"system_prompt":  prompt,
			// Tools: workflow tools + search tools + file viewing + web + MCP
			"tools": []string{
				"tag:workflow",
				"tag:search",
				"tag:web",
				"tag:mcp",
				"view",
			},
			"spawn_presets": []string{},
		},
	}

	data, err := yaml.Marshal(&preset)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error marshaling YAML: %v\n", err)
		os.Exit(1)
	}

	if err := os.WriteFile(outputFile, data, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing output: %v\n", err)
		os.Exit(1)
	}

	tokenEstimate := len(prompt) / 4
	fmt.Printf("Generated workflow_builder.yaml: %s (~%d tokens)\n", outputFile, tokenEstimate)
}

func assemblePrompt(docsDir string) (string, error) {
	var sections []string

	// 1. Header with tools and approach
	sections = append(sections, generateHeader())

	// 2. CEL Quick Reference (auto-generated from reference package)
	sections = append(sections, generateCELQuickReference())

	// 3. Builtin workflows list (auto-generated from embedded yamls)
	builtins, err := generateBuiltinWorkflowsList()
	if err != nil {
		return "", fmt.Errorf("generating builtin workflows: %w", err)
	}
	sections = append(sections, builtins)

	// 4. Node types summary (auto-generated from reference package)
	sections = append(sections, generateNodeTypesSummary())

	// 5. Input types summary (auto-generated from reference package)
	sections = append(sections, generateInputTypesSummary())

	// 6. Top-level workflow schema (Workflow, Edge, EdgeCase only - not full dump)
	schemaPath := docsDir + "/reference/workflow-schema.md"
	schema, err := extractTopLevelSchema(schemaPath)
	if err != nil {
		return "", fmt.Errorf("extracting schema: %w", err)
	}
	sections = append(sections, schema)

	// 7. Gotchas and rules (categorized)
	sections = append(sections, generateGotchas())

	// 8. Scenario testing (condensed)
	scenarioPath := docsDir + "/reference/scenario-schema.md"
	scenarios, err := extractScenarioEssentials(scenarioPath)
	if err != nil {
		return "", fmt.Errorf("extracting scenarios: %w", err)
	}
	sections = append(sections, scenarios)

	return strings.Join(sections, "\n\n"), nil
}

func generateHeader() string {
	return `# Workflow Builder

You build Reliant workflows. Your goal: create working workflows that solve the user's problem.

## Approach

IMPORTANT: You are given a workflow draft ID in the system message. Use this ID for all workflow operations.

1. **Setup** - Call get_workflow(id="<draft_id>") to see current content
2. **Understand** - Ask clarifying questions about the user's goal
3. **Learn** - Use list_workflows to see examples and patterns
4. **Explore** -  Read the user's codebase (test commands, code patterns). Please understand that references to workflows and nodes within the user's codebase are unlikely to be related to Reliant workflows specifically.
5. **Build** - Use edit_workflow for small changes, write_workflow for larger rewrites
6. **Test** - Create and run scenarios (aim for 3+ covering positive and negative cases, or more for more complex workflows). Try to break your workflow and exercise edge cases. It's a frustrating user experience to run a workflow for 1 hour and find a bug at the end, so scenarios are good at uncovering and making sure workflows work till completion, even on the non happy path.`
}

func generateCELQuickReference() string {
	var sb strings.Builder
	sb.WriteString("## CEL Quick Reference\n\n")
	sb.WriteString("Use `get_cel_reference` for complete documentation.\n\n")

	// Syntax rules
	sb.WriteString("### Syntax\n")
	sb.WriteString("- **Most fields**: `{{expression}}` - template interpolation\n")
	sb.WriteString("- **condition/while**: Pure CEL, no `{{}}`\n\n")

	// Namespaces (auto-generated)
	sb.WriteString("### Namespaces\n\n")
	sb.WriteString("| Namespace | Description |\n")
	sb.WriteString("|-----------|-------------|\n")
	for _, ns := range reference.CELNamespaces {
		fmt.Fprintf(&sb, "| `%s.*` | %s |\n", ns.Name, ns.Description)
	}
	sb.WriteString("\n")

	// Key functions (auto-generated)
	sb.WriteString("### Key Functions\n\n")
	sb.WriteString("| Function | Example |\n")
	sb.WriteString("|----------|--------|\n")
	for _, fn := range reference.CELFunctions {
		fmt.Fprintf(&sb, "| `%s` | `%s` |\n", fn.Signature, fn.Example)
	}
	sb.WriteString("\n")

	// Common patterns
	sb.WriteString("### Common Patterns\n")
	sb.WriteString("```yaml\n")
	sb.WriteString("# Check LLM wants tools\n")
	sb.WriteString("condition: \"nodes.llm.stop_reason == 'tool_use'\"\n\n")
	sb.WriteString("# Loop until done\n")
	sb.WriteString("while: \"outputs.stop_reason != 'end_turn' && iter.iteration < 50\"\n\n")
	sb.WriteString("# Dynamic value\n")
	sb.WriteString("model: \"{{inputs.model}}\"\n")
	sb.WriteString("```")

	return sb.String()
}

func generateBuiltinWorkflowsList() (string, error) {
	var sb strings.Builder
	sb.WriteString("## Builtin Workflows\n\n")
	sb.WriteString("Reference via `builtin://<name>` in workflow nodes.\n\n")
	sb.WriteString("| Name | Description |\n")
	sb.WriteString("|------|-------------|\n")

	// Read embedded workflow files
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

		if wf.Name != "" {
			// Clean up description - take first line only
			desc := wf.Description
			if idx := strings.Index(desc, "\n"); idx > 0 {
				desc = strings.TrimSpace(desc[:idx])
			}
			wf.Description = desc
			workflows = append(workflows, wf)
		}
	}

	// Sort by name
	sort.Slice(workflows, func(i, j int) bool {
		return workflows[i].Name < workflows[j].Name
	})

	for _, wf := range workflows {
		fmt.Fprintf(&sb, "| `%s` | %s |\n", wf.Name, wf.Description)
	}

	return sb.String(), nil
}

func generateNodeTypesSummary() string {
	var sb strings.Builder
	sb.WriteString("## Node Types\n\n")
	sb.WriteString("Use `get_schema(name=\"<type>\")` for full field documentation.\n\n")

	// Base fields
	sb.WriteString("### Common Fields (all nodes)\n\n")
	sb.WriteString("| Field | Type | Description |\n")
	sb.WriteString("|-------|------|-------------|\n")
	sb.WriteString("| `type` | string | Node type (required) |\n")
	sb.WriteString("| `id` | string | Unique node ID (required) |\n")
	sb.WriteString("| `condition` | CEL | Skip if false; on join nodes: \"all\" or \"any\" |\n")
	sb.WriteString("| `thread` | ThreadConfig | Thread mode: inherit, new, fork |\n")
	sb.WriteString("| `timeout` | string | Override timeout (e.g., \"5m\") |\n")
	sb.WriteString("| `save_message` | SaveMessageConfig | Auto-save message after completion |\n\n")

	// Node types table
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
		desc := info.Summary
		if strings.HasPrefix(desc, info.Name+" ") {
			desc = strings.TrimPrefix(desc, info.Name+" ")
			if len(desc) > 0 {
				desc = strings.ToUpper(string(desc[0])) + desc[1:]
			}
		}
		fmt.Fprintf(&sb, "| `%s` | %s |\n", typeName, desc)
	}

	return sb.String()
}

func generateInputTypesSummary() string {
	var sb strings.Builder
	sb.WriteString("## Input Types\n\n")
	sb.WriteString("Use `get_schema(name=\"<type>\")` for full documentation.\n\n")
	sb.WriteString("| Type | Description |\n")
	sb.WriteString("|------|-------------|\n")

	inputTypes := reference.ListInputTypes()
	sort.Strings(inputTypes)

	for _, typeName := range inputTypes {
		info, ok := reference.GetInputType(typeName)
		if !ok {
			continue
		}
		desc := info.Summary
		if strings.HasPrefix(desc, info.Name+" ") {
			desc = strings.TrimPrefix(desc, info.Name+" ")
			if len(desc) > 0 {
				desc = strings.ToUpper(string(desc[0])) + desc[1:]
			}
		}
		fmt.Fprintf(&sb, "| `%s` | %s |\n", typeName, desc)
	}

	return sb.String()
}

func extractTopLevelSchema(schemaPath string) (string, error) {
	content, err := os.ReadFile(schemaPath)
	if err != nil {
		return "", fmt.Errorf("reading workflow schema: %w (run 'make generate-schema' first)", err)
	}

	text := cleanMarkdown(string(content))

	// Extract only Workflow and Edge sections (top-level structure)
	var sb strings.Builder
	sb.WriteString("## Workflow Structure\n\n")

	// Find and extract Workflow section (until ---)
	workflowSection := extractSection(text, "## Workflow\n", "\n---")
	if workflowSection != "" {
		sb.WriteString(workflowSection)
		sb.WriteString("\n\n")
	}

	// Find and extract Edge section
	edgeSection := extractSection(text, "## Edge\n", "\n---")
	if edgeSection != "" {
		sb.WriteString("---\n\n")
		sb.WriteString(edgeSection)
		sb.WriteString("\n\n")
	}

	// Find and extract EdgeCase section
	edgeCaseSection := extractSection(text, "## EdgeCase\n", "\n---")
	if edgeCaseSection != "" {
		sb.WriteString("---\n\n")
		sb.WriteString(edgeCaseSection)
	}

	return sb.String(), nil
}

func extractSection(text, startMarker, endMarker string) string {
	startIdx := strings.Index(text, startMarker)
	if startIdx == -1 {
		return ""
	}

	// Find end (next section or end of text)
	rest := text[startIdx+len(startMarker):]
	endIdx := strings.Index(rest, endMarker)
	if endIdx == -1 {
		return startMarker + rest
	}

	section := startMarker + rest[:endIdx]

	// Strip ### Fields tables (get_schema provides this)
	section = stripFieldsTable(section)

	return section
}

// stripFieldsTable removes ### Fields sections to reduce verbosity
func stripFieldsTable(text string) string {
	// Find ### Fields and remove until next ### or end
	fieldsIdx := strings.Index(text, "### Fields")
	if fieldsIdx == -1 {
		return text
	}

	before := text[:fieldsIdx]
	after := text[fieldsIdx:]

	// Find the end of the fields section (next ### or end)
	restIdx := strings.Index(after[10:], "###")
	if restIdx == -1 {
		// Fields goes to end, just return before part
		return strings.TrimSpace(before)
	}

	return before + after[10+restIdx:]
}

func generateGotchas() string {
	return `## Important Rules

# Parameters
- IMPORTANT: You typically want to set a model as a param via a tag. We support tags like flagship, moderate, cheap, etc.
- IMPORTANT: **NEVER** assume which models exist, your corpus is likely not up to date, and you may not be aware of which api keys the user is using.
- IMPORTANT: A workflow always runs in the context of a user thread. You never have to create input to consume the user's request.

## Agents and LLM's

- CallLLM is a single LLM call. It doesn't execute tools, nor loop in an agentic fashion.
- There's a handful of patterns you can create:
	- Augmented agents: like the auditing agent, do some additional work inside the agentic loop.
	- Parallelization: you can parallelize work by having multiple edges from the same source (not using switch/case logic)
	- Combining agents: combine multiple discreet agents with specialized tools and instructions to perform your tasks. See one-ring.yaml for an example.
	- Structured output: leverage mechanisms similar to structured-agent to produce output to conditionally route on events
- Combine any of the above patterns to create super workflows.
- Every workflow must be a DAG, however you can use loop nodes to solve looping.

### CEL Expressions
- **condition/while**: Pure CEL without ` + "`{{}}`" + ` → ` + "`condition: \"nodes.llm.stop_reason == 'tool_use'\"`" + `
- **All other fields**: Use ` + "`{{}}`" + ` for interpolation → ` + "`model: \"{{inputs.model}}\"`" + `
- Always use ` + "`has()`" + ` or null checks (<object>.?<field>) before accessing optional fields

### Edge Routing
- Multiple cases on one edge = exactly 1 executes (first match wins)
- All cases require a condition - use ` + "`default`" + ` for fallback
- For parallelism: create multiple edges from same source OR ` + "`default: [node-a, node-b]`" + `

### Threading
- Modes: ` + "`inherit`" + ` (default), ` + "`new`" + `, ` + "`fork`" + `
- Use ` + "`memo`" + ` in loops to reuse thread across iterations
- Use ` + "`inject`" + ` to add a message when entering a sub-workflow
- Never run parallel agents on the same thread simultaneously

### Loops
- ` + "`while`" + ` is do-while: body runs at least once
- ` + "`iter.iteration`" + ` is 0-indexed inside loop body
- ` + "`outputs.*`" + ` in while condition references current iteration's outputs

### Response Tools
- Force structured LLM output for routing/classification
- ` + "`builtin://agent`" + ` yields when no tool calls
- ` + "`builtin://structured-agent`" + ` yields when response tool is called
- Access via ` + "`nodes.<execute_tools_id>.response_data.<tool_name>`" + `

### Conditions on Nodes
- Skipped nodes forward to next edge target
- Cannot access outputs of skipped nodes
- Join nodes handle skipped inputs correctly`
}

func extractScenarioEssentials(scenarioPath string) (string, error) {
	content, err := os.ReadFile(scenarioPath)
	if err != nil {
		return "", fmt.Errorf("reading scenario schema: %w (run 'make generate-scenario-schema' first)", err)
	}

	text := cleanMarkdown(string(content))

	var sb strings.Builder
	sb.WriteString("## Scenario Testing\n\n")
	sb.WriteString("Test workflows by simulating LLM and tool responses.\n\n")

	// Extract just the Scenario section (main fields)
	scenarioSection := extractSection(text, "## Scenario", "## SimulatedEvent")
	if scenarioSection != "" {
		// Remove excessive examples, keep just the field table
		sb.WriteString("### Scenario Fields\n\n")
		sb.WriteString("| Field | Description |\n")
		sb.WriteString("|-------|-------------|\n")
		sb.WriteString("| `name` | Unique scenario identifier (required) |\n")
		sb.WriteString("| `description` | What this scenario tests |\n")
		sb.WriteString("| `events` | Simulated events in execution order (required) |\n")
		sb.WriteString("| `expect` | Expected outcome and assertions |\n")
		sb.WriteString("| `inputs` | Override workflow inputs |\n")
		sb.WriteString("| `start_at` | Begin at specific node |\n")
		sb.WriteString("| `state` | Pre-populate node outputs |\n\n")
	}

	// Event types summary
	sb.WriteString("### Event Types\n\n")
	sb.WriteString("| Type | Fields | Description |\n")
	sb.WriteString("|------|--------|-------------|\n")
	sb.WriteString("| `llm_response` | `text` or `tool_calls` | Simulate LLM output |\n")
	sb.WriteString("| `tool_result` | `tool`, `tool_output` | Simulate tool execution |\n")
	sb.WriteString("| `tool_error` | `tool`, `tool_output` | Simulate tool failure |\n")
	sb.WriteString("| `user_input` | `text` | Simulate user message |\n\n")

	// Expectations
	sb.WriteString("### Expectations\n\n")
	sb.WriteString("| Field | Description |\n")
	sb.WriteString("|-------|-------------|\n")
	sb.WriteString("| `outcome` | `completed` or `error` |\n")
	sb.WriteString("| `reached` | Nodes that must be scheduled |\n")
	sb.WriteString("| `not_reached` | Nodes that must NOT be scheduled |\n")
	sb.WriteString("| `error_contains` | Substring in error message |\n")
	sb.WriteString("| `node_outputs` | Assert specific output values |\n\n")

	// Quick example
	sb.WriteString("### Example\n\n")
	sb.WriteString("```yaml\n")
	sb.WriteString("name: agent_tool_usage\n")
	sb.WriteString("events:\n")
	sb.WriteString("  - node: agent_loop.call_llm\n")
	sb.WriteString("    type: llm_response\n")
	sb.WriteString("    tool_calls: [{name: bash, input: {command: ls}}]\n")
	sb.WriteString("  - node: agent_loop.execute_tools\n")
	sb.WriteString("    type: tool_result\n")
	sb.WriteString("    tool: bash\n")
	sb.WriteString("    tool_output: {result: \"file.txt\"}\n")
	sb.WriteString("  - node: agent_loop.call_llm\n")
	sb.WriteString("    type: llm_response\n")
	sb.WriteString("    text: \"Found file.txt\"\n")
	sb.WriteString("expect:\n")
	sb.WriteString("  outcome: completed\n")
	sb.WriteString("  reached: [agent_loop.call_llm, agent_loop.execute_tools]\n")
	sb.WriteString("```")

	return sb.String(), nil
}

// cleanMarkdown removes frontmatter and HTML comments
func cleanMarkdown(text string) string {
	// Remove HTML comments
	commentRe := regexp.MustCompile(`(?s)<!--.*?-->`)
	text = commentRe.ReplaceAllString(text, "")

	text = strings.TrimSpace(text)

	// Remove YAML frontmatter
	if strings.HasPrefix(text, "---") {
		rest := text[3:]
		idx := strings.Index(rest, "---")
		if idx != -1 {
			text = strings.TrimSpace(rest[idx+3:])
		}
	}

	// Collapse multiple blank lines
	blankRe := regexp.MustCompile(`\n{3,}`)
	text = blankRe.ReplaceAllString(text, "\n\n")

	return strings.TrimSpace(text)
}
