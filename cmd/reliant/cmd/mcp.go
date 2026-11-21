// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
)

// `reliant mcp` shows a project's MCP servers as the machine holding its
// checkout runs them, and restarts one, through the same MCPService the app's
// Settings -> MCP servers page uses. The config itself stays in mcp.json
// (user, project and project-local scopes), which an edit changes directly:
// the machine applies an edit when an agent's next turn loads the project's
// servers, so there is nothing to sync by hand.
//
// An agent's shell may list servers and their tools, and is refused restart
// (refuseMCPFromAgentShell): a restart stops the process every other chat in
// the project is using.

var errAgentCannotRestartMCP = errors.New("an agent cannot restart MCP servers from its shell: a restart stops the server for every chat in the project. An edit to mcp.json is applied at your next turn without one; otherwise ask the user to restart it in Settings -> MCP servers or with `reliant mcp restart`")

func refuseMCPFromAgentShell(*cobra.Command, []string) error {
	if inAgentShell() {
		return errAgentCannotRestartMCP
	}
	return nil
}

// mcpClient is the slice of MCPService and ProjectService `reliant mcp` uses.
type mcpClient interface {
	ListServers(context.Context, *connect.Request[reliantv1.ListServersRequest]) (*connect.Response[reliantv1.ListServersResponse], error)
	GetServerTools(context.Context, *connect.Request[reliantv1.GetServerToolsRequest]) (*connect.Response[reliantv1.GetServerToolsResponse], error)
	RestartServer(context.Context, *connect.Request[reliantv1.RestartServerRequest]) (*connect.Response[reliantv1.RestartServerResponse], error)
	ListProjects(context.Context, *connect.Request[reliantv1.ListProjectsRequest]) (*connect.Response[reliantv1.ListProjectsResponse], error)
}

type mcpRPC struct {
	reliantv1connect.MCPServiceClient
	projects reliantv1connect.ProjectServiceClient
}

func (c mcpRPC) ListProjects(ctx context.Context, req *connect.Request[reliantv1.ListProjectsRequest]) (*connect.Response[reliantv1.ListProjectsResponse], error) {
	return c.projects.ListProjects(ctx, req)
}

func newMCPCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Show a project's MCP servers and their tools, and restart one",
		Long: `Shows the MCP servers a project's agents can use, as the machine holding the
project runs them: each server's status, where it is configured, and the tools
it offers. 'restart' restarts one.

Servers are configured in mcp.json: ~/.reliant/mcp.json (every project),
<project>/.reliant/mcp.json (the project, checked in) and
<project>/.reliant.local/mcp.json (yours, not checked in). An edit is applied
when an agent's next turn loads the project's servers, so a restart is needed
only to recover a server that stopped working. Settings -> MCP servers in the app
manages the same servers.

--project names the project by id, name or path; the default is the current
directory.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.PersistentFlags().String("project", ".", "The project, by id, name or path")
	cmd.AddCommand(newMCPListCmd(), newMCPToolsCmd(), newMCPRestartCmd())
	return cmd
}

// mcpSession is a resolved connection, client and project for one command.
type mcpSession struct {
	conn      *connection
	client    mcpClient
	projectID string
}

func openMCPSession(cmd *cobra.Command) (*mcpSession, error) {
	conn, err := resolveConnection(cmd)
	if err != nil {
		return nil, err
	}
	client := mcpRPC{
		MCPServiceClient: reliantv1connect.NewMCPServiceClient(conn.httpClient(), conn.ServerURL),
		projects:         newProjectServiceClient(conn),
	}
	ref, _ := cmd.Flags().GetString("project")
	ref = strings.TrimSpace(ref)
	if ref == "" {
		ref = "."
	}
	project, err := findProjectRef(cmd.Context(), client, ref)
	if err != nil {
		return nil, conn.annotate(err)
	}
	return &mcpSession{conn: conn, client: client, projectID: project.GetId()}, nil
}

// mcpServerRow is one server as `mcp list` shows it.
type mcpServerRow struct {
	Name      string `json:"name"`
	Status    string `json:"status"`
	Enabled   bool   `json:"enabled"`
	Scope     string `json:"scope"`
	Transport string `json:"transport"`
	Target    string `json:"target"`
	ToolCount int32  `json:"tool_count"`
	LastError string `json:"last_error,omitempty"`
}

func newMCPListCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the project's MCP servers and their status",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := openMCPSession(cmd)
			if err != nil {
				return err
			}
			resp, err := s.client.ListServers(cmd.Context(), connect.NewRequest(&reliantv1.ListServersRequest{ProjectId: s.projectID}))
			if err != nil {
				return s.conn.annotate(fmt.Errorf("listing MCP servers: %w", err))
			}
			rows := mcpServerRows(resp.Msg.GetServers())
			if jsonOut {
				return writeJSON(cmd.OutOrStdout(), map[string]any{"project_id": s.projectID, "servers": rows})
			}
			writeMCPServers(cmd.OutOrStdout(), rows)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Print JSON")
	return cmd
}

func newMCPToolsCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "tools <server>",
		Short: "List the tools an MCP server offers the project's agents",
		Long: `Lists the tools one MCP server offers the project's agents. An agent sees each
