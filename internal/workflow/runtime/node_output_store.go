// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"github.com/reliant-labs/reliant/internal/logging"
)

// NodeOutputStore provides centralized management for step outputs.
// It wraps the underlying map[string]interface{} and provides:
// - Consistent Set/Get/Has operations
// - Optional debug logging
// - Future extensibility for output validation
//
// This replaces the scattered direct map access throughout the workflow execution code.
type NodeOutputStore struct {
	data         map[string]interface{}
	debugEnabled bool
	workflowName string // For debug logging context
}

// NewNodeOutputStore creates a new store.
func NewNodeOutputStore(workflowName string) *NodeOutputStore {
	return &NodeOutputStore{
		data:         make(map[string]interface{}),
		workflowName: workflowName,
	}
}

// NewNodeOutputStoreFrom creates a store from an existing map.
// This is useful for gradual migration - existing code can pass a map
// and the store wraps it without copying.
func NewNodeOutputStoreFrom(data map[string]interface{}, workflowName string) *NodeOutputStore {
	if data == nil {
		data = make(map[string]interface{})
	}
	return &NodeOutputStore{
		data:         data,
		workflowName: workflowName,
	}
}

// EnableDebug turns on debug logging for all store operations.
func (s *NodeOutputStore) EnableDebug() *NodeOutputStore {
	s.debugEnabled = true
	return s
}

// Set stores an output for a step ID.
// If output is nil, the step is still marked as completed (with nil output).
func (s *NodeOutputStore) Set(stepID string, output interface{}) {
	s.data[stepID] = output

	if s.debugEnabled {
		logging.Debug("[NodeOutputStore] Set",
			"workflow", s.workflowName,
			"step", stepID,
			"hasOutput", output != nil,
		)
	}
}

// Get retrieves the output for a step ID.
// Returns (output, true) if found, (nil, false) if not found.
func (s *NodeOutputStore) Get(stepID string) (interface{}, bool) {
	output, exists := s.data[stepID]
	return output, exists
}

// Has returns true if output exists for the given step ID.
func (s *NodeOutputStore) Has(stepID string) bool {
	_, exists := s.data[stepID]
	return exists
}

// Keys returns all step IDs that have outputs.
func (s *NodeOutputStore) Keys() []string {
	keys := make([]string, 0, len(s.data))
	for k := range s.data {
		keys = append(keys, k)
	}
	return keys
}

// Count returns the number of stored outputs.
func (s *NodeOutputStore) Count() int {
	return len(s.data)
}

// AsMap returns the underlying map for CEL context building.
// The returned map is the actual data, not a copy.
// This allows existing code that expects map[string]interface{} to work.
func (s *NodeOutputStore) AsMap() map[string]interface{} {
	return s.data
}

// Clone creates a deep copy of the store.
// Useful when spawning sub-workflows that need their own output space.
func (s *NodeOutputStore) Clone() *NodeOutputStore {
	newData := make(map[string]interface{}, len(s.data))
	for k, v := range s.data {
		newData[k] = v
	}
	return &NodeOutputStore{
		data:         newData,
		debugEnabled: s.debugEnabled,
		workflowName: s.workflowName,
	}
}

// Merge copies all outputs from another store into this one.
// Existing keys are overwritten.
func (s *NodeOutputStore) Merge(other *NodeOutputStore) {
	if other == nil {
		return
	}
	for k, v := range other.data {
		s.data[k] = v
	}
}

// MergeMap copies all outputs from a map into this store.
// Existing keys are overwritten.
func (s *NodeOutputStore) MergeMap(data map[string]interface{}) {
	for k, v := range data {
		s.data[k] = v
	}
}

// Clear removes all stored outputs.
func (s *NodeOutputStore) Clear() {
	s.data = make(map[string]interface{})
}

// GetMap retrieves the output for a step ID as a map.
// Returns nil if not found or if the output is not a map.
// This is a convenience method for the common case where step outputs are maps.
func (s *NodeOutputStore) GetMap(stepID string) map[string]interface{} {
	output, exists := s.data[stepID]
	if !exists {
		return nil
	}
	if m, ok := output.(map[string]interface{}); ok {
		return m
	}
	return nil
}

// GetString retrieves a string field from a step's output map.
// Returns empty string if step not found, output not a map, or field not a string.
func (s *NodeOutputStore) GetString(stepID, field string) string {
	m := s.GetMap(stepID)
	if m == nil {
		return ""
	}
	if v, ok := m[field].(string); ok {
		return v
	}
	return ""
}

// GetBool retrieves a boolean field from a step's output map.
// Returns false if step not found, output not a map, or field not a bool.
func (s *NodeOutputStore) GetBool(stepID, field string) bool {
	m := s.GetMap(stepID)
	if m == nil {
		return false
	}
	if v, ok := m[field].(bool); ok {
		return v
	}
	return false
}

// GetInt retrieves an integer field from a step's output map.
// Returns 0 if step not found, output not a map, or field not an int.
func (s *NodeOutputStore) GetInt(stepID, field string) int {
	m := s.GetMap(stepID)
	if m == nil {
		return 0
	}
	switch v := m[field].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return 0
}
