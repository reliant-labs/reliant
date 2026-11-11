package templates

import (
	"regexp"
	"strings"
	"testing"
)

// Source maps hold the app's original sources. The scaffolded build configs
// emit them ONLY when forge asks (FORGE_SOURCEMAPS=1, set by the static stage,
// which then moves them out of the served tree). A config that emitted them
// unconditionally would put them in every other build of the same frontend —
// a Docker image, a local `npm run build` — where nothing takes them out.
//
// Only the static export goes through the static stage, so only that Next
// branch is gated; standalone and server builds emit no browser maps at all.
func TestFrontendBuildConfigsEmitMapsOnlyWhenForgeAsks(t *testing.T) {
	cases := []struct {
		tmpl    string
		want    *regexp.Regexp
		forbids []string
	}{
		{
			tmpl:    "vite-spa/vite.config.ts.tmpl",
			want:    regexp.MustCompile(`sourcemap:\s*process\.env\.FORGE_SOURCEMAPS === "1"`),
			forbids: []string{"sourcemap: true", `sourcemap: "hidden"`},
		},
		{
			tmpl:    "nextjs/next.config.ts.tmpl",
			want:    regexp.MustCompile(`productionBrowserSourceMaps:\s*process\.env\.FORGE_SOURCEMAPS === "1"`),
			forbids: []string{"productionBrowserSourceMaps: true"},
		},
	}
	for _, c := range cases {
		t.Run(c.tmpl, func(t *testing.T) {
			out, err := FrontendTemplates().Render(c.tmpl, FrontendTemplateData{Output: "static"})
			if err != nil {
				t.Fatalf("render %s: %v", c.tmpl, err)
			}
			body := string(out)
			if strings.Contains(c.tmpl, "next") && strings.Contains(body, "output: \"standalone\",") {
				t.Fatal("rendered the standalone branch; the static one is under test")
			}
			if !c.want.MatchString(body) {
				t.Errorf("%s does not gate source maps on FORGE_SOURCEMAPS=1", c.tmpl)
			}
			for _, bad := range c.forbids {
				if strings.Contains(body, bad) {
					t.Errorf("%s contains %q: maps would be emitted by every build", c.tmpl, bad)
				}
			}
		})
	}
}
