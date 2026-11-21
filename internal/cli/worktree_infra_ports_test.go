package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Two actual git worktrees, fresh command context each time, same logical
// declarations and no running listeners. Exercises the up/store/render wiring
// rather than only the allocator. No stacks, compose daemon, or IdP are started.
func TestWorktreeInfraPortsThroughLaunchAndStatusRender(t *testing.T) {
	if testing.Short() {
		t.Skip("git worktrees and native KCL evaluation; runs in task test")
	}
	resetDevStackGlobals(t)
	primary, linked := portblockRepo(t)
	const source = `import kcl_plugin.forge as fp
ports = {
    idp = fp.resolve_port("project-dev-idp", 8080)
    hyperdx = fp.resolve_port("project-dev-hyperdx", 8180)
    otlp_http = fp.resolve_port("project-dev-otlp-http", 4318)
    otlp_grpc = fp.resolve_port("project-dev-otlp-grpc", 4317)
    profiles = fp.resolve_port("project-dev-otlp-profiles", 4328)
}
`
	seen := map[int]string{}
	for _, dir := range []string{primary, linked} {
		kdir := filepath.Join(dir, "deploy", "kcl", "dev")
		if err := os.MkdirAll(kdir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(kdir, "main.k"), []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
		resetResolverForTest(t)
		activateDevStack(context.Background(), dir, "dev", renderToLaunch, claimNewBlocks)
		read := func() map[string]int {
			data, err := renderKCLRaw(context.Background(), dir, "dev")
			if err != nil {
				t.Fatal(err)
			}
			var doc struct{ Ports map[string]int }
			if err := json.Unmarshal(data, &doc); err != nil {
				t.Fatal(err)
			}
			return doc.Ports
		}
		launch := read()
		for role, p := range launch {
			if prev := seen[p]; prev != "" {
				t.Errorf("%s/%s intersects %s at %d", dir, role, prev, p)
			}
			seen[p] = dir + "/" + role
		}
		resetResolverForTest(t)
		activateDevStack(context.Background(), dir, "dev", renderToLaunch, inspectBlocks)
		for role, p := range read() {
			if p != launch[role] {
				t.Errorf("status changed %s from %d to %d", role, launch[role], p)
			}
		}
	}
}
