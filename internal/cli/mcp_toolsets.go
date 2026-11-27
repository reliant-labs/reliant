package cli

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A client resends every tool's name, description and input schema on every
// model call, so a tool an agent never calls still costs it tokens on each
// turn. `forge mcp --tools` picks which groups the server lists. core is what
// an agent changing a forge project calls mid-task; the rest matter once an
// env is deployed (ops, deploy), when forge itself fails (support), or when
// deciding what forge offers (catalog). When some group is left out the
// server also lists forge_tools, which enables the others in-session.
//
// The default is core (--tools core; owner, 2026-11-27): about a fifth of the
// full list's bytes on every call. It changes no tool: every one stays
// reachable mid-session through forge_tools, and --tools all, the default
// before, lists them all up front and stays accepted permanently.

// mcpToolGroup is one selectable set of tools.
type mcpToolGroup struct {
	name    string
	summary string
	// tools are the names add registers, in the order forge_tools and
	// --help list them (TestMCPToolGroupsRegisterExactlyTheirTools).
	tools []string
	add   func(*mcp.Server, *mcpRunner)
}

// mcpToolGroups is every group, in registration order.
var mcpToolGroups = []mcpToolGroup{
	{name: "core", summary: "recon, lint, codegen and skills",
		tools: []string{"audit", "shapes", "skill_search", "skill_load", "lint", "generate"}, add: addCoreTools},
	{name: "catalog", summary: "what forge is and offers",
		tools: []string{"benefits", "why", "capabilities", "schema", "annotations", "libraries", "skill_list"}, add: addCatalogTools},
	{name: "support", summary: "when forge itself fails",
		tools: []string{"explain_error", "report_bug"}, add: addSupportTools},
	{name: "ops", summary: "a deployed env's telemetry and data",
		tools: []string{"blast_radius", "db_query", "obs_status", "obs_issues", "obs_issue", "obs_logs", "obs_check"}, add: addOpsTools},
	{name: "deploy", summary: "a no-account deploy",
		tools: []string{"deploy_claimable", "deploy_status"}, add: addDeployTools},
}

// mcpToolsAll is the --tools value that selects every group.
const mcpToolsAll = "all"

// mcpToolsDefault is the --tools value `forge mcp` starts with when none is
// given: the core group, with forge_tools for the rest.
const mcpToolsDefault = "core"

func mcpToolGroupNames() []string {
	names := make([]string, len(mcpToolGroups))
	for i, g := range mcpToolGroups {
		names[i] = g.name
	}
	return names
}

// parseMCPTools resolves a --tools value: all on its own, or a
// comma-separated list of group names (case-insensitive, duplicates allowed).
// Every token is checked before anything is returned, so an unknown or empty
// token fails wherever it sits, and all beside any other token is refused
// rather than silently widening the selection. It returns the groups in
// registration order.
func parseMCPTools(spec string) ([]string, error) {
	valid := mcpToolGroupNames()
	want := map[string]bool{}
	sawAll := false
	tokens := strings.Split(spec, ",")
	for _, part := range tokens {
		name := strings.ToLower(strings.TrimSpace(part))
		switch {
		case name == mcpToolsAll:
			sawAll = true
		case name == "":
			return nil, fmt.Errorf("--tools %q: empty group name; want all, or a comma-separated list of %s",
				spec, strings.Join(valid, ", "))
		case slices.Contains(valid, name):
			want[name] = true
		default:
			return nil, fmt.Errorf("--tools %q: unknown group %q; want all, or a comma-separated list of %s",
				spec, name, strings.Join(valid, ", "))
		}
	}
	if sawAll {
		if len(tokens) > 1 {
			return nil, fmt.Errorf("--tools %q: all selects every group and cannot be combined with other groups; pass all on its own, or a list of %s",
				spec, strings.Join(valid, ", "))
		}
		return valid, nil
	}
	var groups []string
	for _, name := range valid {
		if want[name] {
			groups = append(groups, name)
		}
	}
	return groups, nil
}

