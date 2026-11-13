package cli

// The hosted MCP tools: forge's ACCOUNT-scoped tools (monitoring, releases,
// envs) plus the catalog tools (skills, benefits, explain_error), served by a
// remote MCP server that runs inside someone else's process — Reliant's
// "Reliant Forge" connector on reliant-api-server — on behalf of whoever
// signed in to it. forge/cli.AddHostedTools is the public seam.
//
// THE SPLIT. The local stdio `forge mcp` keeps the repo tools (lint,
// generate, shapes, adopt …): they need the repo. The hosted server needs no
// install and no repo, so it serves what lives in the account. Where both
// serve a tool (skill_*, benefits, explain_error) it is the same tool: the
// same name and the same input schema, less the local server's repo-only
// `dir` (both are built from one args struct, mcp_tools.go).
//
// MULTI-TENANT, SO NO PROCESS STATE. The local server runs a cobra command
// tree with os.Stdout and the working directory swapped under one global
// lock; a hosted server answering many people at once must not. Each call here
// builds its own leaf command (newObs*CmdWith, newEnvReleases*Cmd) with its own
// writers and its own deps: the control plane at a fixed URL, the caller's
// bearer as a human credential (never elevated, never read from or written to
// a credentials file), the project the call names, and no checkout, KCL,
// forge.yaml, machine ledger or environment variable. The same verbs the CLI
// runs, so the documents are the CLI's `--json` documents.
//
// AUTHORITY. The control plane enforces what the person may see (the
// project resolves through their credential). The host may narrow it further:
// Authorize sees each call's resolved target (org, project, env) before
// anything is read or written, and refuses what the host's grant does not
// cover. A nil Authorize refuses every account call: fail closed.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/benefits"
	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/internal/devtelemetry"
	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/internal/releasetimeline"
	"github.com/reliant-labs/forge/pkg/release"
)

// hostedVia is what hosted calls name themselves as in the control plane's
// activity stream ("sean promoted v1.2 via mcp").
const hostedVia = "mcp"

// hostedCLIName is the command skills and next steps are written for: the
// person's agent runs forge on their own machine, through npx or a binary.
const hostedCLIName = "forge"

// AccountTarget is what one account tool call acts on, resolved before
// anything is read or written.
type AccountTarget struct {
	// OrgID is the control-plane organization, resolved from Project by the
	// control plane through the caller's credential.
	OrgID string
	// Project is the forge project name (the tool's `project` input).
	Project string
	// Env is the environment; "" for a project-wide tool (envs_list).
	Env string
}

// AccountToolInfo describes one hosted tool, for a host's consent catalog
// and for Authorize.
type AccountToolInfo struct {
	Name        string
	Title       string
	Description string
	// Mutating tools change state (obs_init, release_record): clients ask
	// before calling them.
	Mutating bool
	// NeedsTarget is false for the catalog tools (skill_*, benefits,
	// explain_error), which read nothing of the account and never reach
	// Authorize.
	NeedsTarget bool
}

// AccountToolsOptions configures AddHostedTools.
type AccountToolsOptions struct {
	// ControlPlaneURL is the control plane's platform API base, e.g.
	// https://admin.reliantapi.com. Required.
	ControlPlaneURL string
	// ConsoleURL is the dashboard base links point at; "" is
	// https://forge.reliantlabs.io.
	ConsoleURL string
	// Token returns the bearer each call is made with, as the signed-in
	// person. Called once per tool call. Required.
	Token func(ctx context.Context, req *mcp.CallToolRequest) (string, error)
	// Authorize runs after a call's target resolves and before anything is
	// read or written; its error refuses the call, and its text is what the
	// model sees. nil refuses every account call.
	Authorize func(ctx context.Context, tool AccountToolInfo, target AccountTarget) error
	// NewProjectOrg names the organization obs_init creates a project in
	// when the project it names does not exist yet. nil: obs_init works on
	// existing projects only.
	NewProjectOrg func(ctx context.Context, req *mcp.CallToolRequest) (string, error)
	// Tools limits registration to these names; nil registers every tool.
	Tools []string
	// HTTPClient is the client control-plane calls use; nil is a client with
	// the CLI's default timeout.
	HTTPClient *http.Client
}

