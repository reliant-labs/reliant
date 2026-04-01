// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"fmt"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	yaml "gopkg.in/yaml.v3"
)

// ResolveWorkflowTemplates takes a raw workflow map and resolves all {{...}} template
// expressions using the provided inputs. Returns a new map with resolved values.
//
// Template expressions are evaluated using CEL with inputs available at top level.
// Example: "{{inputs.max_turns}}" accesses the max_turns input.
//
// IMPORTANT: Only templates that can be resolved with inputs are resolved here.
// Templates referencing runtime context (nodes.*, output.*, iter.*) are left as-is
// because they must be evaluated at step execution time.
//
// Deferred sections (preserved as templates for runtime evaluation):
//
//   - "outputs": Workflow-level outputs - resolved at completion via EvaluateWorkflowOutputs()
//     Uses: nodes.*, workflow.* (with execution context)
//
//   - "nodes[*].inputs": Node inputs - resolved at step execution time
//     Uses: nodes.*, inputs.*, workflow.* (with step context)
//
//   - "nodes[*].save_message": Save message configs - resolved after step completion
//     Uses: output.*, nodes.*, inputs.* (with step output context)
//
//   - "nodes[*].loop.inline.outputs": Inline loop outputs - resolved at each loop iteration
//     Uses: nodes.* (scoped to inline loop body nodes)
//
// Why "outputs" appears in both skip maps:
//   - topLevelSkip: Skip workflow.outputs (e.g., outputs.final_result)
//   - runtimeEvaluatedKeys: Skip ANY outputs inside nodes subtree (e.g., nodes[0].loop.inline.outputs)
//     This covers inline loop outputs which reference nodes.* from the loop body.
func ResolveWorkflowTemplates(raw map[string]interface{}, inputs map[string]interface{}) (map[string]interface{}, error) {
	// Build the context for template resolution
	// Templates use "inputs.X" format directly - NOT workflow.inputs.X
	// See cel_env.go for namespace documentation
	context := map[string]interface{}{
		"inputs": inputs,
		// workflow.* provides metadata only, NOT inputs
		"workflow": map[string]interface{}{},
	}

	// topLevelSkip: Keys to skip at workflow root (only applies to direct children of workflow)
	topLevelSkip := map[string]bool{
		"outputs": true, // Workflow outputs reference nodes.*, evaluated at completion
	}

	// runtimeEvaluatedKeys: Keys to skip when inside ANY node in the nodes array
	// This applies to the entire subtree under each node element (including nested structures)
	runtimeEvaluatedKeys := map[string]bool{
		"args":         true, // Node args reference nodes.*/inputs.*, evaluated at step execution
		"save_message": true, // Save message templates reference output.*, evaluated after step
		"outputs":      true, // Outputs inside nodes (e.g., loop.inline.outputs) reference nodes.*, evaluated at runtime
		"thread":       true, // Node thread config (inject.content, etc.) references iter.*, evaluated at runtime
		"project":      true, // Project path may reference nodes.* for dynamic worktree paths
		"run":          true, // Legacy: Run commands can reference nodes.* for dynamic command construction
		"command":      true, // Run commands can reference inputs.*/nodes.* for dynamic command construction
		"yield":        true, // Loop yield condition uses {{inputs.yield}}, evaluated at runtime by evaluateYieldCondition
		"ref":          true, // Dynamic workflow refs (e.g., nodes.classify.response.workflow), evaluated at runtime
	}

	resolved, err := resolveWorkflowTemplatesRecursive(raw, context, topLevelSkip, runtimeEvaluatedKeys, false)
	if err != nil {
		return nil, err
	}

	return resolved.(map[string]interface{}), nil
}

