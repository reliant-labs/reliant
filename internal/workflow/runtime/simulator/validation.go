// Copyright (c) 2025 Reliant Labs
package simulator

import (
	"fmt"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// ValidationResult contains the results of scenario validation.
type ValidationResult struct {
	Valid    bool     // True if no errors (warnings are ok)
	Errors   []string // Critical errors that prevent execution
	Warnings []string // Non-critical warnings (e.g., unknown output fields)
}

// AddError adds an error to the result and marks it as invalid.
func (r *ValidationResult) AddError(format string, args ...interface{}) {
	r.Errors = append(r.Errors, fmt.Sprintf(format, args...))
	r.Valid = false
}

// AddWarning adds a warning to the result (doesn't affect validity).
func (r *ValidationResult) AddWarning(format string, args ...interface{}) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}

// String returns a formatted string of all errors and warnings.
func (r *ValidationResult) String() string {
	var parts []string
	if len(r.Errors) > 0 {
		parts = append(parts, "Errors:")
		for _, e := range r.Errors {
			parts = append(parts, "  - "+e)
		}
	}
	if len(r.Warnings) > 0 {
		parts = append(parts, "Warnings:")
		for _, w := range r.Warnings {
			parts = append(parts, "  - "+w)
		}
	}
	return strings.Join(parts, "\n")
}

// ValidateScenario validates a scenario against a workflow definition.
// Returns a ValidationResult with errors and warnings.
//
// Validations performed:
// 1. All event.Node references exist in the workflow (including qualified IDs)
// 2. All expect.Reached nodes exist in the workflow
// 3. All expect.NotReached nodes exist in the workflow
// 4. Scenario inputs match workflow input schema
// 5. Output fields match activity types (warnings only)
// 6. No mixing of black-box and internal events for the same workflow node
func ValidateScenario(scenario *Scenario, workflow *reliantv1.Workflow) *ValidationResult {
	result := &ValidationResult{Valid: true}

	// Collect all valid node IDs from the workflow
	validNodes := collectValidNodes(workflow, "")

	// Validate event node references
	for i, event := range scenario.Events {
		if event.Node != "" {
			if !isValidNodeRef(event.Node, validNodes) {
				result.AddError("event[%d]: unknown node %q (valid nodes: %s)",
					i, event.Node, formatNodeList(validNodes))
			} else {
				// Validate output fields against activity type (warnings only)
				validateOutputFields(result, event, workflow, i)
			}
		}
	}

	// Validate expect.Reached nodes
	if scenario.Expect != nil {
		for _, nodeRef := range scenario.Expect.Reached {
			if !isValidNodeRef(nodeRef, validNodes) {
				result.AddError("expect.reached: unknown node %q", nodeRef)
			}
		}

		// Validate expect.NotReached nodes
		for _, nodeRef := range scenario.Expect.NotReached {
			if !isValidNodeRef(nodeRef, validNodes) {
				result.AddError("expect.not_reached: unknown node %q", nodeRef)
			}
		}

		// Validate expect.Completed nodes
		for _, nodeRef := range scenario.Expect.Completed {
			if !isValidNodeRef(nodeRef, validNodes) {
				result.AddError("expect.completed: unknown node %q", nodeRef)
			}
		}

		// Validate expect.Skipped nodes
		for _, nodeRef := range scenario.Expect.Skipped {
			if !isValidNodeRef(nodeRef, validNodes) {
				result.AddError("expect.skipped: unknown node %q", nodeRef)
			}
		}

		// Validate expect.NodeOutputs references
		for nodeRef := range scenario.Expect.NodeOutputs {
			if !isValidNodeRef(nodeRef, validNodes) {
				result.AddError("expect.node_outputs: unknown node %q", nodeRef)
			}
		}
	}

	// Validate StartAt node
	if scenario.StartAt != "" {
		if !isValidNodeRef(scenario.StartAt, validNodes) {
			result.AddError("start_at: unknown node %q", scenario.StartAt)
		}
	}

	// Validate State node references
	for nodeRef := range scenario.State {
		if !isValidNodeRef(nodeRef, validNodes) {
			result.AddError("state: unknown node %q", nodeRef)
		}
	}

	// Validate scenario inputs against workflow input schema
	validateScenarioInputs(result, scenario.Inputs, workflow.GetInputs(), "")

	// Validate no mixing of black-box and internal events for same node
	validateMockingConsistency(result, scenario.Events)

	return result
}

