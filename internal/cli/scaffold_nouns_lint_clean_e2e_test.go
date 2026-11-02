//go:build e2e

package cli

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestE2EScaffoldNounsLeaveLintGreen holds every `forge scaffold <noun>` to the
// invariant a scaffold owes its project: green before, green after. Each noun
// runs in a fresh copy of a fresh project, and `forge lint` follows it with NO
// `forge generate` in between — a scaffold that needs one more command before
// the tree lints clean is a two-step procedure, and the second step is the
// one that gets skipped.
//
// The matrix was born red on five nouns:
//
//	frontend  wrote proto/config/v1/<fe>_config.proto and stopped, so
//	          gen/forge_descriptor/ was stale and the convention and
//	          graph lanes refused it ("run `forge generate`")
//	adapter   same stale descriptor, and config.<Name>Config undefined until
//	          generate compiled the block it composed onto AppConfig
//	package   observe_chain.go's newObserveChain unused until generate wrote
//	          the middleware_gen.go that calls it
//	operator  doc.go's doc-comment code block indented with spaces (gofmt)
//	crd       addKnownTypes_<Type> (revive var-naming), and the controller
//	          test reading the deprecated ctrl.Result.Requeue (SA1019)
//	binary    the Run doc's code blocks indented with spaces (gofmt)
//
// Frontend lanes: the two frontend nouns start from a project with no other
// frontend and install the new one's node_modules, so `forge lint` runs its
// eslint, typecheck and style lanes too — and the scaffold's own `npm run
// lint` must report zero problems, warnings included (vite-spa shipped nine
// react-refresh/only-export-components warnings). Every other noun lints with
// --skip-frontends: its project's frontend (the scenario noun needs one) has
// no node_modules, so those lanes could not run, and nothing these nouns
// write reaches them.
//
// --no-fix: the scaffold must be clean as WRITTEN, not rescued by the
// auto-fix pre-pass.
func TestE2EScaffoldNounsLeaveLintGreen(t *testing.T) {
	t.Parallel()
	requireTool(t, "golangci-lint", "node", "npm")
	forgeBin := buildforgeBinary(t)
	contractlintDir := buildContractlintBinary(t)
	// The frontend nouns' installs resolve @reliantlabs/forge-web-runtime
	// over a file: link into this checkout; build it once so the parallel
	// installs do not race its prepare script.
	prebuildWebRuntimeE2E(t)

	// `project new` and the module bridge are identical for every noun, so
	// each base is made once and copied.
	newBase := func(name string, extra ...string) string {
		dir := t.TempDir()
		runCmd(t, dir, forgeBin, append([]string{"project", "new", name, "--mod", "example.com/" + name, "--service", "orders"}, extra...)...)
		projectDir := filepath.Join(dir, name)
		addCorpusForgePkgReplace(t, projectDir)
		return projectDir
	}
	base := newBase("nounapp")
	baseWithFrontend := newBase("nounfeapp", "--frontend", "web")

	for _, tc := range []struct {
		noun  string
		base  string
		steps [][]string // `forge` invocations: prerequisites, then the noun
		// frontend is the frontend the noun adds. Its lanes must run; when
		// empty, lint runs --skip-frontends (see the doc comment).
		frontend string
	}{
		{noun: "worker", base: base, steps: [][]string{{"scaffold", "worker", "sweeper"}}},
		{noun: "operator", base: base, steps: [][]string{{"scaffold", "operator", "manager"}}},
		{noun: "crd", base: base, steps: [][]string{
			{"scaffold", "operator", "manager"},
			{"scaffold", "crd", "Widget"},
		}},
		{noun: "binary", base: base, steps: [][]string{{"scaffold", "binary", "proxy"}}},
		{noun: "adapter", base: base, steps: [][]string{{"scaffold", "adapter", "pricing"}}},
		{noun: "webhook", base: base, steps: [][]string{{"scaffold", "webhook", "scanner", "--service", "orders"}}},
		{noun: "package", base: base, steps: [][]string{{"scaffold", "package", "blobstore"}}},
		{noun: "library", base: base, steps: [][]string{{"scaffold", "library", "strutil"}}},
		{noun: "handler-file", base: base, steps: [][]string{{"scaffold", "handler-file", "orders", "reports"}}},
		{noun: "scenario", base: baseWithFrontend, steps: [][]string{{"scaffold", "scenario", "empty-state"}}},
		{noun: "frontend-nextjs", base: base, frontend: "admin", steps: [][]string{{"scaffold", "frontend", "admin"}}},
		{noun: "frontend-vite-spa", base: base, frontend: "spa", steps: [][]string{{"scaffold", "frontend", "spa", "--kind", "vite-spa"}}},
	} {
		t.Run(tc.noun, func(t *testing.T) {
			t.Parallel()
			projectDir := filepath.Join(t.TempDir(), filepath.Base(tc.base))
			runCmd(t, filepath.Dir(projectDir), "cp", "-a", tc.base, projectDir)

			for _, step := range tc.steps {
				runCmdTimeout(t, projectDir, 10*time.Minute, forgeBin, step...)
			}

			lintArgs := []string{"lint", "--no-fix"}
			if tc.frontend == "" {
				lintArgs = append(lintArgs, "--skip-frontends")
			} else {
				feDir := filepath.Join(projectDir, "frontends", tc.frontend)
				// The scaffold's own install is best-effort by design (it warns
				// and carries on), so it cannot be this test's precondition.
				runCmdTimeout(t, feDir, 5*time.Minute, "pnpm", "install", "--no-frozen-lockfile", "--prefer-offline")
				assertScaffoldIsEslintCleanAsEmitted(t, feDir)
			}

			out, err := runLintE2E(t, projectDir, contractlintDir, forgeBin, lintArgs...)
			if err != nil {
				t.Fatalf("forge lint must exit 0 straight after `forge %s`, with no generate in between, got: %v\n%s",
					strings.Join(tc.steps[len(tc.steps)-1], " "), err, out)
			}
			if strings.Contains(out, "❌") {
				t.Errorf("forge lint printed error findings after `forge %s`:\n%s", strings.Join(tc.steps[len(tc.steps)-1], " "), out)
			}
			// A lane that did not run makes the exit-0 vacuous.
			for _, banned := range []string{"golangci-lint not found", "contractlint not available", "node_modules not found"} {
				if strings.Contains(out, banned) {
					t.Errorf("lint lane was skipped (%q) — the exit-0 assertion is vacuous:\n%s", banned, out)
				}
			}
			markers := []string{"✓ golangci-lint", "✓ contract linter"}
			if tc.frontend != "" {
				markers = append(markers,
					"✓ frontend lint",
					tc.frontend+": npm run lint passed",
					tc.frontend+": typecheck passed",
				)
			}
			for _, marker := range markers {
				if !strings.Contains(out, marker) {
					t.Errorf("lint lane %q did not run:\n%s", marker, out)
				}
			}
		})
	}
}
