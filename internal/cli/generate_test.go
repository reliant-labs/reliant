package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator"
)

// addforgeReplaceMain points the project's go.mod at the in-repo forge with a
// `require`+`replace` pair, so `go mod tidy` resolves pkg/* (auth, crud, orm,
// ...) from this checkout. Used in tests where the generated project
// references a forge/pkg subpackage not present in the latest published forge.
//
// The target is the repo ROOT, not <repo>/pkg: pkg/* are packages in the one
// forge module. A replace at <repo>/pkg names a directory with no go.mod,
// which the go command rejects with "no such file or directory".
func addforgeReplaceMain(t *testing.T, projectDir string) {
	t.Helper()
	repoRoot := findForgeRepoRoot(t)
	goModPath := filepath.Join(projectDir, "go.mod")
	data, err := os.ReadFile(goModPath)
	if err != nil {
		t.Fatalf("read project go.mod: %v", err)
	}
	content := string(data)
	if strings.Contains(content, "replace github.com/reliant-labs/forge ") {
		return
	}
	// A directory replace still needs a require to hang off: the scaffold
	// omits one whenever the generating binary has no proxy-resolvable
	// version, which is always true under `go test`.
	if !strings.Contains(content, "require github.com/reliant-labs/forge ") {
		content += "\nrequire github.com/reliant-labs/forge v0.0.0\n"
	}
	// And the RETIRED submodule must be excluded, not merely unrequired.
	// `go mod tidy` resolves a forge/pkg/* import by picking any module that
	// can provide it, and the published forge/pkg still can — leaving a graph
	// with both, where every forge/pkg/* import is "ambiguous import: found
	// package ... in multiple modules". An exclude keeps tidy off it so the
	// local forge is the only provider.
	content += "\nexclude github.com/reliant-labs/forge/pkg v0.1.15\n"
	content += fmt.Sprintf("\nreplace github.com/reliant-labs/forge => %s\n", repoRoot)
	if err := os.WriteFile(goModPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write project go.mod: %v", err)
	}
}