// mcpToolset is the groups one server has registered.
type mcpToolset struct {
	srv *mcp.Server
	r   *mcpRunner

	mu      sync.Mutex
	enabled map[string]bool
}

// enable registers each named group not yet registered and returns the
// tools it added. The SDK sends notifications/tools/list_changed to every
// connected session when a tool is added.
func (ts *mcpToolset) enable(names ...string) []string {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	var added []string
	for _, g := range mcpToolGroups {
		if !slices.Contains(names, g.name) || ts.enabled[g.name] {
			continue
		}
		g.add(ts.srv, ts.r)
		ts.enabled[g.name] = true
		added = append(added, g.tools...)
	}
	return added
}

func (ts *mcpToolset) status() string {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	var b strings.Builder
	for _, g := range mcpToolGroups {
		state := "off"
		if ts.enabled[g.name] {
			state = "on "
		}
		fmt.Fprintf(&b, "%-8s %s  %s: %s\n", g.name, state, g.summary, strings.Join(g.tools, ", "))
	}
	return b.String()
}

type forgeToolsInput struct {
	Enable []string `json:"enable,omitempty" jsonschema:"groups to add, or all; omit to list them"`
}

// mcpDescForgeTools is forge_tools' description, with each group's tools
// named so an agent knows what enabling one buys.
func mcpDescForgeTools() string {
	var groups []string
	for _, g := range mcpToolGroups[1:] {
		groups = append(groups, fmt.Sprintf("%s (%s)", g.name, strings.Join(g.tools, ", ")))
	}
	return "This server lists only some of forge's tools. List or enable the other groups: " +
		strings.Join(groups, "; ") + ". Or restart it with forge mcp --tools all."
}

// addForgeToolsTool registers forge_tools, the way to the groups the server
// was started without.
func addForgeToolsTool(ts *mcpToolset) {
	mcp.AddTool(ts.srv, &mcp.Tool{
		Name:        "forge_tools",
		Description: mcpDescForgeTools(),
		Annotations: &mcp.ToolAnnotations{Title: "More forge tools", ReadOnlyHint: true, OpenWorldHint: ptr(false)},
	}, func(_ context.Context, _ *mcp.CallToolRequest, in forgeToolsInput) (*mcp.CallToolResult, any, error) {
		var names []string
		for _, e := range in.Enable {
			groups, err := parseMCPTools(e)
			if err != nil {
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: strings.Replace(err.Error(), "--tools", "enable", 1)}}}, nil, nil
			}
			names = append(names, groups...)
		}
		text := ts.status()
		if len(names) > 0 {
			if added := ts.enable(names...); len(added) > 0 {
				text = fmt.Sprintf("Added %s. Your client was sent notifications/tools/list_changed; if they do not appear, restart the server with forge mcp --tools %s.\n\n%s",
					strings.Join(added, ", "), mcpToolsAll, ts.status())
			} else {
				text = "Already enabled.\n\n" + text
			}
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil, nil
	})
}

// mcpInstructionsFor is the server's instructions for a selection of groups.
// Every group gets mcpInstructions, the front door for any repo. A selection
// (core, the default, among them) is a lean server, so it gets a few lines:
// the disambiguation, the entry skills, and where the other tools are.
func mcpInstructionsFor(groups []string) string {
	if len(groups) == len(mcpToolGroups) {
		return mcpInstructions
	}
	s := "This is Reliant Forge (github.com/reliant-labs/forge) — not Atlassian Forge and not Foundry's forge. "
	if spec := strings.Join(groups, ","); spec == mcpToolsDefault {
		s += "This server lists forge's core tools (--tools core, the default). "
	} else {
		s += "This server was started with --tools " + spec + ". "
	}
	if slices.Contains(groups, "core") {
		s += "In a forge project start with skill_load name=forge; in any other repo, name=adopt. " +
			"Recon with shapes before grepping; gate with lint (scoped while iterating) and generate. "
	}
	return s + "forge_tools lists and enables the other tool groups, or restart with forge mcp --tools all. " +
		"Every tool takes an optional dir: the repo to act on (default: the directory the server was started in)."
}
