package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// graph_test.go — exercises buildGraphDoc end-to-end against a fixture
// project laid out in t.TempDir. We point RenderKCL at a stub via
// FORGE_KCL_RENDER_FIXTURE (the same env-var hook deploy/audit tests
// use) so the suite stays hermetic — no real `kcl` binary required.
//
// What we deliberately do NOT assert:
//   - exact JSON byte equality. The graph is additive — new edge kinds
//     or new top-level fields shouldn't break the test. We assert each
//     section is populated and each expected edge is present.
//   - per-warning string formatting. Wording is allowed to drift; we
//     assert presence/count instead.

// writeGraphProjectYAML writes a minimal forge.yaml the graph command
// can consume, plus the frontend ("web") it derives from disk, and the
// schema-required empty sections. The service inventory comes from the proto descriptor and the
// internal-package inventory from internal/<pkg>/contract.go, so neither is
// declared here. The caller may add more bodies on top via os.WriteFile.
func writeGraphProjectYAML(t *testing.T, dir string) {
	t.Helper()
	body := `name: demo
module_path: github.com/demo/demo
version: 0.0.1
forge_version: dev
database: {}
ci: {}
docker: {}
k8s: {}
lint: {}
contracts: {}
auth: {}
`
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write forge.yaml: %v", err)
	}
	// The frontend inventory is derived: a directory under frontends/ with its
	// framework's config file.
	if err := os.MkdirAll(filepath.Join(dir, "frontends", "web"), 0o755); err != nil {
		t.Fatalf("mkdir frontends/web: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "frontends", "web", "next.config.ts"), []byte("export default {}\n"), 0o644); err != nil {
		t.Fatalf("write next.config.ts: %v", err)
	}
	// The service inventory is enumerated from the proto descriptor
	// (codegen.IntrospectComponents). "TasksService"
	// maps to component "tasks" with Path internal/handlers/tasks — the dir
	// writeTasksContract populates with the Repo dep.
	writeForgeDescriptor(t, dir, "TasksService")
}

// writeRepoAdapterPackage lays down internal/repo/contract.go carrying the
// `//forge:outbound-io` marker. That file IS the package declaration the
// graph discovers, and the marker IS the claim it reports — there is no
// forge.yaml entry to disagree with either.
func writeRepoAdapterPackage(t *testing.T, dir string) {
	t.Helper()
	pkgDir := filepath.Join(dir, "internal", "repo")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("mkdir internal/repo: %v", err)
	}
	src := `// forge:outbound-io
package repo

type Service interface{}

type Deps struct{}

func New(d Deps) Service { return nil }
`
	if err := os.WriteFile(filepath.Join(pkgDir, "contract.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write internal/repo/contract.go: %v", err)
	}
}

// writeTasksContract writes a contract.go for the tasks service whose
// Deps struct references the "repo" package. ParseServiceDeps will
// recover the field; resolveDepsPackage matches the selector prefix
// against the forge.yaml-declared package.
func writeTasksContract(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "internal", "handlers", "tasks"), 0o755); err != nil {
		t.Fatalf("mkdir handlers/tasks: %v", err)
	}
	src := `package tasks

import "log/slog"

type Service interface{}

type Deps struct {
	Logger *slog.Logger
	Repo   repo.Storer
}

func New(d Deps) Service { return nil }
`
	if err := os.WriteFile(filepath.Join(dir, "internal", "handlers", "tasks", "service.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write contract: %v", err)
	}
}

