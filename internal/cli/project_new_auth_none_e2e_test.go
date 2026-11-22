//go:build e2e

package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestE2EProjectNewAuthModeNone is `forge project new --auth-mode none` end to
// end, the way a user gets the project: new → author an entity and one public
// rpc → scaffold → generate → go build → install → forge lint → tsc.
//
// What it pins:
//   - project new says which auth it scaffolded;
//   - the project declares no users: no deploy/kcl/identity.k, no sign-in
//     screens, no route guard mounted, no IdP in docker-compose.yml;
//   - the API still fails closed: the born CRUD rpcs keep auth_required: true,
//     and only the rpc the proto publishes is in the generated allow-list;
//   - generate, build, lint and the frontend typecheck are green on the result.
func TestE2EProjectNewAuthModeNone(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	requireTool(t, "go", "git", "node", "npm", "golangci-lint")
	forgeBin := buildforgeBinary(t)
	contractlintDir := buildContractlintBinary(t)
	dir := t.TempDir()

	cmd := exec.Command(forgeBin, "project", "new", "openapp", "--mod", "example.com/openapp",
		"--service", "board", "--frontend", "web", "--auth-mode", "none")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("forge project new --auth-mode none: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Auth: none (--auth-mode none)") {
		t.Errorf("project new does not say which auth it scaffolded:\n%s", out)
	}
	projectDir := filepath.Join(dir, "openapp")
	addCorpusForgePkgReplace(t, projectDir)

	for _, rel := range []string{
		"deploy/kcl/identity.k",
		"frontends/web/src/app/auth",
		"frontends/web/src/components/auth-ui.tsx",
		"frontends/web/src/components/sign-in-methods.tsx",
	} {
		if _, err := os.Stat(filepath.Join(projectDir, filepath.FromSlash(rel))); err == nil {
			t.Errorf("a project with no sign-in system was handed %s", rel)
		}
	}
	if strings.Contains(readFileE2E(t, filepath.Join(projectDir, "frontends", "web", "src", "app", "providers.tsx")), "<RouteGuard") {
		t.Error("the public frontend mounts the RouteGuard")
	}

	// An entity (its CRUD rpcs are born auth_required: true) and one rpc the
	// proto publishes.
	protoPath := filepath.Join(projectDir, "proto", "services", "board", "v1", "board.proto")
	proto := readFileE2E(t, protoPath)
	const anchor = "  // TODO: Add your RPC methods here."
	if !strings.Contains(proto, anchor) {
		t.Fatalf("scaffolded service block has no %q to add rpcs at:\n%s", anchor, proto)
	}
	proto = strings.Replace(proto, anchor,
		"  rpc CountPosts(CountPostsRequest) returns (CountPostsResponse) {\n"+
			"    option (forge.v1.method) = {\n"+
			"      auth_required: false\n"+
			"      activity: {none: \"fixture: a read\"}\n"+
			"    };\n"+
			"  }", 1)
	proto += `
// forge:entity
message Post {
  string id = 1;
  string title = 2;
}

message CountPostsRequest {}
message CountPostsResponse {
  int64 count = 1;
}
`
	if err := os.WriteFile(protoPath, []byte(proto), 0o644); err != nil {
		t.Fatal(err)
	}

	runCmd(t, projectDir, forgeBin, "scaffold")
	// The born posts table declares no owner and no exemption: whose rows
	// they are is the author's to say (the crud-table-unowned lint asks).
	// A project with no users has no caller to scope by.
	writeFileE2E(t, nextMigrationPathE2E(t, projectDir, "declare_posts_scope"),
		"-- forge:backward-compatible — a table comment only; no schema change\n"+
			"COMMENT ON TABLE posts IS 'forge:unscoped: the project has no users to scope rows by';\n")
	runCmd(t, projectDir, forgeBin, "generate")
	runCmd(t, projectDir, "go", "build", "./...")

	// Fail closed: the generated allow-list publishes the one rpc the proto
	// declared public, and none of the born CRUD rpcs.
	procedures := readFileE2E(t, filepath.Join(projectDir, "pkg", "middleware", "procedures_gen.go"))
	if !strings.Contains(procedures, "/CountPosts") {
		t.Errorf("procedures_gen.go does not publish CountPosts, declared auth_required: false:\n%s", procedures)
	}
	for _, rpc := range []string{"/CreatePost", "/ListPosts", "/DeletePost"} {
		if strings.Contains(procedures, rpc) {
			t.Errorf("procedures_gen.go publishes %s, which no proto declared public:\n%s", rpc, procedures)
		}
	}
	if compose := readFileE2E(t, filepath.Join(projectDir, "docker-compose.yml")); strings.Contains(compose, "\n  idp:\n") {
		t.Error("generate put the dev IdP back into docker-compose.yml of a project with no users")
	}
	if _, err := os.Stat(filepath.Join(projectDir, "deploy", "kcl", "identity.k")); err == nil {
		t.Error("generate wrote deploy/kcl/identity.k into a project created with --auth-mode none")
	}

	webDir := filepath.Join(projectDir, "frontends", "web")
	prebuildWebRuntimeE2E(t)
	runCmdTimeout(t, webDir, 5*time.Minute, "pnpm", "install", "--no-frozen-lockfile", "--prefer-offline")
	runCmdTimeout(t, webDir, 3*time.Minute, "npx", "--no-install", "tsc", "--noEmit")

	lintOut, lintErr := runLintE2E(t, projectDir, contractlintDir, forgeBin, "lint", "--no-fix")
	if lintErr != nil {
		t.Fatalf("forge lint on a --auth-mode none project: %v\n%s", lintErr, lintOut)
	}
}
