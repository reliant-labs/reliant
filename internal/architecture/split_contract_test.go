// Package architecture contains compile-free contract guards that enforce the
// microservice split boundaries. These tests use only string/grep-style checks
// against source files—no generated packages required.
package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// root returns the repository root relative to this test file.
func root(t *testing.T) string {
	t.Helper()
	// internal/architecture/ → repo root is ../..
	dir, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve root: %v", err)
	}
	return dir
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func fileExists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	return err == nil
}

// ─── API decoupling ─────────────────────────────────────────────────────────

func TestAPIHandlers_NoIntegrationImport(t *testing.T) {
	src := readFile(t, filepath.Join(root(t), "internal/api/handlers/handlers.go"))
	if strings.Contains(src, `"github.com/reliant-labs/reliant/internal/integration"`) {
		t.Error("handlers.go still imports integration package")
	}
}

func TestAPIHandlers_NoIntegrationServer(t *testing.T) {
	src := readFile(t, filepath.Join(root(t), "internal/api/handlers/handlers.go"))
	if strings.Contains(src, "*integration.Server") {
		t.Error("handlers.go still references *integration.Server")
	}
}

func TestFilePreview_UsesDBRepository(t *testing.T) {
	src := readFile(t, filepath.Join(root(t), "internal/api/handlers/file_preview.go"))
	if strings.Contains(src, `"github.com/reliant-labs/reliant/internal/integration"`) {
		t.Error("file_preview.go still imports integration package")
	}
	if !strings.Contains(src, "db.Repository") {
		t.Error("file_preview.go should reference db.Repository")
	}
}

func TestAPIServer_NarrowSignature(t *testing.T) {
	src := readFile(t, filepath.Join(root(t), "internal/api/server.go"))
	if strings.Contains(src, "integrationServer *integration.Server") {
		t.Error("server.go still accepts *integration.Server")
	}
	if !strings.Contains(src, "database db.Repository") {
		t.Error("server.go should accept db.Repository")
	}
}

// ─── server-side filesystem access ban ─────────────────────────────────────
//
// Server-side code must NOT directly access the filesystem. All FS operations
// should be routed through the daemon (gRPC calls). This test scans source
// files for banned function patterns to enforce the split boundary.
//
// Allowed daemon-side packages are excluded (see allowedDaemonPkgs below).
// Test files (*_test.go) are always skipped.

// bannedFSPatterns lists function call patterns that indicate direct filesystem
// access. Any server-side source file containing these is a violation.
var bannedFSPatterns = []string{
	// File I/O
	"os.ReadFile(",
	"os.WriteFile(",
	"os.Create(",
	"os.Open(",
	"os.OpenFile(",
	// Stat / metadata
	"os.Stat(",
	"os.Lstat(",
	// Directory operations
	"os.MkdirAll(",
	"os.Mkdir(",
	"os.Remove(",
	"os.RemoveAll(",
	"os.ReadDir(",
	// Process execution
	"exec.Command(",
	"exec.CommandContext(",
	// Filesystem walking
	"filepath.WalkDir(",
	"filepath.Walk(",
	// Git utilities (any call)
	"gitutil.",
}

// knownViolation tracks files that still have direct FS access and are pending
// migration to route through the daemon. Each entry documents why.
type knownViolation struct {
	File   string
	Reason string
}

// knownViolations lists server-side files that still violate the FS access ban.
// These are tracked as technical debt — other tasks are actively migrating them.
//
// TODO: remove entries as each service is migrated to route through daemon
var knownViolations = []knownViolation{
	// grpc/services
	{File: "internal/grpc/services/mcp.go", Reason: "TODO: route through daemon — reads/writes MCP config, stat checks"},
	{File: "internal/grpc/services/system.go", Reason: "desktop-only DevAuth endpoints — guarded by localMode flag, unavailable in cloud mode"},
	// skills
	{File: "internal/skills/catalog/runtime.go", Reason: "TODO: route through daemon — reads skill files, walks directories"},
	{File: "internal/skills/materialize/runtime.go", Reason: "TODO: route through daemon — walks skill dirs, reads files"},
	// config
	{File: "internal/config/project_meta.go", Reason: "daemon-side only — called exclusively from tools (metadata_writer.go) which execute on daemon"},
}

