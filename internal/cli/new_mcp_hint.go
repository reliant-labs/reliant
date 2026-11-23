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
// noise. The tool names are the server's own (mcp.go), each with what it gives.
func mcpRestartHint(targetPath string, hadMCP bool) string {
	if hadMCP || !mcpJSONRegistersForge(filepath.Join(targetPath, ".mcp.json")) {
		return ""
	}
	return "Restart your agent session to load Reliant Forge's MCP tools from .mcp.json (a running session does not pick it up): " +
		"deploy_claimable (a live URL plus a claim link, no account), " +
		"shapes (every rpc, table, handler and hook with file:line), " +
		"explain_error (what a forge failure means and the exact next command)."
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