// collectValidNodes recursively collects all valid node IDs from a workflow.
// The prefix is used for qualified IDs (e.g., "loop_id.inner_node_id").
func collectValidNodes(workflow *reliantv1.Workflow, prefix string) map[string]nodeInfo {
	nodes := make(map[string]nodeInfo)
	if workflow == nil {
		return nodes
	}

	for _, node := range workflow.GetNodes() {
		nodeID := node.GetId()
		nodeType := node.GetType()

		// Build qualified ID
		qualifiedID := nodeID
		if prefix != "" {
			qualifiedID = prefix + "." + nodeID
		}

		// Add the node with its type
		nodes[qualifiedID] = nodeInfo{
			nodeType:  nodeType,
			protoNode: node,
		}

		// For loop and workflow nodes with inline definitions, recurse
		if la := model.GetLoopArgs(node); la != nil {
			if la.GetInline() != nil {
				innerNodes := collectValidNodes(la.GetInline(), qualifiedID)
				for id, info := range innerNodes {
					nodes[id] = info
				}
			}
			if ref := model.CelStringRaw(la.GetRef()); ref != "" {
				nodes[ref] = nodeInfo{
					nodeType: "ref",
					isRef:    true,
				}
			}
		} else if swa := model.GetSubWorkflowArgs(node); swa != nil {
			if swa.GetInline() != nil {
				innerNodes := collectValidNodes(swa.GetInline(), qualifiedID)
				for id, info := range innerNodes {
					nodes[id] = info
				}
			}
			if ref := model.CelStringRaw(swa.GetRef()); ref != "" {
				nodes[ref] = nodeInfo{
					nodeType: "ref",
					isRef:    true,
				}
			}
		}
	}

	return nodes
}

// nodeInfo stores information about a node for validation.
type nodeInfo struct {
	nodeType  string
	protoNode *reliantv1.Node
	isRef     bool // True if this is a ref target, not an actual node
}

// isValidNodeRef checks if a node reference is valid.
// For qualified IDs (e.g., "impl_1.agent_loop.call_llm"), it validates as much
// as possible but allows internal paths through workflow-type nodes with refs
// since those reference external workflows we can't validate.
func isValidNodeRef(nodeRef string, validNodes map[string]nodeInfo) bool {
	// Direct match
	if _, ok := validNodes[nodeRef]; ok {
		return true
	}

	// For qualified IDs, check if we're traversing through a workflow node with a ref
	if strings.Contains(nodeRef, ".") {
		parts := strings.Split(nodeRef, ".")
		// Build up the path and check each segment
		for i := 1; i < len(parts); i++ {
			parentPath := strings.Join(parts[:i], ".")
			if info, ok := validNodes[parentPath]; ok && info.protoNode != nil {
				// If this is a workflow node with a ref, we can't validate deeper
				// Allow the reference since internal nodes are in an external workflow
				if swa := model.GetSubWorkflowArgs(info.protoNode); swa != nil && model.CelStringRaw(swa.GetRef()) != "" {
					return true
				}
				if la := model.GetLoopArgs(info.protoNode); la != nil && model.CelStringRaw(la.GetRef()) != "" {
					return true
				}
			}
		}
	}

	return false
}

// formatNodeList formats a list of valid nodes for error messages.
func formatNodeList(nodes map[string]nodeInfo) string {
	if len(nodes) == 0 {
		return "(none)"
	}
	names := make([]string, 0, len(nodes))
	for name, info := range nodes {
		if !info.isRef {
			names = append(names, name)
		}
	}
	if len(names) > 10 {
		return strings.Join(names[:10], ", ") + fmt.Sprintf(" ... and %d more", len(names)-10)
	}
	return strings.Join(names, ", ")
}

// validateOutputFields validates that output fields match the activity type.
// Uses the workflow CEL TypeRegistry to check field names.
// Adds warnings for unknown fields (not errors, since outputs are flexible).
func validateOutputFields(result *ValidationResult, event SimulatedEvent, workflow *reliantv1.Workflow, eventIndex int) {
	if len(event.Output) == 0 {
		return
	}

	// Get the node type for this event
	nodeType := getNodeType(event.Node, workflow)
	if nodeType == "" {
		return // Can't determine type, skip validation
	}

	// Look up the type in the registry
	registry := wfcel.NewTypeRegistry()
	fields := registry.OutputFieldsForNodeType(nodeType)
	if fields == nil {
		return // Type not registered, skip validation
	}

	// Build a set of valid field names
	validFieldNames := make(map[string]bool, len(fields))
	for _, f := range fields {
		validFieldNames[f.Name] = true
	}

	// Check each output field
	for fieldName := range event.Output {
		if !validFieldNames[fieldName] {
			names := make([]string, 0, len(fields))
			for _, f := range fields {
				names = append(names, f.Name)
			}
			result.AddWarning("event[%d]: output field %q not in %s type (valid: %s)",
				eventIndex, fieldName, nodeType, strings.Join(names, ", "))
		}
	}
}

// getNodeType returns the node type for a qualified node ID.
func getNodeType(qualifiedID string, workflow *reliantv1.Workflow) string {
	if workflow == nil {
		return ""
	}

	parts := strings.Split(qualifiedID, ".")
	return findNodeType(parts, workflow)
}

// findNodeType recursively finds the node type for a path of IDs.
func findNodeType(path []string, workflow *reliantv1.Workflow) string {
	if len(path) == 0 || workflow == nil {
		return ""
	}

	// Find the node with this ID
	for _, node := range workflow.GetNodes() {
		if node.GetId() == path[0] {
			if len(path) == 1 {
				// This is the target node
				return node.GetType()
			}
			// Need to go deeper into inline workflow
			if la := model.GetLoopArgs(node); la != nil && la.GetInline() != nil {
				return findNodeType(path[1:], la.GetInline())
			}
			if swa := model.GetSubWorkflowArgs(node); swa != nil && swa.GetInline() != nil {
				return findNodeType(path[1:], swa.GetInline())
			}
			return "" // Can't traverse into ref nodes
		}
	}

	return ""
}

