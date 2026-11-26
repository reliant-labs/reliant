package accesstoken

import "slices"

// ── The hosted Reliant Forge MCP server's token (mcp:forge) ──────────
//
// The hosted forge tools (forge/cli.AddHostedTools: monitoring, releases,
// environments) run forge's own verbs in-process and call the control plane
// as the signed-in person. An MCP client's OAuth token for that server is
// mcp:forge: it holds no product scope, so the control plane needs to know
// which procedures the tools call and what each one needs. This package
// otherwise knows no rpc; this table is the exception because forge is the
// caller: the tools and the table change in one commit, and forge's
// internal/cli tests fail when a tool reaches a procedure the table does not
// name.
//
// A control plane admits an mcp:forge token on a procedure only when:
//
//   - the procedure is in this table;
//   - the token's audience is a hosted forge MCP resource it serves;
//   - the token's person holds the procedure's scope in the token's org
//     right now (their live grants and membership).
//
// It then runs the call holding THAT scope alone, so the handler's own checks
// apply unchanged and the call can do no more than the procedure's scope
// allows. Every other procedure is refused.
//
// The writes a hosted tool makes are release_record's (CutRelease,
// EnsureEnvironment, Promote; deploy:write). obs_init is not served: it mints
// telemetry ingest tokens (TelemetryTokenService, minted by a person only) and
// creates projects, so it fails on an mcp:forge token with the control plane's
// refusal, and works only for a person's own session.

// mcpForgeProcedures is the table: the Connect procedure path of every
// control-plane call a hosted forge tool makes, and the one scope it needs.
var mcpForgeProcedures = map[string]Scope{
	// Every account tool resolves its project first, and lists its
	// environments.
	"/controlplane.v1.DeployService/ResolveHostedProject": ScopeDeployRead,
	"/controlplane.v1.DeployService/ListEnvironments":     ScopeDeployRead,

	// The release timeline (obs_changes, obs_status, releases_list,
	// release_show) and the release-health attribution of obs_check.
	"/controlplane.v1.DeployService/ListPromotions":   ScopeDeployRead,
	"/controlplane.v1.DeployService/ListReleases":     ScopeDeployRead,
	"/controlplane.v1.DeployService/ListConvergences": ScopeDeployRead,
	"/controlplane.v1.DeployService/ListGates":        ScopeDeployRead,
	"/controlplane.v1.DeployService/CompareReleases":  ScopeDeployRead,
	"/controlplane.v1.ActivityService/ListActivity":   ScopeObsRead,

	// release_record: the only writes.
	"/controlplane.v1.DeployService/CutRelease":        ScopeDeployWrite,
	"/controlplane.v1.DeployService/EnsureEnvironment": ScopeDeployWrite,
	"/controlplane.v1.DeployService/Promote":           ScopeDeployWrite,

	// Telemetry reads (obs_*).
	"/controlplane.v1.ObservabilityService/GetServiceOverview":        ScopeObsRead,
	"/controlplane.v1.ObservabilityService/ListWorkloads":             ScopeObsRead,
	"/controlplane.v1.ObservabilityService/SearchLogs":                ScopeObsRead,
	"/controlplane.v1.ObservabilityService/ListTraces":                ScopeObsRead,
	"/controlplane.v1.ObservabilityService/GetTrace":                  ScopeObsRead,
	"/controlplane.v1.ObservabilityService/QueryPanel":                ScopeObsRead,
	"/controlplane.v1.ObservabilityService/GetReleaseHealth":          ScopeObsRead,
	"/controlplane.v1.AlertingService/ListAlerts":                     ScopeObsRead,
	"/controlplane.v1.AlertingService/ListAlertHistory":               ScopeObsRead,
	"/controlplane.v1.IssuesService/ListIssues":                       ScopeObsRead,
	"/controlplane.v1.IssuesService/GetIssue":                         ScopeObsRead,
	"/controlplane.v1.IssuesService/ListIssueEvents":                  ScopeObsRead,
	"/controlplane.v1.IssuesService/ListIssueTimeline":                ScopeObsRead,
	"/controlplane.v1.TelemetryStatusService/ListTelemetryDeliveries": ScopeObsRead,
}

// MCPForgeScopeFor is the scope an mcp:forge token's call to procedure runs
// with (see the section above), and false for a procedure the hosted forge
// tools never call, which a control plane refuses. procedure is a Connect
// procedure path: "/controlplane.v1.DeployService/ListReleases".
func MCPForgeScopeFor(procedure string) (Scope, bool) {
	s, ok := mcpForgeProcedures[procedure]
	return s, ok
}

// MCPForgeProcedures lists the table's procedures, sorted.
func MCPForgeProcedures() []string {
	out := make([]string, 0, len(mcpForgeProcedures))
	for p := range mcpForgeProcedures {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}
