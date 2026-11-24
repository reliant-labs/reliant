// Copyright (c) 2026 Reliant Labs
package types

// RecordNodeObservationActivityName is the local activity that records the
// observe: block of a node the workflow completes itself: compute, join,
// router, loop, workflow, approval, ask_question, a tool batch the workflow
// assembles, and a tolerated failure (dev-docs/design/workflow-observe.md,
// section 4, site B). An activity-backed node is observed by the
// ActivityWrapper instead, so no node is observed twice.
const RecordNodeObservationActivityName = "RecordNodeObservation"

// RecordNodeObservationInput is what the workflow hands RecordNodeObservation:
// the node's observe request, the output it completed with, and the identity
// its stored row is keyed by.
//
// A local activity's argument reaches the registered function by reflection,
// never through the data converter (mailbox.go), so this is the exact type the
// registered function takes. For the same reason it is never written to
// Temporal history: only the activity's marker is, so shipping the output here
// costs the history nothing.
//
// The top-level chat_id, thread, workflow_id, step_id, node_type, loop scope
// and telemetry are the shape the run-scope interceptor reads
// (observability.RunScopeOfActivityArgs): it puts the run on the activity's
// context and opens the node's workflow.node span around it.
type RecordNodeObservationInput struct {
	ChatID        string        `json:"chat_id"`
	Thread        string        `json:"thread,omitempty"`
	WorkflowID    string        `json:"workflow_id"`
	StepID        string        `json:"step_id"`
	NodeType      string        `json:"node_type"`
	NodePath      string        `json:"node_path,omitempty"`
	LoopNodeID    string        `json:"loop_node_id,omitempty"`
	LoopIteration int           `json:"loop_iteration,omitempty"`
	Telemetry     *RunTelemetry `json:"telemetry,omitempty"`

	// Attempt keys the stored row. The workflow completed the node once, so
	// it is always 1: a retried or replayed local activity converges on the
	// same row.
	Attempt int32 `json:"attempt"`
	// DurationMs is the node's `node.duration_ms`: how long the workflow
	// held the node, by workflow time, or 0 where the workflow does not time
	// it.
	DurationMs int64 `json:"duration_ms,omitempty"`

	// Observe is the node's effective observe block and the context its
	// expressions read, exactly as an activity-backed node receives it.
	Observe *ObserveRequest `json:"observe,omitempty"`
	// Output is the node's result as the workflow sees it (nodes.<id>):
	// what the block's `output` reads.
	Output map[string]interface{} `json:"output,omitempty"`
}
