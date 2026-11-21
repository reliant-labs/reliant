// Copyright (c) 2025 Reliant Labs
//
//forge:exclude-contract: Temporal activity payload types and their JSON codecs; no I/O or state
package types

import (
	"bytes"
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
		refused, err := legacyRefusedToolCalls(raw.Node)
		if err != nil {
			return err
		}
		for id, reason := range refused {
			if a.Runtime.ActivationRefusals == nil {
				a.Runtime.ActivationRefusals = make(map[string]string, len(refused))
			}
			if _, ok := a.Runtime.ActivationRefusals[id]; !ok {
				a.Runtime.ActivationRefusals[id] = reason
			}
		}
	}

	return nil
}

// legacyRefusedToolCalls reads ExecuteToolsArgs.refused_tool_calls (field 7,
// now reserved) from an ExecuteTools activity an earlier release scheduled:
// the calls a person denied at the removed integration action approval
// prompt. protojson discards the field as unknown, and the activity would then
// run a call the person refused, so it is read here and enforced as an
// activation refusal. A payload it cannot read fails the decode rather than
// dropping a denial. Delete it once no ExecuteTools activity scheduled by a
// release that still had the field can be pending.
func legacyRefusedToolCalls(node json.RawMessage) (map[string]string, error) {
	type args struct {
		Proto map[string]string `json:"refused_tool_calls"`
		JSON  map[string]string `json:"refusedToolCalls"`
	}
	if !bytes.Contains(node, []byte("refused_tool_calls")) && !bytes.Contains(node, []byte("refusedToolCalls")) {
		return nil, nil // every payload this release writes: no second parse
	}
	var legacy struct {
		Proto *args `json:"execute_tools"`
		JSON  *args `json:"executeTools"`
	}
	if err := json.Unmarshal(node, &legacy); err != nil {
		return nil, err
	}
	var refused map[string]string
	for _, a := range []*args{legacy.Proto, legacy.JSON} {
		if a == nil {
			continue
		}
		for _, m := range []map[string]string{a.Proto, a.JSON} {
			for id, reason := range m {
				if refused == nil {
					refused = map[string]string{}
				}
				refused[id] = reason
			}
		}
	}
	return refused, nil
}