// validateScenarioInputs validates scenario inputs against workflow input schema.
func validateScenarioInputs(result *ValidationResult, inputs map[string]interface{}, schema map[string]*reliantv1.Input, prefix string) {
	if schema == nil {
		return
	}

	for name, value := range inputs {
		inputPath := name
		if prefix != "" {
			inputPath = prefix + "." + name
		}

		// Check if this input is defined in the schema
		input, ok := schema[name]
		if !ok {
			result.AddWarning("input %q is not defined in workflow schema", inputPath)
			continue
		}

		// For group inputs, recursively validate
		if input.GetType() == "group" {
			if gc := input.GetGroupInput(); gc != nil && gc.GetInputs() != nil {
				if nestedInputs, ok := value.(map[string]interface{}); ok {
					validateScenarioInputs(result, nestedInputs, gc.GetInputs(), inputPath)
				}
			}
			continue
		}

		// Basic type validation for common input types
		if value != nil {
			if err := validateInputValue(input, value); err != nil {
				result.AddError("input %q: %s", inputPath, err)
			}
		}
	}
}

// validateInputValue performs basic type checking for scenario inputs.
func validateInputValue(input *reliantv1.Input, value interface{}) error {
	switch input.GetType() {
	case "string", "message":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("expected string, got %T", value)
		}
	case "number":
		switch value.(type) {
		case float64, float32, int, int64, int32:
			// ok
		default:
			return fmt.Errorf("expected number, got %T", value)
		}
	case "integer":
		switch v := value.(type) {
		case int, int64, int32:
			// ok
		case float64:
			if v != float64(int(v)) {
				return fmt.Errorf("expected integer, got float %v", v)
			}
		default:
			return fmt.Errorf("expected integer, got %T", value)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("expected boolean, got %T", value)
		}
	case "enum":
		if ec := input.GetEnumInput(); ec != nil {
			allowed := ec.GetEnumValues()
			if ec.GetMulti() {
				// Multi-select: value must be an array of strings
				var items []string
				switch v := value.(type) {
				case []string:
					items = v
				case []interface{}:
					for _, elem := range v {
						s, ok := elem.(string)
						if !ok {
							return fmt.Errorf("expected array of strings for multi-enum, got element %T", elem)
						}
						items = append(items, s)
					}
				default:
					return fmt.Errorf("expected array for multi-select enum, got %T", value)
				}
				for _, item := range items {
					found := false
					for _, a := range allowed {
						if a == item {
							found = true
							break
						}
					}
					if !found {
						return fmt.Errorf("%q is not in enum list %v", item, allowed)
					}
				}
			} else {
				// Single-select: value must be a string
				str, ok := value.(string)
				if !ok {
					return fmt.Errorf("expected string for enum, got %T", value)
				}
				found := false
				for _, v := range allowed {
					if v == str {
						found = true
						break
					}
				}
				if !found {
					return fmt.Errorf("%q is not in enum list %v", str, allowed)
				}
			}
		}
	}
	return nil
}

// ValidateScenarioNodes is a convenience function that only validates node references.
// Use this for quick validation when you don't need full validation.
func ValidateScenarioNodes(scenario *Scenario, workflow *reliantv1.Workflow) []string {
	result := ValidateScenario(scenario, workflow)
	return result.Errors
}

// validateMockingConsistency checks that scenarios don't mix black-box and internal
// events for the same workflow node.
//
// For example, these events conflict:
//   - node: impl_1 (black-box: "here's what impl_1 returns")
//   - node: impl_1.agent_loop.call_llm (internal: "simulate impl_1's internals")
//
// You must choose one approach per workflow node.
func validateMockingConsistency(result *ValidationResult, events []SimulatedEvent) {
	// Collect all node references from events
	blackBoxNodes := make(map[string]bool) // Nodes targeted directly (black-box)
	internalNodes := make(map[string]bool) // Parent nodes of qualified IDs (internal)

	for _, event := range events {
		if event.Node == "" {
			continue // Sequential event, skip
		}

		if strings.Contains(event.Node, ".") {
			// This is a qualified ID (internal mocking)
			// Extract the top-level node ID
			parts := strings.SplitN(event.Node, ".", 2)
			topLevel := parts[0]
			internalNodes[topLevel] = true
		} else {
			// This is a direct node ID (black-box mocking)
			blackBoxNodes[event.Node] = true
		}
	}

	// Check for conflicts
	for node := range blackBoxNodes {
		if internalNodes[node] {
			result.AddError(
				"node %q has both black-box events (node: %q) and internal events (node: %q.*). "+
					"Choose one mocking approach per workflow node.",
				node, node, node,
			)
		}
	}
}
