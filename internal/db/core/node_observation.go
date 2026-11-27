package core

import "time"

// Who recorded a node observation (node_observations.source).
const (
	// NodeObservationSourceWorkflow is the node's observe: block.
	NodeObservationSourceWorkflow = "workflow"
	// NodeObservationSourceModel is an event the model recorded itself
	// (dev-docs/design/workflow-observe.md, section 12). Not written yet.
	NodeObservationSourceModel = "model"
	// NodeObservationSourceReconciler is the workflow reconciler's own
	// record of a run (its stall watchdog), not any node's: its step_id
	// names the reconciler check, not a node of the workflow.
	NodeObservationSourceReconciler = "reconciler"
)

// NodeObservation is what one node attempt's observe: block recorded when the
// node finished: the attributes and events its expressions produced, already
// redacted and bounded by the writer (internal/workflow/observe), and the
// expressions that failed. One row per node attempt and source.
type NodeObservation struct {
	ID         string
	ChatID     string
	WorkflowID string
	// RunID is the root run (forge.ai.run.id); "" when the activity carried
	// no run telemetry.
	RunID    string
	StepID   string
	NodePath string
	// LoopNodeID and LoopIteration are the loop scope, nil outside a loop.
	LoopNodeID    *string
	LoopIteration *int64
	Attempt       int32
	Source        string
	// Attributes, Events and Errors are JSON text: an object, an array of
	// {name, attributes} and an array of strings.
	Attributes string
	Events     string
	Errors     string
	Truncated  bool
	CreatedAt  time.Time
}

// NodeObservationWrite is how a RecordNodeObservation call ended.
type NodeObservationWrite int

const (
	// NodeObservationStored: the row was written.
	NodeObservationStored NodeObservationWrite = iota
	// NodeObservationDuplicate: the same node attempt had already recorded
	// one (an activity retry, a replayed recording); nothing was written.
	NodeObservationDuplicate
	// NodeObservationCapped: the run already holds its cap of rows; nothing
	// was written.
	NodeObservationCapped
)
