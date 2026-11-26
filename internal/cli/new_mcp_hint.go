package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// mcpRestartHint is the one line `project new` prints when it wrote a
// .mcp.json that registers forge's own MCP server. A running agent session
// does not load a .mcp.json that appeared after it started, so without the
// line an agent drives forge through the CLI for the whole session and never
// sees the tools the file exists to give it.
//
// hadMCP is whether the file was there before the scaffold ran: a file the
// user already had is theirs, forge did not write it, and the hint would be
// noise. The tool names are the server's own (mcp.go), each with its CLI
// twin and what it gives.
//
// The restart is optional, and the line says so: an agent mid-task read the
// bare imperative as an interruption it had to make, when every tool it names
// is also a command it can run in the session it already has.
func mcpRestartHint(targetPath string, hadMCP bool) string {
	if hadMCP || !mcpJSONRegistersForge(filepath.Join(targetPath, ".mcp.json")) {
		return ""
	}
	name := Name()
	return "Optional: restart your agent session to load Reliant Forge's MCP tools from .mcp.json (a running session does not pick it up). " +
		"Nothing waits on it, every tool is also a CLI command: " +
		"deploy_claimable (`" + name + " env deploy prod --claimable`, a live URL plus a claim link, no account), " +
		"shapes (`" + name + " project shapes`, every rpc, table, handler and hook with file:line), " +
		"explain_error (`" + name + " explain <reason>`, what a forge failure means and the exact next command)."
}

// mcpJSONRegistersForge reports whether the .mcp.json at path declares an
// MCP server named "forge". Unreadable or malformed: no.
func mcpJSONRegistersForge(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var doc struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if json.Unmarshal(data, &doc) != nil {
		return false
	}
	_, ok := doc.MCPServers["forge"]
	return ok
}
