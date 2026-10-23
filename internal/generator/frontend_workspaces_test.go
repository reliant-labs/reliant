package generator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNewFrontendWorkspaceLayout_DerivesScopeFromProjectName asserts
// that the workspace package names are derived from the project name
// — `my-app` produces `@my-app/api` / `@my-app/hooks` / `@my-app/ui-web`.
// Uppercase and disallowed chars are stripped so the scope is a valid
// npm segment.
func TestNewFrontendWorkspaceLayout_DerivesScopeFromProjectName(t *testing.T) {
	tests := []struct {
		name      string
		project   string
		wantScope string
		wantAPI   string
		wantHooks string
		wantUIWeb string
	}{
		{"plain name", "myapp", "myapp", "@myapp/api", "@myapp/hooks", "@myapp/ui-web"},
		{"hyphen preserved", "my-app", "my-app", "@my-app/api", "@my-app/hooks", "@my-app/ui-web"},
		{"underscore preserved", "my_app", "my_app", "@my_app/api", "@my_app/hooks", "@my_app/ui-web"},
		{"uppercase lowered", "MyApp", "myapp", "@myapp/api", "@myapp/hooks", "@myapp/ui-web"},
		{"dots stripped", "my.app", "myapp", "@myapp/api", "@myapp/hooks", "@myapp/ui-web"},
		{"empty falls back to app", "", "app", "@app/api", "@app/hooks", "@app/ui-web"},
		{"all-invalid falls back to app", "@@@", "app", "@app/api", "@app/hooks", "@app/ui-web"},
		{"leading digit trimmed", "9app", "app", "@app/api", "@app/hooks", "@app/ui-web"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NewFrontendWorkspaceLayout(tt.project)
			if got.Scope != tt.wantScope {
				t.Errorf("Scope = %q, want %q", got.Scope, tt.wantScope)
			}
			if got.APIPackage != tt.wantAPI {
				t.Errorf("APIPackage = %q, want %q", got.APIPackage, tt.wantAPI)
			}
			if got.HooksPackage != tt.wantHooks {
				t.Errorf("HooksPackage = %q, want %q", got.HooksPackage, tt.wantHooks)
			}
			if got.UIWebPackage != tt.wantUIWeb {
				t.Errorf("UIWebPackage = %q, want %q", got.UIWebPackage, tt.wantUIWeb)
			}
		})
	}
}

