// Copyright (c) 2025 Reliant Labs
package tools

import (
	"reflect"
	"sort"

	"github.com/invopop/jsonschema"
)

// Node exposure is OPT-IN, and the opt-in is this table.
//
// Most tools are conversational affordances. `shell_output` answers "what has
// that background process printed since I last looked", `spawn_status` answers
// "is my sub-agent done yet", `load_tool` answers "I need a capability I do not
// currently have". Each of those is a question an agent asks itself mid-turn;
// none is a step a human would draw on a graph. A node palette listing all
// fifty registered tools would bury the handful that mean something as a node
// under forty-odd that do not, which is worse than offering no tool nodes at
// all.
//
// So exposure is declared, never derived. A tool appears as an `invoke_tool`
// target only by being named here, and adding an entry is the whole opt-in.

// NodeExposure declares that a tool may be invoked as a workflow node, and
// names the Go type its structured output takes.
//
// OutputType is what makes field-level references checkable. A tool's real
// output contract is its Go output struct; reflecting that struct to JSON
// schema — with the same reflector ParamSchema() uses — yields the field names
// and types a graph edge can reference, without declaring a proto message per
// tool.
type NodeExposure struct {
	// Tool is the registered tool name, e.g. "generate_image".
	Tool string
	// OutputType is the tool's output struct — the `O` of its ToolWrapper.
	// A zero value of the type is fine; only its shape is read.
	OutputType reflect.Type
}

// nodeExposedTools is the opt-in table. Keep it short and keep the bar high:
// a tool belongs here when a human would reasonably draw it as a step, not
// merely when it happens to return structured output.
var nodeExposedTools = []NodeExposure{
	// generate_image is the first, and for now the only, opt-in. "Generate a
	// hero image, then write it into the page" is a graph, and its output
	// (attachment_id, saved_to) is exactly what the next node needs.
	{Tool: ToolGenerateImage, OutputType: reflect.TypeOf(GenerateImageOutput{})},
}

// NodeExposedTools returns every tool that opted in to node exposure, ordered
// by name so a palette and its tests are deterministic.
func NodeExposedTools() []NodeExposure {
	exposures := make([]NodeExposure, len(nodeExposedTools))
	copy(exposures, nodeExposedTools)
	sort.Slice(exposures, func(i, j int) bool { return exposures[i].Tool < exposures[j].Tool })
	return exposures
}

// IsNodeExposedTool reports whether a tool may be invoked as a workflow node.
// This is the check an invoke_tool node is validated against: naming a tool
// that did not opt in is an error, not a silent no-op, because the alternative
// is a workflow that looks correct and does nothing.
func IsNodeExposedTool(name string) bool {
	for _, exposure := range nodeExposedTools {
		if exposure.Tool == name {
			return true
		}
	}
	return false
}

// NodeOutputSchema returns the JSON schema of a node-exposed tool's structured
// output, or nil when the tool did not opt in.
//
// The reflector settings match fullParamSchema's deliberately: a validator that
// disagreed with the tool's own schema about references or additional
// properties would report field errors the tool would not.
func NodeOutputSchema(name string) *jsonschema.Schema {
	for _, exposure := range nodeExposedTools {
		if exposure.Tool != name || exposure.OutputType == nil {
			continue
		}
		reflector := jsonschema.Reflector{
			AllowAdditionalProperties: false,
			DoNotReference:            true,
		}
		return ResolveSchemaRefs(reflector.ReflectFromType(exposure.OutputType))
	}
	return nil
}
