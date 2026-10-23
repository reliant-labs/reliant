package generator

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Coverage for the canonical kebab/snake → snake_case rule is owned
// by internal/naming.ServicePackage; see internal/naming/naming_test.go
// (TestServicePackage). This file used to host TestServicePackageName for
// the now-deleted generator.ServicePackageName shim — the test moved with
// the canonical function.

// TestGenerateServiceFilesMultiWordKebabName_ScaffoldsSnakeDir is the
// regression guard for the pre-2026-06-08 bug where `forge scaffold service
// admin-server` (or any multi-word kebab/PascalCase service name)
// scaffolded `handlers/adminserver/` (compact, separator-stripped) which
// collided with the universal snake_case proto layout convention. Post-
// fix the dir leaf is `handlers/admin_server/`, the proto path is
// `proto/services/admin_server/v1/`, and both match the proto buf
// emitted package convention.
func TestGenerateServiceFilesMultiWordKebabName_ScaffoldsSnakeDir(t *testing.T) {
	root := t.TempDir()

	if err := GenerateServiceFiles(root, "example.com/myapp", "admin-server", "myapp"); err != nil {
		t.Fatalf("GenerateServiceFiles(admin-server) error = %v", err)
	}

	// Snake_case handler dir + proto path.
	for _, want := range []string{
		"internal/handlers/admin_server/service.go",
		"proto/services/admin_server/v1/admin_server.proto",
	} {
		if _, err := os.Stat(filepath.Join(root, want)); err != nil {
			t.Errorf("expected %s to exist: %v", want, err)
		}
	}

	// NOT the legacy compact dir.
	if _, err := os.Stat(filepath.Join(root, "internal", "handlers", "adminserver")); !os.IsNotExist(err) {
		t.Errorf("legacy compact dir handlers/adminserver/ must NOT be created (got err=%v)", err)
	}

	// service.go's `package` decl must match the snake dir leaf.
	svc, err := os.ReadFile(filepath.Join(root, "internal", "handlers", "admin_server", "service.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(svc), "package admin_server") {
		t.Errorf("service.go should declare `package admin_server`, got:\n%s", string(svc))
	}
}

func TestGenerateServiceFilesCreatesExpectedFiles(t *testing.T) {
	root := t.TempDir()

	if err := GenerateServiceFiles(root, "example.com/myapp", "orders", "myapp"); err != nil {
		t.Fatalf("GenerateServiceFiles() error = %v", err)
	}

	// Verify all expected files exist
	expectedFiles := []string{
		"internal/handlers/orders/service.go",
		"proto/services/orders/v1/orders.proto",
	}
	for _, f := range expectedFiles {
		path := filepath.Join(root, f)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected file %s to exist: %v", f, err)
		}
	}

	// handlers.go should NOT exist at scaffold (zero RPC methods).
	if _, err := os.Stat(filepath.Join(root, "internal", "handlers", "orders", "handlers.go")); !os.IsNotExist(err) {
		t.Errorf("handlers.go should not be emitted at scaffold with zero RPC methods (got err=%v)", err)
	}
}

func TestGenerateServiceFilesServiceGoUsesTemplates(t *testing.T) {
	root := t.TempDir()

	if err := GenerateServiceFiles(root, "example.com/myapp", "orders", "myapp"); err != nil {
		t.Fatalf("GenerateServiceFiles() error = %v", err)
	}

	content, err := os.ReadFile(filepath.Join(root, "internal", "handlers", "orders", "service.go"))
	if err != nil {
		t.Fatalf("ReadFile error = %v", err)
	}
	svc := string(content)

	// The template version includes the Unimplemented handler embedding
	if !strings.Contains(svc, "UnimplementedOrdersServiceHandler") {
		t.Errorf("service.go should embed UnimplementedOrdersServiceHandler, got:\n%s", svc)
	}
	// The template version uses the correct Connect import path
	if !strings.Contains(svc, "gen/services/orders/v1/ordersv1connect") {
		t.Errorf("service.go should import ordersv1connect package, got:\n%s", svc)
	}
	// Package name should follow template convention
	if !strings.HasPrefix(svc, "package orders") {
		t.Errorf("service.go should have package orders, got:\n%s", svc)
	}
}