// TestWriteFrontendWorkspaceFiles_DisabledNoop asserts that calling
// WriteFrontendWorkspaceFiles with workspaces=false writes nothing.
// This is the load-bearing invariant for snapshot stability: existing
// projects that don't opt in must see byte-identical output.
func TestWriteFrontendWorkspaceFiles_DisabledNoop(t *testing.T) {
	dir := t.TempDir()
	if err := WriteFrontendWorkspaceFiles(dir, "myapp", false); err != nil {
		t.Fatalf("WriteFrontendWorkspaceFiles: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read tempdir: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("workspaces=false should produce no files, got: %v", names)
	}
}

// TestWriteFrontendWorkspaceFiles_EmitsWorkspaceLayout asserts the
// pnpm-workspace.yaml + packages/api + packages/hooks scaffolding lands
// at the expected paths with valid JSON manifests and the project-
// derived scope.
func TestWriteFrontendWorkspaceFiles_EmitsWorkspaceLayout(t *testing.T) {
	dir := t.TempDir()
	if err := WriteFrontendWorkspaceFiles(dir, "myapp", true); err != nil {
		t.Fatalf("WriteFrontendWorkspaceFiles: %v", err)
	}

	// pnpm-workspace.yaml at root.
	body := mustRead(t, filepath.Join(dir, "pnpm-workspace.yaml"))
	if !strings.Contains(body, "packages/*") {
		t.Errorf("pnpm-workspace.yaml should list packages/*, got:\n%s", body)
	}
	if !strings.Contains(body, "frontends/*") {
		t.Errorf("pnpm-workspace.yaml should list frontends/*, got:\n%s", body)
	}

	// packages/api/package.json declares the scoped name + bufbuild + connectrpc deps.
	apiPkgPath := filepath.Join(dir, "packages", "api", "package.json")
	var apiPkg map[string]any
	if err := json.Unmarshal([]byte(mustRead(t, apiPkgPath)), &apiPkg); err != nil {
		t.Fatalf("parse packages/api/package.json: %v", err)
	}
	if apiPkg["name"] != "@myapp/api" {
		t.Errorf("packages/api name = %v, want @myapp/api", apiPkg["name"])
	}
	apiDeps, _ := apiPkg["dependencies"].(map[string]any)
	if _, ok := apiDeps["@bufbuild/protobuf"]; !ok {
		t.Errorf("packages/api should depend on @bufbuild/protobuf, got: %v", apiDeps)
	}
	if _, ok := apiDeps["@connectrpc/connect"]; !ok {
		t.Errorf("packages/api should depend on @connectrpc/connect, got: %v", apiDeps)
	}

	// packages/hooks/package.json declares workspace:* dep on @myapp/api
	// plus @tanstack/react-query.
	hooksPkgPath := filepath.Join(dir, "packages", "hooks", "package.json")
	var hooksPkg map[string]any
	if err := json.Unmarshal([]byte(mustRead(t, hooksPkgPath)), &hooksPkg); err != nil {
		t.Fatalf("parse packages/hooks/package.json: %v", err)
	}
	if hooksPkg["name"] != "@myapp/hooks" {
		t.Errorf("packages/hooks name = %v, want @myapp/hooks", hooksPkg["name"])
	}
	hooksDeps, _ := hooksPkg["dependencies"].(map[string]any)
	if hooksDeps["@myapp/api"] != "workspace:*" {
		t.Errorf("packages/hooks should declare \"@myapp/api\": \"workspace:*\", got: %v", hooksDeps)
	}
	if _, ok := hooksDeps["@tanstack/react-query"]; !ok {
		t.Errorf("packages/hooks should depend on @tanstack/react-query, got: %v", hooksDeps)
	}

	// Base hook wrappers exist.
	uq := mustRead(t, filepath.Join(dir, "packages", "hooks", "src", "use-api-query.ts"))
	if !strings.Contains(uq, "useApiQuery") {
		t.Errorf("use-api-query.ts should export useApiQuery, got:\n%s", uq)
	}
	um := mustRead(t, filepath.Join(dir, "packages", "hooks", "src", "use-api-mutation.ts"))
	if !strings.Contains(um, "useApiMutation") {
		t.Errorf("use-api-mutation.ts should export useApiMutation, got:\n%s", um)
	}
	// Transport bootstrap is the bridge from per-frontend Transport to
	// the shared hooks — without it generated hooks have no way to
	// reach the network. Pin its presence.
	tr := mustRead(t, filepath.Join(dir, "packages", "hooks", "src", "transport.ts"))
	if !strings.Contains(tr, "setApiTransport") || !strings.Contains(tr, "connectClient") {
		t.Errorf("transport.ts should export setApiTransport + connectClient, got:\n%s", tr)
	}

	// packages/hooks/tsconfig.json must NOT include "DOM" in lib — the
	// package has to stay consumable from React Native (no document/
	// window). This is a regression guardrail: a future contributor who
	// adds DOM-only APIs will hit tsc errors in the hooks workspace.
	hooksTsconfig := mustRead(t, filepath.Join(dir, "packages", "hooks", "tsconfig.json"))
	if strings.Contains(hooksTsconfig, "\"DOM\"") {
		t.Errorf("packages/hooks/tsconfig.json must not include DOM lib (hooks are DOM-free), got:\n%s", hooksTsconfig)
	}
}

// TestWriteFrontendWorkspaceFiles_Idempotent asserts re-running the
// emitter doesn't clobber user edits to package.json / tsconfig.json.
// This is the safety net for `forge generate` cycles: the user owns
// these files once they exist on disk.
func TestWriteFrontendWorkspaceFiles_Idempotent(t *testing.T) {
	dir := t.TempDir()
	if err := WriteFrontendWorkspaceFiles(dir, "myapp", true); err != nil {
		t.Fatalf("first emit: %v", err)
	}

	// Simulate a user edit to packages/api/package.json — add a custom
	// dependency. A second `forge generate` must preserve it.
	apiPkgPath := filepath.Join(dir, "packages", "api", "package.json")
	userEdit := `{"name":"@myapp/api","version":"0.0.0","dependencies":{"custom":"^1.0.0"}}`
	if err := os.WriteFile(apiPkgPath, []byte(userEdit), 0o644); err != nil {
		t.Fatalf("write user edit: %v", err)
	}

	if err := WriteFrontendWorkspaceFiles(dir, "myapp", true); err != nil {
		t.Fatalf("second emit: %v", err)
	}

	got := mustRead(t, apiPkgPath)
	if got != userEdit {
		t.Errorf("user edit was clobbered on second emit\nwant: %s\n got: %s", userEdit, got)
	}
}

// TestWriteFrontendWorkspaceFiles_EmitsUIWebPackage asserts the
// packages/ui-web/ workspace lands with package.json, tsconfig.json,
// the core components copied from the embedded library, and a re-export
// barrel at src/index.ts.
//
// The ui-web package is the "scaffold once, own forever" replacement
// for per-frontend src/components/ui/ copies in multi-frontend
// projects — the test pins all four load-bearing pieces.
func TestWriteFrontendWorkspaceFiles_EmitsUIWebPackage(t *testing.T) {
	dir := t.TempDir()
	if err := WriteFrontendWorkspaceFiles(dir, "myapp", true); err != nil {
		t.Fatalf("WriteFrontendWorkspaceFiles: %v", err)
	}

	// packages/ui-web/package.json declares the scoped name + react as
	// a peer dep (NOT a hard dep — the frontend owns the React version).
	uiPkgPath := filepath.Join(dir, "packages", "ui-web", "package.json")
	var uiPkg map[string]any
	if err := json.Unmarshal([]byte(mustRead(t, uiPkgPath)), &uiPkg); err != nil {
		t.Fatalf("parse packages/ui-web/package.json: %v", err)
	}
	if uiPkg["name"] != "@myapp/ui-web" {
		t.Errorf("packages/ui-web name = %v, want @myapp/ui-web", uiPkg["name"])
	}
	peerDeps, _ := uiPkg["peerDependencies"].(map[string]any)
	if _, ok := peerDeps["react"]; !ok {
		t.Errorf("packages/ui-web should declare react as peerDependency, got: %v", peerDeps)
	}
	if _, ok := peerDeps["react-dom"]; !ok {
		t.Errorf("packages/ui-web should declare react-dom as peerDependency, got: %v", peerDeps)
	}

	// tsconfig must include DOM in lib — these ARE DOM-bound web
	// components. (Mirror of the hooks-package DOM-free guardrail.)
	uiTsconfig := mustRead(t, filepath.Join(dir, "packages", "ui-web", "tsconfig.json"))
	if !strings.Contains(uiTsconfig, "\"DOM\"") {
		t.Errorf("packages/ui-web/tsconfig.json must include DOM lib (web components are DOM-bound), got:\n%s", uiTsconfig)
	}
	if !strings.Contains(uiTsconfig, "\"jsx\": \"preserve\"") {
		t.Errorf("packages/ui-web/tsconfig.json must preserve JSX (consumers transform), got:\n%s", uiTsconfig)
	}

	// At least one well-known core component lands under
	// src/components/ui/ — matching the per-frontend layout
	// (src/components/ui/<name>.tsx). The /ui/ namespace lets the
	// tsconfig path mapping target ONLY component-library paths and
	// leave non-ui local paths (e.g. the owned
	// `@/components/nav`) resolving against the per-frontend src.
	for _, name := range []string{"button", "card", "login_form", "row_actions_menu"} {
		path := filepath.Join(dir, "packages", "ui-web", "src", "components", "ui", name+".tsx")
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected ui-web component ui/%s.tsx to exist: %v", name, err)
		}
	}

	// src/index.ts is the re-export barrel — should mention at least
	// the well-known components in PascalCase named-export form,
	// pointing at the components/ui/ subpath.
	barrel := mustRead(t, filepath.Join(dir, "packages", "ui-web", "src", "index.ts"))
	for _, want := range []string{
		`export { default as Button } from "./components/ui/button"`,
		`export { default as Card } from "./components/ui/card"`,
		`export { default as LoginForm } from "./components/ui/login_form"`,
		`export { default as RowActionsMenu } from "./components/ui/row_actions_menu"`,
	} {
		if !strings.Contains(barrel, want) {
			t.Errorf("ui-web barrel should contain %q, got:\n%s", want, barrel)
		}
	}
}