// hostedToolInfos is every hosted tool, in registration order.
var hostedToolInfos = []AccountToolInfo{
	{Name: "obs_status", Title: "Monitoring status", NeedsTarget: true,
		Description: "What is wrong in an environment right now: per-service requests, error rate and latency, the open issues, firing alerts, telemetry freshness and the release in force. The `forge obs status <env> --json` document."},
	{Name: "obs_issues", Title: "Error issues", NeedsTarget: true,
		Description: "The environment's error issues, grouped by fingerprint: what is failing, how often, since which release. The `forge obs issues <env> --json` document."},
	{Name: "obs_issue", Title: "One error issue", NeedsTarget: true,
		Description: "Everything known about one issue: the latest event and call site, events per version, related issues, the release it first appeared in, and its timeline. The `forge obs issue <id> <env> --json` document (suspect commits need the repo: run the CLI there)."},
	{Name: "obs_logs", Title: "Search logs", NeedsTarget: true,
		Description: "Search the environment's logs with the log query language (service:api severity:error \"connection refused\", trace_id:…, attr.http.route:/v1/jobs). The `forge obs logs <env> <query> --json` document."},
	{Name: "obs_traces", Title: "Find traces", NeedsTarget: true,
		Description: "The environment's traces, newest first or slowest first, failing only or by route, with a rate / errors / duration summary. The `forge obs traces <env> --json` document."},
	{Name: "obs_check", Title: "Release health verdict", NeedsTarget: true,
		Description: "Judge a release: PASS, FAIL or INCONCLUSIVE for the env's current promotion (or the one named), comparing the windows before and after it. exit_code 0 good, 1 regressed (see findings and suggested), 2 not proven good, 8 not enough data yet: check again later. The `forge obs check <env> --json` document."},
	{Name: "obs_changes", Title: "What changed", NeedsTarget: true,
		Description: "What changed in the environment, newest first: releases promoted or rolled back, deploys that failed, and the activity feed. The `forge obs changes <env> --json` document."},
	{Name: "releases_list", Title: "Release timeline", NeedsTarget: true,
		Description: "The environment's release timeline, newest first: promotions (BEHIND marks a rollback), failed and timed-out deploys, who and via what, gates and health verdicts. The `forge env releases list <env> --json` document."},
	{Name: "release_show", Title: "One release", NeedsTarget: true,
		Description: "One release of the environment in full, by version or promotion id: what changed against the release it replaced, gates and health. The `forge env releases show <env> <ref> --json` document (its commit list needs the repo: run the CLI there)."},
	{Name: "envs_list", Title: "Environments", NeedsTarget: true,
		Description: "The project's environments on Reliant, each with its console link."},
	{Name: "obs_init", Title: "Start monitoring an app", Mutating: true, NeedsTarget: true,
		Description: "Monitoring for an app forge did not build and does not deploy, in any language: registers each env as self-managed and mints a per-env telemetry ingest token, returning the exact OTEL_* variables the app needs (the token is shown once: store it as a secret). Creates the project on first use. `forge obs init <env…> --no-write --json`; the CLI also records the envs in forge.yaml."},
	{Name: "release_record", Title: "Record a deploy", Mutating: true, NeedsTarget: true,
		Description: "Record that the team's own pipeline deployed a version to an environment: a record-only release and its promotion, the deploy marker obs_changes lists and obs_check judges. Idempotent for the same version and content. `forge release record --env --version --commit [--workload --image]…`."},
	{Name: "benefits", Title: "What forge does",
		Description: "What forge does for an existing repo in any language, as JSON: each benefit with its first step (adopt) and the agent time to first value (estimate), to quote to your user. all=true lists every shipped benefit. Then skill_load name=adopt."},
	{Name: "skill_list", Title: "Skill catalog", Description: mcpDescSkillList},
	{Name: "skill_search", Title: "Find a skill", Description: mcpDescSkillSearch},
	{Name: "skill_load", Title: "Load a skill", Description: mcpDescSkillLoad},
	{Name: "explain_error", Title: "Explain a forge error",
		Description: "Turn a forge failure into what it means and the exact command to run next. Pass error_reason and exit_code from a --json document when you have them, else the error text."},
}

// HostedTools lists every tool AddHostedTools can register, in order.
func HostedTools() []AccountToolInfo { return append([]AccountToolInfo(nil), hostedToolInfos...) }

// HostedInstructions is the instructions text of the hosted server.
func HostedInstructions() string { return hostedInstructions }

const hostedInstructions = "This is Reliant Forge (github.com/reliant-labs/forge), signed in to the user's Reliant account — not " +
	"Atlassian Forge and not Foundry's forge. These tools read and act on what lives in the account: monitoring " +
	"(obs_status, obs_issues, obs_issue, obs_logs, obs_traces, obs_check, obs_changes), releases (releases_list, " +
	"release_show, release_record) and environments (envs_list, obs_init). Each takes `project` (the forge project " +
	"name, as in the console's https://forge.reliantlabs.io/forge/p/<project>) and usually `env`; start with " +
	"envs_list or obs_status. Every result is the CLI's --json document plus console_url, the page that shows it. " +
	"No project yet? obs_init registers envs and returns the OTEL_* variables that send any app's telemetry here. " +
	"The repo tools (lint, generate, shapes, adopt) need the repo, so they live in the local server: " +
	"`npx -y @reliantlabs/forge mcp`. skill_load name=connect says how to add either to each client; " +
	"skill_load name=adopt is the playbook for an existing app."

// AddHostedTools registers the hosted tools on srv. Safe for concurrent use
// by many sessions: it keeps no process state (see the file comment).
func AddHostedTools(srv *mcp.Server, o AccountToolsOptions) error {
	if strings.TrimSpace(o.ControlPlaneURL) == "" {
		return errors.New("forge hosted tools: ControlPlaneURL is required")
	}
	if o.Token == nil {
		return errors.New("forge hosted tools: Token is required")
	}
	h := &hostedTools{opts: o, url: strings.TrimRight(strings.TrimSpace(o.ControlPlaneURL), "/")}
	h.console = strings.TrimRight(strings.TrimSpace(o.ConsoleURL), "/")
	if h.console == "" {
		h.console = cloud.DefaultConsoleURL
	}
	want := map[string]bool{}
	for _, n := range o.Tools {
		want[n] = true
	}
	for _, info := range hostedToolInfos {
		if o.Tools != nil && !want[info.Name] {
			continue
		}
		h.register(srv, info)
	}
	return nil
}

type hostedTools struct {
	opts    AccountToolsOptions
	url     string
	console string
}

// ── inputs ───────────────────────────────────────────────────────────────

// hostedTarget is the project and env every account tool takes.
type hostedTarget struct {
	Project string `json:"project" jsonschema:"the forge project name: forge.yaml name, the console's /forge/p/<project>"`
	Env     string `json:"env" jsonschema:"the environment, e.g. prod or staging (envs_list names them)"`
}

