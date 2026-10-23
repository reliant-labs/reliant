package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGenerateFrontendFiles_DefaultsToStatic verifies the new-scaffold
// default: a Next.js frontend scaffolded WITHOUT FrontendGenOptions.Output
// set is a static export — next.config.ts gates `output: "export"` on
// production, and the Dockerfile serves out/ from nginx.
//
// Static is the default because it ships everywhere: the hosted runtime
// serves a frontend ONLY as a static site, and a bucket or CDN takes the
// same out/. It became possible once the generated CRUD pages stopped
// using `[id]` segments (cli.TestGenerateFrontendPages_NextjsRoutesAreStatic);
// before that a static default failed `npm run build` on the first entity.
func TestGenerateFrontendFiles_DefaultsToStatic(t *testing.T) {
	dir := t.TempDir()
	if err := GenerateFrontendFiles(dir, "example.com/myapp", "myapp", "web", 8080, ""); err != nil {
		t.Fatalf("GenerateFrontendFiles: %v", err)
	}
	assertStaticExportFrontend(t, filepath.Join(dir, "frontends", "web"))
}

// TestGenerateFrontendFiles_Static verifies that passing Output="static"
// explicitly yields the same static-export shape as the default.
func TestGenerateFrontendFiles_Static(t *testing.T) {
	dir := t.TempDir()
	if err := GenerateFrontendFilesWithOptions(
		dir, "example.com/myapp", "myapp", "web", 8080, "",
		FrontendGenOptions{Output: "static"},
	); err != nil {
		t.Fatalf("GenerateFrontendFilesWithOptions: %v", err)
	}
	assertStaticExportFrontend(t, filepath.Join(dir, "frontends", "web"))
}

// assertStaticExportFrontend checks the two files a frontend's output shape
// renders into: next.config.ts exports in production, and the Dockerfile
// serves the export instead of running a Node server that is not there.
func assertStaticExportFrontend(t *testing.T, feDir string) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(feDir, "next.config.ts"))
	if err != nil {
		t.Fatalf("read next.config.ts: %v", err)
	}
	s := string(body)

	want := `...(process.env.NODE_ENV === "production" ? { output: "export" } : {}),`
	if !strings.Contains(s, want) {
		t.Errorf("next.config.ts must contain the NODE_ENV-gated static-export shape %q; got:\n%s", want, s)
	}
	// No active standalone wiring in static mode (the literal may
	// appear in explanatory comments).
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") {
			continue
		}
		if strings.Contains(trimmed, `output: "standalone"`) {
			t.Errorf("next.config.ts emitted active `output: \"standalone\"` line outside comments; got:\n%s\nfull file:\n%s", trimmed, s)
		}
	}

	dockerfile, err := os.ReadFile(filepath.Join(feDir, "Dockerfile"))
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	if d := string(dockerfile); !strings.Contains(d, "/out ") || strings.Contains(d, ".next-prod/standalone") {
		t.Errorf("Dockerfile must serve the static export (out/), not copy a standalone server; got:\n%s", d)
	}
}

