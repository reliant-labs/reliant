// Copyright (c) 2025 Reliant Labs
package runtime

// ============================================================================
// RUNTIME THREAD TRACKING
// ============================================================================
//
// This provides simple runtime tracking of threads for UI display.
// Thread activity is determined by workflow execution state, not static analysis.

// LogicalThreadName represents a logical thread identifier.
// Format: "thread:root" for the main workflow thread.
type LogicalThreadName string

const (
	// ThreadRoot is the logical name for the workflow's main thread
	ThreadRoot LogicalThreadName = "thread:root"
)

// String returns the string representation
func (l LogicalThreadName) String() string {
	return string(l)
}

// RuntimeThreadMapping tracks threads and step completion at runtime.
type RuntimeThreadMapping struct {
	// LogicalToActual maps logical thread names to their resolved UUIDs
	LogicalToActual map[LogicalThreadName]string

	// ActualToLogical maps actual UUIDs back to logical names
	ActualToLogical map[string]LogicalThreadName

	// CompletedSteps tracks which steps have completed
	CompletedSteps map[string]bool
}

// NewRuntimeThreadMapping creates a new runtime thread mapping tracker
func NewRuntimeThreadMapping() *RuntimeThreadMapping {
	return &RuntimeThreadMapping{
		LogicalToActual: make(map[LogicalThreadName]string),
		ActualToLogical: make(map[string]LogicalThreadName),
		CompletedSteps:  make(map[string]bool),
	}
}

// RecordThreadResolution records the mapping when a thread is resolved.
func (m *RuntimeThreadMapping) RecordThreadResolution(logicalName LogicalThreadName, actualUUID string) {
	if m == nil || logicalName == "" || actualUUID == "" {
		return
	}

	m.LogicalToActual[logicalName] = actualUUID
	m.ActualToLogical[actualUUID] = logicalName
}

// MarkStepCompleted marks a step as completed
func (m *RuntimeThreadMapping) MarkStepCompleted(stepID string) {
	if m == nil {
		return
	}
	m.CompletedSteps[stepID] = true
}

// IsStepCompleted checks if a step has completed
func (m *RuntimeThreadMapping) IsStepCompleted(stepID string) bool {
	if m == nil || m.CompletedSteps == nil {
		return false
	}
	return m.CompletedSteps[stepID]
}

// GetLogicalName returns the logical thread name for an actual UUID
func (m *RuntimeThreadMapping) GetLogicalName(actualUUID string) LogicalThreadName {
	if m == nil || m.ActualToLogical == nil {
		return ""
	}
	return m.ActualToLogical[actualUUID]
}

// GetActualUUID returns the actual UUID for a logical thread name
func (m *RuntimeThreadMapping) GetActualUUID(logicalName LogicalThreadName) string {
	if m == nil || m.LogicalToActual == nil {
		return ""
	}
	return m.LogicalToActual[logicalName]
}

// ============================================================================
// THREAD STATUS FOR UI
// ============================================================================

// ThreadStatus provides status information about a thread for API/UI.
// With the simplified model, IsActive is true while the workflow is running.
type ThreadStatus struct {
	LogicalName LogicalThreadName `json:"logical_name"`
	ActualUUID  string            `json:"actual_uuid,omitempty"`
	IsActive    bool              `json:"is_active"`
}

// ThreadTracker provides simple thread status tracking for UI display.
// Thread activity is based on workflow execution state.
type ThreadTracker struct {
	Mapping *RuntimeThreadMapping
}

// NewThreadTracker creates a new thread tracker
func NewThreadTracker() *ThreadTracker {
	return &ThreadTracker{
		Mapping: NewRuntimeThreadMapping(),
	}
}

// GetAllThreadStatuses returns status for all tracked threads.
// All threads are considered active while the workflow is running.
func (t *ThreadTracker) GetAllThreadStatuses() []*ThreadStatus {
	if t == nil || t.Mapping == nil {
		return nil
	}

	var statuses []*ThreadStatus
	for logicalName, actualUUID := range t.Mapping.LogicalToActual {
		statuses = append(statuses, &ThreadStatus{
			LogicalName: logicalName,
			ActualUUID:  actualUUID,
			IsActive:    true, // Active while workflow is running
		})
	}
	return statuses
}

// GetThreadStatus returns status for a specific thread
func (t *ThreadTracker) GetThreadStatus(logicalName LogicalThreadName) *ThreadStatus {
	if t == nil || t.Mapping == nil {
		return nil
	}

	actualUUID := t.Mapping.GetActualUUID(logicalName)
	if actualUUID == "" {
		return nil
	}

	return &ThreadStatus{
		LogicalName: logicalName,
		ActualUUID:  actualUUID,
		IsActive:    true, // Active while workflow is running
	}
}
