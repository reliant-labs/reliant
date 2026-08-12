// Copyright (c) 2025 Reliant Labs
package tools

import (
	"encoding/json"

	"github.com/invopop/jsonschema"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// SetTitleToolName is the tool the title-generation request is pinned to.
const SetTitleToolName = "set_title"

// SetTitleTitleField is the sole property of the set_title schema. The caller
// reads the generated title out of the tool call's input under this key.
const SetTitleTitleField = "title"

// SetTitleInput is the decoded argument object of a set_title call.
type SetTitleInput struct {
	Title string `json:"title"`
}

// SetTitleTool is the response tool used for chat title generation.
//
// Titles are produced by a one-shot LLM request whose tool_choice is pinned to
// this tool, which makes a tool_use block the only thing the model can emit.
// That structural constraint — not prompt wording — is what stops it from
// replying in agent voice ("I'll investigate..."): title generation shares the
// Claude Code system prompt, which instructs the model to narrate before acting
// (see ccprompts/output_verbose.txt), and that instruction outweighs a short
// contrary caller prompt.
//
// This is deliberately not a ResponseTool: that type appends a warning about
// ending the agent loop, which is meaningless for a single-request call.
type SetTitleTool struct{}

// NewSetTitleTool builds the title-generation response tool.
func NewSetTitleTool() Tool {
	return &SetTitleTool{}
}

func (t *SetTitleTool) Name() string {
	return SetTitleToolName
}

func (t *SetTitleTool) Description() string {
	return "Record the title for this conversation. Call this exactly once with a short title describing what the user wants."
}

func (t *SetTitleTool) ParamSchema() *jsonschema.Schema {
	props := jsonschema.NewProperties()
	props.Set(SetTitleTitleField, &jsonschema.Schema{
		Type:        "string",
		Description: "A title of at most 4 words in title case, describing the user's topic or intent. No quotes, no trailing punctuation, no assistant or product names.",
	})
	return &jsonschema.Schema{
		Type:       "object",
		Properties: props,
		Required:   []string{SetTitleTitleField},
	}
}

func (t *SetTitleTool) RequiresPermission(rctx *rctx.ToolContext, params ToolCall) (bool, error) {
	return false, nil
}

func (t *SetTitleTool) IsReadOnly() bool {
	return true
}

func (t *SetTitleTool) IsResponseTool() bool {
	return true
}

// Run is unreachable in the title-generation flow: that path reads the pinned
// tool call's arguments directly rather than executing the tool. It is
// implemented only to satisfy the Tool interface, and echoes its input back so
// it stays harmless if the tool is ever handed to a real agent loop.
func (t *SetTitleTool) Run(rctx *rctx.ToolContext, params ToolCall) (ToolResponse, error) {
	var input SetTitleInput
	if err := json.Unmarshal([]byte(params.Input), &input); err != nil {
		return NewTextErrorResponse("Invalid JSON input: " + err.Error()), nil
	}
	return NewTextResponse(input.Title), nil
}
