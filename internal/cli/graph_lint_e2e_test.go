//go:build e2e

package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestE2EGraphLintAndNarrowDeps pins composition graph PR 5 on a real
// scaffold:
//
//  1. a fresh graph-mode project is clean under `forge lint --graph`;
//  2. a daemon-audience service no root reaches fails it (unreachable);
//  3. handing the user listener the daemon route set fails it (audience),
//     and generate still succeeds — the check is the lint's;
//  4. a handler taking a 12-method foreign interface and calling one method
//     is a forgeconv-deps-surface error (§9.2), and --strict on the narrowed
//     project still passes (the scaffold's root-config Deps only warn);
//  5. `forge fix narrow-deps` writes the 1-method interface and its mock,
//     the project builds, and the rule goes quiet.
func TestE2EGraphLintAndNarrowDeps(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()
	runCmd(t, dir, forgeBin, "project", "new", "glint", "--mod", "github.com/test/glint", "--service", "alpha")
	projectDir := filepath.Join(dir, "glint")
	addCorpusForgePkgReplace(t, projectDir)
	runCmd(t, projectDir, forgeBin, "generate")

	lint := func(args ...string) (string, error) {
		t.Helper()
		cmd := exec.Command(forgeBin, append([]string{"lint"}, args...)...)
		cmd.Dir = projectDir
		cmd.Env = append(os.Environ(), "FORGE_ALLOW_VERSION_SKEW=1")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := lint("--graph"); err != nil || !strings.Contains(out, "✓ graph") {
		t.Fatalf("a fresh project must be graph-clean: %v\n%s", err, out)
	}

	// ── 2. A daemon-audience service: reached by no root ────────────────
	toolsDir := filepath.Join(projectDir, "proto", "services", "tools", "v1")
	if err := os.MkdirAll(toolsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const toolsProto = `syntax = "proto3";

package services.tools.v1;

import "forge/v1/forge.proto";

option go_package = "github.com/test/glint/gen/services/tools/v1;toolsv1";

service ToolsService {
  option (forge.v1.service) = {
    name: "ToolsService"
    audience: "daemon"
  };

  rpc Ping(PingRequest) returns (PingResponse) {
    option idempotency_level = NO_SIDE_EFFECTS;
  }
}

message PingRequest {}

message PingResponse {}
`
	if err := os.WriteFile(filepath.Join(toolsDir, "tools.proto"), []byte(toolsProto), 0o644); err != nil {
		t.Fatal(err)
	}
	runCmd(t, projectDir, forgeBin, "generate")
	out, err := lint("--graph")
	if err == nil || !strings.Contains(out, "internal/handlers/tools [forge-graph-unreachable]") || !strings.Contains(out, "DaemonRoutes") {
		t.Fatalf("an unreachable daemon service must fail --graph, naming its route set: %v\n%s", err, out)
	}

	// ── 3. The user listener over the daemon routes: audience ───────────
	mustReplaceInFile(t, filepath.Join(projectDir, "internal", "app", "providers.go"),
		"\tRoutes    *UserRoutes\n", "\tRoutes    serverkit.Routes\n")
	composePath := filepath.Join(projectDir, "internal", "app", "compose.go")
	compose := readFileE2E(t, composePath)
	routesKey := regexp.MustCompile(`Routes: +g\.UserRoutes\(\),`)
	if !routesKey.MatchString(compose) {
		t.Fatalf("compose.go's newAPIListener no longer hands the listener g.UserRoutes():\n%s", compose)
	}
	if err := os.WriteFile(composePath, []byte(routesKey.ReplaceAllString(compose, "Routes: g.DaemonRoutes(),")), 0o644); err != nil {
		t.Fatal(err)
	}
	runCmd(t, projectDir, forgeBin, "generate")
	out, err = lint("--graph")
	if err == nil || !strings.Contains(out, "[forge-graph-audience]") || !strings.Contains(out, `(audience "daemon") through a chain authenticated by UserAuth (audience "user")`) {
		t.Fatalf("a daemon service behind the user chain must fail --graph: %v\n%s", err, out)
	}

	// ── 4. A wide foreign Deps interface ────────────────────────────────
	writeE2EFile(t, filepath.Join(projectDir, "internal", "widestore", "store.go"), `// Package widestore is the wide store every component used to take whole.
package widestore

import "context"

// Store is the 12-method store.
type Store interface {
	Get(ctx context.Context, id string) (string, error)
	Put(ctx context.Context, id, v string) error
	A()
	B()
	C()
	D()
	E()
	F()
	G()
	H()
	I()
	J()
}
`)
	svcPath := filepath.Join(projectDir, "internal", "handlers", "alpha", "service.go")
	svc := readFileE2E(t, svcPath)
	svc = strings.Replace(svc, "type Deps struct {\n", "type Deps struct {\n\tStore widestore.Store // forge:optional-dep\n", 1)
	svc = strings.Replace(svc, "import (\n", "import (\n\t\"github.com/test/glint/internal/widestore\"\n", 1)
	if err := os.WriteFile(svcPath, []byte(svc), 0o644); err != nil {
		t.Fatal(err)
	}
	writeE2EFile(t, filepath.Join(projectDir, "internal", "handlers", "alpha", "peek.go"), `package alpha

import "context"

// Peek reads one value.
func (s *Service) Peek(ctx context.Context, id string) (string, error) {
	return s.deps.Store.Get(ctx, id)
}
`)
	runCmd(t, projectDir, "gofmt", "-w", svcPath)
	runCmd(t, projectDir, "go", "build", "./...")
	out, err = lint("--conventions")
	if err == nil || !strings.Contains(out, "✗ internal/handlers/alpha/service.go") || !strings.Contains(out, "[forgeconv-deps-surface]") {
		t.Fatalf("a foreign interface of more than ten methods with unused ones is a deps-surface error: %v\n%s", err, out)
	}

	// ── 5. Narrow it ─────────────────────────────────────────────────────
	out = runCmdStdout(t, projectDir, forgeBin, "fix", "narrow-deps", "internal/handlers/alpha")
	if !strings.Contains(out, "Deps.Store: widestore.Store → Store (1 method(s): Get)") {
		t.Fatalf("narrow-deps output:\n%s", out)
	}
	assertPathExistsE2E(t, filepath.Join(projectDir, "internal", "handlers", "alpha", "deps.go"))
	assertPathExistsE2E(t, filepath.Join(projectDir, "internal", "handlers", "alpha", "mock_gen.go"))
	runCmd(t, projectDir, "go", "build", "./...")
	runCmd(t, projectDir, "go", "vet", "./internal/handlers/alpha/")
	if out, err = lint("--conventions", "--strict"); err != nil || strings.Contains(out, "forgeconv-deps-surface") {
		t.Fatalf("after narrow-deps the rule is quiet: %v\n%s", err, out)
	}
	// generate keeps the mock in step and the tree builds.
	runCmd(t, projectDir, forgeBin, "generate")
	runCmd(t, projectDir, "go", "build", "./...")
}

func writeE2EFile(t *testing.T, path, src string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}
