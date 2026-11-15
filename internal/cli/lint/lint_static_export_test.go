package lint

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/linter/staticexport"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func loadTestConfig(t *testing.T, yaml string) *config.ProjectConfig {
	t.Helper()
	cfg, err := config.LoadProject([]byte(yaml), "forge.yaml")
	if err != nil {
		t.Fatalf("load forge.yaml: %v", err)
	}
	return cfg
}

const staticExportForgeYAML = `name: acme
module_path: github.com/example/acme
`

// staticExportFrontends is the inventory the lane judges. The inventory is
// derived from frontends/<name> on disk, never declared in forge.yaml, so a
// test that wants a particular shape sets it on the loaded config.
func staticExportFrontends() []config.FrontendConfig {
	return []config.FrontendConfig{
		config.FrontendConfig{Name: "web", Type: "nextjs", Output: "standalone"}.WithDir("frontends/web"),
		config.FrontendConfig{Name: "admin", Type: "nextjs", Output: "static"}.WithDir("frontends/admin"),
		config.FrontendConfig{Name: "docs", Type: "nextjs", Output: "standalone"}.WithDir("frontends/docs"),
		config.FrontendConfig{Name: "spa", Type: "vite-spa"}.WithDir("frontends/spa"),
	}
}

func loadStaticExportConfig(t *testing.T) *config.ProjectConfig {
	t.Helper()
	cfg := loadTestConfig(t, staticExportForgeYAML)
	cfg.Frontends = staticExportFrontends()
	return cfg
}

// TestStaticExportTargets: the lane judges a Next.js frontend when it is
// detected as a static export OR any env binds it to a static runtime — and nothing
// else. A server build nobody ships statically is none of its business, and
// a Vite app has no server features to find.
func TestStaticExportTargets(t *testing.T) {
	cfg := loadStaticExportConfig(t)
	bindings := func(context.Context, string, *config.ProjectConfig) map[string][]staticexport.Binding {
		return map[string][]staticexport.Binding{
			"web": {{Env: "prod", Runtime: "hosted"}},
			"spa": {{Env: "prod", Runtime: "bucket"}},
		}
	}
	targets := staticExportTargets(context.Background(), "/p", cfg, bindings)
	var got []string
	for _, tg := range targets {
		got = append(got, tg.Name+":"+tg.RelDir)
	}
	want := "web:frontends/web admin:frontends/admin"
	if strings.Join(got, " ") != want {
		t.Errorf("targets = %v, want %s", got, want)
	}
	if len(targets) > 0 && (len(targets[0].Bindings) != 1 || targets[0].Bindings[0].Env != "prod") {
		t.Errorf("web's binding was not carried: %+v", targets[0].Bindings)
	}
}

// TestStaticExportFindings_EndToEnd runs the lane over a real tree: the
// findings are project-relative (what --scope matches), errors gate, and a
// reasoned suppression directive silences exactly its line.
func TestStaticExportFindings_EndToEnd(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"frontends/admin/next.config.ts":             "const nextConfig = {\n  ...(process.env.NODE_ENV === \"production\" ? { output: \"export\" } : {}),\n  images: { unoptimized: true },\n};\nexport default nextConfig;\n",
		"frontends/admin/src/app/jobs/[id]/page.tsx": "\"use client\";\nexport default function P() { return null }\n",
		"frontends/admin/src/app/api/x/route.ts":     "// forge:lint-disable-next-line static-export-route-handler: posted to only by the dev tooling\nexport async function POST() { return new Response(null) }\n",
	})
	cfg := loadStaticExportConfig(t)
	none := func(context.Context, string, *config.ProjectConfig) map[string][]staticexport.Binding { return nil }
	fs, judged, err := staticExportFindings(context.Background(), root, cfg, none)
	if err != nil {
		t.Fatal(err)
	}
	if judged != 1 {
		t.Errorf("judged %d frontends, want 1 (admin)", judged)
	}
	if len(fs) != 1 {
		t.Fatalf("findings = %+v, want exactly the dynamic route", fs)
	}
	f := fs[0]
	if f.Rule != staticexport.RuleDynamicSegment || f.File != "frontends/admin/src/app/jobs/[id]/page.tsx" || f.Line != 2 {
		t.Errorf("finding = %s %s:%d", f.Rule, f.File, f.Line)
	}
	scope, err := parseLintScope(root, []string{"frontends/admin/src/app/jobs"})
	if err != nil {
		t.Fatal(err)
	}
	if !scope.contains(f.File) {
		t.Errorf("--scope frontends/admin/src/app/jobs does not contain %s", f.File)
	}
	js := findingsToJSON(fs)
	if !anyErrorFinding(js) {
		t.Error("a dynamic route the export refuses must gate")
	}
}

// TestRenderStaticBindings_SeesAServerBuildOnAStaticRuntime: the lane learns
// bindings from each env's REAL render — and that render must not trip the
// render-time refusal of the very binding the lane exists to explain, which
// is why it does not bind frontend_outputs.
func TestRenderStaticBindings_SeesAServerBuildOnAStaticRuntime(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL; skipped in -short")
	}
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"forge.yaml":         staticExportForgeYAML,
		"deploy/kcl/kcl.mod": "[package]\nname = \"acme_deploy\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n",
		"deploy/kcl/prod/main.k": `import forge

# Declared in every env file, used in none here: a mention is not a binding.
_on_bucket = lambda f: forge.Frontend -> forge.Frontend {
    f | {runtime = forge.OnBucket {bucket = "acme"}}
}
_hosted_frontend = lambda f: forge.Frontend -> forge.Frontend {
    f | {image = "ghcr.io/acme/web", runtime = forge.OnHosted {}}
}
_web = forge.Frontend {name = "web", path = "frontends/web", public_dir = "out"}

output = forge.render(forge.Bundle {
    project = "acme"
    control_plane = forge.ControlPlane {endpoint = "https://cp.example"}
    frontends = [_hosted_frontend(_web)]
})
`,
		"deploy/kcl/dev/main.k": `import forge

output = forge.render(forge.Bundle {
    project = "acme"
    frontends = [forge.Frontend {name = "web", path = "frontends/web", runtime = forge.OnHost {}}]
})
`,
	})
	cfg := loadStaticExportConfig(t)
	got := renderStaticBindings(context.Background(), root, cfg)
	if len(got) != 1 || len(got["web"]) != 1 || got["web"][0] != (staticexport.Binding{Env: "prod", Runtime: "hosted"}) {
		t.Errorf("bindings = %+v, want web on hosted in prod only", got)
	}
}

func TestRenderedFrontendRuntimes(t *testing.T) {
	raw := []byte(`{"output":{"frontends":[{"name":"web","runtime":{"type":"hosted"}},{"name":"dev","runtime":{"type":"host"}}]}}`)
	got := renderedFrontendRuntimes(raw)
	if len(got) != 2 || got[0] != (renderedFrontend{"web", "hosted"}) || got[1] != (renderedFrontend{"dev", "host"}) {
		t.Errorf("parsed %+v", got)
	}
	if renderedFrontendRuntimes([]byte("not json")) != nil {
		t.Error("unparseable render must yield nothing")
	}
}
