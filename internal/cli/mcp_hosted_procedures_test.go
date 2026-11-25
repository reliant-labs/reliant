package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/reliant-labs/forge/pkg/accesstoken"
)

// procedureRecorder is a control plane that answers just enough for every
// hosted tool to run its whole path, and records each procedure it is called
// on.
type procedureRecorder struct {
	mu      sync.Mutex
	calls   map[string]bool
	missing bool // ResolveHostedProject{name} answers NotFound
}

func (f *procedureRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	f.calls[r.URL.Path] = true
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/controlplane.v1.DeployService/ResolveHostedProject":
		if f.missing && body["create"] != true {
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"code":"not_found","message":"no such project"}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"projectId":"p-1","orgId":"org-1"}`)
	case "/controlplane.v1.DeployService/ListEnvironments":
		_, _ = fmt.Fprint(w, `{"environments":[{"id":"env-1","name":"prod","project":"shop"}]}`)
	case "/controlplane.v1.DeployService/ListPromotions":
		_, _ = fmt.Fprint(w, `{"promotions":[{"id":"pr-2","releaseId":"r-2","version":"v2","environmentId":"env-1","status":"DEPLOY_PROMOTION_STATUS_SUCCEEDED","createdAt":"2026-01-02T00:00:00Z","completedAt":"2026-01-02T00:01:00Z"},{"id":"pr-1","releaseId":"r-1","version":"v1","environmentId":"env-1","status":"DEPLOY_PROMOTION_STATUS_SUCCEEDED","createdAt":"2026-01-01T00:00:00Z","completedAt":"2026-01-01T00:01:00Z"}]}`)
	case "/controlplane.v1.DeployService/ListReleases":
		_, _ = fmt.Fprint(w, `{"releases":[{"id":"r-2","version":"v2","createdAt":"2026-01-02T00:00:00Z"},{"id":"r-1","version":"v1","createdAt":"2026-01-01T00:00:00Z"}]}`)
	default:
		_, _ = fmt.Fprint(w, `{}`)
	}
}

// notServedOnMCPForge are the procedures a hosted tool calls that an MCP
// client's mcp:forge token is deliberately refused (accesstoken's mcp_forge.go):
// obs_init mints telemetry ingest tokens, which only a person does.
var notServedOnMCPForge = map[string]string{
	"/controlplane.v1.TelemetryTokenService/ListIngestTokens":  "obs_init",
	"/controlplane.v1.TelemetryTokenService/CreateIngestToken": "obs_init",
}

// TestHostedToolsCallOnlyMCPForgeProcedures pins accesstoken's mcp:forge
// procedure table to what the hosted tools really call. A control plane admits
// an mcp:forge token only on the table, so a tool that reaches a procedure the
// table does not name breaks for every MCP client on that token: add the
// procedure to the table (with the narrowest scope it needs) in the same
// change, or to notServedOnMCPForge with a reason.
func TestHostedToolsCallOnlyMCPForgeProcedures(t *testing.T) {
	target := func(extra map[string]any) map[string]any {
		m := map[string]any{"project": "shop", "env": "prod"}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	digest := "r/a@sha256:" + strings.Repeat("a", 64)
	cases := []struct {
		tool    string
		args    map[string]any
		missing bool
	}{
		{"obs_status", target(nil), false},
		{"obs_issues", target(map[string]any{"since_release": "v1"}), false},
		{"obs_issue", target(map[string]any{"id": "ISS-1", "events": 3}), false},
		{"obs_logs", target(nil), false},
		{"obs_logs", target(map[string]any{"trace": "0123456789abcdef0123456789abcdef"}), false},
		{"obs_traces", target(map[string]any{"errors": true, "slow": true}), false},
		{"obs_ops", target(nil), false},
		{"obs_check", target(nil), false},
		{"obs_check", target(map[string]any{"release": "v1"}), false},
		{"obs_check", target(map[string]any{"promotion": "pr-1"}), false},
		{"obs_changes", target(nil), false},
		{"releases_list", target(nil), false},
		{"release_show", target(map[string]any{"ref": "v2"}), false},
		{"release_show", target(map[string]any{"ref": "pr-1"}), false},
		{"envs_list", map[string]any{"project": "shop"}, false},
		{"obs_init", map[string]any{"project": "shop", "envs": []string{"prod", "staging"}}, false},
		{"obs_init", map[string]any{"project": "newp", "envs": []string{"prod"}}, true},
		{"release_record", target(map[string]any{"version": "v3"}), false},
		{"release_record", target(map[string]any{"version": "v3", "commit": "abcdef1",
			"workloads": []map[string]any{{"workload": "api", "image": digest}}, "run_url": "https://ci/x"}), false},
		{"benefits", map[string]any{}, false},
		{"why", map[string]any{}, false},
		{"skill_list", map[string]any{}, false},
		{"skill_search", map[string]any{"query": "db"}, false},
		{"skill_load", map[string]any{"name": "forge"}, false},
		{"explain_error", map[string]any{"error": "x"}, false},
	}
	covered := map[string]bool{}
	reached := map[string]bool{}
	for _, tc := range cases {
		covered[tc.tool] = true
		cp := &procedureRecorder{calls: map[string]bool{}, missing: tc.missing}
		ts := httptest.NewServer(cp)
		cs := connectHosted(t, AccountToolsOptions{ControlPlaneURL: ts.URL, Token: staticToken("tok"),
			Authorize:     func(context.Context, AccountToolInfo, AccountTarget) error { return nil },
			NewProjectOrg: func(context.Context, *mcp.CallToolRequest) (string, error) { return "org-1", nil }})
		if _, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tc.tool, Arguments: tc.args}); err != nil {
			t.Fatalf("%s: %v", tc.tool, err)
		}
		ts.Close()
		for p := range cp.calls {
			reached[p] = true
			if _, ok := accesstoken.MCPForgeScopeFor(p); ok {
				continue
			}
			if notServedOnMCPForge[p] == tc.tool {
				continue
			}
			t.Errorf("%s calls %s, which accesstoken's mcp:forge table does not name", tc.tool, p)
		}
	}
	for _, info := range HostedTools() {
		if !covered[info.Name] {
			t.Errorf("hosted tool %s is not exercised here: add a case", info.Name)
		}
	}
	// The always-reached core of the table, so a recorder that silently stops
	// recording cannot pass this test.
	for _, p := range []string{
		"/controlplane.v1.DeployService/ResolveHostedProject",
		"/controlplane.v1.DeployService/ListEnvironments",
		"/controlplane.v1.DeployService/CutRelease",
		"/controlplane.v1.ObservabilityService/SearchLogs",
	} {
		if !reached[p] {
			t.Errorf("no hosted tool reached %s", p)
		}
	}
	var unreached []string
	for _, p := range accesstoken.MCPForgeProcedures() {
		if !reached[p] {
			unreached = append(unreached, p)
		}
	}
	slices.Sort(unreached)
	t.Logf("table procedures reached only on data-dependent branches the recorder does not drive: %v", unreached)
}
