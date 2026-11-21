package staticexport

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/linter/finding"
)

// scaffoldViteConfig is the shape forge's vite-spa scaffold writes: the
// function form returning an object, a build block with no outDir, and
// spreads and ternaries that sit inside other keys.
const scaffoldViteConfig = `import { defineConfig } from "vitest/config";
// build: { outDir: "out" } — a comment must not count.

export default defineConfig(({ command, mode }) => {
  const devApiProxy = command === "serve" && mode !== "test" ? "/_api" : "";
  return {
    define: devApiProxy
      ? { "import.meta.env.FORGE_DEV_API_PROXY": JSON.stringify(devApiProxy) }
      : {},
    plugins: [react()],
    build: {
      sourcemap: process.env.FORGE_SOURCEMAPS === "1",
    },
    server: {
      proxy: {
        ...(devApiProxy ? { [devApiProxy]: { target: "x", rewrite: (p: string) => p.slice(1) } } : {}),
      },
    },
    test: { environment: "jsdom", root: "src" },
  };
});
`

const vitePackageJSON = `{
  "name": "spa",
  "scripts": {
    "dev": "vite",
    "build": "tsc -b && vite build"
  },
  "dependencies": {
    "react": "^19.0.0"
  }
}
`

// viteFrontend is a Vite frontend on disk bound to hosted in each env, with
// the given public_dir and base_path.
func viteFrontend(t *testing.T, files map[string]string, bindings ...Binding) Frontend {
	t.Helper()
	if _, ok := files["package.json"]; !ok {
		files["package.json"] = vitePackageJSON
	}
	fe := frontendTree(t, files)
	fe.Name, fe.RelDir = "spa", "frontends/spa"
	if len(bindings) == 0 {
		bindings = []Binding{{Env: "prod", Runtime: "hosted", PublicDir: "dist"}}
	}
	fe.Bindings = bindings
	return fe
}

