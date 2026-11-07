package templates

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const builtWithForgeURL = "https://reliantlabs.io/?utm_source=forge&utm_medium=app-footer&utm_campaign=built-with-forge"

// builtWithForgeShells are the scaffolded shells that carry the "Built with
// Forge" credit: the app shells (Next.js, Vite SPA) and the static-site
// starters. Expo is deliberately absent.
var builtWithForgeShells = []string{
	filepath.Join("nextjs", "src", "components", "app_shell.tsx.tmpl"),
	filepath.Join("vite-spa", "src", "App.tsx.tmpl"),
	filepath.Join("site", "nextjs", "src", "app", "layout.tsx.tmpl"),
	filepath.Join("site", "vite", "src", "App.tsx.tmpl"),
}

var builtWithForgeLinkRE = regexp.MustCompile(`(?s)<a\s[^>]*href=\{BUILT_WITH_FORGE_URL\}[^>]*>\s*Forge\s*</a>`)

// TestScaffoldedShellsCarryBuiltWithForgeCredit: a fresh scaffold ends every
// page with a small "Built with ❤️ using Forge" credit whose link carries the
// UTM, opens in a new tab with rel="noopener", and is plain markup — no
// image, no script — so it survives static export.
func TestScaffoldedShellsCarryBuiltWithForgeCredit(t *testing.T) {
	t.Parallel()
	data := FrontendTemplateData{FrontendName: "web", ProjectName: "shop"}
	for _, shell := range builtWithForgeShells {
		out, err := FrontendTemplates().Render(shell, data)
		if err != nil {
			t.Fatalf("render %s: %v", shell, err)
		}
		src := string(out)

		if !strings.Contains(src, `const BUILT_WITH_FORGE_URL =`) || !strings.Contains(src, `"`+builtWithForgeURL+`"`) {
			t.Errorf("%s: no single BUILT_WITH_FORGE_URL constant set to %s", shell, builtWithForgeURL)
		}
		if n := strings.Count(src, "reliantlabs.io"); n != 1 {
			t.Errorf("%s: the marketing URL must be spelled once (the constant), found %d", shell, n)
		}
		if !strings.Contains(src, "<footer") {
			t.Errorf("%s: no <footer> for the credit", shell)
		}
		if !strings.Contains(src, "Built with ❤️ using") {
			t.Errorf("%s: credit text %q missing", shell, "Built with ❤️ using")
		}
		link := builtWithForgeLinkRE.FindString(src)
		if link == "" {
			t.Errorf("%s: the word Forge is not a real link to BUILT_WITH_FORGE_URL", shell)
			continue
		}
		for _, want := range []string{`target="_blank"`, `rel="noopener"`} {
			if !strings.Contains(link, want) {
				t.Errorf("%s: credit link lacks %s:\n%s", shell, want, link)
			}
		}
		for _, banned := range []string{"next/image", "<img", "<Image", "<script", "dangerouslySetInnerHTML"} {
			if strings.Contains(src, banned) {
				t.Errorf("%s: contains %q — the credit is a plain link, no image or script", shell, banned)
			}
		}
		if strings.Contains(src, "next/image") {
			t.Errorf("%s: imports next/image, which breaks static export", shell)
		}
		if strings.Contains(src, "{{") {
			t.Errorf("%s: unrendered template action", shell)
		}
	}
}

// TestBuiltWithForgeCreditUsesTokensInApps: the app shells paint the credit
// with the palette's semantic tokens (muted ink, so it clears AA in both
// themes), never a raw palette color. The static-site starters have no
// palette; they dim the inherited color instead.
func TestBuiltWithForgeCreditUsesTokensInApps(t *testing.T) {
	t.Parallel()
	data := FrontendTemplateData{FrontendName: "web", ProjectName: "shop"}
	for _, shell := range builtWithForgeShells[:2] {
		out, err := FrontendTemplates().Render(shell, data)
		if err != nil {
			t.Fatalf("render %s: %v", shell, err)
		}
		src := string(out)
		i := strings.Index(src, "<footer")
		if i < 0 {
			t.Fatalf("%s: no <footer>", shell)
		}
		footer := src[i:strings.Index(src, "</footer>")]
		if !strings.Contains(footer, "text-ink-muted") || !strings.Contains(footer, "text-xs") {
			t.Errorf("%s: credit is not small, muted text: %s", shell, footer)
		}
		if hits := rawPaletteClass.FindAllString(footer, -1); len(hits) > 0 {
			t.Errorf("%s: credit uses raw palette colors %v; use the semantic tokens", shell, hits)
		}
		if !strings.Contains(footer, "focus-visible:ring-2") {
			t.Errorf("%s: credit link has no visible focus style", shell)
		}
		// The credit is a shell sibling of the scrolling <main>, not part of
		// the main landmark or its scroll: it opens after </main> closes.
		if end := strings.Index(src, "</main>"); end < 0 || i < end {
			t.Errorf("%s: the credit footer must sit outside the <main> scroller", shell)
		}
		if !strings.Contains(footer, "shrink-0") {
			t.Errorf("%s: the shell footer must not shrink in the flex column", shell)
		}
	}
}

// TestBuiltWithForgeCreditStaysOffAuthScreens: the credit lives in the main
// shell, which the bare /auth routes skip, so sign-in screens do not carry it.
func TestBuiltWithForgeCreditStaysOffAuthScreens(t *testing.T) {
	t.Parallel()
	shell, err := FrontendTemplates().Render(builtWithForgeShells[0], FrontendTemplateData{FrontendName: "web", ProjectName: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	src := string(shell)
	bare := strings.Index(src, "if (isBare(pathname))")
	footer := strings.Index(src, "<footer")
	if bare < 0 || footer < bare {
		t.Error("the credit must be rendered after the bare-route early return, so /auth screens omit it")
	}
	for _, rel := range []string{"layout.tsx"} {
		files, err := ListFrontendTree("nextjs")
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if strings.HasPrefix(f.Rel, "src/app/auth/") && strings.HasSuffix(f.Rel, rel) {
				b, err := FrontendTemplates().Get(f.Path)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(b), "BUILT_WITH_FORGE_URL") {
					t.Errorf("%s carries the credit; it belongs in the main shell", f.Rel)
				}
			}
		}
	}
}
