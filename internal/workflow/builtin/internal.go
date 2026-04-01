// Copyright (c) 2025 Reliant Labs
package builtin

// InternalWorkflowNames is the list of workflow names that are internal
// (not shown in the workflow picker UI, but still executable).
var InternalWorkflowNames = map[string]bool{
	"compact": true,
}

// IsInternalWorkflow returns true if the workflow name is an internal workflow.
func IsInternalWorkflow(name string) bool {
	return InternalWorkflowNames[name]
}

// internalWorkflowYAML stores the YAML definitions for internal workflows.
// These are defined inline rather than as embedded files since they are
// simple utility workflows that don't need to be user-editable.
var internalWorkflowYAML = map[string][]byte{
	"compact": []byte(`name: compact
apiVersion: "0.0.5"
description: "Manual context compaction workflow - summarizes conversation history to free up context window"
entry:
  - compact
nodes:
  - id: compact
    type: compact
    timeout: "10m"
edges: []
`),
}

// GetInternalWorkflowYAML returns the YAML bytes for an internal workflow, or nil if not found.
func GetInternalWorkflowYAML(name string) []byte {
	return internalWorkflowYAML[name]
}
