// Copyright (c) 2025 Reliant Labs
package tools

import (
	"encoding/json"
	"fmt"

	"github.com/invopop/jsonschema"

	rctxpkg "github.com/reliant-labs/reliant/internal/rctx"
)

// A CHAT CAN WORK ACROSS SEVERAL PROJECTS: its primary (chats.project_id) and
// the projects attached to it. A file, search or shell call picks one with
// `project`, and `repo` keeps naming a repo inside the one it picked.
//
// `project` is the WORKER's argument, never a tool's. No tool's params struct
// has it, and every tool decodes its input strictly (additionalProperties:
// false, DisallowUnknownFields), so a daemon of any version would refuse a call
// that carried it. So it is added to the model-facing schema only
// (WithProjectParam, in call_llm, and only when the chat has more than one
// project), and execute_tools takes it out of the input (TakeProjectArg)
// before dispatch, rewriting the request to the chosen project's checkout. The
// daemon receives an ordinary single-project request.

// ProjectParam is the routing argument's name.
const ProjectParam = "project"

// projectRoutedTools are the built-in tools whose calls run in a checkout of
// the chat's project and take `repo` to name a repo inside it: the ones a
// `project` can send to another of the chat's projects.
// TestProjectRoutedToolsAreTheRepoTakingTools holds this list equal to the
// registry's built-in tools whose schema has `repo`, so a new one cannot be
// left out by accident. MCP and integration tools are never in it: their
// `repo` (a GitHub repository, say) means something else.
var projectRoutedTools = map[string]struct{}{
	ToolView:             {},
	ToolWrite:            {},
	ToolEdit:             {},
	ToolFindReplace:      {},
	ToolMoveCode:         {},
	ToolCodeContext:      {},
	ShellToolName:        {},
	ToolProveTest:        {},
	ToolSaveAttachment:   {},
	ToolPresentImage:     {},
	ToolComponentLibrary: {},
	ToolGenerateImage:    {},
	ToolGenerateVideo:    {},
}

// RoutesByProject reports whether a call to the tool named name can be sent to
// another of its chat's projects with `project`.
func RoutesByProject(name string) bool {
	_, ok := projectRoutedTools[name]
	return ok
}

// WithProjectParam returns tool with `project` in the schema the model sees,
// accepting exactly the values in accepted; primary names the project an
// omitted `project` means. A tool that does not route by project (or whose
// schema has no `repo`, as a bound or reshaped copy might not) is returned as
// it is, as is every tool when there is nothing to choose between.
func WithProjectParam(tool Tool, accepted []string, primary string) Tool {
	if tool == nil || len(accepted) < 2 || !RoutesByProject(tool.Name()) || !hasParam(tool.ParamSchema(), "repo") {
		return tool
	}
	return projectParamTool{Tool: tool, accepted: append([]string(nil), accepted...), primary: primary}
}

func hasParam(schema *jsonschema.Schema, name string) bool {
	if schema == nil || schema.Properties == nil {
		return false
	}
	_, ok := schema.Properties.Get(name)
	return ok
}

// projectParamTool is a tool offered with `project`. It exists only in the
// turn's tool array, for the schema; the executor builds the plain tool for
// every call, from input execute_tools has already taken `project` out of.
// Run and RequiresPermission take it out too, so the wrapper is safe to call
// directly.
type projectParamTool struct {
	Tool
	accepted []string
	primary  string
}

// ParamSchema is the tool's own model-facing schema plus `project`, an
// optional string limited to the chat's projects. The tool's schema is copied,
// never changed: a hand-rolled tool may hand out a shared one.
func (t projectParamTool) ParamSchema() *jsonschema.Schema {
	inner := t.Tool.ParamSchema()
	if inner == nil {
		return nil
	}
	schema := *inner
	props := jsonschema.NewProperties()
	for pair := inner.Properties.Oldest(); pair != nil; pair = pair.Next() {
		props.Set(pair.Key, pair.Value)
	}
	enum := make([]any, len(t.accepted))
	for i, v := range t.accepted {
		enum[i] = v
	}
	props.Set(ProjectParam, &jsonschema.Schema{
		Type: "string",
		Enum: enum,
		Description: fmt.Sprintf("Multi-project chats only. Which of this chat's projects the call runs in (the system prompt lists them). "+
			"Omit it for the primary project, %s. The call runs in that project's main checkout, and `repo` names a repo inside it.", t.primary),
	})
	schema.Properties = props
	return &schema
}

// Unwrap returns the tool inside, for the optional interfaces callers look
// for on the turn's tools.
func (t projectParamTool) Unwrap() any {
	if u, ok := t.Tool.(interface{ Unwrap() any }); ok {
		return u.Unwrap()
	}
	return t.Tool
}

func (t projectParamTool) RequiresPermission(tc *rctxpkg.ToolContext, call ToolCall) (bool, error) {
	_, input, _, err := TakeProjectArg(call.Input)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrInvalidParameters, err)
	}
	call.Input = input
	return t.Tool.RequiresPermission(tc, call)
}

func (t projectParamTool) Run(tc *rctxpkg.ToolContext, call ToolCall) (ToolResponse, error) {
	_, input, _, err := TakeProjectArg(call.Input)
	if err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}
	call.Input = input
	return t.Tool.Run(tc, call)
}

// TakeProjectArg takes `project` out of a call's input: its value, the input
// without it, and whether the input had it. Input without the key is returned
// byte for byte, and so is input that is not a JSON object (the tool's own
// decoder reports that better). A `project` that is not a string is an error.
func TakeProjectArg(input string) (value, rest string, present bool, err error) {
	trimmed := trimJSONWhitespace(input)
	if trimmed == "" || trimmed[0] != '{' {
		return "", input, false, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &fields); err != nil {
		return "", input, false, nil
	}
	raw, ok := fields[ProjectParam]
	if !ok {
		return "", input, false, nil
	}
	if string(raw) != "null" {
		if err := json.Unmarshal(raw, &value); err != nil {
			return "", input, true, fmt.Errorf("`%s` must be a string naming one of this chat's projects", ProjectParam)
		}
	}
	delete(fields, ProjectParam)
	out, err := json.Marshal(fields)
	if err != nil {
		return "", input, true, fmt.Errorf("removing `%s` from the call: %w", ProjectParam, err)
	}
	return value, string(out), true, nil
}