func isKnownViolation(relPath string) string {
	for _, v := range knownViolations {
		if v.File == relPath {
			return v.Reason
		}
	}
	return ""
}

// serverSidePackages lists packages whose non-test .go files must not contain
// direct filesystem access.
var serverSidePackages = []string{
	"internal/grpc/services",
	"internal/workflow/runtime/activities",
	"internal/workflow/runtime/activities/handlers",
	"internal/skills/catalog",
	"internal/skills/materialize",
	"internal/config",
}

func TestServerSide_NoDirectFilesystemAccess(t *testing.T) {
	repoRoot := root(t)

	for _, pkg := range serverSidePackages {
		pkg := pkg
		t.Run(pkg, func(t *testing.T) {
			pkgDir := filepath.Join(repoRoot, pkg)
			entries, err := os.ReadDir(pkgDir)
			if err != nil {
				t.Fatalf("read dir %s: %v", pkgDir, err)
			}

			for _, entry := range entries {
				name := entry.Name()

				// Skip non-Go files, test files, and directories
				if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
					continue
				}

				relPath := filepath.Join(pkg, name)
				filePath := filepath.Join(repoRoot, relPath)
				src := readFile(t, filePath)

				for _, banned := range bannedFSPatterns {
					if !strings.Contains(src, banned) {
						continue
					}

					// Check if this is a known violation (pending migration)
					if reason := isKnownViolation(relPath); reason != "" {
						t.Logf("KNOWN VIOLATION: %s contains %s — %s", relPath, banned, reason)
						continue
					}

					t.Errorf("%s contains banned filesystem call: %s — must route through daemon", relPath, banned)
				}
			}
		})
	}
}

// TestCallLLM_NoDirectFilesystem is a focused regression test for the most
// critical server-side file (call_llm.go handles every LLM request).
func TestCallLLM_NoDirectFilesystem(t *testing.T) {
	src := readFile(t, filepath.Join(root(t), "internal/workflow/runtime/activities/handlers/call_llm.go"))
	for _, banned := range bannedFSPatterns {
		if strings.Contains(src, banned) {
			t.Errorf("call_llm.go still contains banned call: %s", banned)
		}
	}
}

// ─── filesystem abstraction ────────────────────────────────────────────────

func TestFilesystemService_UsesLocalFS(t *testing.T) {
	src := readFile(t, filepath.Join(root(t), "internal/grpc/services/filesystem.go"))
	// Should NOT contain bare os.ReadFile etc.
	for _, banned := range []string{"os.ReadFile(", "os.WriteFile(", "os.ReadDir(", "os.Stat(", "os.Open(", "filepath.Walk("} {
		if strings.Contains(src, banned) {
			t.Errorf("filesystem.go still contains bare call: %s", banned)
		}
	}
	// Should reference localfs
	if !strings.Contains(src, "localfs.FS") || !strings.Contains(src, "localfs.New()") {
		t.Error("filesystem.go should use localfs.FS / localfs.New()")
	}
}

// ─── split entrypoints ────────────────────────────────────────────────────

func TestSplitEntrypoints_Exist(t *testing.T) {
	for _, entry := range []string{
		"cmd/reliant/main.go",
	} {
		if !fileExists(t, filepath.Join(root(t), entry)) {
			t.Errorf("missing entrypoint: %s", entry)
		}
	}
}

// ─── Dockerfiles ───────────────────────────────────────────────────────────

func TestDockerfiles_Exist(t *testing.T) {
	if !fileExists(t, filepath.Join(root(t), "Dockerfile")) {
		t.Error("missing Dockerfile")
	}
}

// ─── docker-compose ────────────────────────────────────────────────────────

func TestDockerCompose_Exists(t *testing.T) {
	if !fileExists(t, filepath.Join(root(t), "docker-compose.yml")) {
		t.Error("missing docker-compose.yml")
	}
}

// ─── KCL overlays ──────────────────────────────────────────────────────────

func TestKCLOverlays_Exist(t *testing.T) {
	for _, f := range []string{
		"deploy/kcl/schema.k",
		"deploy/kcl/dev/main.k",
		"deploy/kcl/staging/main.k",
		"deploy/kcl/prod/main.k",
	} {
		if !fileExists(t, filepath.Join(root(t), f)) {
			t.Errorf("missing KCL file: %s", f)
		}
	}
}