// TestGraph_EmitsAllResourceTypes is the headline contract: a fully
// populated project produces a graphDoc with every section non-empty
// AND the explicit deps + routes-to edges present in the edges array.
func TestGraph_EmitsAllResourceTypes(t *testing.T) {
	dir := t.TempDir()
	writeGraphProjectYAML(t, dir)
	writeTasksContract(t, dir)
	writeRepoAdapterPackage(t, dir)

	// Stub the KCL render: one service ("tasks", cluster deploy with
	// one env var), one frontend ("web", port overridden to 3001 so
	// we can see KCL win), one gateway ("web-gw"), one HTTPRoute
	// ("api-route") attached to web-gw and routing to tasks.
	fixture := `{
  "output": {
    "frontends": [
      {
        "name": "web",
        "type": "nextjs",
        "path": "frontends/web",
        "port": 3001
      }
    ],
    "gateways": [
      {
        "name": "web-gw",
        "host": "demo.test",
        "listeners": [
          {
            "name": "https",
            "port": 443,
            "protocol": "HTTPS"
          }
        ]
      }
    ],
    "http_routes": [
      {
        "name": "api-route",
        "gateway": "web-gw",
        "listener": "https",
        "service": "tasks",
        "port": 8080,
        "path": "/api"
      }
    ],
    "workloads": [
      {
        "name": "tasks",
        "kind": "service",
        "image": "r/tasks",
        "runtime": {
          "type": "cluster",
          "cluster": "c",
          "namespace": "n"
        },
        "spec": {
          "kind": "service",
          "env": [
            {
              "name": "DATABASE_URL",
              "value": "postgres://"
            }
          ]
        }
      }
    ]
  }
}`
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, fixture))

	doc := buildGraphDoc(context.Background(), dir, "dev")

	// Project metadata round-trips from forge.yaml.
	if doc.Project.Name != "demo" {
		t.Errorf("project.name = %q, want demo", doc.Project.Name)
	}
	if doc.Project.ModulePath != "github.com/demo/demo" {
		t.Errorf("project.module_path = %q, want github.com/demo/demo", doc.Project.ModulePath)
	}

	// One of each declared type.
	if len(doc.Services) != 1 {
		t.Fatalf("services len = %d, want 1; doc=%+v", len(doc.Services), doc)
	}
	if len(doc.Frontends) != 1 {
		t.Fatalf("frontends len = %d, want 1", len(doc.Frontends))
	}
	if len(doc.Packages) != 1 {
		t.Fatalf("packages len = %d, want 1", len(doc.Packages))
	}
	// The type is read off the package's own `//forge:outbound-io` marker.
	if doc.Packages[0].Name != "repo" || doc.Packages[0].Type != "outbound-io" {
		t.Errorf("packages[0] = %+v, want {repo outbound-io}", doc.Packages[0])
	}
	// No composition marker: the server composes it.
	if doc.Packages[0].Composition != "composed" {
		t.Errorf("packages[0].composition = %q, want composed", doc.Packages[0].Composition)
	}
	if len(doc.Gateways) != 1 {
		t.Fatalf("gateways len = %d, want 1", len(doc.Gateways))
	}
	if len(doc.Routes) != 1 {
		t.Fatalf("routes len = %d, want 1", len(doc.Routes))
	}

	// Service should pick up KCL's deploy_type + env_vars.
	svc := doc.Services[0]
	if svc.DeployType != "cluster" {
		t.Errorf("svc.DeployType = %q, want cluster", svc.DeployType)
	}
	if len(svc.EnvVars) != 1 || svc.EnvVars[0].Name != "DATABASE_URL" {
		t.Errorf("svc.EnvVars = %+v, want [{DATABASE_URL kcl}]", svc.EnvVars)
	}

	// Deps was parsed and the Repo field was resolved to the repo
	// package via selector-prefix matching.
	var sawRepoDep bool
	for _, d := range svc.Deps {
		if d.Field == "Repo" && d.Package == "repo" {
			sawRepoDep = true
		}
	}
	if !sawRepoDep {
		t.Errorf("svc.Deps did not contain Repo→repo edge; deps=%+v", svc.Deps)
	}

	// Frontend port: KCL overrides forge.yaml when KCL provides one.
	if doc.Frontends[0].Port != 3001 {
		t.Errorf("frontend port = %d, want 3001 (KCL override)", doc.Frontends[0].Port)
	}

	// Route → service edge present.
	var sawRouteEdge, sawDepsEdge, sawAttachedEdge bool
	for _, e := range doc.Edges {
		switch {
		case e.From == "route:api-route" && e.To == "service:tasks" && e.Kind == "routes-to":
			sawRouteEdge = true
		case e.From == "service:tasks" && e.To == "package:repo" && e.Kind == "deps":
			sawDepsEdge = true
		case e.From == "route:api-route" && e.To == "gateway:web-gw" && e.Kind == "attached-to":
			sawAttachedEdge = true
		}
	}
	if !sawRouteEdge {
		t.Errorf("missing edge route:api-route → service:tasks (routes-to); edges=%+v", doc.Edges)
	}
	if !sawDepsEdge {
		t.Errorf("missing edge service:tasks → package:repo (deps); edges=%+v", doc.Edges)
	}
	if !sawAttachedEdge {
		t.Errorf("missing edge route:api-route → gateway:web-gw (attached-to); edges=%+v", doc.Edges)
	}
}

