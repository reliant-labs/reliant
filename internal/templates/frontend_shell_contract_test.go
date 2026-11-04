package templates

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Contract tests for the scaffolded app shell: the declared font actually
// applies, the mock-data banner survives a static export, native controls
// follow the dark palette, and the page has one main landmark. Each guards a
// defect a real scaffold shipped — measured in a browser, not inferred — so
// the assertions pin the mechanism that broke rather than the exact markup.

func renderNextLayout(t *testing.T, data FrontendTemplateData) string {
	t.Helper()
	out, err := FrontendTemplates().Render(filepath.Join("nextjs", "src", "app", "layout.tsx.tmpl"), data)
	if err != nil {
		t.Fatalf("render layout.tsx.tmpl: %v", err)
	}
	return string(out)
}

// openingTag returns the first `<tag ...>` in src, attributes included.
func openingTag(t *testing.T, src, tag string) string {
	t.Helper()
	m := regexp.MustCompile(`<` + tag + `(\s[^>]*)?>`).FindString(src)
	if m == "" {
		t.Fatalf("no <%s> in the rendered layout", tag)
	}
	return m
}

// TestNextLayoutPutsFontVariablesOnHTML pins the fix for a font that was
// declared and never rendered. globals.css declares `--font-sans:
// var(--font-geist-sans), …` inside @theme, which Tailwind emits on :root —
// the <html> element. The loader's variable class used to sit on <body>, so
// at :root the referenced variable was undefined, --font-sans was invalid at
// computed-value time, and every font-* utility fell back to the OS stack.
func TestNextLayoutPutsFontVariablesOnHTML(t *testing.T) {
	t.Parallel()
	layout := renderNextLayout(t, FrontendTemplateData{FrontendName: "web", ProjectName: "shop"})

	for _, want := range []string{`from "geist/font/sans"`, `from "geist/font/mono"`} {
		if !strings.Contains(layout, want) {
			t.Errorf("layout.tsx does not import %s — the scaffold's declared faces are Geist and Geist Mono, self-hosted by the geist package", want)
		}
	}
	if strings.Contains(layout, "next/font/google") {
		t.Error("layout.tsx loads a next/font/google family — that fetches from Google at build time; the declared faces are self-hosted from the geist package")
	}

	html := openingTag(t, layout, "html")
	if !strings.Contains(html, "fontVariables") {
		t.Errorf("<html> does not carry the font variables (%s). They must sit on the element that declares --font-sans (:root), or the token resolves to nothing", html)
	}
	if !strings.Contains(layout, "GeistSans.variable") || !strings.Contains(layout, "GeistMono.variable") {
		t.Error("fontVariables no longer names GeistSans.variable and GeistMono.variable")
	}
	body := openingTag(t, layout, "body")
	if strings.Contains(body, ".variable") || strings.Contains(body, "fontVariables") {
		t.Errorf("<body> carries a font variable (%s): below :root it cannot define what --font-sans reads", body)
	}

	css := renderPalette(t, "nextjs")
	for _, want := range []string{"--font-sans: var(--font-geist-sans)", "--font-mono: var(--font-geist-mono)"} {
		if !strings.Contains(css, want) {
			t.Errorf("globals.css does not declare %q — the token must read the variable the geist loader sets", want)
		}
	}
}

// TestViteScaffoldSelfHostsGeist: the SPA has no next/font, so the faces come
// from @fontsource-variable, imported once in main.tsx and named by index.css.
func TestViteScaffoldSelfHostsGeist(t *testing.T) {
	t.Parallel()
	main, err := FrontendTemplates().Get(filepath.Join("vite-spa", "src", "main.tsx"))
	if err != nil {
		t.Fatalf("read vite-spa main.tsx: %v", err)
	}
	for _, want := range []string{`import "@fontsource-variable/geist";`, `import "@fontsource-variable/geist-mono";`} {
		if !strings.Contains(string(main), want) {
			t.Errorf("vite-spa main.tsx is missing %s", want)
		}
	}
	css := renderPalette(t, "vite-spa")
	for _, want := range []string{`--font-sans: "Geist Variable"`, `--font-mono: "Geist Mono Variable"`} {
		if !strings.Contains(css, want) {
			t.Errorf("vite-spa index.css does not declare %s", want)
		}
	}
}

