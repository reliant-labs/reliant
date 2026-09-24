// Copyright (c) 2025 Reliant Labs
package types

import (
	"encoding/json"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"google.golang.org/protobuf/encoding/protojson"
)

// SaveMessageRequest delegates a node's save_message to the worker that
// executes the node.
//
// A node's save_message is written by whoever executes the node. For an
// activity-backed node that is the ActivityWrapper, right after the activity
// returns: it evaluates Config against the activity's result and writes the
// message itself, so the result never has to travel back through the workflow
// and out again as a SaveMessage activity input. The workflow attaches this
// request at dispatch — its presence IS the decision that the save is
// delegated.
//
// Everything here is what the save_message CEL environment may read besides
// `output`: inputs, workflow and iter.
type SaveMessageRequest struct {
	// Config is the node's save_message, verbatim. Carried explicitly because
	// the dispatched node is not always the declared one (execute_tools
	// rebuilds its node without save_message).
	Config *reliantv1.SaveMessageConfig `json:"-"`

	// Inputs holds only the workflow inputs the config references (bare
	// `inputs` or a dynamic index sends the whole map), plus `thread`, which
	// the save always needs. The full inputs map is several KB and would
	// re-bloat the history this delegation exists to shrink.
	Inputs map[string]interface{} `json:"inputs,omitempty"`

	// Workflow is the `workflow` CEL namespace.
	Workflow model.WorkflowContext `json:"workflow"`

	// Iter is the `iter` CEL namespace; nil outside a loop.
	Iter *model.IterContext `json:"iter,omitempty"`

	// AgentName is persisted to messages.agent.
	AgentName string `json:"agent_name,omitempty"`
}

// RunStepSaveMessageKey is the key a SaveMessageRequest rides under in the run
// step's flat map input (the run step has no RuntimeContext). Same JSON name
// as RuntimeContext.SaveMessage, so one lookup finds either.
const RunStepSaveMessageKey = "save_message"

// saveMessageRequestWire is the JSON shape. Config is a proto message with
// oneof fields, so it needs protojson rather than encoding/json (the same
// reason ActivityInput marshals its Node by hand).
type saveMessageRequestWire struct {
	Config    json.RawMessage        `json:"config,omitempty"`
	Inputs    map[string]interface{} `json:"inputs,omitempty"`
	Workflow  model.WorkflowContext  `json:"workflow"`
	Iter      *model.IterContext     `json:"iter,omitempty"`
	AgentName string                 `json:"agent_name,omitempty"`
}

// MarshalJSON implements json.Marshaler.
func (r SaveMessageRequest) MarshalJSON() ([]byte, error) {
	wire := saveMessageRequestWire{
		Inputs:    r.Inputs,
		Workflow:  r.Workflow,
		Iter:      r.Iter,
		AgentName: r.AgentName,
	}
	if r.Config != nil {
		b, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(r.Config)
		if err != nil {
			return nil, err
		}
		wire.Config = b
	}
	return json.Marshal(wire)
}

// UnmarshalJSON implements json.Unmarshaler.
func (r *SaveMessageRequest) UnmarshalJSON(data []byte) error {
	var wire saveMessageRequestWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*r = SaveMessageRequest{
		Inputs:    wire.Inputs,
		Workflow:  wire.Workflow,
		Iter:      wire.Iter,
		AgentName: wire.AgentName,
	}
	if len(wire.Config) > 0 && string(wire.Config) != "null" {
		r.Config = &reliantv1.SaveMessageConfig{}
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(wire.Config, r.Config); err != nil {
			return err
		}
	}
	return nil
}