func checkVite(t *testing.T, fe Frontend) ViteResult {
	t.Helper()
	res, err := CheckVite(fe)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func notesContain(res ViteResult, want string) bool {
	for _, n := range res.Notes {
		if strings.Contains(n, want) {
			return true
		}
	}
	return false
}

// TestCheckVite_ScaffoldWithPublicDirOut is the defect this check exists
// for: forge's own scaffold bound a vite-spa to hosted with public_dir "out"
// while Vite builds into its default dist/.
func TestCheckVite_ScaffoldWithPublicDirOut(t *testing.T) {
	fe := viteFrontend(t, map[string]string{"vite.config.ts": scaffoldViteConfig},
		Binding{Env: "staging", Runtime: "hosted", PublicDir: "out"},
		Binding{Env: "prod", Runtime: "hosted", PublicDir: "out"})
	res := checkVite(t, fe)
	if len(res.Findings) != 1 {
		t.Fatalf("findings = %+v, want exactly the outDir mismatch", res.Findings)
	}
	f := res.Findings[0]
	if f.Rule != RuleViteOutDir || f.Severity != finding.SeverityError {
		t.Errorf("finding = %s/%s", f.Rule, f.Severity)
	}
	// No outDir is set, so it anchors on the config object (`return {`).
	if f.File != "frontends/spa/vite.config.ts" || f.Line != 6 {
		t.Errorf("anchored at %s:%d, want frontends/spa/vite.config.ts:6", f.File, f.Line)
	}
	for _, want := range []string{`public_dir "out"`, `writes to "dist"`, "Vite's default", "env staging, prod"} {
		if !strings.Contains(f.Message+f.Remediation, want) {
			t.Errorf("finding does not say %q:\n%s\n%s", want, f.Message, f.Remediation)
		}
	}
	if !notesContain(res, `base "/"`) {
		t.Errorf("the default base at the site root must be confirmed in a note: %v", res.Notes)
	}
}

func TestCheckVite_OutDir(t *testing.T) {
	cases := []struct {
		name      string
		config    string
		pkg       string
		publicDir string
		// wantLine is the finding's line, 0 for no finding.
		wantLine int
		// wantNote is a substring of a note, when no finding is expected.
		wantNote string
	}{
		{
			name:      "default dist matches the default public_dir",
			config:    scaffoldViteConfig,
			publicDir: "dist",
			wantNote:  `build.outDir "dist" (Vite's default`,
		},
		{
			name:      "a literal outDir matching public_dir, spelled differently",
			config:    "export default defineConfig({\n  build: { outDir: \"./out/\" },\n});\n",
			publicDir: "out",
			wantNote:  `build.outDir "out"`,
		},
		{
			name:      "a literal outDir that disagrees",
			config:    "export default defineConfig({\n  build: {\n    outDir: 'build',\n  },\n});\n",
			publicDir: "dist",
			wantLine:  3,
		},
		{
			name:      "outDir is resolved against root",
			config:    "export default defineConfig({\n  root: \"app\",\n  build: { outDir: \"dist\" },\n});\n",
			publicDir: "app/dist",
			wantNote:  `build.outDir "app/dist"`,
		},
		{
			name:      "a computed outDir is not guessed",
			config:    "export default defineConfig({\n  build: { outDir: process.env.OUT ?? \"dist\" },\n});\n",
			publicDir: "out",
			wantNote:  "build.outDir is not statically determinable: build.outDir at vite.config.ts:2 is computed",
		},
		{
			name:      "two different literals are not guessed",
			config:    "export default defineConfig(({ command }) =>\n  command === \"build\" ? { build: { outDir: \"out\" } } : { build: { outDir: \"dist\" } },\n);\n",
			publicDir: "out",
			wantNote:  "set to different values",
		},
		{
			name:      "mergeConfig may carry it",
			config:    "import base from \"./vite.base\";\nexport default mergeConfig(base, defineConfig({}));\n",
			publicDir: "out",
			wantNote:  "merges configs",
		},
		{
			name:      "a spread into build may carry it",
			config:    "export default defineConfig({\n  build: { ...shared.build },\n});\n",
			publicDir: "out",
			wantNote:  "the spread at vite.config.ts:2",
		},
		{
			name:      "a build that is not an object literal",
			config:    "const buildOptions = {};\nexport default defineConfig({\n  build: buildOptions,\n});\n",
			publicDir: "out",
			wantNote:  "is not an object literal",
		},
		{
			name:      "a config re-exported from elsewhere proves nothing",
			config:    "import config from \"./shared.vite\";\nexport default config;\n",
			publicDir: "out",
			wantNote:  "no config object literal",
		},
		{
			name:      "the build script's --outDir wins over the config",
			config:    "export default defineConfig({\n  build: { outDir: \"dist\" },\n});\n",
			pkg:       `{"scripts": {"build": "tsc && vite build --outDir out"}}`,
			publicDir: "out",
			wantNote:  "--outDir in package.json's build script",
		},
		{
			name:      "a build script that does not run vite build",
			config:    scaffoldViteConfig,
			pkg:       `{"scripts": {"build": "node build.mjs"}}`,
			publicDir: "out",
			wantNote:  "does not run `vite build`",
		},
		{
			name:      "a config assigned to a variable it exports",
			config:    "const config: UserConfig = {\n  build: { outDir: \"public\" },\n};\nexport default config;\n",
			publicDir: "dist",
			wantLine:  2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string]string{"vite.config.ts": tc.config}
			if tc.pkg != "" {
				files["package.json"] = tc.pkg
			}
			res := checkVite(t, viteFrontend(t, files, Binding{Env: "prod", Runtime: "hosted", PublicDir: tc.publicDir}))
			var got []finding.Finding
			for _, f := range res.Findings {
				if f.Rule == RuleViteOutDir {
					got = append(got, f)
				}
			}
			if tc.wantLine == 0 {
				if len(got) != 0 {
					t.Fatalf("want no outDir finding, got %+v", got)
				}
				if !notesContain(res, tc.wantNote) {
					t.Errorf("no note contains %q: %v", tc.wantNote, res.Notes)
				}
				return
			}
			if len(got) != 1 || got[0].Line != tc.wantLine {
				t.Fatalf("want one outDir finding at line %d, got %+v", tc.wantLine, got)
			}
		})
	}
}

