package templates

import (
	"encoding/json"
	"path"
	"strings"
	"testing"
)

// TestWebScaffoldTypeChecksItsTests holds the web scaffolds' TypeScript
// project to the rule that `tsc` (the `typecheck` script, and the `tsc -b` in
// a Vite build) type-checks test files as well as app code.
//
// The failure it guards against is silent. Vite's own template splits the
// project into a tsconfig.app.json that EXCLUDES every test file, and nothing
// else includes them; vitest strips types without checking them, so a test
// importing an export the app removed passes both `tsc -b` and the test run.
// reliant's adopted web frontend shipped exactly that split. forge's scaffolds
// keep one project whose include covers the tests; the frontend/adopt skill
// tells an adopted app how to add a tsconfig.test.json when it has the split.
func TestWebScaffoldTypeChecksItsTests(t *testing.T) {
	t.Parallel()
	tests := []string{
		"src/hooks/orders-service-hooks.test.tsx",
		"src/lib/__tests__/connect.test.ts",
		"src/components/ui/button.spec.tsx",
		"src/test/setup.ts",
	}
	for _, tmpl := range []string{"nextjs/tsconfig.json.tmpl", "vite-spa/tsconfig.json.tmpl"} {
		out, err := FrontendTemplates().Render(tmpl, FrontendTemplateData{FrontendName: "web", ProjectName: "demo"})
		if err != nil {
			t.Fatalf("render %s: %v", tmpl, err)
		}
		var cfg struct {
			Files      []string `json:"files"`
			References []any    `json:"references"`
			Include    []string `json:"include"`
			Exclude    []string `json:"exclude"`
		}
		if err := json.Unmarshal(out, &cfg); err != nil {
			t.Fatalf("%s: rendered tsconfig is not JSON: %v\n%s", tmpl, err, out)
		}
		if len(cfg.References) > 0 {
			// A solution-style root: the tests' coverage then depends on
			// every referenced project, which this test does not read.
			t.Fatalf("%s has project references; extend this test to check every referenced project", tmpl)
		}
		for _, f := range tests {
			if !tsconfigMatches(cfg.Include, f) {
				t.Errorf("%s: include %v does not cover %s, so tsc never type-checks it", tmpl, cfg.Include, f)
			}
			if tsconfigMatches(cfg.Exclude, f) {
				t.Errorf("%s: exclude %v drops %s, so tsc never type-checks it", tmpl, cfg.Exclude, f)
			}
		}
	}
}

// TestAdoptSkillTestProjectChecksOnlyTests holds the tsconfig.test.json the
// frontend/adopt skill gives an app with Vite's split to the shape that
// type-checks each file ONCE. Its first version extended the app project with
// `"include": ["src"]`, so `tsc -b` type-checked every app file in both
// projects; on reliant's web app that made the typecheck the slowest step of
// its merge gate. The test project must cover the tests, leave app code to the
// app project, reach it by reference, and the app project must be composite
// (tsc -b refuses a reference to one that is not).
func TestAdoptSkillTestProjectChecksOnlyTests(t *testing.T) {
	t.Parallel()
	const skill = "skills/forge/frontend/adopt/SKILL.md"
	b, err := ProjectTemplates().Get(skill)
	if err != nil {
		t.Fatalf("read %s: %v", skill, err)
	}
	type project struct {
		CompilerOptions map[string]any `json:"compilerOptions"`
		Extends         string         `json:"extends"`
		References      []struct {
			Path string `json:"path"`
		} `json:"references"`
		Include []string `json:"include"`
		Exclude []string `json:"exclude"`
	}
	var testProject, appOptions *project
	for _, block := range jsonFences(string(b)) {
		var p project
		if err := json.Unmarshal([]byte(block), &p); err != nil {
			continue // the package.json "forge" fragment
		}
		switch {
		case p.Extends == "./tsconfig.app.json":
			testProject = &p
		case p.CompilerOptions["composite"] == true:
			appOptions = &p
		}
	}
	if testProject == nil {
		t.Fatalf("%s: no tsconfig.test.json snippet (a JSON block extending ./tsconfig.app.json)", skill)
	}
	for _, f := range []string{"src/lib/__tests__/connect.test.ts", "src/App.test.tsx", "src/test/setup.ts", "src/hooks/orders-service-hooks_gen.test.tsx"} {
		if !tsconfigMatches(testProject.Include, f) || tsconfigMatches(testProject.Exclude, f) {
			t.Errorf("tsconfig.test.json include %v / exclude %v does not cover %s", testProject.Include, testProject.Exclude, f)
		}
	}
	for _, f := range []string{"src/App.tsx", "src/lib/connect.ts", "src/hooks/orders-service-hooks_gen.ts"} {
		if tsconfigMatches(testProject.Include, f) && !tsconfigMatches(testProject.Exclude, f) {
			t.Errorf("tsconfig.test.json include %v covers app file %s, which the app project already type-checks", testProject.Include, f)
		}
	}
	refs := false
	for _, r := range testProject.References {
		refs = refs || r.Path == "./tsconfig.app.json"
	}
	if !refs {
		t.Errorf("tsconfig.test.json must reference ./tsconfig.app.json, so the tests read the app's declarations; references %v", testProject.References)
	}
	if testProject.CompilerOptions["incremental"] != true {
		t.Errorf("tsconfig.test.json must set incremental (composite: false turns it off), or tsc -b re-checks every test on any change")
	}
	if appOptions == nil || appOptions.CompilerOptions["emitDeclarationOnly"] != true {
		t.Errorf("%s: no app-project snippet making tsconfig.app.json composite with emitDeclarationOnly", skill)
	}
}