// hostedWindow is the time range and page size of a read.
type hostedWindow struct {
	Since    string   `json:"since,omitempty" jsonschema:"how far back to read: 15m, 2h, 7d (default: the tool's own window)"`
	From     string   `json:"from,omitempty" jsonschema:"range start, RFC 3339 (overrides since)"`
	To       string   `json:"to,omitempty" jsonschema:"range end, RFC 3339 (default now)"`
	Services []string `json:"services,omitempty" jsonschema:"only these service.name values"`
	Limit    int      `json:"limit,omitempty" jsonschema:"most items to return (default: the tool's own page size)"`
}

func (w hostedWindow) args() []string {
	var a []string
	a = flagArg(a, "--since", w.Since)
	a = flagArg(a, "--from", w.From)
	a = flagArg(a, "--to", w.To)
	for _, s := range w.Services {
		a = flagArg(a, "--service", s)
	}
	if w.Limit > 0 {
		a = append(a, "--limit", fmt.Sprint(w.Limit))
	}
	return a
}

type hostedObsStatusInput struct {
	hostedTarget
	hostedWindow
}

type hostedObsIssuesInput struct {
	hostedTarget
	hostedWindow
	Status       []string `json:"status,omitempty" jsonschema:"only issues in these states (default: the open ones)"`
	SinceRelease string   `json:"since_release,omitempty" jsonschema:"only issues first seen in this release version"`
	Sort         string   `json:"sort,omitempty" jsonschema:"last | first | events (default last)"`
	PageToken    string   `json:"page_token,omitempty" jsonschema:"resume from a next_page_token"`
}

type hostedObsIssueInput struct {
	hostedTarget
	ID     string `json:"id" jsonschema:"the issue id from obs_issues or obs_status, e.g. ISS-7f3a or sfp1-…"`
	Events int    `json:"events,omitempty" jsonschema:"also list this many of its latest events"`
}

type hostedObsLogsInput struct {
	hostedTarget
	hostedWindow
	Query     string   `json:"query,omitempty" jsonschema:"the log query: service:api severity:error \"connection refused\", body:timeout OR body:deadline, trace_id:…"`
	Severity  []string `json:"severity,omitempty" jsonschema:"only these severities: trace, debug, info, warn, error, fatal"`
	Trace     string   `json:"trace,omitempty" jsonschema:"only the logs of this trace id"`
	Oldest    bool     `json:"oldest,omitempty" jsonschema:"oldest first"`
	PageToken string   `json:"page_token,omitempty" jsonschema:"resume from a next_page_token"`
}

type hostedObsTracesInput struct {
	hostedTarget
	hostedWindow
	Errors      bool   `json:"errors,omitempty" jsonschema:"only traces with an error span"`
	Slow        bool   `json:"slow,omitempty" jsonschema:"slowest first"`
	MinDuration string `json:"min_duration,omitempty" jsonschema:"only traces at least this long, e.g. 500ms"`
	Route       string `json:"route,omitempty" jsonschema:"only traces whose root span name contains this"`
	PageToken   string `json:"page_token,omitempty" jsonschema:"resume from a next_page_token"`
}

type hostedObsCheckInput struct {
	hostedTarget
	Release   string `json:"release,omitempty" jsonschema:"judge the promotion of this release version (default: the env's current one)"`
	Promotion string `json:"promotion,omitempty" jsonschema:"judge this promotion id"`
	Window    string `json:"window,omitempty" jsonschema:"width of each window (default the env's release_health.window, 15m; max 6h)"`
}

type hostedObsChangesInput struct {
	hostedTarget
	Since string `json:"since,omitempty" jsonschema:"how far back to read: 2h, 7d (default 2h)"`
	Limit int    `json:"limit,omitempty" jsonschema:"most changes to return"`
}

type hostedReleasesListInput struct {
	hostedTarget
	Since string `json:"since,omitempty" jsonschema:"window start: a duration ago (7d, 36h) or a time (default 7d)"`
	Limit int    `json:"limit,omitempty" jsonschema:"most entries to return (default 20)"`
}

type hostedReleaseShowInput struct {
	hostedTarget
	Ref string `json:"ref" jsonschema:"a release version, or a promotion id"`
}

type hostedEnvsListInput struct {
	Project string `json:"project" jsonschema:"the forge project name: forge.yaml name, the console's /forge/p/<project>"`
}

type hostedObsInitInput struct {
	Project   string   `json:"project" jsonschema:"the forge project name; created on first use"`
	Envs      []string `json:"envs" jsonschema:"the environments to monitor, e.g. [\"prod\", \"staging\"]"`
	Service   string   `json:"service,omitempty" jsonschema:"fill OTEL_SERVICE_NAME with this service.name (default: a placeholder per deployable)"`
	TokenName string   `json:"token_name,omitempty" jsonschema:"the ingest token's label, e.g. ci, prod-k8s (default forge-obs-init)"`
	TTL       string   `json:"ttl,omitempty" jsonschema:"the ingest token's lifetime, e.g. 720h or 30d (default and maximum 90 days, at least 1h)"`
}

type hostedWorkloadImage struct {
	Workload string `json:"workload" jsonschema:"the workload it deployed, e.g. api"`
	Image    string `json:"image" jsonschema:"the image it runs, pinned by digest: <ref>@sha256:<hex>"`
}

type hostedReleaseRecordInput struct {
	hostedTarget
	Version   string                `json:"version" jsonschema:"the version the pipeline deployed, e.g. v1.4.2"`
	Commit    string                `json:"commit,omitempty" jsonschema:"the git commit it was built from (7-64 hex characters)"`
	Workloads []hostedWorkloadImage `json:"workloads,omitempty" jsonschema:"each workload and the image it runs; omit for a version-only record"`
	RunURL    string                `json:"run_url,omitempty" jsonschema:"link to the CI run"`
	RunID     string                `json:"run_id,omitempty" jsonschema:"CI run id, stable across retries (default: the run_url)"`
}

