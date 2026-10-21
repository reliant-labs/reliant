// Copyright (c) 2025 Reliant Labs
package app

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

const (
	mcpPkg           = "github.com/reliant-labs/reliant/internal/mcp"
	daemonRuntimePkg = "github.com/reliant-labs/reliant/internal/toolexec/daemonruntime"
)

// The api-server must never construct an mcp.Manager: a stdio MCP server
// would spawn on the server pod instead of the user's daemon. mcp.NewManager
// is called only by daemonruntime, so neither package may be reachable from
// what the api root builds — its nodes, constructed here in internal/app, and
// the packages they come from (every server root's, so the worker's and the
// gateway's too).
func TestServerAPIDoesNotBuildMCPManager(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to go list; runs in the full lane")
	}

	direct := goList(t, "-f", `{{join .Imports "\n"}}`, ".")
	if slices.Contains(direct, mcpPkg) {
		t.Errorf("internal/app imports %s directly; no server root may own an MCP manager", mcpPkg)
	}

	deps := goList(t, "-deps", "-f", "{{.ImportPath}}", ".")
	if slices.Contains(deps, daemonRuntimePkg) {
		t.Errorf("internal/app transitively depends on %s, which constructs mcp.Manager", daemonRuntimePkg)
	}

	// And no node the api root builds is the manager, whatever it is named.
	for _, name := range PlanAPI().Names() {
		if strings.Contains(name, "mcp_manager") {
			t.Errorf("the api root builds %s", name)
		}
	}
}

func goList(t *testing.T, args ...string) []string {
	t.Helper()
	out, err := exec.Command("go", append([]string{"list"}, args...)...).Output()
	if err != nil {
		t.Fatalf("go list %v: %v", args, err)
	}
	return strings.Fields(string(out))
}