// TestGraph_PartialDataEmitsWarnings asserts the tolerance contract:
// an unreadable KCL render produces a warning, not a panic, and the
// command still emits whatever else is parseable (forge.yaml in this
// case).
func TestGraph_PartialDataEmitsWarnings(t *testing.T) {
	dir := t.TempDir()
	writeGraphProjectYAML(t, dir)

	// Invalid JSON in the KCL fixture path — RenderKCL surfaces a
	// "parse kcl json" error which we should fold into warnings.
	fixturePath := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(fixturePath, []byte("{not-valid-json"), 0o644); err != nil {
		t.Fatalf("write bad fixture: %v", err)
	}
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", fixturePath)

	doc := buildGraphDoc(context.Background(), dir, "dev")

	// forge.yaml still loaded.
	if doc.Project.Name != "demo" {
		t.Errorf("project.name = %q, want demo (forge.yaml should still parse)", doc.Project.Name)
	}
	// At least one warning, and it should name KCL.
	if len(doc.Warnings) == 0 {
		t.Fatalf("expected at least one warning; got none")
	}
	var sawKCLWarning bool
	for _, w := range doc.Warnings {
		if strings.Contains(w, "kcl") {
			sawKCLWarning = true
		}
	}
	if !sawKCLWarning {
		t.Errorf("warnings did not mention kcl; warnings=%v", doc.Warnings)
	}
}

// TestGraph_OutputIsValidJSON confirms the encoded document round-
// trips through encoding/json. A nil-pointer panic during marshal
// would fail this; so would a non-marshallable field type sneaking
// in via a future graphDoc addition.
func TestGraph_OutputIsValidJSON(t *testing.T) {
	dir := t.TempDir()
	writeGraphProjectYAML(t, dir)

	doc := buildGraphDoc(context.Background(), dir, "dev")
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var roundTrip map[string]any
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatalf("unmarshal: %v\npayload=%s", err, string(data))
	}
	// "project" and "edges" are always emitted — the test pins those
	// top-level keys to catch a future accidental `omitempty` that
	// would break consumers asserting the shape with jq.
	if _, ok := roundTrip["project"]; !ok {
		t.Errorf("missing top-level project key; payload=%s", string(data))
	}
	if _, ok := roundTrip["edges"]; !ok {
		t.Errorf("missing top-level edges key (must be present even when empty); payload=%s", string(data))
	}
}

// TestGraph_EmptyProjectDoesNotCrash covers the CLI-kind / no-services
// path: no forge.yaml, no KCL fixture — the function should return a
// valid document with warnings, not panic.
func TestGraph_EmptyProjectDoesNotCrash(t *testing.T) {
	dir := t.TempDir()
	// Deliberately no forge.yaml, no KCL dir, no FORGE_KCL_RENDER_FIXTURE.
	// Override the fixture env var explicitly to "" so the test
	// environment's value (if any) doesn't leak in.
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", "")

	doc := buildGraphDoc(context.Background(), dir, "dev")

	// Edges must still be present (encoded as []) so downstream
	// jq queries don't have to null-guard.
	if doc.Edges == nil {
		t.Errorf("edges should be empty slice, not nil")
	}
	// Two warnings expected: forge.yaml missing + KCL render fails.
	if len(doc.Warnings) < 1 {
		t.Errorf("expected warnings on empty project; got %v", doc.Warnings)
	}
}
