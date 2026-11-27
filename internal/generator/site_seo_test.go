package generator

import (
	"bytes"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/siteaudit"
)

// writeStarterSite writes a whole greenfield site of kind ("nextjs" or
// "vite-spa"), starter and forge files, as `forge project new --kind site`
// does, and returns its root.
func writeStarterSite(t *testing.T, kind string) string {
	t.Helper()
	fe, ok := SiteStarterFrontend(kind, "web")
	if !ok {
		t.Fatalf("no starter for %s", kind)
	}
	dir := t.TempDir()
	if err := WriteSiteStarter(dir, "landing", fe); err != nil {
		t.Fatal(err)
	}
	if err := WriteSiteScaffold(dir, "landing", HarnessNone, fe); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The placeholder favicon and social card are what a fresh site links, and
// both carry siteaudit's marker, so the audit names a site that still ships
// them (og_image_placeholder) instead of passing them as the site's own.
func TestSiteStarterPlaceholderImages(t *testing.T) {
	for _, kind := range SiteStarterKindNames() {
		t.Run(kind, func(t *testing.T) {
			dir := writeStarterSite(t, kind)
			og, err := os.ReadFile(filepath.Join(dir, "public", "og.png"))
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := png.DecodeConfig(bytes.NewReader(og))
			if err != nil {
				t.Fatalf("public/og.png is not a PNG: %v", err)
			}
			if cfg.Width != 1200 || cfg.Height != 630 {
				t.Errorf("public/og.png is %dx%d, want the 1200x630 social card size", cfg.Width, cfg.Height)
			}
			for _, rel := range []string{"public/og.png", "public/favicon.svg"} {
				b, err := os.ReadFile(filepath.Join(dir, rel))
				if err != nil {
					t.Fatal(err)
				}
				if !siteaudit.IsPlaceholderImage(b) {
					t.Errorf("%s lacks %q, so the site audit cannot name it as the placeholder", rel, siteaudit.PlaceholderImageMarker)
				}
			}
		})
	}
}

// Both starters generate robots.txt at build time (its Sitemap: line needs
// the absolute SITE_URL), so neither ships a static one: beside Next.js's
// src/app/robots.ts it is a "conflicting public file and page file" build
// failure, and Vite's forgeSiteFiles() never overwrites it, so it would
// silently replace the generated one.
func TestSiteStartersHaveNoPublicRobots(t *testing.T) {
	for _, kind := range SiteStarterKindNames() {
		dir := writeStarterSite(t, kind)
		if _, err := os.Stat(filepath.Join(dir, "public", "robots.txt")); err == nil {
			t.Errorf("the %s site starter ships public/robots.txt, which the build generates", kind)
		}
	}
	vite, err := os.ReadFile(filepath.Join(writeStarterSite(t, "vite-spa"), "vite.config.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(vite), "forgeSiteFiles({ siteUrl: process.env.SITE_URL }),") {
		t.Errorf("the Vite site starter does not write robots.txt and sitemap.xml:\n%s", vite)
	}
	dir := writeStarterSite(t, "nextjs")
	for _, rel := range []string{"src/app/robots.ts", "src/app/sitemap.ts"} {
		b, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), `export const dynamic = "force-static";`) {
			t.Errorf("%s must be force-static, or the static export refuses it", rel)
		}
	}
}

// siteSEOGoldenFiles are the files of a fresh site the SEO and accessibility
// defaults live in, per starter kind. None depends on the web runtime floor
// (package.json and vite.config.ts do, and the release bookkeeping raises the
// floor without regenerating goldens), so a floor bump does not churn them.
// TestWriteSiteStarter checks package.json's eslint script and runtime range.
var siteSEOGoldenFiles = map[string][]string{
	"vite-spa": {"index.html", "eslint.config.mjs", "deploy/kcl/dev/main.k", "deploy/kcl/prod/main.k"},
	"nextjs": {"src/app/layout.tsx", "src/app/page.tsx", "src/app/robots.ts", "src/app/sitemap.ts", "src/lib/site.ts",
		"eslint.config.mjs"},
}

// Each starter lints with eslint and the jsx-a11y plugin its config loads.
func TestSiteStarterDeclaresItsLint(t *testing.T) {
	for kind, deps := range map[string][]string{
		"vite-spa": {"eslint", "@eslint/js", "eslint-plugin-jsx-a11y", "typescript-eslint"},
		"nextjs":   {"eslint", "@eslint/js", "eslint-plugin-jsx-a11y", "eslint-config-next"},
	} {
		pkg, err := os.ReadFile(filepath.Join(writeStarterSite(t, kind), "package.json"))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range append([]string{`"lint": "eslint ."`}, deps...) {
			if !strings.Contains(string(pkg), `"`+strings.Trim(want, `"`)) {
				t.Errorf("%s package.json lacks %s:\n%s", kind, want, pkg)
			}
		}
	}
}

// TestSiteStarterSEOGoldens pins the rendered head tags, metadata, robots and
// sitemap routes, eslint config and env declarations of both starters.
// FORGE_UPDATE_GOLDEN=1 rewrites testdata/site_starter.
func TestSiteStarterSEOGoldens(t *testing.T) {
	for kind, files := range siteSEOGoldenFiles {
		t.Run(kind, func(t *testing.T) {
			dir := writeStarterSite(t, kind)
			for _, rel := range files {
				got, err := os.ReadFile(filepath.Join(dir, rel))
				if err != nil {
					t.Fatal(err)
				}
				golden := filepath.Join("testdata", "site_starter", kind, rel+".golden")
				if os.Getenv("FORGE_UPDATE_GOLDEN") == "1" {
					if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(golden, got, 0o644); err != nil {
						t.Fatal(err)
					}
					continue
				}
				want, err := os.ReadFile(golden)
				if err != nil {
					t.Fatalf("read %s (FORGE_UPDATE_GOLDEN=1 writes it): %v", golden, err)
				}
				if !bytes.Equal(got, want) {
					t.Errorf("%s differs from %s; if the change is intended, regenerate: "+
						"FORGE_UPDATE_GOLDEN=1 go test ./internal/generator -run TestSiteStarterSEOGoldens\n--- got ---\n%s", rel, golden, got)
				}
			}
		})
	}
}