// resolveWorkflowTemplatesRecursive walks a value and resolves template expressions,
// with awareness of workflow structure to skip runtime-evaluated sections.
//
// The skip logic works as follows:
// 1. topLevelSkip applies ONLY to direct children of the workflow root
// 2. runtimeEvaluatedKeys applies to ANY key once we're inside the nodes array
// 3. insideNodeSubtree=true is inherited through the ENTIRE subtree under a node
//
// Example path: workflow → nodes → [0] → loop → inline → outputs
//   - At "outputs" under workflow root: topLevelSkip applies → skip
//   - At "outputs" under nodes[0].loop.inline: insideNodeSubtree=true → skip via runtimeEvaluatedKeys
//
// Parameters:
// - value: the value to process
// - context: CEL evaluation context (inputs.*, workflow.*)
// - topLevelSkip: keys to skip at workflow root only
// - runtimeEvaluatedKeys: keys to skip anywhere inside a node's subtree
// - insideNodeSubtree: true when inside ANY node element from the nodes array (inherited by all descendants)
func resolveWorkflowTemplatesRecursive(
	value interface{},
	context map[string]interface{},
	topLevelSkip map[string]bool,
	runtimeEvaluatedKeys map[string]bool,
	insideNodeSubtree bool,
) (interface{}, error) {
	switch v := value.(type) {
	case map[string]interface{}:
		result := make(map[string]interface{})
		for key, val := range v {
			// Rule 1: Skip top-level keys (only at workflow root, not inherited)
			if topLevelSkip != nil && topLevelSkip[key] {
				result[key] = val
				continue
			}

			// Rule 2: Skip runtime-evaluated keys when inside any node's subtree
			// This flag is inherited through ALL nested structures (loop.inline.outputs, etc.)
			if insideNodeSubtree && runtimeEvaluatedKeys != nil && runtimeEvaluatedKeys[key] {
				result[key] = val
				continue
			}

			// Detect if we're entering the nodes array
			// Clear topLevelSkip for children (no longer at workflow root)
			childTopLevelSkip := topLevelSkip
			if key == "nodes" {
				childTopLevelSkip = nil
			}

			resolved, err := resolveWorkflowTemplatesRecursive(val, context, childTopLevelSkip, runtimeEvaluatedKeys, insideNodeSubtree)
			if err != nil {
				return nil, fmt.Errorf("key '%s': %w", key, err)
			}
			result[key] = resolved
		}
		return result, nil

	case []interface{}:
		result := make([]interface{}, len(v))
		for i, val := range v {
			// Inherit insideNodeSubtree from parent, or set it if entering a node element
			// Once inside a node subtree, ALL descendants remain inside
			childInsideNodeSubtree := insideNodeSubtree
			if !childInsideNodeSubtree && runtimeEvaluatedKeys != nil && isMapValue(val) {
				// Entering a new node element (only matters at nodes array level)
				childInsideNodeSubtree = true
			}

			resolved, err := resolveWorkflowTemplatesRecursive(val, context, nil, runtimeEvaluatedKeys, childInsideNodeSubtree)
			if err != nil {
				return nil, fmt.Errorf("index %d: %w", i, err)
			}
			result[i] = resolved
		}
		return result, nil

	case string:
		return resolveTemplateString(v, context)

	default:
		// Non-string primitives (int, float, bool, nil) pass through unchanged
		return value, nil
	}
}

// isMapValue returns true if the value is a map
func isMapValue(v interface{}) bool {
	_, ok := v.(map[string]interface{})
	return ok
}

// resolveTemplateString resolves {{...}} expressions in a string using CEL.
// If the entire string is a single template (e.g., "{{workflow.inputs.max_turns}}"),
// returns the actual typed value (int, bool, etc).
// If the string contains mixed content, returns string with interpolated values.
// Supports full CEL expressions: arithmetic, conditionals, function calls.
func resolveTemplateString(s string, context map[string]interface{}) (interface{}, error) {
	// Use the existing evaluateCELTemplate which handles all the cases:
	// - No template: returns string as-is
	// - Pure expression: returns native type
	// - Mixed content: interpolates to string
	return evaluateCELTemplate(s, context)
}

// parseResolvedWorkflow parses a raw workflow map (with templates already resolved)
// into a proto V2Workflow.
func parseResolvedWorkflow(resolved map[string]interface{}) (*reliantv1.Workflow, error) {
	// Marshal the resolved map to YAML, then parse via wfyaml.
	yamlData, err := yaml.Marshal(resolved)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal resolved workflow to YAML: %w", err)
	}
	return wfyaml.ParseWorkflow(yamlData)
}

// ResolveAndParseWorkflow resolves all templates in raw workflow YAML using
// the provided inputs, then parses to a proto V2Workflow.
//
// Use this at runtime when inputs are available.
func ResolveAndParseWorkflow(yamlData []byte, inputs map[string]interface{}) (*reliantv1.Workflow, error) {
	// Parse to raw map
	var raw map[string]interface{}
	if err := yaml.Unmarshal(yamlData, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse workflow YAML: %w", err)
	}

	// Resolve all templates
	resolved, err := ResolveWorkflowTemplates(raw, inputs)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve workflow templates: %w", err)
	}

	// Parse to proto
	return parseResolvedWorkflow(resolved)
}
