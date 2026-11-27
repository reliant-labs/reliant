package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The local server's read tools over an env's telemetry and app database
// (NS FG-23): an agent in Claude Code, Cursor or Codex reads prod through the
// same verbs a person runs, with no shell and no hosted connector. Each runs
// the CLI command in-process with --json, so the guards are the command's own:
// `db query` takes the env's data_access policy (an audited env needs
// confirm_env and a reason), reads in a READ ONLY transaction as the env's
// read-only role, and audits the call; `obs` reads with this machine's
// credential, which the control plane authorizes. Nothing here is a second
// implementation of either.
//
// The obs tools are the hosted connector's, by name and by schema less the
// project (mcp_hosted.go's obs*Args): the repo the server runs in names it.

// envInput is the repo and env every local account read takes. The env's
// description is hostedTarget's word for word: the obs tools' schemas are one
// schema on both servers (TestHostedObsToolSchemasMatchLocal).
type envInput struct {
	dirInput
	Env string `json:"env" jsonschema:"the environment, e.g. prod or staging"`
}

type localObsStatusInput struct {
	envInput
	obsStatusArgs
}

type localObsIssuesInput struct {
	envInput
	obsIssuesArgs
}

type localObsIssueInput struct {
	envInput
	obsIssueArgs
}

type localObsLogsInput struct {
	envInput
	obsLogsArgs
}

type localObsCheckInput struct {
	envInput
	obsCheckArgs
}

type dbQueryInput struct {
	envInput
	SQL        string `json:"sql" jsonschema:"one read-only SQL statement"`
	Workload   string `json:"workload,omitempty" jsonschema:"workload whose database to read (default api)"`
	Database   string `json:"database,omitempty" jsonschema:"a declared database, when there are several"`
	MaxRows    int    `json:"max_rows,omitempty" jsonschema:"row cap (default: the command's)"`
	ConfirmEnv string `json:"confirm_env,omitempty" jsonschema:"the env name again; required on an audited env"`
	Reason     string `json:"reason,omitempty" jsonschema:"why you need this read, e.g. an issue id; required on an audited env"`
}

// dbQueryArgs is the command line a db_query call runs.
func dbQueryArgs(in dbQueryInput) []string {
	args := []string{"db", "query", in.Env, in.SQL, "--json"}
	args = flagArg(args, "--workload", in.Workload)
	args = flagArg(args, "--database", in.Database)
	if in.MaxRows > 0 {
		args = append(args, "--max-rows", fmt.Sprint(in.MaxRows))
	}
	args = flagArg(args, "--confirm-env", in.ConfirmEnv)
	return flagArg(args, "--reason", in.Reason)
}

// addAccountReadTools registers db_query and the obs read tools.
func addAccountReadTools(srv *mcp.Server, r *mcpRunner) {
	// Read-only, open-world: each may reach the env's control plane.
	read := func(title string) *mcp.ToolAnnotations {
		return &mcp.ToolAnnotations{Title: title, ReadOnlyHint: true, OpenWorldHint: ptr(true)}
	}
	desc := func(name string) string {
		for _, t := range hostedToolInfos {
			if t.Name == name {
				return t.Description
			}
		}
		return ""
	}
	addJSONTool(srv, r, &mcp.Tool{
		Name: "db_query", Annotations: read("Query an env's database"),
		Description: "Run one read-only SQL statement against an env's app database, rows as JSON (forge db query <env> --json): read-only role, capped and audited. A real env needs confirm_env and reason.",
	}, func(in dbQueryInput) []string {
		if strings.TrimSpace(in.SQL) == "" {
			return nil
		}
		return dbQueryArgs(in)
	})
	addJSONTool(srv, r, &mcp.Tool{Name: "obs_status", Description: desc("obs_status"), Annotations: read("Monitoring status")},
		func(in localObsStatusInput) []string {
			return append([]string{"obs", "status", in.Env, "--json"}, in.obsStatusArgs.args()...)
		})
	addJSONTool(srv, r, &mcp.Tool{Name: "obs_issues", Description: desc("obs_issues"), Annotations: read("Error issues")},
		func(in localObsIssuesInput) []string {
			return append([]string{"obs", "issues", in.Env, "--json"}, in.obsIssuesArgs.args()...)
		})
	addJSONTool(srv, r, &mcp.Tool{Name: "obs_issue", Description: desc("obs_issue"), Annotations: read("One error issue")},
		func(in localObsIssueInput) []string {
			if strings.TrimSpace(in.ID) == "" {
				return nil
			}
			return append([]string{"obs", "issue", in.ID, in.Env, "--json"}, in.obsIssueArgs.args()...)
		})
	addJSONTool(srv, r, &mcp.Tool{Name: "obs_logs", Description: desc("obs_logs"), Annotations: read("Search logs")},
		func(in localObsLogsInput) []string {
			return append([]string{"obs", "logs", in.Env, in.Query, "--json"}, in.obsLogsArgs.args()...)
		})
	addJSONTool(srv, r, &mcp.Tool{Name: "obs_check", Description: desc("obs_check"), Annotations: read("Release health verdict")},
		func(in localObsCheckInput) []string {
			return append([]string{"obs", "check", in.Env, "--json"}, in.obsCheckArgs.args()...)
		})
}

// addJSONTool registers a tool whose result is a command's --json document.
// The document is stdout alone: a refusal's document is there too, with its
// exit_code, so a non-zero exit with a document is the answer, not a failure
// of the call; anything that failed before the document (an unknown flag) is
// only on stderr. A nil command line is a missing required input.
func addJSONTool[In interface{ dir() string }](srv *mcp.Server, r *mcpRunner, tool *mcp.Tool, args func(In) []string) {
	mcp.AddTool(srv, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		a := args(in)
		if a == nil {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: tool.Name + ": a required input is missing (see the tool's input schema)"}}}, nil, nil
		}
		stdout, stderr, err := r.execStreams(ctx, in.dir(), nil, a...)
		if err != nil && ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		res := &mcp.CallToolResult{}
		text := strings.TrimSpace(stdout)
		if err != nil {
			res.IsError = true
			if text == "" {
				text = strings.TrimSpace(stderr + "\nError: " + err.Error())
			}
		}
		if text == "" {
			text = "(no output)"
		}
		res.Content = []mcp.Content{&mcp.TextContent{Text: text}}
		return res, nil, nil
	})
}

func (d dirInput) dir() string { return d.Dir }