// TestMockBannerRendersClientSide pins the fix for a banner that a static
// export compiled out. The server layout evaluated loadConfig() at BUILD
// time, where window.__FORGE_CONFIG__ does not exist, so the banner rendered
// nothing into out/*.html — while connect.ts, deciding in the browser from
// config.js, served fixtures. The banner must read the config on the client.
func TestMockBannerRendersClientSide(t *testing.T) {
	t.Parallel()
	data := FrontendTemplateData{FrontendName: "web", ProjectName: "shop", HasMockAPI: true, HasTypedConfig: true}

	layout := renderNextLayout(t, data)
	if strings.Contains(layout, "loadConfig") {
		t.Error("the server layout reads loadConfig() — at build time that sees no config.js, and the static export loses the banner")
	}
	if !strings.Contains(layout, `from "@/components/mock_mode_banner"`) {
		t.Error("layout.tsx no longer imports the client MockModeBanner")
	}

	out, err := FrontendTemplates().Render(filepath.Join("nextjs", "src", "components", "mock_mode_banner.tsx.tmpl"), data)
	if err != nil {
		t.Fatalf("render mock_mode_banner.tsx.tmpl: %v", err)
	}
	banner := string(out)
	if !strings.HasPrefix(banner, `"use client";`) {
		t.Error(`mock_mode_banner.tsx must start with "use client" — it reads window.__FORGE_CONFIG__`)
	}
	// The server snapshot is what the prerender emits and the client snapshot
	// is what the browser renders after hydration; without the split, the
	// first client render disagrees with the HTML and React reports a
	// hydration mismatch.
	if !strings.Contains(banner, "useSyncExternalStore") || !strings.Contains(banner, "loadConfig().MOCK_API") {
		t.Error("mock_mode_banner.tsx no longer reads loadConfig().MOCK_API through useSyncExternalStore")
	}
}

// TestPaletteDeclaresColorScheme: without color-scheme the browser draws
// native <select> popups, date pickers and scrollbars light-on-dark.
func TestPaletteDeclaresColorScheme(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"nextjs", "vite-spa"} {
		css := renderPalette(t, kind)
		for _, want := range []*regexp.Regexp{
			regexp.MustCompile(`(?m)^:root \{\s*color-scheme: light dark;`),
			regexp.MustCompile(`:root\[data-theme="light"\] \{\s*color-scheme: light;`),
			regexp.MustCompile(`:root\[data-theme="dark"\] \{\s*color-scheme: dark;`),
		} {
			if !want.MatchString(css) {
				t.Errorf("%s palette has no rule matching %s", kind, want)
			}
		}
	}
}

// TestShellHasOneMainLandmarkAndASkipLink: the shell owns the page's only
// <main>, a skip link reaches it, and the 404 / error pages — rendered inside
// that main — do not open a second one.
func TestShellHasOneMainLandmarkAndASkipLink(t *testing.T) {
	t.Parallel()
	data := FrontendTemplateData{FrontendName: "web", ProjectName: "shop"}
	for _, shell := range []string{
		filepath.Join("nextjs", "src", "components", "app_shell.tsx.tmpl"),
		filepath.Join("vite-spa", "src", "App.tsx.tmpl"),
	} {
		out, err := FrontendTemplates().Render(shell, data)
		if err != nil {
			t.Fatalf("render %s: %v", shell, err)
		}
		src := string(out)
		if !strings.Contains(src, `<main id="main"`) {
			t.Errorf("%s: <main> has no id=\"main\" for the skip link to target", shell)
		}
		if !strings.Contains(src, `href="#main"`) {
			t.Errorf("%s: no skip link to #main", shell)
		}
	}
	for _, page := range []string{"not-found.tsx", "error.tsx"} {
		src, err := FrontendTemplates().Get(filepath.Join("nextjs", "src", "app", page))
		if err != nil {
			t.Fatalf("read %s: %v", page, err)
		}
		if strings.Contains(string(src), "<main") {
			t.Errorf("%s renders a <main> inside the shell's <main> — use a <section>", page)
		}
	}
}