as mcp__<server>__<tool>.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openMCPSession(cmd)
			if err != nil {
				return err
			}
			name := args[0]
			resp, err := s.client.GetServerTools(cmd.Context(), connect.NewRequest(&reliantv1.GetServerToolsRequest{ProjectId: s.projectID, Name: name}))
			if err != nil {
				if connect.CodeOf(err) == connect.CodeNotFound {
					return fmt.Errorf("no MCP server %q is configured for this project: 'reliant mcp list' shows the ones that are", name)
				}
				return s.conn.annotate(fmt.Errorf("listing the tools of %s: %w", name, err))
			}
			tools := resp.Msg.GetTools()
			sort.Slice(tools, func(i, j int) bool { return tools[i].GetName() < tools[j].GetName() })
			if jsonOut {
				type toolRow struct {
					Name        string `json:"name"`
					AgentName   string `json:"agent_name"`
					Description string `json:"description"`
				}
				out := make([]toolRow, 0, len(tools))
				for _, t := range tools {
					out = append(out, toolRow{Name: t.GetName(), AgentName: mcpAgentToolName(name, t.GetName()), Description: t.GetDescription()})
				}
				return writeJSON(cmd.OutOrStdout(), map[string]any{"server": name, "tools": out})
			}
			w := cmd.OutOrStdout()
			if len(tools) == 0 {
				fmt.Fprintf(w, "%s offers no tools in this project. 'reliant mcp list' shows whether it is running, and its last error.\n", name)
				return nil
			}
			tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "TOOL\tAGENT SEES\tDESCRIPTION")
			for _, t := range tools {
				fmt.Fprintf(tw, "%s\t%s\t%s\n", t.GetName(), mcpAgentToolName(name, t.GetName()), firstLine(strings.TrimSpace(t.GetDescription())))
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Print JSON")
	return cmd
}

func newMCPRestartCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "restart <server>",
		Short: "Restart an MCP server from its current config",
		Long: `Restarts one MCP server on the machine holding the project, from the config
that machine's mcp.json files declare now. Every chat in the project that uses
the server gets the new process.`,
		Args:    cobra.ExactArgs(1),
		PreRunE: refuseMCPFromAgentShell,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openMCPSession(cmd)
			if err != nil {
				return err
			}
			name := args[0]
			resp, err := s.client.RestartServer(cmd.Context(), connect.NewRequest(&reliantv1.RestartServerRequest{ProjectId: s.projectID, Name: name}))
			if err != nil {
				if connect.CodeOf(err) == connect.CodeNotFound {
					return fmt.Errorf("no MCP server %q is configured for this project: 'reliant mcp list' shows the ones that are", name)
				}
				return s.conn.annotate(fmt.Errorf("restarting %s: %w", name, err))
			}
			if !resp.Msg.GetSuccess() {
				return fmt.Errorf("%s did not restart: %s", name, resp.Msg.GetMessage())
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", name, resp.Msg.GetMessage())
			return nil
		},
	}
	return cmd
}

func mcpServerRows(servers []*reliantv1.MCPServer) []mcpServerRow {
	rows := make([]mcpServerRow, 0, len(servers))
	for _, s := range servers {
		row := mcpServerRow{
			Name:      s.GetName(),
			Status:    mcpStatusName(s),
			Enabled:   s.GetEnabled(),
			Scope:     mcpScopeName(s.GetScope()),
			ToolCount: s.GetToolCount(),
			LastError: s.GetLastError(),
		}
		cfg := s.GetConfig()
		row.Transport = cfg.GetType()
		if cfg.GetUrl() != "" {
			if row.Transport == "" {
				row.Transport = "http"
			}
			row.Target = cfg.GetUrl()
		} else {
			if row.Transport == "" {
				row.Transport = "stdio"
			}
			row.Target = strings.TrimSpace(cfg.GetCommand() + " " + strings.Join(cfg.GetArgs(), " "))
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows
}

func mcpStatusName(s *reliantv1.MCPServer) string {
	if !s.GetEnabled() {
		return "disabled"
	}
	switch s.GetStatus() {
	case reliantv1.MCPServerStatus_MCP_SERVER_STATUS_HEALTHY:
		return "healthy"
	case reliantv1.MCPServerStatus_MCP_SERVER_STATUS_UNHEALTHY:
		return "unhealthy"
	case reliantv1.MCPServerStatus_MCP_SERVER_STATUS_DISCONNECTED:
		return "not running"
	case reliantv1.MCPServerStatus_MCP_SERVER_STATUS_DISABLED:
		return "disabled"
	default:
		return "unknown"
	}
}

func mcpScopeName(scope reliantv1.ConfigScope) string {
	switch scope {
	case reliantv1.ConfigScope_CONFIG_SCOPE_GLOBAL:
		return "user"
	case reliantv1.ConfigScope_CONFIG_SCOPE_PROJECT:
		return "project"
	case reliantv1.ConfigScope_CONFIG_SCOPE_PROJECT_LOCAL:
		return "local"
	default:
		return "built-in"
	}
}

func writeMCPServers(w io.Writer, rows []mcpServerRow) {
	if len(rows) == 0 {
		fmt.Fprintln(w, "No MCP servers are configured for this project. Add one to .reliant/mcp.json, or in Settings -> MCP servers.")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SERVER\tSTATUS\tTOOLS\tSCOPE\tRUNS")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\n", r.Name, r.Status, r.ToolCount, r.Scope, r.Target)
	}
	_ = tw.Flush()
	for _, r := range rows {
		if r.LastError != "" {
			fmt.Fprintf(w, "\n%s: %s\n", r.Name, r.LastError)
		}
	}
}

// mcpAgentToolName is the name an agent calls a server's tool by.
func mcpAgentToolName(server, tool string) string {
	return "mcp__" + server + "__" + tool
}
