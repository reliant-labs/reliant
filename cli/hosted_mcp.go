package cli

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"

	internalcli "github.com/reliant-labs/forge/internal/cli"
)

// The hosted MCP seam: forge's account-scoped tools (monitoring, releases,
// envs) and its catalog tools (skills, benefits, explain_error), for a remote
// MCP server that serves them to a signed-in person with no install. Reliant
// mounts them as the "Reliant Forge" connector. The local `forge mcp` keeps
// the repo tools; a tool both serve has one name and one input schema (the
// local one adds only its repo-only `dir`).
//
// The tools run the CLI's own verbs in-process and return their --json
// documents, each with console_url, the forge.reliantlabs.io page that shows
// it. They keep no process state, so one process serves many people at once.

// AccountTarget is what one account tool call acts on (org, project, env),
// resolved before anything is read or written and handed to Authorize.
type AccountTarget = internalcli.AccountTarget

// AccountToolInfo describes one hosted tool: name, title, description,
// whether it mutates, and whether it reaches the account at all.
type AccountToolInfo = internalcli.AccountToolInfo

// AccountToolsOptions configures AddHostedTools: the control plane, the
// caller's bearer per call, the host's Authorize gate, and which tools.
type AccountToolsOptions = internalcli.AccountToolsOptions

// HostedTools lists every tool AddHostedTools can register, in order.
func HostedTools() []AccountToolInfo { return internalcli.HostedTools() }

// AddHostedTools registers the hosted tools on srv. Safe for concurrent use
// by many sessions.
func AddHostedTools(srv *mcp.Server, opts AccountToolsOptions) error {
	return internalcli.AddHostedTools(srv, opts)
}

// HostedInstructions is the `instructions` text for the hosted server.
func HostedInstructions() string { return internalcli.HostedInstructions() }