func TestCheckVite_Base(t *testing.T) {
	routerPkg := `{"scripts": {"build": "vite build"}, "dependencies": {"react-router-dom": "^7.0.0"}}`
	cases := []struct {
		name     string
		config   string
		pkg      string
		src      string
		basePath string
		wantSev  finding.Severity
		wantLine int
		wantNote string
	}{
		{
			name:     "an absolute base off the site root",
			config:   "export default defineConfig({\n  base: \"/app/\",\n});\n",
			wantSev:  finding.SeverityError,
			wantLine: 2,
		},
		{
			name:     "the default base under a base_path",
			config:   "export default defineConfig({\n  plugins: [],\n});\n",
			basePath: "/admin",
			wantSev:  finding.SeverityError,
			wantLine: 1,
		},
		{
			name:     "a base equal to base_path",
			config:   "export default defineConfig({\n  base: \"/admin\",\n});\n",
			basePath: "/admin/",
			wantNote: `base "/admin" (base at vite.config.ts:2) matches the mount in env prod`,
		},
		{
			name:     "the build script's --base",
			config:   "export default defineConfig({});\n",
			pkg:      `{"scripts": {"build": "vite build --base=/docs/"}}`,
			basePath: "/docs",
			wantNote: "--base in package.json's build script",
		},
		{
			name:     "a relative base with a path router",
			config:   "export default defineConfig({\n  base: \"./\",\n});\n",
			pkg:      routerPkg,
			wantSev:  finding.SeverityWarning,
			wantLine: 2,
		},
		{
			name:     "a hash router mention does not prove path routing is unused",
			config:   "export default defineConfig({\n  base: \"./\",\n});\n",
			pkg:      routerPkg,
			src:      "import { createBrowserRouter, createHashRouter } from \"react-router-dom\";\nexport const router = createBrowserRouter([]);\nconst unusedDemo = createHashRouter([]);\n",
			wantSev:  finding.SeverityWarning,
			wantLine: 2,
		},
		{
			name:     "a relative base with no router",
			config:   "export default defineConfig({\n  base: \"\",\n});\n",
			wantNote: "no known client-side router is a dependency",
		},
		{
			name:     "an absolute URL base",
			config:   "export default defineConfig({\n  base: \"https://cdn.example.com/spa/\",\n});\n",
			basePath: "/admin",
			wantNote: "absolute URL",
		},
		{
			name:     "a plugin's own base option is not the config's",
			config:   "export default defineConfig({\n  plugins: [VitePWA({ base: \"/app/\" })],\n});\n",
			wantNote: "base is not statically determinable",
		},
		{
			name:     "a base nested in another key is ignored",
			config:   "export default defineConfig({\n  server: { base: \"/x/\" },\n});\n",
			wantNote: `base "/" (Vite's default`,
		},
		{
			name:     "a computed base",
			config:   "export default defineConfig({\n  base: process.env.BASE_URL,\n});\n",
			wantNote: "base at vite.config.ts:2 is computed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string]string{"vite.config.ts": tc.config}
			if tc.pkg != "" {
				files["package.json"] = tc.pkg
			}
			if tc.src != "" {
				files["src/router.tsx"] = tc.src
			}
			res := checkVite(t, viteFrontend(t, files, Binding{Env: "prod", Runtime: "hosted", PublicDir: "dist", BasePath: tc.basePath}))
			var got []finding.Finding
			for _, f := range res.Findings {
				if f.Rule == RuleViteBase {
					got = append(got, f)
				}
			}
			if tc.wantLine == 0 {
				if len(got) != 0 {
					t.Fatalf("want no base finding, got %+v", got)
				}
				if !notesContain(res, tc.wantNote) {
					t.Errorf("no note contains %q: %v", tc.wantNote, res.Notes)
				}
				return
			}
			if len(got) != 1 || got[0].Line != tc.wantLine || got[0].Severity != tc.wantSev {
				t.Fatalf("want one %s base finding at line %d, got %+v", tc.wantSev, tc.wantLine, got)
			}
			if got[0].File != "frontends/spa/vite.config.ts" {
				t.Errorf("file = %s", got[0].File)
			}
		})
	}
}

