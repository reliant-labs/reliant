// Copyright (c) 2025 Reliant Labs
package schema

// NodeOutputRegistry tracks completed node outputs.
// It stores outputs for nodes.node_id.field references in CEL expressions.
type NodeOutputRegistry struct {
	outputs map[string]map[string]interface{} // node_id -> output map
}

// NewNodeOutputRegistry creates a new node output registry
func NewNodeOutputRegistry() *NodeOutputRegistry {
	return &NodeOutputRegistry{
		outputs: make(map[string]map[string]interface{}),
	}
}

// StoreOutput stores a node's output.
// activityName should include the V2_ prefix (e.g., "CallLLM").
// Missing fields are filled with zero values from the schema.
func (r *NodeOutputRegistry) StoreOutput(stepID, activityName string, rawOutput map[string]interface{}) error {
	if rawOutput == nil {
		rawOutput = make(map[string]interface{})
	}

	// Get schema defaults for this activity's output
	defaults := GetOutputDefaults(activityName)

	// Build output with all schema fields present
	validated := make(map[string]interface{})

	// First, apply defaults for all schema fields
	for field, defaultValue := range defaults {
		validated[field] = defaultValue
	}

	// Then overlay actual output values
	for field, value := range rawOutput {
		validated[field] = value
	}

	r.outputs[stepID] = validated
	return nil
}

// GetOutput returns the output for a node, or nil if not found
func (r *NodeOutputRegistry) GetOutput(nodeID string) map[string]interface{} {
	return r.outputs[nodeID]
}

// GetField returns a specific field from a node's output.
// Returns (value, true) if found, (nil, false) if node or field not found.
func (r *NodeOutputRegistry) GetField(nodeID, fieldName string) (interface{}, bool) {
	output := r.outputs[nodeID]
	if output == nil {
		return nil, false
	}
	value, ok := output[fieldName]
	return value, ok
}

// HasNode returns true if the node has stored outputs
func (r *NodeOutputRegistry) HasNode(nodeID string) bool {
	_, ok := r.outputs[nodeID]
	return ok
}

// GetAllOutputs returns a copy of all node outputs for CEL evaluation.
// The returned map is suitable for the nodes.* namespace in CEL.
func (r *NodeOutputRegistry) GetAllOutputs() map[string]interface{} {
	result := make(map[string]interface{})
	for nodeID, output := range r.outputs {
		result[nodeID] = output
	}
	return result
}

// Clear removes all stored outputs (for testing or reset)
func (r *NodeOutputRegistry) Clear() {
	r.outputs = make(map[string]map[string]interface{})
}