// TestWriteFrontendWorkspaceFiles_DisabledNoopUIWeb asserts that
// WriteFrontendWorkspaceFiles(..., false) does NOT emit any
// packages/ui-web/ files. The non-workspaces flow stays byte-identical
// to projects scaffolded before this feature landed.
func TestWriteFrontendWorkspaceFiles_DisabledNoopUIWeb(t *testing.T) {
	dir := t.TempDir()
	if err := WriteFrontendWorkspaceFiles(dir, "myapp", false); err != nil {
		t.Fatalf("WriteFrontendWorkspaceFiles: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "packages", "ui-web")); !os.IsNotExist(err) {
		t.Errorf("workspaces=false must not create packages/ui-web; got err=%v", err)
	}
}

// TestWriteUIWebPackageFiles_StandaloneEmitter asserts the public
// WriteUIWebPackageFiles entry point (separate from the bundled
// WriteFrontendWorkspaceFiles) emits the ui-web scaffold on its own.
// This is what the generate-pipeline ensure step calls during refresh
// runs.
func TestWriteUIWebPackageFiles_StandaloneEmitter(t *testing.T) {
	dir := t.TempDir()
	if err := WriteUIWebPackageFiles(dir, "myapp", true); err != nil {
		t.Fatalf("WriteUIWebPackageFiles: %v", err)
	}
	// Sanity: the package.json + at least one component + the barrel
	// must all land. We don't re-check every field — the deeper test
	// above pins those.
	for _, rel := range []string{
		filepath.Join("packages", "ui-web", "package.json"),
		filepath.Join("packages", "ui-web", "tsconfig.json"),
		filepath.Join("packages", "ui-web", "src", "components", "ui", "button.tsx"),
		filepath.Join("packages", "ui-web", "src", "index.ts"),
		filepath.Join("packages", "ui-web", "README.md"),
	} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Errorf("expected %s to exist: %v", rel, err)
		}
	}

	// workspaces=false must NOT emit anything.
	dir2 := t.TempDir()
	if err := WriteUIWebPackageFiles(dir2, "myapp", false); err != nil {
		t.Fatalf("WriteUIWebPackageFiles disabled: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir2, "packages")); !os.IsNotExist(err) {
		t.Errorf("workspaces=false must not create packages/; got err=%v", err)
	}
}