func flagArg(a []string, flag, v string) []string {
	if strings.TrimSpace(v) == "" {
		return a
	}
	return append(a, flag, v)
}

// ── registration ─────────────────────────────────────────────────────────

func (h *hostedTools) register(srv *mcp.Server, info AccountToolInfo) {
	readOnly := &mcp.ToolAnnotations{Title: info.Title, ReadOnlyHint: true, OpenWorldHint: ptr(info.NeedsTarget)}
	mutating := &mcp.ToolAnnotations{Title: info.Title, DestructiveHint: ptr(false), IdempotentHint: true, OpenWorldHint: ptr(true)}
	tool := &mcp.Tool{Name: info.Name, Title: info.Title, Description: info.Description, Annotations: readOnly}
	if info.Mutating {
		tool.Annotations = mutating
	}
	obs := func(build func(obsDepsFunc) *cobra.Command, args func() []string, link func(c hostedCall) string) func(context.Context, *mcp.CallToolRequest, hostedTarget) (*mcp.CallToolResult, any, error) {
		return func(ctx context.Context, req *mcp.CallToolRequest, t hostedTarget) (*mcp.CallToolResult, any, error) {
			c, err := h.open(ctx, req, info, t.Project, t.Env)
			if err != nil {
				return hostedRefused(err), nil, nil
			}
			return h.runObs(ctx, c, build, append([]string{t.Env}, args()...), link(c)), nil, nil
		}
	}
	switch info.Name {
	case "obs_status":
		mcp.AddTool(srv, tool, func(ctx context.Context, req *mcp.CallToolRequest, in hostedObsStatusInput) (*mcp.CallToolResult, any, error) {
			return obs(newObsStatusCmdWith, in.hostedWindow.args, func(c hostedCall) string { return c.console.Home(c.target.Env) })(ctx, req, in.hostedTarget)
		})
	case "obs_issues":
		mcp.AddTool(srv, tool, func(ctx context.Context, req *mcp.CallToolRequest, in hostedObsIssuesInput) (*mcp.CallToolResult, any, error) {
			args := func() []string {
				a := in.hostedWindow.args()
				for _, s := range in.Status {
					a = flagArg(a, "--status", s)
				}
				a = flagArg(a, "--since-release", in.SinceRelease)
				a = flagArg(a, "--sort", in.Sort)
				return flagArg(a, "--page-token", in.PageToken)
			}
			return obs(newObsIssuesCmdWith, args, func(c hostedCall) string { return c.console.View("errors", c.target.Env) })(ctx, req, in.hostedTarget)
		})
	case "obs_issue":
		mcp.AddTool(srv, tool, func(ctx context.Context, req *mcp.CallToolRequest, in hostedObsIssueInput) (*mcp.CallToolResult, any, error) {
			if strings.TrimSpace(in.ID) == "" {
				return hostedRefused(errors.New("id is required: an issue id from obs_issues")), nil, nil
			}
			c, err := h.open(ctx, req, info, in.Project, in.Env)
			if err != nil {
				return hostedRefused(err), nil, nil
			}
			args := []string{in.ID, in.Env}
			if in.Events > 0 {
				args = append(args, "--events", fmt.Sprint(in.Events))
			}
			return h.runObs(ctx, c, newObsIssueCmdWith, args, c.console.View("errors", c.target.Env)), nil, nil
		})
	case "obs_logs":
		mcp.AddTool(srv, tool, func(ctx context.Context, req *mcp.CallToolRequest, in hostedObsLogsInput) (*mcp.CallToolResult, any, error) {
			c, err := h.open(ctx, req, info, in.Project, in.Env)
			if err != nil {
				return hostedRefused(err), nil, nil
			}
			// Two positionals always, so the env is never read as a query.
			args := append([]string{in.Env, in.Query}, in.hostedWindow.args()...)
			for _, s := range in.Severity {
				args = flagArg(args, "--severity", s)
			}
			args = flagArg(args, "--trace", in.Trace)
			args = flagArg(args, "--page-token", in.PageToken)
			if in.Oldest {
				args = append(args, "--oldest")
			}
			return h.runObs(ctx, c, newObsLogsCmdWith, args, c.console.View("logs", c.target.Env)), nil, nil
		})
	case "obs_traces":
		mcp.AddTool(srv, tool, func(ctx context.Context, req *mcp.CallToolRequest, in hostedObsTracesInput) (*mcp.CallToolResult, any, error) {
			args := func() []string {
				a := in.hostedWindow.args()
				if in.Errors {
					a = append(a, "--errors")
				}
				if in.Slow {
					a = append(a, "--slow")
				}
				a = flagArg(a, "--min-duration", in.MinDuration)
				a = flagArg(a, "--route", in.Route)
				return flagArg(a, "--page-token", in.PageToken)
			}
			return obs(newObsTracesCmdWith, args, func(c hostedCall) string { return c.console.View("traces", c.target.Env) })(ctx, req, in.hostedTarget)
		})
	case "obs_check":
		mcp.AddTool(srv, tool, func(ctx context.Context, req *mcp.CallToolRequest, in hostedObsCheckInput) (*mcp.CallToolResult, any, error) {
			args := func() []string {
				a := flagArg(nil, "--release", in.Release)
				a = flagArg(a, "--promotion", in.Promotion)
				return flagArg(a, "--window", in.Window)
			}
			link := func(c hostedCall) string {
				if in.Release != "" {
					return c.console.Release(in.Release, c.target.Env)
				}
				return c.console.Environment(c.target.Env)
			}
			return obs(newObsCheckCmdWith, args, link)(ctx, req, in.hostedTarget)
		})
	case "obs_changes":
		mcp.AddTool(srv, tool, func(ctx context.Context, req *mcp.CallToolRequest, in hostedObsChangesInput) (*mcp.CallToolResult, any, error) {
			args := func() []string {
				a := flagArg(nil, "--since", in.Since)
				if in.Limit > 0 {
					a = append(a, "--limit", fmt.Sprint(in.Limit))
				}
				return a
			}
			return obs(newObsChangesCmdWith, args, func(c hostedCall) string { return c.console.Environment(c.target.Env) })(ctx, req, in.hostedTarget)
		})
	case "releases_list":
		mcp.AddTool(srv, tool, func(ctx context.Context, req *mcp.CallToolRequest, in hostedReleasesListInput) (*mcp.CallToolResult, any, error) {
			c, err := h.open(ctx, req, info, in.Project, in.Env)
			if err != nil {
				return hostedRefused(err), nil, nil
			}
			args := flagArg([]string{in.Env}, "--since", in.Since)
			if in.Limit > 0 {
				args = append(args, "--limit", fmt.Sprint(in.Limit))
			}
			return h.runCmd(ctx, newEnvReleasesListCmd(h.releasesDeps(c)), args, c.console.Link("/releases", c.target.Env)), nil, nil
		})
	case "release_show":
		mcp.AddTool(srv, tool, func(ctx context.Context, req *mcp.CallToolRequest, in hostedReleaseShowInput) (*mcp.CallToolResult, any, error) {
			if strings.TrimSpace(in.Ref) == "" {
				return hostedRefused(errors.New("ref is required: a release version or promotion id from releases_list")), nil, nil
			}
			c, err := h.open(ctx, req, info, in.Project, in.Env)
			if err != nil {
				return hostedRefused(err), nil, nil
			}
			return h.runCmd(ctx, newEnvReleasesShowCmd(h.releasesDeps(c)), []string{in.Env, in.Ref}, c.console.Release(in.Ref, c.target.Env)), nil, nil
		})
	case "envs_list":
		mcp.AddTool(srv, tool, func(ctx context.Context, req *mcp.CallToolRequest, in hostedEnvsListInput) (*mcp.CallToolResult, any, error) {
			c, err := h.open(ctx, req, info, in.Project, "")
			if err != nil {
				return hostedRefused(err), nil, nil
			}
			return h.envsList(ctx, c), nil, nil
		})
	case "obs_init":
		mcp.AddTool(srv, tool, func(ctx context.Context, req *mcp.CallToolRequest, in hostedObsInitInput) (*mcp.CallToolResult, any, error) {
			return h.obsInit(ctx, req, info, in), nil, nil
		})
	case "release_record":
		mcp.AddTool(srv, tool, func(ctx context.Context, req *mcp.CallToolRequest, in hostedReleaseRecordInput) (*mcp.CallToolResult, any, error) {
			return h.releaseRecord(ctx, req, info, in), nil, nil
		})
	case "benefits":
		mcp.AddTool(srv, tool, func(_ context.Context, _ *mcp.CallToolRequest, in benefitsArgs) (*mcp.CallToolResult, any, error) {
			return hostedBenefits(in), nil, nil
		})
	case "skill_list":
		mcp.AddTool(srv, tool, func(_ context.Context, _ *mcp.CallToolRequest, in skillListArgs) (*mcp.CallToolResult, any, error) {
			return hostedSkillList(in), nil, nil
		})
	case "skill_search":
		mcp.AddTool(srv, tool, func(_ context.Context, _ *mcp.CallToolRequest, in skillSearchArgs) (*mcp.CallToolResult, any, error) {
			return hostedSkillSearch(in), nil, nil
		})
	case "skill_load":
		mcp.AddTool(srv, tool, func(_ context.Context, _ *mcp.CallToolRequest, in skillLoadArgs) (*mcp.CallToolResult, any, error) {
			return hostedSkillLoad(in), nil, nil
		})
	case "explain_error":
		addExplainErrorTool(srv)
	}
}

