// Copyright (c) 2025 Reliant Labs
package tools

import (
	"strings"

	"github.com/reliant-labs/reliant/internal/nomachine"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// RequestMachineParams is request_machine's input.
type RequestMachineParams struct {
	Reason string `json:"reason" jsonschema:"required,description=One sentence the user will read: what in this task needs their computer and why (for example 'Running the test suite needs a checkout of the repository.')"`
}

const requestMachineDescription = `Offer the user the choice to connect a machine, when the task genuinely needs their computer.

This chat has no machine: there is no checkout, filesystem, shell or local MCP server. Call this when part of the task cannot be done without one — editing or running code in their project, reading local files, running a command. The user sees your reason with a "Connect a machine" button. Connecting moves this chat onto that machine, and your next turn has the full tool set.

It returns at once. After calling it, stop: end your turn with a short note of what you will do once a machine is connected, and wait for the user. Do not call it again in the same turn, and do not call it for work the tools you already have can do (web access, integrations, planning).`

type requestMachineTool struct{}

// NewRequestMachineTool returns request_machine. It is offered only in a run
// with no machine (see OnlyWithoutMachine): the run is marked on its context,
// and on a machine the call is refused rather than shown to the user.
func NewRequestMachineTool() Tool {
	return NewToolWrapper[RequestMachineParams, ToolResponse](&requestMachineTool{})
}

func (t *requestMachineTool) Name() string { return ToolRequestMachine }

func (t *requestMachineTool) Description() string { return requestMachineDescription }

func (t *requestMachineTool) RequiresPermission(RequestMachineParams) (bool, error) {
	return false, nil
}

func (t *requestMachineTool) IsReadOnly() bool { return true }

// Execute records nothing. The offer the user sees is this tool call itself,
// rendered from its reason; connecting goes through SetChatDaemon.
func (t *requestMachineTool) Execute(rc *rctx.ToolContext, params RequestMachineParams) (ToolResponse, error) {
	if !nomachine.Is(rc.Context) {
		return NewTextErrorResponse("This chat already has a machine, so there is nothing to request. " +
			"Continue with the tools you have."), nil
	}
	if strings.TrimSpace(params.Reason) == "" {
		return NewTextErrorResponse("reason is required: say in one sentence what needs the user's computer."), nil
	}
	return NewTextResponse("The user has been shown your reason with a \"Connect a machine\" button. " +
		"Stop here and wait for them: end your turn with a short note of what you will do once a machine " +
		"is connected. Do not call request_machine again in this turn."), nil
}
