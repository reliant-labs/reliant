package cli

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/templates"
)

var updateMemoryGolden = flag.Bool("update-memory-golden", false, "rewrite internal/cli/testdata/memory/*.md from the current template")

// writeTree writes files (relative path → body) under root.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// renderMemoryAs renders the memory RenderProjectMemory renders for root,
// with the CLI spelled "forge" so the golden does not depend on the test
// binary's name.
func renderMemoryAs(t *testing.T, root string) string {
	t.Helper()
	data, err := projectMemoryData(root, "shop")
	if err != nil {
		t.Fatalf("projectMemoryData: %v", err)
	}
	data.CLI = "forge"
	out, err := templates.ProjectTemplates().Render("reliant.md.tmpl", data)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return string(out)
}

func checkMemoryGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "memory", name+".md")
	if *updateMemoryGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (run with -update-memory-golden to create it): %v", path, err)
	}
	if got != string(want) {
		t.Errorf("memory for %s differs from %s; review the diff and run `go test ./internal/cli -run TestProjectMemoryGolden -update-memory-golden`\n--- got ---\n%s", name, path, got)
	}
	if strings.Contains(got, "{{") || strings.Contains(got, "<no value>") {
		t.Errorf("memory for %s carries an unrendered template action", name)
	}
}

// deployEnvs is a deploy/kcl tree with dev, staging and prod.
func deployEnvs(extra map[string]string) map[string]string {
	files := map[string]string{
		"deploy/kcl/kcl.mod":          "[package]\nname = \"shop\"\n",
		"deploy/kcl/dev/main.k":       "import forge\n",
		"deploy/kcl/staging/main.k":   "import forge\n",
		"deploy/kcl/prod/main.k":      "import forge\n",
		"deploy/kcl/dev/kcl.mod.lock": "",
	}
	for k, v := range extra {
		files[k] = v
	}
	return files
}

// TestProjectMemoryGolden pins the memory each kind of project gets, rendered
// from a tree that has that shape. The forge Go service is the guide every
// forge-built project (forge, control-plane, reliant) has always had: it
// must not move when an adopted app's memory does.
func TestProjectMemoryGolden(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		files map[string]string
	}{
		{
			// (a) A forge Go service: what `forge project new` scaffolds.
			name: "go-service",
			files: deployEnvs(map[string]string{
				"forge.yaml":                               "name: shop\nmodule_path: example.com/shop\n",
				"go.mod":                                   "module example.com/shop\n",
				"Taskfile.yml":                             "version: '3'\ntasks:\n  test: {cmds: [go test ./...]}\n  test:short: {cmds: [go test -short ./...]}\n  ci: {cmds: [task test]}\n",
				"internal/handlers/orders/service.go":      "package orders\n",
				"proto/services/orders/v1/orders.proto":    "syntax = \"proto3\";\npackage orders.v1;\nservice OrdersService {}\n",
				"db/migrations/20260101000000_init.up.sql": "CREATE TABLE orders (id text primary key);\n",
			}),
		},
		{
			// (b) An adopted Node app: only forge.yaml and deploy/kcl are forge's.
			name: "adopted-node-app",
			files: deployEnvs(map[string]string{
				"forge.yaml":             "name: shop\n",
				"package.json":           `{"name":"shop","scripts":{"start":"node server.js","test":"vitest run","lint":"eslint .","build":"tsc -p ."},"dependencies":{"express":"^5"}}`,
				"package-lock.json":      "{}\n",
				"server.js":              "require('express')().listen(process.env.PORT)\n",
				"Dockerfile":             "FROM node:22\n",
				"prisma/schema.prisma":   "datasource db { provider = \"postgresql\" }\n",
				"deploy/kcl/workloads.k": "import forge\n",
			}),
		},
		{
			// (c) A static site.
			name: "static-site",
			files: deployEnvs(map[string]string{
				"forge.yaml":     "name: shop\n",
				"package.json":   `{"name":"shop","scripts":{"build":"next build"},"dependencies":{"next":"^16"}}`,
				"next.config.ts": "export default { output: \"export\" }\n",
			}),
		},
		{
			// (d) The monitoring-only entry point: forge.yaml and nothing else.
			name: "monitoring-only",
			files: map[string]string{
				"forge.yaml": "name: shop\nenvs: [prod, staging]\nmonitoring: true\n",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeTree(t, root, tc.files)
			checkMemoryGolden(t, tc.name, renderMemoryAs(t, root))
		})
	}
}