// ── the signed-in call ───────────────────────────────────────────────────

// hostedCall is one tool call's resolved, authorized connection.
type hostedCall struct {
	client  *cloud.Client
	target  AccountTarget
	console hostedConsole
}

// hostedConsole is cloud.Console plus the links it has no helper for.
type hostedConsole struct{ cloud.Console }

// Link is the project page at path, scoped to env.
func (c hostedConsole) Link(path, env string) string {
	base := c.Home(env)
	if base == "" {
		return ""
	}
	if i := strings.IndexByte(base, '?'); i >= 0 {
		return base[:i] + path + base[i:]
	}
	return base + path
}

// client builds the caller's control-plane client for project.
func (h *hostedTools) client(ctx context.Context, req *mcp.CallToolRequest, project string) (*cloud.Client, error) {
	tok, err := h.opts.Token(ctx, req)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(tok) == "" {
		return nil, errors.New("this connection is not signed in: reconnect the Reliant Forge connector to sign in")
	}
	// A human credential the caller handed over: used verbatim, never
	// elevated, never stored.
	c := cloud.NewClient(cloud.Endpoint{URL: h.url, ProjectName: project},
		cloud.Credential{Kind: "human", Token: tok, Source: cloud.SourceFlag, From: "the MCP connector's sign-in"})
	c.Via = hostedVia
	if h.opts.HTTPClient != nil {
		c.HTTP = h.opts.HTTPClient
	}
	return c, nil
}

