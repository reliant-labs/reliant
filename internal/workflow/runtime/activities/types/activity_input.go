// Copyright (c) 2025 Reliant Labs
package types

import (
	"encoding/json"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// ActivityInput wraps RuntimeContext and a Node for Temporal serialization.
// Each handler extracts its specific args using model.GetXxxArgs(input.Node).
//
// Placed in activities/types to avoid import cycles between the runtime engine
// and activity handlers packages — both can import this package.
type ActivityInput struct {
	Runtime RuntimeContext  `json:"runtime"`
	Node    *reliantv1.Node `json:"node"`
}

// MarshalJSON implements custom JSON marshaling for ActivityInput.
// The Node field is a proto message with oneof fields that require protojson for proper serialization.
func (a ActivityInput) MarshalJSON() ([]byte, error) {
	var nodeJSON json.RawMessage
	if a.Node != nil {
		marshaler := protojson.MarshalOptions{
			EmitUnpopulated: false,
			UseProtoNames:   true,
		}
		b, err := marshaler.Marshal(a.Node)
		if err != nil {
			return nil, err
		}
		nodeJSON = b
	} else {
		nodeJSON = []byte("null")
	}

	type runtimeOnly struct {
		Runtime RuntimeContext  `json:"runtime"`
		Node    json.RawMessage `json:"node"`
	}
	return json.Marshal(runtimeOnly{
		Runtime: a.Runtime,
		Node:    nodeJSON,
	})
}

// UnmarshalJSON implements custom JSON unmarshaling for ActivityInput.
// The Node field is a proto message with oneof fields that require protojson for proper deserialization.
func (a *ActivityInput) UnmarshalJSON(data []byte) error {
	type runtimeOnly struct {
		Runtime RuntimeContext  `json:"runtime"`
		Node    json.RawMessage `json:"node"`
	}
	var raw runtimeOnly
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	a.Runtime = raw.Runtime

	if len(raw.Node) > 0 && string(raw.Node) != "null" {
		a.Node = &reliantv1.Node{}
		unmarshaler := protojson.UnmarshalOptions{
			DiscardUnknown: true,
		}
		if err := unmarshaler.Unmarshal(raw.Node, a.Node); err != nil {
			return err
		}
	}

	return nil
}
