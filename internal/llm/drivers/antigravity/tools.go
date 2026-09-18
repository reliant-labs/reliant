// Copyright (c) 2025 Reliant Labs
package antigravity

import (
	"fmt"
	"strings"

	"github.com/invopop/jsonschema"
	toolsPkg "github.com/reliant-labs/reliant/internal/llm/tools"
)

// reliantToolPrefix is the prefix every tool is advertised under, so Reliant's
// tools present as one coherent namespace rather than as names that collide
// with the host client's own vocabulary.
const reliantToolPrefix = "reliant__"

// prefixedToolName returns the name a tool is presented under in the request.
// Already-prefixed names and the empty string pass through unchanged, which is
// what makes it safe to apply to a ForceToolChoice that may or may not have
// been through here already.
//
// convertTools, buildToolConfig and the response-side strip must ALL agree on
// this name. If they diverge, either a tool_choice pin names a function absent
// from the tools array, or a returned call names a tool Reliant cannot execute.
func prefixedToolName(name string) string {
	if name == "" || strings.HasPrefix(name, reliantToolPrefix) {
		return name
	}
	return reliantToolPrefix + name
}

// unprefixedToolName is the inverse, applied to every function call coming
// back off the wire before it becomes a message.ToolCall.
func unprefixedToolName(name string) string {
	return strings.TrimPrefix(name, reliantToolPrefix)
}

// convertTools renders Reliant's tools as Gemini function declarations under
// the reliant__ prefix.
//
// The capture shows 17 NATIVE Antigravity tool names (view_file, run_command,
// manage_task, …). We deliberately do not advertise them: Reliant supplies its
// own tool implementations, and declaring a native name we cannot execute would
// invite the model to call into nothing.
func convertTools(list []toolsPkg.Tool) []*toolDecls {
	if len(list) == 0 {
		return nil
	}
	decls := make([]*functionDeclaration, 0, len(list))
	for _, tool := range list {
		decls = append(decls, &functionDeclaration{
			Name:        prefixedToolName(tool.Name()),
			Description: tool.Description(),
			Parameters:  convertToSchema(tool.ParamSchema()),
		})
	}
	return []*toolDecls{{FunctionDeclarations: decls}}
}

// buildToolConfig returns a ToolConfig pinning function calling to a single
// tool, or nil when there is nothing to pin.
//
// The pin uses the SAME prefixed name convertTools emitted. A pin naming a
// function absent from the tools array is a provider error, so it is only
// honored when the named tool is actually present.
func buildToolConfig(forceToolChoice string, list []toolsPkg.Tool) *toolConfig {
	if forceToolChoice == "" || len(list) == 0 {
		return nil
	}
	found := false
	for _, tool := range list {
		if tool.Name() == forceToolChoice {
			found = true
			break
		}
	}
	if !found {
		return nil
	}
	return &toolConfig{
		FunctionCallingConfig: &functionCallingConfig{
			Mode:                 "ANY",
			AllowedFunctionNames: []string{prefixedToolName(forceToolChoice)},
		},
	}
}

// convertToSchema projects a jsonschema.Schema onto the Gemini schema subset.
// Ported from the gemini driver, whose inner payload shape is identical.
func convertToSchema(param *jsonschema.Schema) *schema {
	if param == nil {
		return nil
	}
	out := &schema{
		Type:        param.Type,
		Description: param.Description,
	}

	if len(param.Enum) > 0 {
		out.Enum = make([]string, len(param.Enum))
		for i, v := range param.Enum {
			out.Enum[i] = fmt.Sprint(v)
		}
	}

	switch param.Type {
	case "array":
		if param.Items != nil {
			out.Items = convertToSchema(param.Items)
		}
	case "object":
		if param.Properties != nil {
			out.Properties = make(map[string]*schema)
			for pair := param.Properties.Oldest(); pair != nil; pair = pair.Next() {
				out.Properties[pair.Key] = convertToSchema(pair.Value)
			}
		}
		if len(param.Required) > 0 {
			out.Required = param.Required
		}
	}

	return out
}