// open signs the call in as its person, resolves its target and asks
// Authorize, before anything is read.
func (h *hostedTools) open(ctx context.Context, req *mcp.CallToolRequest, info AccountToolInfo, project, env string) (hostedCall, error) {
	project, env = strings.TrimSpace(project), strings.TrimSpace(env)
	if project == "" {
		return hostedCall{}, errors.New("project is required: the forge project name (the console's /forge/p/<project>)")
	}
	if env == "" && info.Name != "envs_list" {
		return hostedCall{}, fmt.Errorf("env is required: an environment of %s (envs_list names them)", project)
	}
	client, err := h.client(ctx, req, project)
	if err != nil {
		return hostedCall{}, err
	}
	org, err := resolveHostedProject(ctx, client)
	if err != nil {
		if isCloudNotFound(err) {
			return hostedCall{}, fmt.Errorf("project %s does not exist in an organization you belong to; obs_init creates it", project)
		}
		return hostedCall{}, obsbackend.MapCloudError(err)
	}
	return h.authorize(ctx, info, client, AccountTarget{OrgID: org, Project: project, Env: env})
}

func (h *hostedTools) authorize(ctx context.Context, info AccountToolInfo, client *cloud.Client, t AccountTarget) (hostedCall, error) {
	if h.opts.Authorize == nil {
		return hostedCall{}, errors.New("this server authorizes no account access")
	}
	if err := h.opts.Authorize(ctx, info, t); err != nil {
		return hostedCall{}, err
	}
	return hostedCall{client: client, target: t, console: hostedConsole{cloud.NewConsole(h.console, t.Project)}}, nil
}

// resolveHostedProject resolves the client's project through the caller's
// credential (cloud.ResolveOrganization's question) and pins its id on the
// client, so the calls that follow do not resolve it again.
func resolveHostedProject(ctx context.Context, c *cloud.Client) (string, error) {
	var reply struct {
		ProjectID string `json:"projectId"`
		OrgID     string `json:"orgId"`
	}
	if err := c.Call(ctx, "controlplane.v1.DeployService/ResolveHostedProject", map[string]any{"name": c.Endpoint.ProjectName}, &reply); err != nil {
		return "", err
	}
	if reply.ProjectID == "" || reply.OrgID == "" {
		return "", errors.New("the control plane returned an incomplete project identity")
	}
	c.Endpoint.ProjectID = reply.ProjectID
	return reply.OrgID, nil
}

func isCloudNotFound(err error) bool {
	var ce *cloud.Error
	return errors.As(err, &ce) && ce.HasCode(cloud.CodeNotFound)
}

// obsDeps is what a hosted obs verb reads the world through: the control
// plane only.
func (h *hostedTools) obsDeps(c hostedCall, stdout, stderr io.Writer) obsDeps {
	timeline := releasetimeline.NewCloud(c.client, c.target.Project)
	obsbackend.DeclareCloudScopes(c.client)
	return obsDeps{
		Facts: func(_ context.Context, _ io.Writer, env string) (obsbackend.Facts, error) {
			return obsbackend.Facts{Env: env, Declared: true, Enabled: true, ControlPlane: h.url}, nil
		},
		Local: func(env string) obsbackend.Backend { return nil },
		Cloud: func(ctx context.Context, env string) (obsbackend.Cloud, error) {
			id, err := deploytarget.LookupHostedEnvironment(ctx, c.client, c.target.Project, env)
			if errors.Is(err, deploytarget.ErrHostedEnvironmentNotFound) {
				return obsbackend.Cloud{}, obsbackend.Undetermined("env %s of %s does not exist on Reliant yet: deploy to it, or register it with obs_init", env, c.target.Project)
			}
			if err != nil {
				return obsbackend.Cloud{}, obsbackend.MapCloudError(err)
			}
			return obsbackend.Cloud{Env: env, EnvID: id, OrgID: c.target.OrgID, Caller: c.client}, nil
		},
		HostedDev: func(context.Context, string, devtelemetry.Record) (obsbackend.Cloud, error) {
			return obsbackend.Cloud{}, errors.New("no hosted dev instances here")
		},
		InstanceRecords: func(context.Context, string) ([]devtelemetry.Record, error) { return nil, nil },
		Release: func(ctx context.Context, env string, at time.Time) (*liveRelease, error) {
			ctx, cancel := context.WithTimeout(ctx, liveReleaseTimeout)
			defer cancel()
			d, err := timeline.Load(ctx, env, time.Time{})
			if err != nil {
				return nil, err
			}
			return releaseFromAt(releasetimeline.Assemble(d, time.Time{}, 0).At(at)), nil
		},
		Now:     time.Now,
		Stdout:  stdout,
		Stderr:  stderr,
		Project: c.target.Project,
		Suspects: func(context.Context, io.Writer, obsbackend.GetIssueResult, string) ([]obsSuspectDoc, string) {
			return []obsSuspectDoc{}, "suspect commits need the repository: run `forge obs issue <id> <env>` in it"
		},
	}
}

// releasesDeps is what a hosted releases verb reads: the control plane's
// ledger, and no checkout.
func (h *hostedTools) releasesDeps(c hostedCall) releasesDeps {
	tl := releasetimeline.NewCloud(c.client, c.target.Project)
	return releasesDeps{
		open: func(context.Context, string) (releasesBackend, error) {
			return releasesBackend{Name: backendCloud, Source: tl, Cloud: &tl, Ledger: h.url}, nil
		},
		now:     time.Now,
		commits: func() releasetimeline.CommitLister { return noCheckout{} },
	}
}

// noCheckout is the commit lister of a server with no repository.
type noCheckout struct{}

func (noCheckout) Commits(context.Context, string, string, int) ([]release.Commit, error) {
	return nil, errors.New("the hosted server has no checkout; run `forge env releases show` in the repository for the commits")
}