// TestWriteUIWebPackageFiles_PreservesUserEdits asserts that re-
// running the emitter never clobbers user edits. This is the entire
// ownership contract for the ui-web package — once a file lands, it's
// the user's.
func TestWriteUIWebPackageFiles_PreservesUserEdits(t *testing.T) {
	dir := t.TempDir()
	if err := WriteUIWebPackageFiles(dir, "myapp", true); err != nil {
		t.Fatalf("first emit: %v", err)
	}

	buttonPath := filepath.Join(dir, "packages", "ui-web", "src", "components", "ui", "button.tsx")
	customButton := "// USER EDIT — must survive re-generation\nexport default function Button() { return null; }\n"
	if err := os.WriteFile(buttonPath, []byte(customButton), 0o644); err != nil {
		t.Fatalf("write user edit: %v", err)
	}

	// User also edits the re-export barrel — drops a component from
	// the public API. Re-emit must respect both edits.
	indexPath := filepath.Join(dir, "packages", "ui-web", "src", "index.ts")
	customIndex := "// USER EDIT — must survive\nexport { default as Button } from \"./components/ui/button\";\n"
	if err := os.WriteFile(indexPath, []byte(customIndex), 0o644); err != nil {
		t.Fatalf("write user index edit: %v", err)
	}

	if err := WriteUIWebPackageFiles(dir, "myapp", true); err != nil {
		t.Fatalf("second emit: %v", err)
	}

	if got := mustRead(t, buttonPath); got != customButton {
		t.Errorf("user-edited component was clobbered on re-emit\nwant: %s\n got: %s", customButton, got)
	}
	if got := mustRead(t, indexPath); got != customIndex {
		t.Errorf("user-edited barrel was clobbered on re-emit\nwant: %s\n got: %s", customIndex, got)
	}
}

