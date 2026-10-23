package templates

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// TestNextjsTsconfigNeedsNoRewriteFromNext is the reproduction for "running
// `next dev` rewrites the tracked tsconfig.json".
//
// Next runs a setup check (next/dist/lib/typescript/writeConfigurationDefaults)
// on every `next dev` and `next build`, and REWRITES tsconfig.json — reformatted
// as JSON.stringify(…, null, 2) — whenever it finds anything to change. The
// scaffold shipped `"jsx": "preserve"`, which Next 16 overrides to `react-jsx`
// as a mandatory value, and omitted the type globs it appends to `include`, so
// every dev run left a diff in a committed file. When nothing is missing, Next
// does not write at all, and the file stays byte-identical.
//
// This mirrors that check as of Next 16.3, so it fails for any value Next
// would change rather than for one remembered symptom.
func TestNextjsTsconfigNeedsNoRewriteFromNext(t *testing.T) {
	t.Parallel()

	for _, workspaces := range []bool{false, true} {
		out, err := FrontendTemplates().Render("nextjs/tsconfig.json.tmpl",
			FrontendTemplateData{FrontendName: "web", ProjectName: "demo", Workspaces: workspaces})
		if err != nil {
			t.Fatalf("render nextjs/tsconfig.json.tmpl: %v", err)
		}
		var cfg struct {
			CompilerOptions map[string]any `json:"compilerOptions"`
			Include         []string       `json:"include"`
			Exclude         []string       `json:"exclude"`
		}
		if err := json.Unmarshal(out, &cfg); err != nil {
			t.Fatalf("workspaces=%v: rendered tsconfig is not JSON: %v\n%s", workspaces, err, out)
		}

		// Mandatory: Next overwrites any other value. moduleResolution and
		// module accept several values; these are the ones forge ships.
		mandatory := map[string]any{
			"jsx":               "react-jsx",
			"module":            "esnext",
			"moduleResolution":  "bundler",
			"esModuleInterop":   true,
			"resolveJsonModule": true,
			"isolatedModules":   true,
		}
		for key, want := range mandatory {
			got := cfg.CompilerOptions[key]
			if s, ok := got.(string); ok {
				got = strings.ToLower(s)
			}
			if got != want {
				t.Errorf("workspaces=%v: compilerOptions.%s = %v; Next rewrites it to %v", workspaces, key, cfg.CompilerOptions[key], want)
			}
		}

		// Suggested: Next adds the key when it is absent, whatever its value.
		for _, key := range []string{"target", "lib", "allowJs", "skipLibCheck", "strict", "noEmit", "incremental"} {
			if _, ok := cfg.CompilerOptions[key]; !ok {
				t.Errorf("workspaces=%v: compilerOptions.%s is absent; Next adds it", workspaces, key)
			}
		}

		// include must already hold the route-type globs Next appends:
		// <distDir>/types and <distDir>/dev/types, for BOTH distDirs
		// next.config.ts selects — .next under `next dev`, .next-prod under
		// `next build`.
		for _, distDir := range []string{".next", ".next-prod"} {
			for _, glob := range []string{distDir + "/types/**/*.ts", distDir + "/dev/types/**/*.ts"} {
				if !slices.Contains(cfg.Include, glob) {
					t.Errorf("workspaces=%v: include lacks %q; Next appends it", workspaces, glob)
				}
			}
		}

		plugins, _ := cfg.CompilerOptions["plugins"].([]any)
		hasNextPlugin := slices.ContainsFunc(plugins, func(p any) bool {
			m, ok := p.(map[string]any)
			return ok && m["name"] == "next"
		})
		if !hasNextPlugin {
			t.Errorf("workspaces=%v: compilerOptions.plugins lacks {name: next}; Next adds it", workspaces)
		}
		if cfg.Exclude == nil {
			t.Errorf("workspaces=%v: exclude is absent; Next adds it", workspaces)
		}

		// The route-type stubs stay out of `tsc --noEmit` all the same:
		// TypeScript's exclude filters what include's globs match.
		for _, dir := range []string{".next", ".next-prod"} {
			if !slices.Contains(cfg.Exclude, dir) {
				t.Errorf("workspaces=%v: exclude lacks %q", workspaces, dir)
			}
		}
	}
}