// runObs runs one obs verb with c's deps, as `--json --backend cloud`.
func (h *hostedTools) runObs(ctx context.Context, c hostedCall, build func(obsDepsFunc) *cobra.Command, args []string, link string) *mcp.CallToolResult {
	var stdout, stderr bytes.Buffer
	d := h.obsDeps(c, &stdout, &stderr)
	cmd := build(func(*cobra.Command) obsDeps { return d })
	return h.exec(ctx, cmd, append(args, "--json", "--backend", obsbackend.BackendCloud), &stdout, &stderr, link)
}

// runCmd runs a leaf verb that takes its deps at construction, as --json.
func (h *hostedTools) runCmd(ctx context.Context, cmd *cobra.Command, args []string, link string) *mcp.CallToolResult {
	var stdout, stderr bytes.Buffer
	return h.exec(ctx, cmd, append(args, "--json"), &stdout, &stderr, link)
}

func (h *hostedTools) exec(ctx context.Context, cmd *cobra.Command, args []string, stdout, stderr *bytes.Buffer, link string) *mcp.CallToolResult {
	cmd.SetArgs(args)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	err := cmd.ExecuteContext(ctx)
	return documentResult(stdout.Bytes(), stderr.String(), err, link)
}

// documentResult is a verb's --json document as a tool result: the document
// is the structured content, with console_url added, and its serialization is
// the text (MCP's rule for structured results). A verb that answered with a
// document is not a failed call even when its exit status is not 0 (an
// obs_check FAIL is an answer); only no document at all is.
func documentResult(doc []byte, stderr string, runErr error, link string) *mcp.CallToolResult {
	var m map[string]any
	if json.Unmarshal(bytes.TrimSpace(doc), &m) != nil || m == nil {
		msg := strings.TrimSpace(stderr)
		if runErr != nil {
			msg = strings.TrimSpace(msg + "\n" + runErr.Error())
		}
		if msg == "" {
			msg = "no answer"
		}
		return hostedRefused(errors.New(msg))
	}
	if link != "" {
		if _, ok := m["console_url"]; !ok {
			m["console_url"] = link
		}
	}
	return structuredResult(m, stderr)
}

func structuredResult(m any, note string) *mcp.CallToolResult {
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return hostedRefused(err)
	}
	text := string(body)
	if n := strings.TrimSpace(note); n != "" {
		text += "\n\n" + n
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}, StructuredContent: m}
}

// hostedRefused is a call that could not answer.
func hostedRefused(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "Error: " + err.Error()}}}
}

// ── envs, init, record ───────────────────────────────────────────────────

func (h *hostedTools) envsList(ctx context.Context, c hostedCall) *mcp.CallToolResult {
	envs, err := deploytarget.ListHostedEnvironments(ctx, c.client, c.target.Project)
	if err != nil {
		return hostedRefused(obsbackend.MapCloudError(err))
	}
	type row struct {
		Name       string `json:"name"`
		ID         string `json:"id"`
		ConsoleURL string `json:"console_url,omitempty"`
	}
	doc := struct {
		Schema     string `json:"schema"`
		Project    string `json:"project"`
		OrgID      string `json:"org_id"`
		Envs       []row  `json:"envs"`
		ConsoleURL string `json:"console_url,omitempty"`
	}{Schema: "forge.mcp.envs/v1", Project: c.target.Project, OrgID: c.target.OrgID, Envs: []row{}, ConsoleURL: c.console.Home("")}
	for _, e := range envs {
		doc.Envs = append(doc.Envs, row{Name: e.Name, ID: e.ID, ConsoleURL: c.console.Environment(e.Name)})
	}
	return structuredResult(doc, "")
}

func (h *hostedTools) obsInit(ctx context.Context, req *mcp.CallToolRequest, info AccountToolInfo, in hostedObsInitInput) *mcp.CallToolResult {
	project := strings.TrimSpace(in.Project)
	if project == "" || !validProjectName(project) {
		return hostedRefused(fmt.Errorf("project %q is not a project name: lowercase letters, digits and dashes", in.Project))
	}
	if len(in.Envs) == 0 {
		return hostedRefused(errors.New("envs is required: the environments to monitor, e.g. [\"prod\"]"))
	}
	o := obsInitOpts{service: in.Service, tokenName: emptyAs(in.TokenName, defaultIngestTokenName)}
	if in.TTL != "" {
		ttl, err := parseObsDuration(in.TTL)
		if err != nil {
			return hostedRefused(fmt.Errorf("ttl: %w", err))
		}
		o.ttl = ttl
	}
	client, err := h.client(ctx, req, project)
	if err != nil {
		return hostedRefused(err)
	}
	org, err := resolveHostedProject(ctx, client)
	var c hostedCall
	switch {
	case err == nil:
		c, err = h.authorize(ctx, info, client, AccountTarget{OrgID: org, Project: project})
	case isCloudNotFound(err) && h.opts.NewProjectOrg != nil:
		// A first obs_init creates the project, in the organization the
		// host confines this session to — authorized before it exists.
		var newOrg string
		if newOrg, err = h.opts.NewProjectOrg(ctx, req); err == nil {
			if c, err = h.authorize(ctx, info, client, AccountTarget{OrgID: newOrg, Project: project}); err == nil {
				err = createHostedProject(ctx, client, project, newOrg)
			}
		}
	case isCloudNotFound(err):
		err = fmt.Errorf("project %s does not exist yet: create it with `forge obs init` from the repository, or in the console", project)
	default:
		err = obsbackend.MapCloudError(err)
	}
	if err != nil {
		return hostedRefused(err)
	}
	sess := obsSession{Caller: c.client, OrgID: c.target.OrgID, ControlPlane: h.url}
	envs, err := registerMonitoredEnvs(ctx, sess, project, in.Envs, c.console.Console, o)
	if err != nil {
		return hostedRefused(err)
	}
	doc := obsInitDoc{Schema: obsInitSchema, Project: project, OrgID: c.target.OrgID, ControlPlane: h.url,
		ConsoleURL: c.console.Home(""), Envs: envs}
	return structuredResult(doc, "Each token is in OTEL_EXPORTER_OTLP_HEADERS and is shown only now: store it as a secret where the app runs. "+
		"Then obs_status shows the app's telemetry once it arrives.")
}