// findForgeRepoRoot walks up from cwd to find the forge repo root (looks for
// go.mod with module github.com/reliant-labs/forge).
func findForgeRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 8; i++ {
		modPath := filepath.Join(dir, "go.mod")
		if data, err := os.ReadFile(modPath); err == nil {
			if strings.Contains(string(data), "module github.com/reliant-labs/forge\n") {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("could not find forge repo root from %s", dir)
	return ""
}

func TestDirExists(t *testing.T) {
	dir := t.TempDir()

	if !dirExists(dir) {
		t.Errorf("dirExists(%q) = false, want true", dir)
	}

	if dirExists(filepath.Join(dir, "nonexistent")) {
		t.Error("dirExists for nonexistent path returned true")
	}

	f := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(f, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if dirExists(f) {
		t.Error("dirExists for a regular file returned true")
	}
}

// TestServiceDirResolution covers the service-name → handlers/<dir>
// mapping generateServiceStubs now derives via the disk-first resolver:
// the synthesized fallback for brand-new services (the snake_case
// canonical form from naming.ServicePackage — the exhaustive
// table-driven cases live at internal/naming/naming_test.go), and the
// on-disk directory winning whenever one already exists under any
// historical naming variant.
func TestServiceDirResolution(t *testing.T) {
	// Fallback synthesis (no dir on disk yet).
	tests := []struct {
		input string
		want  string
	}{
		{"EchoService", "echo"},
		{"UserService", "user"},
		{"NotificationService", "notification"},
		{"Foo", "foo"},
		// Multi-word names canonicalise to snake_case, matching
		// naming.ServicePackage (post-2026-06-08 scaffold form).
		{"AdminServerService", "admin_server"},
	}
	for _, tt := range tests {
		res, err := codegen.ResolveServiceComponent(t.TempDir(), tt.input)
		if err != nil {
			t.Fatalf("ResolveServiceComponent(%q): %v", tt.input, err)
		}
		if res.FromDisk || res.ImportLeaf != tt.want {
			t.Errorf("ResolveServiceComponent(%q) = %+v, want synthesized leaf %q", tt.input, res, tt.want)
		}
	}

	// Disk-first: an existing snake_case dir wins over synthesis.
	projectDir := t.TempDir()
	dir := filepath.Join(projectDir, "internal", "handlers", "admin_server")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "service.go"), []byte("package admin_server\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := codegen.ResolveServiceComponent(projectDir, "AdminServerService")
	if err != nil {
		t.Fatalf("ResolveServiceComponent: %v", err)
	}
	if !res.FromDisk || res.ImportLeaf != "admin_server" || res.PackageName != "admin_server" {
		t.Errorf("ResolveServiceComponent disk-first = %+v, want admin_server/admin_server", res)
	}
}

func TestIsPluginAvailable(t *testing.T) {
	if !isPluginAvailable("go") {
		t.Error("isPluginAvailable(\"go\") = false, expected true")
	}

	if isPluginAvailable("definitely-not-a-real-binary-xyz-123") {
		t.Error("isPluginAvailable returned true for non-existent binary")
	}
}

func TestWriteDefaultBufGenYaml(t *testing.T) {
	dir := t.TempDir()

	if err := writeDefaultBufGenYaml(dir); err != nil {
		t.Fatalf("writeDefaultBufGenYaml() error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "buf.gen.yaml"))
	if err != nil {
		t.Fatalf("failed to read buf.gen.yaml: %v", err)
	}

	content := string(data)
	if len(content) == 0 {
		t.Fatal("buf.gen.yaml is empty")
	}

	// Default scaffold uses local plugins (no BSR auth required).
	// Past regression: defaulted to remote: plugins which forced
	// anonymous users to `buf registry login` before `forge generate`
	// would work reliably (rate-limit hits).
	for _, want := range []string{
		"local: protoc-gen-go",
		"local: protoc-gen-connect-go",
		"paths=source_relative",
	} {
		if !contains(content, want) {
			t.Errorf("buf.gen.yaml missing %q", want)
		}
	}
	// Negative assertion: must NOT contain remote: plugins as ACTIVE
	// list entries. We check by stripping comments and confirming no
	// non-comment line starts with `  - remote:`.
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "- remote:") {
			t.Errorf("buf.gen.yaml unexpectedly contains active BSR remote plugin entry %q (default should be local:)", line)
		}
	}
}

func TestRunBufGenerateTypeScriptWritesWorkspaceRelativeConfig(t *testing.T) {
	requirePOSIXFake(t, "buf")
	dir := t.TempDir()

	feRelDir := filepath.Join("frontends", "web")
	absFeDir := filepath.Join(dir, feRelDir)
	if err := os.MkdirAll(absFeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Create proto/services with a .proto file so the discoverProtoSubdirs
	// helper picks it up. Empty dirs are now filtered out — pack-emitted
	// proto trees are detected via .proto presence, so the canonical dirs
	// follow the same rule for symmetry.
	if err := os.MkdirAll(filepath.Join(dir, "proto", "services"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "proto", "services", "x.proto"), []byte("syntax=\"proto3\";\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Create proto/api with a .proto file so both --path flags are added.
	if err := os.MkdirAll(filepath.Join(dir, "proto", "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "proto", "api", "y.proto"), []byte("syntax=\"proto3\";\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stubPath := filepath.Join(dir, "buf")
	stubScript := "#!/bin/sh\npwd > buf.cwd\nprintf '%s' \"$*\" > buf.args\nexit 0\n"
	if err := os.WriteFile(stubPath, []byte(stubScript), 0o755); err != nil {
		t.Fatal(err)
	}
	// runBufGenerateTypeScript pre-flights the local TS plugin binary now
	// (since the BSR-removal switch). Drop a no-op stub at the path the
	// generated buf.gen.yaml references so the pre-flight passes and the
	// function proceeds to invoke `buf`.
	pluginBinDir := filepath.Join(absFeDir, "node_modules", ".bin")
	if err := os.MkdirAll(pluginBinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginBinDir, "protoc-gen-es"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	pathEnv := os.Getenv("PATH")
	if err := os.Setenv("PATH", dir+string(os.PathListSeparator)+pathEnv); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Setenv("PATH", pathEnv) }()

	cfg := &config.ProjectConfig{ModulePath: "example.com/project"}
	fe := config.FrontendConfig{Name: "web", Type: "nextjs"}.WithDir(feRelDir)
	if err := runBufGenerateTypeScript(fe, cfg, dir); err != nil {
		t.Fatalf("runBufGenerateTypeScript() error = %v", err)
	}

	// buf.gen.yaml should use project-root-relative out: path and no inputs: directive
	content := readFileForTest(t, filepath.Join(absFeDir, "buf.gen.yaml"))
	if !strings.Contains(content, "out: frontends/web/src/gen") {
		t.Fatalf("expected TypeScript buf config out: to be project-root-relative, got:\n%s", content)
	}
	if strings.Contains(content, "inputs:") {
		t.Fatalf("expected TypeScript buf config not to use inputs: directive, got:\n%s", content)
	}
	// Default scaffold uses the local protoc-gen-es plugin (no BSR auth).
	// Past regression: defaulted to `remote: buf.build/bufbuild/es` which
	// forced anonymous users to `buf registry login` to escape rate limits.
	if !strings.Contains(content, "local: ./frontends/web/node_modules/.bin/protoc-gen-es") {
		t.Fatalf("expected TypeScript buf config to use local: plugin, got:\n%s", content)
	}
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "- remote:") {
			t.Fatalf("frontend buf.gen.yaml unexpectedly contains active BSR remote plugin entry %q (default should be local:)", line)
		}
	}

	// buf should run from project root, not frontend dir
	invocationDir := strings.TrimSpace(readFileForTest(t, filepath.Join(dir, "buf.cwd")))
	expectedInvocationDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks(expected project dir) error = %v", err)
	}
	actualInvocationDir, err := filepath.EvalSymlinks(invocationDir)
	if err != nil {
		t.Fatalf("EvalSymlinks(actual invocation dir) error = %v", err)
	}
	if actualInvocationDir != expectedInvocationDir {
		t.Fatalf("expected buf generate to run from project root %q, got %q", expectedInvocationDir, actualInvocationDir)
	}

	// Check command uses --template with relative path and --path flags
	invocationArgs := readFileForTest(t, filepath.Join(dir, "buf.args"))
	expectedTemplate := filepath.Join("frontends", "web", "buf.gen.yaml")
	if !strings.Contains(invocationArgs, "--template "+expectedTemplate) {
		t.Fatalf("expected buf generate to use --template %s, got %q", expectedTemplate, invocationArgs)
	}
	if !strings.Contains(invocationArgs, "--path proto/services") {
		t.Fatalf("expected buf generate to use --path proto/services, got %q", invocationArgs)
	}
	if !strings.Contains(invocationArgs, "--path proto/api") {
		t.Fatalf("expected buf generate to use --path proto/api, got %q", invocationArgs)
	}
}

// The frontend inventory is derived from frontends/<name> on disk, typed by
// the framework's own config file — there is no yaml `type:` to mis-case.
func TestLoadProjectConfigFromDerivesFrontendsFromDisk(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "forge.yaml")
	if err := os.WriteFile(configPath, []byte("name: sample\nmodule_path: example.com/sample\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "frontends", "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "frontends", "web", "next.config.ts"), []byte("export default {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadProjectConfigFrom(configPath)
	if err != nil {
		t.Fatalf("loadProjectConfigFrom() error = %v", err)
	}
	if len(cfg.Frontends) != 1 {
		t.Fatalf("expected one frontend, got %d", len(cfg.Frontends))
	}
	if cfg.Frontends[0].Type != "nextjs" {
		t.Fatalf("expected frontend type nextjs (from next.config.ts), got %q", cfg.Frontends[0].Type)
	}
	if cfg.Frontends[0].DeclaredDir() != filepath.ToSlash(filepath.Join("frontends", "web")) {
		t.Fatalf("expected frontend path frontends/web, got %q", cfg.Frontends[0].DeclaredDir())
	}
}

func TestShouldRunRootGoModTidySkipsWhenGeneratedImportsAreMissing(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/sample\n\ngo 1.23\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	serviceDir := filepath.Join(dir, "internal", "handlers", "api")
	if err := os.MkdirAll(serviceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	serviceSource := `package apiservice

import (
	pb "example.com/sample/gen/services/api/v1"
)

var _ = pb.File_services_api_v1_api_proto
`
	if err := os.WriteFile(filepath.Join(serviceDir, "service.go"), []byte(serviceSource), 0o644); err != nil {
		t.Fatal(err)
	}

	shouldRun, err := shouldRunRootGoModTidy(dir)
	if err != nil {
		t.Fatalf("shouldRunRootGoModTidy() error = %v", err)
	}
	if shouldRun {
		t.Fatal("expected root go mod tidy to be skipped until generated imports exist")
	}
}

func TestShouldRunRootGoModTidyAllowsWhenGeneratedImportsExist(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/sample\n\ngo 1.23\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	serviceDir := filepath.Join(dir, "internal", "handlers", "api")
	generatedDir := filepath.Join(dir, "gen", "services", "api", "v1")
	if err := os.MkdirAll(serviceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(generatedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	serviceSource := `package apiservice

import (
	pb "example.com/sample/gen/services/api/v1"
)

var _ = pb.File_services_api_v1_api_proto
`
	if err := os.WriteFile(filepath.Join(serviceDir, "service.go"), []byte(serviceSource), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(generatedDir, "api.pb.go"), []byte("package apiv1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	shouldRun, err := shouldRunRootGoModTidy(dir)
	if err != nil {
		t.Fatalf("shouldRunRootGoModTidy() error = %v", err)
	}
	if !shouldRun {
		t.Fatal("expected root go mod tidy to run once generated imports exist")
	}
}

func TestWithForcedEnvReplacesExistingValue(t *testing.T) {
	env := []string{"PATH=/usr/bin", "NODE_ENV=development", "HOME=/tmp/home"}

	got := withForcedEnv(env, "NODE_ENV", "production")
	joined := strings.Join(got, "\n")
	if strings.Count(joined, "NODE_ENV=") != 1 {
		t.Fatalf("expected exactly one NODE_ENV entry, got:\n%s", joined)
	}
	if !strings.Contains(joined, "NODE_ENV=production") {
		t.Fatalf("expected NODE_ENV to be forced to production, got:\n%s", joined)
	}
	if !strings.Contains(joined, "PATH=/usr/bin") || !strings.Contains(joined, "HOME=/tmp/home") {
		t.Fatalf("expected unrelated env vars to be preserved, got:\n%s", joined)
	}
}

func TestWithForcedEnvAddsMissingValue(t *testing.T) {
	env := []string{"PATH=/usr/bin"}

	got := withForcedEnv(env, "NODE_ENV", "production")
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "NODE_ENV=production") {
		t.Fatalf("expected NODE_ENV to be added, got:\n%s", joined)
	}
	if !strings.Contains(joined, "PATH=/usr/bin") {
		t.Fatalf("expected existing env vars to be preserved, got:\n%s", joined)
	}
}

func TestBootstrapGeneratedCodeRunsGeneratePipelineInProjectDirectory(t *testing.T) {
	if testing.Short() {
		// Scaffolds a real project and drives the full generate
		// pipeline (real subprocess work, ~4s). Full mode (CI:
		// `go test ./...` without -short) still runs it.
		t.Skip("skipping real scaffold+pipeline run under -short")
	}
	dir := t.TempDir()
	generator := generator.NewProjectGenerator("sample-app", dir, "example.com/sample-app")
	generator.ServiceName = "api"
	generator.FrontendName = "web"
	if err := generator.Generate(); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	// Drive the pipeline with the REAL toolchain.
	//
	// This test used to stub buf with a shell script that hand-wrote a couple
	// of .pb.go files. The stub could not keep pace with the pipeline: it
	// emitted no proto descriptor and no gen/config/v1, both of which later
	// steps came to require, so the run aborted long before the assertions and
	// the test sat permanently red. Chasing that with an ever-larger fake buf
	// just builds a second, drifting implementation of the toolchain.
	//
	// Real buf resolves protoc-gen-go / protoc-gen-connect-go from PATH — the
	// scaffolded buf.gen.yaml uses local plugins, so no BSR and no network are
	// involved — and protoc-gen-forge is served by this very binary through
	// TestMain. The pipeline therefore runs end to end against production code.
	requireProtoToolchain(t)

	// Generated code (pkg/middleware/middleware.go etc.) imports
	// github.com/reliant-labs/forge/pkg/auth which doesn't exist in the
	// last published forge/pkg snapshot. Point the project at the in-repo
	// pkg via a replace directive so `go mod tidy` resolves locally.
	addforgeReplaceMain(t, dir)

	if err := bootstrapGeneratedCode(dir, true); err != nil {
		t.Fatalf("bootstrapGeneratedCode() error = %v", err)
	}

	assertPath := filepath.Join(dir, "gen", "services", "api", "v1", "api.pb.go")
	if _, err := os.Stat(assertPath); err != nil {
		t.Fatalf("expected generated Go stub at %s: %v", assertPath, err)
	}
	connectPath := filepath.Join(dir, "gen", "services", "api", "v1", "apiv1connect", "api.connect.go")
	if _, err := os.Stat(connectPath); err != nil {
		t.Fatalf("expected generated Connect stub at %s: %v", connectPath, err)
	}
}

// TestForgeVersionMismatchWarning covers the three cases the version-pin
// machinery cares about: legacy (no forge_version), explicit mismatch,
// and explicit match. The "dev" binary case is intentionally silent so
// local forge development doesn't spam dogfood projects.
func TestForgeVersionMismatchWarning(t *testing.T) {
	tests := []struct {
		name        string
		yaml        string
		binary      string
		wantContain string // empty = expect no warning
	}{
		{
			"legacy project (no pin) gets baseline nudge",
			"",
			"1.6.0",
			"no forge_version declared",
		},
		{
			"explicit mismatch: bump from old to newer",
			"1.4.0",
			"1.6.0",
			"forge.yaml pins forge_version 1.4.0 but this binary is 1.6.0",
		},
		{
			"matched version is silent",
			"1.6.0",
			"1.6.0",
			"",
		},
		{
			"dev binary is silent (local forge work)",
			"1.6.0",
			"dev",
			"",
		},
		{
			"(devel) binary is silent",
			"1.6.0",
			"(devel)",
			"",
		},
		{
			"empty binary is silent (defensive)",
			"1.6.0",
			"",
			"",
		},
		{
			"go pseudoversion binary is silent (go install of untagged commit)",
			"1.6.0",
			"v0.0.0-20260101120000-abcdef012345",
			"",
		},
		{
			"plain v0.0.0- prefix is silent (defensive)",
			"1.6.0",
			"v0.0.0-anything",
			"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := forgeVersionMismatchWarning(tt.yaml, tt.binary)
			if tt.wantContain == "" {
				if got != "" {
					t.Errorf("forgeVersionMismatchWarning(%q, %q) = %q, want empty",
						tt.yaml, tt.binary, got)
				}
				return
			}
			if !strings.Contains(got, tt.wantContain) {
				t.Errorf("forgeVersionMismatchWarning(%q, %q) = %q, want substring %q",
					tt.yaml, tt.binary, got, tt.wantContain)
			}
		})
	}
}

// TestShouldEmitVersionWarn covers the per-session sentinel gate: an
// empty warning never emits, the first call for a given binary path
// emits and touches the sentinel, and subsequent calls within the same
// "session" (shared $TMPDIR) stay silent.
func TestShouldEmitVersionWarn(t *testing.T) {
	// Sandbox $TMPDIR so this test can't leak state out (or in) and so
	// the sentinel filenames don't collide with a real forge install.
	t.Setenv("TMPDIR", t.TempDir())

	// Empty warning is always silent regardless of sentinel state.
	if shouldEmitVersionWarn("", "/some/path/forge") {
		t.Error("shouldEmitVersionWarn(\"\", …) should be false for an empty warning")
	}

	const binPath = "/tmp/test/forge-bin"
	const warning = "⚠️  forge.yaml declares forge_version: 1.4.0 but binary is 1.6.0."

	// First call → emits + touches sentinel.
	if !shouldEmitVersionWarn(warning, binPath) {
		t.Fatal("first shouldEmitVersionWarn call should return true (no sentinel yet)")
	}
	// Second call → sentinel hit, silent.
	if shouldEmitVersionWarn(warning, binPath) {
		t.Error("second shouldEmitVersionWarn call should return false (sentinel present)")
	}

	// A different binary path gets its own sentinel.
	const otherBin = "/tmp/test/forge-bin-other"
	if !shouldEmitVersionWarn(warning, otherBin) {
		t.Error("shouldEmitVersionWarn for a different binary path should ignore the first path's sentinel")
	}
}

// TestVersionWarnSentinelPath verifies the sentinel filename is
// per-binary-path and lives under $TMPDIR. Two distinct paths must
// produce distinct sentinels; the same path must be deterministic.
func TestVersionWarnSentinelPath(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())

	const a = "/usr/local/bin/forge"
	const b = "/Users/me/src/forge/forge"

	pa1 := versionWarnSentinelPath(a)
	pa2 := versionWarnSentinelPath(a)
	pb := versionWarnSentinelPath(b)

	if pa1 != pa2 {
		t.Errorf("sentinel path must be deterministic for the same binary; got %q and %q", pa1, pa2)
	}
	if pa1 == pb {
		t.Errorf("sentinel paths for distinct binaries must differ; both produced %q", pa1)
	}
	if !strings.HasPrefix(pa1, os.TempDir()) {
		t.Errorf("sentinel %q must live under $TMPDIR (%q)", pa1, os.TempDir())
	}
	if !strings.Contains(pa1, "forge-version-warned-") {
		t.Errorf("sentinel %q must use the forge-version-warned- prefix", pa1)
	}
}

// Migration-skill discovery is exercised in upgrade_migrations_test.go,
// against the single applicability decision every migration surface
// shares (migrationApplies).

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func readFileForTest(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", path, err)
	}
	return string(contents)
}

// TestGenerateCheckFlagRegistered confirms --check is wired and
// defaults to false (so existing scripts behave identically).
func TestGenerateCheckFlagRegistered(t *testing.T) {
	cmd := newGenerateCmd()
	f := cmd.Flags().Lookup("check")
	if f == nil {
		t.Fatal("--check flag not registered on generate command")
	}
	if f.DefValue != "false" {
		t.Errorf("--check default = %q, want %q", f.DefValue, "false")
	}
}
