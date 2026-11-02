// Copyright (c) 2025 Reliant Labs
package types

// The run's bookkeeping activities — ActivityLoadWorkflow, LoadPresetParams
// and WorkflowCheckpoint — are dispatched as LOCAL activities
// (runtime.executeBookkeeping), so their inputs live here for the reason
// mailbox.go gives: a local activity's argument is handed to the registered
// function by reflection, and must already be its parameter type. handlers
// re-exports each as an alias.

// LoadWorkflowInput contains the input for loading a workflow
type LoadWorkflowInput struct {
	ChatID       string `json:"chat_id" reliant:"-"` // Chat ID to resolve project path
	WorkflowName string `json:"workflow_name"`       // Workflow name or path
	// CatalogPin is the project catalog the run started with (the root
	// load's LoadWorkflowOutput.CatalogPin): every later load of the run (a
	// child, a loop body, a router target) reads its project workflows and
	// presets from it, so one run executes one definition tree. Empty pins
	// the catalog as it is now: a root load, or a run recorded before pins.
	CatalogPin string `json:"catalog_pin,omitempty" reliant:"-"`
}

// LoadPresetParamsInput is the input for loading preset params
type LoadPresetParamsInput struct {
	ProjectPath string `json:"project_path"`
	ProjectID   string `json:"project_id"`
	// ChatID is the run's chat: its project, and the machine it runs on,
	// whose synced presets a project preset is read from. It wins over
	// ProjectID.
	ChatID     string `json:"chat_id" reliant:"-"`
	PresetName string `json:"preset_name"`
	// CatalogPin is the project catalog the run started with
	// (LoadWorkflowInput.CatalogPin): a project preset is read from it.
	CatalogPin string `json:"catalog_pin,omitempty" reliant:"-"`
}

// WorkflowCheckpointInput is the input for the WorkflowCheckpoint activity.
// It records the position a workflow run has reached: the top-level node it
// just entered and, for loop nodes, the loop iteration in flight. This is the
// position truth used by SendMessage to resume an interrupted
// (failed/terminated) run at position instead of restarting at graph entry.
type WorkflowCheckpointInput struct {
	ChatID        string `json:"chat_id" reliant:"-"`
	WorkflowID    string `json:"workflow_id"`
	NodeID        string `json:"node_id"`
	LoopIteration int64  `json:"loop_iteration,omitempty"`
	// CatalogPin is the catalog the run started with, recorded beside its
	// position so a restart from here loads the same definitions.
	CatalogPin string `json:"catalog_pin,omitempty"`
}