// createHostedProject creates project in org (DeployService.ResolveHostedProject
// with create), the way a person's first `forge obs init` does.
func createHostedProject(ctx context.Context, c *cloud.Client, project, org string) error {
	var reply struct {
		ProjectID string `json:"projectId"`
	}
	if err := c.Call(ctx, "controlplane.v1.DeployService/ResolveHostedProject",
		map[string]any{"name": project, "create": true, "orgId": org}, &reply); err != nil {
		return obsbackend.MapCloudError(fmt.Errorf("create project %s: %w", project, err))
	}
	c.Endpoint.ProjectID = reply.ProjectID
	return nil
}

func (h *hostedTools) releaseRecord(ctx context.Context, req *mcp.CallToolRequest, info AccountToolInfo, in hostedReleaseRecordInput) *mcp.CallToolResult {
	if strings.TrimSpace(in.Version) == "" {
		return hostedRefused(errors.New("version is required"))
	}
	var workloads, images []string
	for _, w := range in.Workloads {
		workloads, images = append(workloads, w.Workload), append(images, w.Image)
	}
	artifacts, byWorkload, err := recordArtifacts(workloads, images)
	if err != nil {
		return hostedRefused(err)
	}
	noEnv := func(string) string { return "" }
	commit, err := recordCommit(in.Commit, noEnv)
	if err != nil {
		return hostedRefused(err)
	}
	run, err := recordRun(runOptions{ID: in.RunID, URL: in.RunURL}, noEnv)
	if err != nil {
		return hostedRefused(err)
	}
	c, err := h.open(ctx, req, info, in.Project, in.Env)
	if err != nil {
		return hostedRefused(err)
	}
	target := recordLedger{
		ledger:   hostedLedger(c.client, h.url, c.target.Project, deploytarget.HostedEnvSelfManaged),
		endpoint: h.url,
	}
	res, err := recordReleaseInto(ctx, target, recordInput{
		project: c.target.Project, env: c.target.Env, version: strings.TrimSpace(in.Version), commit: commit,
		artifacts: artifacts, workloads: byWorkload, run: run,
	})
	if err != nil {
		return hostedRefused(err)
	}
	res.ConsoleURL = c.console.Release(res.Version, c.target.Env)
	return structuredResult(res, "")
}

// ── catalog tools: forge-shipped only ────────────────────────────────────

// The hosted catalog is forge's own: no project, user or machine skills (the
// server has none of the person's), and commands written for `forge`.

func hostedBenefits(in benefitsArgs) *mcp.CallToolResult {
	reg, err := benefits.Load()
	if err != nil {
		return hostedRefused(err)
	}
	if in.Category != "" && !reg.HasCategory(in.Category) {
		return hostedRefused(fmt.Errorf("no benefit category %q; use one of: %s", in.Category, categoryIDs(reg)))
	}
	// No repo here: what applies to any repo (scope any), or everything.
	return structuredResult(buildBenefitsReport(reg, false, in.All, in.Category), "")
}

func hostedSkills(includeMigrations bool) ([]skillMeta, error) {
	skills, err := listForgeShippedSkills()
	if err != nil || includeMigrations {
		return skills, err
	}
	return filterDefaultRelevance(skills), nil
}

func hostedSkillList(in skillListArgs) *mcp.CallToolResult {
	skills, err := hostedSkills(in.IncludeMigrations)
	if err != nil {
		return hostedRefused(err)
	}
	all := in.All
	if in.Group != "" {
		if skills = skillsInGroup(skills, in.Group); len(skills) == 0 {
			return hostedRefused(fmt.Errorf("no skill or group named %q: skill_list shows the groups, skill_search finds a skill", in.Group))
		}
		all = true
	}
	var b bytes.Buffer
	if err := writeSkillListText(&b, skills, all, false); err != nil {
		return hostedRefused(err)
	}
	return textResult(rewriteForgeCommands(b.Bytes(), hostedCLIName))
}

func hostedSkillSearch(in skillSearchArgs) *mcp.CallToolResult {
	skills, err := listForgeShippedSkills()
	if err != nil {
		return hostedRefused(err)
	}
	var b bytes.Buffer
	if err := writeSkillSearchText(&b, in.Query, searchSkillsIn(skills, in.Query)); err != nil {
		return hostedRefused(err)
	}
	return textResult(b.Bytes())
}

// hostedSkillLoad is RenderSkillContentAt for the forge-shipped scope alone:
// the same body `forge skill load` prints outside a project.
func hostedSkillLoad(in skillLoadArgs) *mcp.CallToolResult {
	name := strings.TrimPrefix(strings.TrimSpace(in.Name), "forge/")
	skills, err := listForgeShippedSkills()
	if err != nil {
		return hostedRefused(err)
	}
	for _, s := range skills {
		if s.Path != name {
			continue
		}
		body, err := s.body()
		if err != nil {
			return hostedRefused(err)
		}
		return textResult(rewriteForgeCommands(RenderSkillForAudience(body, SkillAudienceAll), hostedCLIName))
	}
	return hostedRefused(fmt.Errorf("skill %q not found: skill_list shows the catalog, skill_search finds a skill", name))
}

func textResult(b []byte) *mcp.CallToolResult {
	text := strings.TrimRight(string(b), "\n")
	if text == "" {
		text = "(no output)"
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}
