package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/linter/finding"
	"github.com/reliant-labs/forge/internal/linter/forgeconv"
)

// A freshly scaffolded frontend must pass forge's OWN frontend lint rules.
//
// This is the day-one credibility test. `forge project new` + `forge
// scaffold` produced a tree in which `forge lint` was already red, with
// three forgeconv-frontend-process-env findings in files the author had not
// written and could not fix without editing forge-scaffolded code. A user
// whose first `forge lint` is noise learns on day one to ignore `forge
// lint` output, which is worse than not shipping the rule at all.
//
// The rule itself is right — a build-time-inlined NEXT_PUBLIC_* / VITE_*
// read freezes the artifact to the environment it was built against, so
// `forge env deploy` cannot move it. The scaffold simply has to follow it.
//
// Asserting through the ANALYZER rather than by grepping the templates is
// deliberate: the analyzer owns the allowlist (generated banners, config
// scripts, tests, build-mode discriminators), so a template read that is
// genuinely exempt stays exempt here without this test having to restate
// the exemption and drift from it.

// scaffoldForFrontendLint builds a project with a frontend and the typed
// config module beside it, which is the shape `forge project new` leaves
// behind: the generate pipeline writes src/lib/config_gen.ts, and that
// module's PRESENCE is the rule's precondition. Without it the rule is
// silent and this test would assert nothing.
func scaffoldForFrontendLint(t *testing.T, name, frontend, kind string) (root, feDir string) {
	t.Helper()
	root = filepath.Join(t.TempDir(), name)
	g := NewProjectGenerator(name, root, "example.com/"+name)
	g.FrontendName = frontend
	if err := g.Generate(); err != nil {
		t.Fatalf("Generate(): %v", err)
	}
	platform := "nextjs"
	if kind != "" && kind != "nextjs" {
		// A second frontend rather than a re-render: frontend files are
		// write-if-absent, so rendering another kind over the Next.js tree
		// would silently leave the Next.js files in place and this test
		// would check the same tree twice.
		frontend += "2"
		if err := GenerateFrontendFilesWithOptions(root, "example.com/"+name, name, frontend, 8080, kind,
			FrontendGenOptions{TypedConfig: ScaffoldedFrontendTypedConfig()}); err != nil {
			t.Fatalf("GenerateFrontendFilesWithOptions(%s): %v", kind, err)
		}
		platform = "vite-spa"
		if kind == "mobile" {
			platform = "react-native"
		}
	}
	if err := WriteScaffoldedFrontendConfigTS(root, name, frontend, platform, 8080); err != nil {
		t.Fatalf("WriteScaffoldedFrontendConfigTS: %v", err)
	}
	feDir = filepath.Join(root, "frontends", frontend)
	if _, err := os.Stat(filepath.Join(feDir, codegen.FrontendConfigTSFile)); err != nil {
		t.Fatalf("scaffold wrote no %s, so the rule's precondition is absent and this test "+
			"would assert nothing: %v", codegen.FrontendConfigTSFile, err)
	}
	return root, feDir
}

func TestScaffoldedFrontend_PassesProcessEnvLint(t *testing.T) {
	if testing.Short() {
		t.Skip("scaffolds and generates a full project; runs in task test")
	}
	for _, tc := range []struct {
		name string
		kind string
	}{
		{"nextjs", ""},
		{"vite-spa", "vite-spa"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, feDir := scaffoldForFrontendLint(t, "lintapp", "web", tc.kind)

			res := forgeconv.LintFrontendProcessEnv(root, []string{feDir}, finding.SeverityWarning)
			for _, f := range res.Findings {
				t.Errorf("fresh scaffold fails forge's own lint:\n  [%s] %s:%d\n      %s\n      → %s",
					f.Rule, f.File, f.Line, f.Message, f.Remediation)
			}
			if len(res.Findings) > 0 {
				t.Fatalf("%d forgeconv-frontend-process-env finding(s) in a project with ZERO "+
					"hand-written frontend code. Every new project starts with lint noise its "+
					"author did not create and cannot fix without editing forge-scaffolded "+
					"files, which teaches users to ignore `forge lint` on day one.",
					len(res.Findings))
			}
		})
	}
}

// TestScaffoldedFrontend_ConfigReadsGoThroughTheTypedModule is the positive
// half. The test above would also pass if a template stopped reading its
// config at all, so this one pins that each read site still resolves its
// value — through loadConfig(), the promotable path.
func TestScaffoldedFrontend_ConfigReadsGoThroughTheTypedModule(t *testing.T) {
	for _, tc := range []struct {
		name  string
		kind  string
		files map[string][]string
	}{
		{"nextjs", "", map[string][]string{
			"src/lib/connect.ts":           {"loadConfig().API_URL", "loadConfig().MOCK_API"},
			"src/lib/auth/native-login.ts": {"loadConfig().API_URL"},
			"src/app/layout.tsx":           {"loadConfig().MOCK_API"},
		}},
		{"vite-spa", "vite-spa", map[string][]string{
			"src/lib/connect.ts":           {"loadConfig().API_URL", "loadConfig().MOCK_API"},
			"src/lib/auth/native-login.ts": {"loadConfig().API_URL"},
			"src/App.tsx":                  {"loadConfig().MOCK_API"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, feDir := scaffoldForFrontendLint(t, "typedapp", "web", tc.kind)
			for rel, wants := range tc.files {
				body, err := os.ReadFile(filepath.Join(feDir, rel))
				if err != nil {
					t.Fatalf("read %s: %v", rel, err)
				}
				src := string(body)
				if !strings.Contains(src, `from "@/lib/config_gen"`) {
					t.Errorf("%s does not import the typed config module", rel)
				}
				for _, want := range wants {
					if !strings.Contains(src, want) {
						t.Errorf("%s does not read %s — the lint passes but the value no longer "+
							"reaches the code that needs it", rel, want)
					}
				}
			}
		})
	}
}