// jsonFences returns the bodies of the ```json fenced blocks in markdown,
// indented or not.
func jsonFences(md string) []string {
	var out []string
	var cur []string
	in := false
	for _, line := range strings.Split(md, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case !in && trimmed == "```json":
			in, cur = true, nil
		case in && trimmed == "```":
			in = false
			out = append(out, strings.Join(cur, "\n"))
		case in:
			cur = append(cur, line)
		}
	}
	return out
}

// tsconfigMatches reports whether a tsconfig include/exclude list matches a
// project-relative file: an entry with no wildcard names a file or a directory
// (and everything under it); `**/` matches any number of directories.
func tsconfigMatches(patterns []string, file string) bool {
	for _, p := range patterns {
		p = strings.TrimPrefix(p, "./")
		if !strings.ContainsAny(p, "*?") {
			if file == p || strings.HasPrefix(file, strings.TrimSuffix(p, "/")+"/") {
				return true
			}
			continue
		}
		if globMatch(strings.Split(p, "/"), strings.Split(file, "/")) {
			return true
		}
	}
	return false
}

func globMatch(pat, segs []string) bool {
	if len(pat) == 0 {
		return len(segs) == 0
	}
	if pat[0] == "**" {
		for i := 0; i <= len(segs); i++ {
			if globMatch(pat[1:], segs[i:]) {
				return true
			}
		}
		return false
	}
	if len(segs) == 0 {
		return false
	}
	if ok, _ := path.Match(pat[0], segs[0]); !ok {
		return false
	}
	return globMatch(pat[1:], segs[1:])
}

func TestTsconfigMatches(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		pats []string
		file string
		want bool
	}{
		{[]string{"src"}, "src/a/b.test.ts", true},
		{[]string{"**/*.tsx"}, "src/a/b.test.tsx", true},
		{[]string{"**/*.ts"}, "src/a/b.test.tsx", false},
		{[]string{"src/**/*.test.*"}, "src/a/b.test.ts", true},
		{[]string{"src/**/__tests__/**/*"}, "src/lib/__tests__/x.ts", true},
		{[]string{"node_modules", "dist"}, "src/a.test.ts", false},
		{[]string{"src/test/**/*"}, "src/test/setup.ts", true},
	} {
		if got := tsconfigMatches(tc.pats, tc.file); got != tc.want {
			t.Errorf("tsconfigMatches(%v, %q) = %v, want %v", tc.pats, tc.file, got, tc.want)
		}
	}
}