func TestCheckVite_WrappedAndAmbiguousBuildScripts(t *testing.T) {
	for _, tc := range []struct {
		script    string
		wantNotes string // when empty, both CLI overrides should cause findings
	}{
		{`npx vite build --outDir out --base=/wrong/`, ""},
		{`npx --yes vite build --outDir 'my out' --base=/wrong/`, ""},
		{`pnpm exec vite build --outDir out --base=/wrong/`, ""},
		{`yarn vite build --outDir out --base=/wrong/`, ""},
		{`npm exec -- vite build --outDir out --base=/wrong/`, ""},
		{`echo 'vite build --outDir pretend'; npx vite build --outDir out --base=/wrong/`, ""},
		{`vite build --outDir out --base=/wrong/ && vite build`, "runs multiple `vite build`"},
		{`vite build --outDir out --base=/wrong/ || pnpm exec vite build`, "runs multiple `vite build`"},
		{`cd other && vite build`, "cannot be resolved"},
		{`vite build --outDir "$DIR"`, "cannot be resolved"},
		{`echo 'vite build --outDir pretend'`, "does not run `vite build`"},
	} {
		t.Run(tc.script, func(t *testing.T) {
			pkg, err := json.Marshal(map[string]any{"scripts": map[string]string{"build": tc.script}})
			if err != nil {
				t.Fatal(err)
			}
			fe := viteFrontend(t, map[string]string{"package.json": string(pkg)})
			res := checkVite(t, fe)
			if tc.wantNotes != "" {
				if len(res.Findings) != 0 || len(res.Notes) != 2 {
					t.Fatalf("want both checks to explain ambiguity, got %+v", res)
				}
				for _, note := range res.Notes {
					if !strings.Contains(note, "not statically determinable") || !strings.Contains(note, tc.wantNotes) {
						t.Errorf("note %q does not explain %q", note, tc.wantNotes)
					}
				}
				return
			}
			if len(res.Findings) != 2 || res.Findings[0].Rule != RuleViteOutDir || res.Findings[1].Rule != RuleViteBase {
				t.Fatalf("want both build-script override mismatches, got %+v", res)
			}
		})
	}
}

// TestCheckVite_NoConfig: a Vite app with no config builds with Vite's
// defaults, which are checkable.
func TestCheckVite_NoConfig(t *testing.T) {
	res := checkVite(t, viteFrontend(t, map[string]string{"index.html": "<!doctype html>"},
		Binding{Env: "prod", Runtime: "bucket", PublicDir: "out"}))
	if len(res.Findings) != 1 || res.Findings[0].Rule != RuleViteOutDir || res.Findings[0].File != "frontends/spa" {
		t.Fatalf("findings = %+v, want the outDir mismatch anchored on the frontend dir", res.Findings)
	}
}

// TestCheckVite_MissingDir: no code, nothing to judge.
func TestCheckVite_MissingDir(t *testing.T) {
	res := checkVite(t, Frontend{Name: "spa", Dir: "/nonexistent/spa", Bindings: []Binding{{Env: "prod", Runtime: "hosted"}}})
	if len(res.Findings)+len(res.Notes) != 0 {
		t.Errorf("got %+v", res)
	}
}