// TestGenerateFrontendFiles_WorkspacesSkipsPerFrontendComponents
// asserts that opting into workspaces removes the per-frontend
// src/components/ui/ copy AND that the per-frontend package.json
// declares the workspace dep on @<scope>/ui-web. This is the load-
// bearing invariant that gives multi-frontend projects a single source
// of truth for the component library.
func TestGenerateFrontendFiles_WorkspacesSkipsPerFrontendComponents(t *testing.T) {
	dir := t.TempDir()
	if err := GenerateFrontendFilesWithOptions(
		dir, "example.com/myapp", "myapp", "web", 8080, "",
		FrontendGenOptions{Workspaces: true},
	); err != nil {
		t.Fatalf("GenerateFrontendFilesWithOptions: %v", err)
	}

	// frontends/web/src/components/ui/ must NOT have been populated.
	// The tsconfig path mapping (and vite alias) redirect @/components/*
	// to packages/ui-web instead.
	componentsDir := filepath.Join(dir, "frontends", "web", "src", "components", "ui")
	if entries, err := os.ReadDir(componentsDir); err == nil && len(entries) > 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("frontends/web/src/components/ui/ must be empty in workspaces mode, got: %v", names)
	}

	// package.json declares the @<scope>/ui-web workspace dep.
	pkgPath := filepath.Join(dir, "frontends", "web", "package.json")
	pkgBody := mustRead(t, pkgPath)
	if !strings.Contains(pkgBody, `"@myapp/ui-web": "workspace:*"`) {
		t.Errorf("frontends/web/package.json should declare @myapp/ui-web workspace dep, got:\n%s", pkgBody)
	}

	// tsconfig.json carries the path mapping that redirects
	// `@/components/ui/*` to packages/ui-web/src/components/ui/*.
	// Pin both the redirected mapping AND the existing `@/*` catch-all
	// so the pages that import `@/lib/...` (and the owned components that
	// import `@/components/nav`) keep resolving against the local src
	// tree.
	tsconfigBody := mustRead(t, filepath.Join(dir, "frontends", "web", "tsconfig.json"))
	if !strings.Contains(tsconfigBody, `"@/components/ui/*": ["../../packages/ui-web/src/components/ui/*"]`) {
		t.Errorf("tsconfig.json should map @/components/ui/* to packages/ui-web, got:\n%s", tsconfigBody)
	}
	if !strings.Contains(tsconfigBody, `"@/*": ["./src/*"]`) {
		t.Errorf("tsconfig.json should retain @/* mapping for non-component paths, got:\n%s", tsconfigBody)
	}
}

// TestGenerateFrontendFiles_NoWorkspacesKeepsPerFrontendComponents is
// the snapshot-stability guardrail: with Workspaces=false the existing
// per-frontend installCoreComponents() path stays intact, so existing
// projects regenerate byte-identically.
func TestGenerateFrontendFiles_NoWorkspacesKeepsPerFrontendComponents(t *testing.T) {
	dir := t.TempDir()
	if err := GenerateFrontendFilesWithOptions(
		dir, "example.com/myapp", "myapp", "web", 8080, "",
		FrontendGenOptions{Workspaces: false},
	); err != nil {
		t.Fatalf("GenerateFrontendFilesWithOptions: %v", err)
	}

	// The per-frontend src/components/ui/button.tsx must exist (it's
	// part of the core list installCoreComponents writes).
	buttonPath := filepath.Join(dir, "frontends", "web", "src", "components", "ui", "button.tsx")
	if _, err := os.Stat(buttonPath); err != nil {
		t.Errorf("workspaces=false should populate frontends/web/src/components/ui/button.tsx: %v", err)
	}

	// tsconfig.json must NOT include the ui-web path mapping — the
	// non-workspaces shape stays untouched.
	tsconfigBody := mustRead(t, filepath.Join(dir, "frontends", "web", "tsconfig.json"))
	if strings.Contains(tsconfigBody, "packages/ui-web") {
		t.Errorf("workspaces=false tsconfig should not reference packages/ui-web, got:\n%s", tsconfigBody)
	}

	// package.json must NOT carry the workspace dep on ui-web.
	pkgBody := mustRead(t, filepath.Join(dir, "frontends", "web", "package.json"))
	if strings.Contains(pkgBody, "ui-web") {
		t.Errorf("workspaces=false package.json should not reference ui-web, got:\n%s", pkgBody)
	}
}

// TestToPascalCase pins the snake_case → PascalCase converter used to
// derive named exports for the ui-web barrel. Component file names
// (`data_table`, `login_form`) become named exports (`DataTable`,
// `LoginForm`) consumers can `import { ... }` ergonomically.
func TestToPascalCase(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"button", "Button"},
		{"data_table", "DataTable"},
		{"login_form", "LoginForm"},
		{"row_actions_menu", "RowActionsMenu"},
		{"", ""},
		{"x", "X"},
	}
	for _, tt := range tests {
		if got := toPascalCase(tt.in); got != tt.want {
			t.Errorf("toPascalCase(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
