package cli

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// uiOnlyRPCs are RPCs the Reliant web UI calls that forge deliberately has no
// CLI path for. Every entry needs a reason; "not built yet" is a gap, not a
// reason, and belongs in a tracked follow-up rather than here.
var uiOnlyRPCs = map[string]string{
	"DeployService/ListUsage": "metered-usage and spend charts on the Settings → Billing screen; billing is a UI/dashboard flow, not an environment operation an agent needs.",
}

var forgeRPCCallRe = regexp.MustCompile(`controlplane\.v1\.(DeployService|DomainService)/(\w+)`)

// forgeCalledRPCs is every DeployService/DomainService procedure named by
// non-test forge source: the set of control-plane RPCs a forge CLI path can
// reach.
func forgeCalledRPCs(t *testing.T) map[string]bool {
	t.Helper()
	root := filepath.Join("..", "..", "internal")
	called := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range forgeRPCCallRe.FindAllStringSubmatch(string(src), -1) {
			called[m[1]+"/"+m[2]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan forge source: %v", err)
	}
	return called
}

func readUIRPCSurface(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "ui_rpc_surface.txt"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// TestUIRPCSurfaceHasCLIParity is the guard for "an agent on a user's box can
// do everything the Reliant UI can do for hosted envs": every control-plane
// RPC the UI calls must be reachable from a forge CLI path, or be listed in
// uiOnlyRPCs with a reason.
func TestUIRPCSurfaceHasCLIParity(t *testing.T) {
	called := forgeCalledRPCs(t)
	var missing []string
	for _, rpc := range readUIRPCSurface(t) {
		if called[rpc] {
			if _, allowed := uiOnlyRPCs[rpc]; allowed {
				t.Errorf("%s is allowlisted as UI-only but forge calls it: drop it from uiOnlyRPCs", rpc)
			}
			continue
		}
		if _, allowed := uiOnlyRPCs[rpc]; !allowed {
			missing = append(missing, rpc)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("the Reliant UI calls these control-plane RPCs and forge has no CLI path for them:\n  %s\n\n"+
			"fix: add a forge command that calls each (procedure \"controlplane.v1.<Service>/<Rpc>\"), or — only if it is deliberately UI-only — add it to uiOnlyRPCs with a reason.\n"+
			"If the UI gained a new call, refresh the pin first: CONTROL_PLANE_DIR=<control-plane-checkout> scripts/sync-ui-rpc-surface.sh <reliant-checkout>",
			strings.Join(missing, "\n  "))
	}
	for rpc := range uiOnlyRPCs {
		found := false
		for _, s := range readUIRPCSurface(t) {
			found = found || s == rpc
		}
		if !found {
			t.Errorf("uiOnlyRPCs lists %s but the UI surface file does not: stale allowlist entry", rpc)
		}
	}
}
