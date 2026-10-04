package serverapi

import (
	"os/exec"
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
// serverapi's build graph.
func TestServerAPIDoesNotBuildMCPManager(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to go list")
	}

	direct := goList(t, "-f", `{{join .Imports "\n"}}`, ".")
	if contains(direct, mcpPkg) {
		t.Errorf("internal/serverapi imports %s directly; the api-server must not own an MCP manager", mcpPkg)
	}

	deps := goList(t, "-deps", "-f", "{{.ImportPath}}", ".")
	if contains(deps, daemonRuntimePkg) {
		t.Errorf("internal/serverapi transitively depends on %s, which constructs mcp.Manager", daemonRuntimePkg)
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

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