// The Go guide read off a forge Go service's tree is byte-for-byte the guide
// `forge project new` writes before the tree exists (no derived facts): the
// facts a forge service has are the ones the guide already assumes.
func TestGoServiceMemoryEqualsTheScaffoldGuide(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTree(t, root, deployEnvs(map[string]string{
		"forge.yaml":                          "name: shop\nmodule_path: example.com/shop\n",
		"go.mod":                              "module example.com/shop\n",
		"Taskfile.yml":                        "version: '3'\ntasks:\n  test: {cmds: [go test ./...]}\n",
		"internal/handlers/orders/service.go": "package orders\n",
	}))
	derived := renderMemoryAs(t, root)
	scaffold, err := templates.ProjectTemplates().Render("reliant.md.tmpl", templates.ProjectMemoryData{Name: "shop", CLI: "forge"})
	if err != nil {
		t.Fatal(err)
	}
	if derived != string(scaffold) {
		t.Error("a forge Go service's derived memory differs from the scaffold's guide")
	}
}

// A forge service with `// forge:unmanaged` services names them, so an agent
// does not move their handlers into forge's layout.
func TestGoServiceMemoryNamesUnmanagedServices(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"forge.yaml":                            "name: shop\nmodule_path: example.com/shop\n",
		"go.mod":                                "module example.com/shop\n",
		"Taskfile.yml":                          "version: '3'\ntasks:\n  test: {cmds: [go test ./...]}\n",
		"proto/services/chat/v1/chat.proto":     "syntax = \"proto3\";\npackage chat.v1;\n\n// forge:unmanaged — handlers still in internal/grpc.\nservice ChatService {}\n",
		"proto/services/orders/v1/orders.proto": "syntax = \"proto3\";\npackage orders.v1;\nservice OrdersService {}\n",
	})
	memory := renderMemoryAs(t, root)
	if !strings.Contains(memory, "**Services forge does not manage:** `ChatService`") {
		t.Errorf("memory does not name the unmanaged service:\n%s", memory)
	}
	if strings.Contains(memory, "`OrdersService`") {
		t.Error("memory names a managed service as unmanaged")
	}
}

// A Go service with no Taskfile is not told to run `task test`: the gate is
// the project's own (here a Makefile).
func TestGoServiceMemoryWithoutTaskfileUsesTheProjectsGate(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"forge.yaml":                          "name: shop\nmodule_path: example.com/shop\n",
		"go.mod":                              "module example.com/shop\n",
		"Makefile":                            "test:\n\tgo test ./...\nlint: vet\n\tgolangci-lint run\nVERSION := 1\n",
		"internal/handlers/orders/service.go": "package orders\n",
	})
	memory := renderMemoryAs(t, root)
	for _, want := range []string{"go build ./... && make test", "`make test` (tests); `make lint` (lint)", "no Taskfile"} {
		if !strings.Contains(memory, want) {
			t.Errorf("memory lacks %q", want)
		}
	}
	for _, banned := range []string{"&& task test", "`task test` is the test suite", "task test:short"} {
		if strings.Contains(memory, banned) {
			t.Errorf("memory without a Taskfile still says %q", banned)
		}
	}
}

// An adopted app's memory never prescribes forge's greenfield gate or layout.
func TestAdoptedMemoryNeverPrescribesTheGreenfieldGate(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"adopted-node-app", "monitoring-only"} {
		body, err := os.ReadFile(filepath.Join("testdata", "memory", name+".md"))
		if err != nil {
			t.Fatal(err)
		}
		memory := string(body)
		for _, banned := range []string{
			"go build ./...", "&& task test", "`task test` is the test suite", "This is a Go project",
			"## Architecture in one screen", "Proto is the API's canonical input", "Migrations are the DB source of truth",
		} {
			if strings.Contains(memory, banned) {
				t.Errorf("%s memory contains %q", name, banned)
			}
		}
		for _, want := range []string{"adopted", "keeps its own language, layout and tooling", "Never move existing code into forge's layout", "skill load adopt", "## Project Notes"} {
			if !strings.Contains(memory, want) {
				t.Errorf("%s memory lacks %q", name, want)
			}
		}
	}
}