// TestGenerateServiceFilesProtoHasServiceOption verifies the scaffolded
// proto carries the (forge.v1.service) option block (name/version/description)
// plus the forge/v1/forge.proto import the option extension needs. The option
// carries no access-control annotations — forge generates none.
func TestGenerateServiceFilesProtoHasServiceOption(t *testing.T) {
	root := t.TempDir()
	if err := GenerateServiceFiles(root, "example.com/myapp", "orders", "myapp"); err != nil {
		t.Fatalf("GenerateServiceFiles() error = %v", err)
	}
	content, err := os.ReadFile(filepath.Join(root, "proto", "services", "orders", "v1", "orders.proto"))
	if err != nil {
		t.Fatalf("read proto: %v", err)
	}
	proto := string(content)
	for _, want := range []string{
		`import "forge/v1/forge.proto";`,
		"option (forge.v1.service) = {",
		`name: "OrdersService"`,
		`version: "1.0.0"`,
		"service OrdersService {",
	} {
		if !strings.Contains(proto, want) {
			t.Errorf("scaffolded proto missing %q:\n%s", want, proto)
		}
	}
}

func TestGenerateServiceFilesProtoSkipsExisting(t *testing.T) {
	root := t.TempDir()

	// Pre-create a proto file
	protoDir := filepath.Join(root, "proto", "services", "orders", "v1")
	if err := os.MkdirAll(protoDir, 0755); err != nil {
		t.Fatal(err)
	}
	existing := []byte("syntax = \"proto3\";\n// existing proto\n")
	if err := os.WriteFile(filepath.Join(protoDir, "orders.proto"), existing, 0644); err != nil {
		t.Fatal(err)
	}

	if err := GenerateServiceFiles(root, "example.com/myapp", "orders", "myapp"); err != nil {
		t.Fatalf("GenerateServiceFiles() error = %v", err)
	}

	// Proto should not be overwritten
	content, err := os.ReadFile(filepath.Join(protoDir, "orders.proto"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "// existing proto") {
		t.Errorf("existing proto should not be overwritten, got:\n%s", string(content))
	}
}

// TestGenerateServiceFilesResumeSkipsExisting verifies that ScaffoldResume
// preserves every pre-existing output file and reports skips. The recovery
// scenario this guards: a partial `forge scaffold service` run wrote service.go
// but `buf generate` failed mid-pipeline; the user re-runs with --resume
// expecting service.go to stay untouched.
func TestGenerateServiceFilesResumeSkipsExisting(t *testing.T) {
	root := t.TempDir()

	// Pre-seed every output file with sentinel content that the scaffolder
	// would never produce. If --resume erroneously rewrites them, this
	// content disappears.
	preExisting := map[string]string{
		"internal/handlers/orders/service.go":   "// user edits to service.go\npackage orders\n",
		"proto/services/orders/v1/orders.proto": "syntax = \"proto3\";\n// user-edited proto\n",
	}
	for rel, content := range preExisting {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var progress bytes.Buffer
	if err := GenerateServiceFilesWithMode(root, "example.com/myapp", "orders", "myapp",
		ScaffoldResume, &progress); err != nil {
		t.Fatalf("GenerateServiceFilesWithMode(resume) error = %v", err)
	}

	for rel, want := range preExisting {
		got, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if string(got) != want {
			t.Errorf("--resume should not have rewritten %s; got:\n%s", rel, string(got))
		}
	}

	// One "✓ skipped" line per file the scaffold would otherwise have
	// emitted.
	if got := progress.String(); !strings.Contains(got, "skipped: ") {
		t.Errorf("expected progress output to report skipped files, got:\n%s", got)
	}
}

// TestGenerateServiceFilesForceOverwrites verifies that ScaffoldForce
// rewrites every output file (including the proto) and reports the
// overwrites.
func TestGenerateServiceFilesForceOverwrites(t *testing.T) {
	root := t.TempDir()

	// Pre-seed with sentinel content so we can detect overwrites.
	// No scaffold test here: it is one file per RPC and this path runs
	// before any RPC exists, so the zero-RPC scaffold writes none.
	preExisting := []string{
		"internal/handlers/orders/service.go",
		"proto/services/orders/v1/orders.proto",
	}
	for _, rel := range preExisting {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("// SENTINEL"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var progress bytes.Buffer
	if err := GenerateServiceFilesWithMode(root, "example.com/myapp", "orders", "myapp",
		ScaffoldForce, &progress); err != nil {
		t.Fatalf("GenerateServiceFilesWithMode(force) error = %v", err)
	}

	for _, rel := range preExisting {
		got, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if strings.Contains(string(got), "SENTINEL") {
			t.Errorf("--force should have rewritten %s; sentinel still present", rel)
		}
	}

	if got := progress.String(); !strings.Contains(got, "overwriting: ") {
		t.Errorf("expected progress output to report overwriting files, got:\n%s", got)
	}
}

// TestGenerateServiceFilesFailModeKeepsProto re-asserts the historical
// guard: even in the default ScaffoldFail mode, an existing proto file is
// preserved (it carries hand-written RPC definitions that the scaffold
// has no way to reconstruct).
func TestGenerateServiceFilesFailModeKeepsProto(t *testing.T) {
	root := t.TempDir()

	protoDir := filepath.Join(root, "proto", "services", "orders", "v1")
	if err := os.MkdirAll(protoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	want := "syntax = \"proto3\";\n// hand-written\n"
	if err := os.WriteFile(filepath.Join(protoDir, "orders.proto"), []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := GenerateServiceFilesWithMode(root, "example.com/myapp", "orders", "myapp",
		ScaffoldFail, nil); err != nil {
		t.Fatalf("GenerateServiceFilesWithMode(fail) error = %v", err)
	}

	got, err := os.ReadFile(filepath.Join(protoDir, "orders.proto"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("ScaffoldFail should preserve existing proto; got:\n%s", string(got))
	}
}

func TestGenerateFrontendFilesCreatesExpectedFiles(t *testing.T) {
	root := t.TempDir()

	if err := GenerateFrontendFiles(root, "example.com/myapp", "myapp", "web", 8080, ""); err != nil {
		t.Fatalf("GenerateFrontendFiles() error = %v", err)
	}

	// At minimum, package.json should exist
	path := filepath.Join(root, "frontends", "web", "package.json")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected package.json to exist: %v", err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "web") {
		t.Errorf("package.json should reference frontend name, got:\n%s", string(content))
	}
	if !strings.Contains(string(content), "lint:styles") {
		t.Errorf("package.json should include lint:styles, got:\n%s", string(content))
	}
	if !strings.Contains(string(content), "stylelint") {
		t.Errorf("package.json should include stylelint dependencies, got:\n%s", string(content))
	}
	if _, err := os.Stat(filepath.Join(root, "frontends", "web", "stylelint.config.mjs")); err != nil {
		t.Errorf("expected stylelint.config.mjs to exist: %v", err)
	}

	// A nested go.mod must exist so that `go test ./...` from the project
	// root skips this subtree (frontend node_modules may contain stray .go
	// files from transitive npm deps).
	goModPath := filepath.Join(root, "frontends", "web", "go.mod")
	goModBytes, err := os.ReadFile(goModPath)
	if err != nil {
		t.Fatalf("expected frontend go.mod to exist: %v", err)
	}
	if !strings.Contains(string(goModBytes), "module example.com/myapp/frontends/web") {
		t.Errorf("frontend go.mod should declare nested module path, got:\n%s", string(goModBytes))
	}

	// test-utils.tsx must be scaffolded so page tests can swap the Connect
	// transport via setTransport(). The skill `frontend-testing` documents
	// the API; the scaffold needs to ship the file.
	testUtilsPath := filepath.Join(root, "frontends", "web", "src", "lib", "test-utils.tsx")
	testUtils, err := os.ReadFile(testUtilsPath)
	if err != nil {
		t.Fatalf("expected src/lib/test-utils.tsx to exist: %v", err)
	}
	for _, want := range []string{"mockTransport", "renderWithTransport", "setTransport"} {
		if !strings.Contains(string(testUtils), want) {
			t.Errorf("test-utils.tsx should export %q, got:\n%s", want, string(testUtils))
		}
	}

	// connect.ts must export setTransport so test-utils.tsx can swap the
	// active transport for hermetic tests.
	connectTS, err := os.ReadFile(filepath.Join(root, "frontends", "web", "src", "lib", "connect.ts"))
	if err != nil {
		t.Fatalf("expected src/lib/connect.ts to exist: %v", err)
	}
	if !strings.Contains(string(connectTS), "export function setTransport") {
		t.Errorf("connect.ts should export setTransport, got:\n%s", string(connectTS))
	}
}

func TestGenerateFrontendFilesReactNative(t *testing.T) {
	root := t.TempDir()

	if err := GenerateFrontendFiles(root, "example.com/myapp", "myapp", "mobile-app", 8080, "mobile"); err != nil {
		t.Fatalf("GenerateFrontendFiles(mobile) error = %v", err)
	}

	feDir := filepath.Join(root, "frontends", "mobile-app")

	// Core files must exist
	for _, file := range []string{
		"package.json", "app.json", "tsconfig.json", "babel.config.js",
		".gitignore", "buf.gen.yaml",
		"src/lib/connect.ts", "src/lib/apiurl_gen.ts", "src/lib/query-client.ts",
		"src/hooks/use-api-query.ts", "src/hooks/use-api-mutation.ts",
		"app/_layout.tsx", "app/index.tsx",
		"go.mod",
	} {
		if _, err := os.Stat(filepath.Join(feDir, file)); err != nil {
			t.Errorf("expected %s to exist: %v", file, err)
		}
	}

	// package.json should reference expo and the frontend name
	pkgJSON, err := os.ReadFile(filepath.Join(feDir, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(pkgJSON), "mobile-app") {
		t.Error("package.json should contain frontend name")
	}
	if !strings.Contains(string(pkgJSON), "expo") {
		t.Error("package.json should contain expo dependency")
	}

	// The dev-URL floor lives in the regenerated apiurl_gen.ts (the
	// hand-editable .env.local was ripped out); connect.ts imports
	// DEV_API_URL from it and reads EXPO_PUBLIC_API_URL for overrides.
	apiURLGen, err := os.ReadFile(filepath.Join(feDir, "src", "lib", "apiurl_gen.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(apiURLGen), `DEV_API_URL = "http://localhost:8080"`) {
		t.Errorf("apiurl_gen.ts should bake the dev API URL, got:\n%s", string(apiURLGen))
	}

	// connect.ts should use EXPO_PUBLIC_API_URL and the apiurl_gen floor.
	connectTS, err := os.ReadFile(filepath.Join(feDir, "src", "lib", "connect.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(connectTS), "EXPO_PUBLIC_API_URL") {
		t.Error("connect.ts should reference EXPO_PUBLIC_API_URL")
	}
	if !strings.Contains(string(connectTS), "DEV_API_URL") {
		t.Error("connect.ts should import DEV_API_URL from apiurl_gen")
	}

	// go.mod should declare nested module
	goModBytes, err := os.ReadFile(filepath.Join(feDir, "go.mod"))
	if err != nil {
		t.Fatalf("expected frontend go.mod to exist: %v", err)
	}
	if !strings.Contains(string(goModBytes), "module example.com/myapp/frontends/mobile-app") {
		t.Errorf("frontend go.mod should declare nested module path, got:\n%s", string(goModBytes))
	}
}

// TestGenerateFrontendFilesViteSPA exercises the vite-spa kind end-to-end
// through the generator and asserts the produced file tree includes the
// Vite-specific entries (index.html, vite.config.ts, src/main.tsx,
// src/routes.tsx), uses VITE_* env vars in connect.ts and .env.local,
// and that core UI components are installed alongside the scaffold.
func TestGenerateFrontendFilesViteSPA(t *testing.T) {
	root := t.TempDir()

	if err := GenerateFrontendFiles(root, "example.com/myapp", "myapp", "spa-app", 8080, "vite-spa"); err != nil {
		t.Fatalf("GenerateFrontendFiles(vite-spa) error = %v", err)
	}

	feDir := filepath.Join(root, "frontends", "spa-app")

	// Core scaffold files must exist.
	for _, file := range []string{
		"package.json", "vite.config.ts", "tsconfig.json", "tsconfig.node.json",
		"index.html", ".gitignore", "buf.gen.yaml",
		"eslint.config.mjs",
		"src/main.tsx", "src/App.tsx", "src/routes.tsx", "src/index.css",
		"src/vite-env.d.ts",
		"src/stores/ui-store.ts",
		"src/lib/connect.ts", "src/lib/apiurl_gen.ts", "src/lib/query-client.ts",
		"src/lib/events.ts", "src/lib/event-context.tsx",
		"src/lib/search-schemas.ts", "src/lib/format-utils.ts",
		"src/lib/auth/provider.ts", "src/lib/auth/session-provider.ts",
		"src/lib/auth/context.tsx",
		"src/hooks/use-api-query.ts", "src/hooks/use-api-mutation.ts",
		"src/lib/test-utils.tsx",
		"go.mod",
	} {
		if _, err := os.Stat(filepath.Join(feDir, file)); err != nil {
			t.Errorf("expected %s to exist: %v", file, err)
		}
	}

	// connect.ts must expose setTransport so test-utils can swap the
	// active transport in hermetic page tests.
	connectForTest, err := os.ReadFile(filepath.Join(feDir, "src", "lib", "connect.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(connectForTest), "export function setTransport") {
		t.Errorf("vite-spa connect.ts should export setTransport, got:\n%s", string(connectForTest))
	}

	// Core UI components must be installed (vite-spa is browser-targeted
	// just like nextjs and uses the same component primitives).
	if _, err := os.Stat(filepath.Join(feDir, "src", "components", "ui", "button.tsx")); err != nil {
		t.Errorf("expected core component button.tsx to exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(feDir, "src", "components", "ui", "page_header.tsx")); err != nil {
		t.Errorf("expected core component page_header.tsx to exist: %v", err)
	}

	// package.json should reference vite and the frontend name.
	pkgJSON, err := os.ReadFile(filepath.Join(feDir, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(pkgJSON), `"name": "spa-app"`) {
		t.Error("package.json should contain frontend name")
	}
	if !strings.Contains(string(pkgJSON), `"vite"`) {
		t.Error("package.json should contain vite dependency")
	}
	if !strings.Contains(string(pkgJSON), "@tanstack/react-router") {
		t.Error("package.json should contain @tanstack/react-router")
	}

	// The dev-URL floor lives in the regenerated apiurl_gen.ts (the
	// hand-editable .env.local was ripped out); connect.ts imports
	// DEV_API_URL from it and reads VITE_API_URL for overrides.
	apiURLGen, err := os.ReadFile(filepath.Join(feDir, "src", "lib", "apiurl_gen.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(apiURLGen), `DEV_API_URL = "http://localhost:8080"`) {
		t.Errorf("apiurl_gen.ts should bake the dev API URL, got:\n%s", string(apiURLGen))
	}

	// connect.ts should use VITE_API_URL (not NEXT_PUBLIC_API_URL).
	connectTS, err := os.ReadFile(filepath.Join(feDir, "src", "lib", "connect.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(connectTS), "VITE_API_URL") {
		t.Error("connect.ts should reference VITE_API_URL")
	}
	if strings.Contains(string(connectTS), "NEXT_PUBLIC_API_URL") {
		t.Error("connect.ts should NOT reference NEXT_PUBLIC_API_URL")
	}

	// go.mod should declare nested module.
	goModBytes, err := os.ReadFile(filepath.Join(feDir, "go.mod"))
	if err != nil {
		t.Fatalf("expected frontend go.mod to exist: %v", err)
	}
	if !strings.Contains(string(goModBytes), "module example.com/myapp/frontends/spa-app") {
		t.Errorf("frontend go.mod should declare nested module path, got:\n%s", string(goModBytes))
	}

	// routes.tsx should embed the FORGE-ROUTES marker block so the page
	// generator can stitch in entity routes.
	routesTSX, err := os.ReadFile(filepath.Join(feDir, "src", "routes.tsx"))
	if err != nil {
		t.Fatalf("expected src/routes.tsx to exist: %v", err)
	}
	if !strings.Contains(string(routesTSX), "FORGE-ROUTES: BEGIN") {
		t.Error("routes.tsx should contain the FORGE-ROUTES marker block")
	}
}
