// Copyright (c) 2026 Reliant Labs
package types

import (
	"encoding/json"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"google.golang.org/protobuf/encoding/protojson"
)

// ObserveRequest hands a node's observe: block to the worker that executes the
// node (dev-docs/design/workflow-observe.md, section 4). The ActivityWrapper
// evaluates it right after the node's save_message, over the activity's
// result, and records what it produced: the stored row, the node span's
// attributes and events, and the metrics. Its presence is the decision that
// the node is observed; nothing it does can fail the node.
//
// Carried the way SaveMessageRequest is, for the same reasons: the dispatched
// node is not always the declared one (execute_tools rebuilds its node), and
// only the inputs the block reads are shipped, because the request rides in
// Temporal history.
type ObserveRequest struct {
	// Block is the node's effective observe block: the workflow's defaults
	// merged with the node's own (model.MergeObserve), at most
	// model.ObserveMaxRequestBytes of JSON.
	Block *reliantv1.NodeObserve `json:"-"`

	// Inputs holds only the workflow inputs the block references (bare
	// `inputs` or a dynamic index sends the whole map), plus the trigger and
	// failure the `trigger` and `failure` namespaces are derived from when
	// the block reads them.
	Inputs map[string]interface{} `json:"inputs,omitempty"`

	// Workflow is the `workflow` CEL namespace.
	Workflow model.WorkflowContext `json:"workflow"`

	// Iter is the `iter` CEL namespace; nil outside a loop.
	Iter *model.IterContext `json:"iter,omitempty"`

	// NodeID, NodeType and NodePath name the node the block belongs to: the
	// `node` namespace's id, type and path, and the stored row's node_path.
	NodeID   string `json:"node_id,omitempty"`
	NodeType string `json:"node_type,omitempty"`
	NodePath string `json:"node_path,omitempty"`
}

// RunStepObserveKey is the key an ObserveRequest rides under in the run step's
// flat map input. Same JSON name as RuntimeContext.Observe, so one lookup
// finds either.
const RunStepObserveKey = "observe"

// observeRequestWire is the JSON shape. Block is a proto message, so it needs
// protojson rather than encoding/json (as SaveMessageRequest's Config does).
type observeRequestWire struct {
	Block    json.RawMessage        `json:"block,omitempty"`
	Inputs   map[string]interface{} `json:"inputs,omitempty"`
	Workflow model.WorkflowContext  `json:"workflow"`
	Iter     *model.IterContext     `json:"iter,omitempty"`
	NodeID   string                 `json:"node_id,omitempty"`
	NodeType string                 `json:"node_type,omitempty"`
	NodePath string                 `json:"node_path,omitempty"`
}

// MarshalJSON implements json.Marshaler.
func (r ObserveRequest) MarshalJSON() ([]byte, error) {
	wire := observeRequestWire{
		Inputs:   r.Inputs,
		Workflow: r.Workflow,
		Iter:     r.Iter,
		NodeID:   r.NodeID,
		NodeType: r.NodeType,
		NodePath: r.NodePath,
	}
	if r.Block != nil {
		b, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(r.Block)
		if err != nil {
			return nil, err
		}
		wire.Block = b
	}
	return json.Marshal(wire)
}

// UnmarshalJSON implements json.Unmarshaler. Fields a later release adds to
// the block are ignored, never an error.
func (r *ObserveRequest) UnmarshalJSON(data []byte) error {
	var wire observeRequestWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*r = ObserveRequest{
		Inputs:   wire.Inputs,
		Workflow: wire.Workflow,
		Iter:     wire.Iter,
		NodeID:   wire.NodeID,
		NodeType: wire.NodeType,
		NodePath: wire.NodePath,
	}
	if len(wire.Block) > 0 && string(wire.Block) != "null" {
		r.Block = &reliantv1.NodeObserve{}
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(wire.Block, r.Block); err != nil {
			return err
		}
	}
	return nil
}
