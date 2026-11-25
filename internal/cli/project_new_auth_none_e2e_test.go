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
// end, the way a user gets the project: new → author an entity, one public
// rpc and one that declares no posture → scaffold → generate → go build →
// install → forge lint → tsc.
//
// What it pins:
//   - project new says which auth it scaffolded;
//   - the project declares no users: no deploy/kcl/identity.k, no sign-in
//     screens, no route guard mounted, no IdP in docker-compose.yml;
//   - the frontend has no client of the API's /auth/* routes: no
//     native-login.ts, no password-field component, and neither the auth
//     context nor telemetry asks /auth/session;
//   - the born CRUD rpcs are public (auth_required: false, written in the
//     proto), so the app is usable with nobody signed in;
//   - the API still fails closed: an rpc that declares no posture stays out of
//     the generated allow-list, and forge lint warns about it;
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
		"frontends/web/src/components/ui/login_form.tsx",
		"frontends/web/src/lib/auth/native-login.ts",
	} {
		if _, err := os.Stat(filepath.Join(projectDir, filepath.FromSlash(rel))); err == nil {
			t.Errorf("a project with no sign-in system was handed %s", rel)
		}
	}
	if strings.Contains(readFileE2E(t, filepath.Join(projectDir, "frontends", "web", "src", "app", "providers.tsx")), "<RouteGuard") {
		t.Error("the public frontend mounts the RouteGuard")
	}

	// An entity (its CRUD rpcs are born auth_required: false), one rpc the
	// proto publishes and one that declares no posture.
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
			"  }\n"+
			"  rpc PurgePosts(PurgePostsRequest) returns (PurgePostsResponse) {\n"+
			"    option (forge.v1.method) = {activity: {none: \"fixture: an operator purge\"}};\n"+
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

message PurgePostsRequest {}
message PurgePostsResponse {}
`
	if err := os.WriteFile(protoPath, []byte(proto), 0o644); err != nil {
		t.Fatal(err)
	}

	scaffoldOut := runCmdKeep(t, projectDir, forgeBin, "scaffold")
	if !strings.Contains(scaffoldOut, "born public (auth_required: false)") {
		t.Errorf("scaffold does not say it birthed the CRUD rpcs public:\n%s", scaffoldOut)
	}
	if born := readFileE2E(t, protoPath); strings.Count(born, "auth_required: false") != 6 {
		t.Errorf("want the five born CRUD rpcs and CountPosts to declare auth_required: false:\n%s", born)
	}
	// The born posts table declares no owner and no exemption: whose rows
	// they are is the author's to say (the crud-table-unowned lint asks).
	// A project with no users has no caller to scope by.
	writeFileE2E(t, nextMigrationPathE2E(t, projectDir, "declare_posts_scope"),
		"-- forge:backward-compatible — a table comment only; no schema change\n"+
			"COMMENT ON TABLE posts IS 'forge:unscoped: the project has no users to scope rows by';\n")
	runCmd(t, projectDir, forgeBin, "generate")
	runCmd(t, projectDir, "go", "build", "./...")

	// The allow-list follows the proto: the public rpcs (the born CRUD and
	// CountPosts) are in it, and PurgePosts, which declares no posture, fails
	// closed.
	procedures := readFileE2E(t, filepath.Join(projectDir, "pkg", "middleware", "procedures_gen.go"))
	for _, rpc := range []string{"/CountPosts", "/CreatePost", "/ListPosts", "/DeletePost"} {
		if !strings.Contains(procedures, rpc) {
			t.Errorf("procedures_gen.go does not publish %s, declared auth_required: false:\n%s", rpc, procedures)
		}
	}
	if strings.Contains(procedures, "/PurgePosts") {
		t.Errorf("procedures_gen.go publishes /PurgePosts, which no proto declared public:\n%s", procedures)
	}
	if compose := readFileE2E(t, filepath.Join(projectDir, "docker-compose.yml")); strings.Contains(compose, "\n  idp:\n") {
		t.Error("generate put the dev IdP back into docker-compose.yml of a project with no users")
	}
	if _, err := os.Stat(filepath.Join(projectDir, "deploy", "kcl", "identity.k")); err == nil {
		t.Error("generate wrote deploy/kcl/identity.k into a project created with --auth-mode none")
	}

	webDir := filepath.Join(projectDir, "frontends", "web")
	// Nothing in the frontend asks for a session the project cannot have.
	for _, rel := range []string{"src/lib/auth/context.tsx", "src/lib/otel_gen.ts"} {
		body := readFileE2E(t, filepath.Join(webDir, filepath.FromSlash(rel)))
		for _, banned := range []string{"fetchIdentity", "sessionUrl", "native-login\""} {
			if strings.Contains(body, banned) {
				t.Errorf("frontends/web/%s of a project with no sign-in still calls the session (%q)", rel, banned)
			}
		}
	}
	prebuildWebRuntimeE2E(t)
	runCmdTimeout(t, webDir, 5*time.Minute, "pnpm", "install", "--no-frozen-lockfile", "--prefer-offline")
	runCmdTimeout(t, webDir, 3*time.Minute, "npx", "--no-install", "tsc", "--noEmit")
	// The public tree's own auth suites: the context answers with no request,
	// and the session menu renders without native-login.ts.
	runCmdTimeout(t, webDir, 5*time.Minute, "npx", "--no-install", "vitest", "run",
		"src/lib/auth/context.test.tsx", "src/components/session_nav.test.tsx")

	lintOut, lintErr := runLintE2E(t, projectDir, contractlintDir, forgeBin, "lint", "--no-fix")
	if lintErr != nil {
		t.Fatalf("forge lint on a --auth-mode none project: %v\n%s", lintErr, lintOut)
	}
	// Advisory: the rpc that still requires a caller is named, the public
	// ones are not.
	if !strings.Contains(lintOut, "[forge-auth-required-no-sign-in]") || !strings.Contains(lintOut, "PurgePosts") {
		t.Errorf("forge lint does not warn that PurgePosts requires a caller nobody can be:\n%s", lintOut)
	}
	if strings.Contains(lintOut, "BoardService.ListPosts requires") {
		t.Errorf("forge lint warns about a public rpc:\n%s", lintOut)
	}
}