// TestGenerateFrontendFiles_StandaloneOptIn verifies the explicit opt-in to
// a Node server: `output: "standalone"` with a workspace-rooted
// outputFileTracingRoot, the shape the standalone Dockerfile copies
// (.next-prod/standalone/server.js).
func TestGenerateFrontendFiles_StandaloneOptIn(t *testing.T) {
	dir := t.TempDir()
	if err := GenerateFrontendFilesWithOptions(
		dir, "example.com/myapp", "myapp", "web", 8080, "",
		FrontendGenOptions{Output: "standalone"},
	); err != nil {
		t.Fatalf("GenerateFrontendFilesWithOptions: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(dir, "frontends", "web", "next.config.ts"))
	if err != nil {
		t.Fatalf("read next.config.ts: %v", err)
	}
	s := string(body)

	if !strings.Contains(s, `output: "standalone"`) {
		t.Errorf("next.config.ts (Output=standalone) must contain `output: \"standalone\"`; got:\n%s", s)
	}
	if !strings.Contains(s, `outputFileTracingRoot`) {
		t.Errorf("next.config.ts (Output=standalone) must contain outputFileTracingRoot; got:\n%s", s)
	}
	// It must resolve an ANCESTOR of the frontend, never the frontend
	// itself: traced files land at <distDir>/standalone/<path relative to
	// the root>, so a frontend-pinned root turns the hoisted workspace
	// install of `next` into `../../node_modules/next/...` and writes a
	// pruned copy back into frontends/<name>/node_modules, which shadows
	// the real one on the next build.
	if strings.Contains(s, `outputFileTracingRoot: path.join(__dirname)`) {
		t.Errorf("next.config.ts pins outputFileTracingRoot to the frontend dir — that makes each build poison the next; got:\n%s", s)
	}
	if strings.Contains(s, `{ output: "export" }`) {
		t.Errorf("next.config.ts (Output=standalone) emitted the static-export conditional; got:\n%s", s)
	}
}

// TestGenerateFrontendFiles_RealBackendByDefault pins the J-round fix 4:
// the scaffold must NOT ship with mock mode silently enabled. Since the
// hand-editable .env.local was ripped out, the guarantee is now
// STRUCTURAL: no committed dotenv can bake mock on, and connect.ts gates
// mock strictly on the NEXT_PUBLIC_MOCK_API env var (unset => real
// backend). When mock IS on the generated layout renders a visible "MOCK
// DATA" banner so a working-looking UI can never masquerade as a working
// stack.
func TestGenerateFrontendFiles_RealBackendByDefault(t *testing.T) {
	dir := t.TempDir()
	if err := GenerateFrontendFiles(dir, "example.com/myapp", "myapp", "web", 8080, ""); err != nil {
		t.Fatalf("GenerateFrontendFiles: %v", err)
	}
	feDir := filepath.Join(dir, "frontends", "web")

	// The hand-editable .env.local is gone — mock mode can no longer be
	// silently baked into a committed dotenv; it is an explicit runtime
	// env-var opt-in (or KCL config).
	if _, err := os.Stat(filepath.Join(feDir, ".env.local")); !os.IsNotExist(err) {
		t.Errorf("scaffold must not ship a .env.local (stat err = %v); mock is a runtime env opt-in", err)
	}

	// connect.ts is the structural guard: mock engages ONLY when
	// NEXT_PUBLIC_MOCK_API is explicitly set, so the default (unset) is the
	// real backend.
	connect, err := os.ReadFile(filepath.Join(feDir, "src", "lib", "connect.ts"))
	if err != nil {
		t.Fatalf("read connect.ts: %v", err)
	}
	if !strings.Contains(string(connect), "NEXT_PUBLIC_MOCK_API") {
		t.Error("connect.ts must gate mock mode on NEXT_PUBLIC_MOCK_API (real backend is the unset default)")
	}

	layout, err := os.ReadFile(filepath.Join(feDir, "src", "app", "layout.tsx"))
	if err != nil {
		t.Fatalf("read layout.tsx: %v", err)
	}
	if !strings.Contains(string(layout), "MOCK DATA — backend not connected") {
		t.Error("layout.tsx should render the mock-mode banner when NEXT_PUBLIC_MOCK_API is enabled")
	}
	if !strings.Contains(string(layout), "MockModeBanner") {
		t.Error("layout.tsx should mount MockModeBanner")
	}
}

// TestGenerateFrontendFiles_ViteRealBackendByDefault is the vite-spa
// flavor of the same pin.
func TestGenerateFrontendFiles_ViteRealBackendByDefault(t *testing.T) {
	dir := t.TempDir()
	if err := GenerateFrontendFiles(dir, "example.com/myapp", "myapp", "web", 8080, "vite-spa"); err != nil {
		t.Fatalf("GenerateFrontendFiles: %v", err)
	}
	feDir := filepath.Join(dir, "frontends", "web")

	if _, err := os.Stat(filepath.Join(feDir, ".env.local")); !os.IsNotExist(err) {
		t.Errorf("scaffold must not ship a .env.local (stat err = %v); mock is a runtime env opt-in", err)
	}

	connect, err := os.ReadFile(filepath.Join(feDir, "src", "lib", "connect.ts"))
	if err != nil {
		t.Fatalf("read connect.ts: %v", err)
	}
	if !strings.Contains(string(connect), "VITE_MOCK_API") {
		t.Error("connect.ts must gate mock mode on VITE_MOCK_API (real backend is the unset default)")
	}

	app, err := os.ReadFile(filepath.Join(feDir, "src", "App.tsx"))
	if err != nil {
		t.Fatalf("read App.tsx: %v", err)
	}
	if !strings.Contains(string(app), "MOCK DATA — backend not connected") {
		t.Error("App.tsx should render the mock-mode banner when VITE_MOCK_API is enabled")
	}
}
