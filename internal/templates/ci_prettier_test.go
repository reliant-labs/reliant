package templates

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// scaffoldPrettierVersion is the prettier release the scaffolded
// .pre-commit-config.yaml hook runs (see internal/generator/dx_files.go).
// Every YAML forge scaffolds must already be in the shape that version
// writes, or a FRESH project fails its own pre-commit hook on the first
// commit.
const scaffoldPrettierVersion = PrettierVersion

// renderedWorkflows renders every GitHub Actions template across the data
// shapes the generator feeds them, so each conditional branch that emits a
// scalar is exercised: frontends on and off, e2e under both runtimes, the
// reconcile opt-in, the hosted release pipeline, and a lone env.
func renderedWorkflows(t *testing.T) map[string][]byte {
	t.Helper()
	fe := []FrontendCIConfig{{Name: "web", Path: "frontends/web"}}
	full := CIWorkflowData{
		ProjectName: "demo", HasFrontends: true, Frontends: fe, HasServices: true,
		LintGolangci: true, LintBuf: true, LintBufBreaking: true, LintFrontend: true,
		LintFrontendStyles: true, LintMigrationSafety: true,
		TestRace: true, TestCoverage: true,
		VulnGo: true, VulnDocker: true, VulnNPM: true, LicenseCheck: true,
		E2EEnabled: true, E2ERuntime: "docker-compose", PermContents: "read",
		HasKCL: true, HasDocker: true, VerifyGenerated: true,
		Environments: []string{"dev", "staging", "prod"},
		Module:       "github.com/example/demo", FrontendName: "web", GitHubOwner: "example",
	}
	k3d := full
	k3d.E2ERuntime = "k3d"
	k3d.TestRace = false
	minimal := CIWorkflowData{ProjectName: "tool", LintGolangci: true, TestRace: true, VulnGo: true, LicenseCheck: true, PermContents: "read", VerifyGenerated: true}
	envs := []DeployEnv{{Name: "staging", Auto: true}, {Name: "prod", Protection: true, URL: "https://example.com"}}

	cases := []struct {
		name, tmpl string
		data       any
	}{
		{"ci full", "ci.yml.tmpl", full},
		{"ci k3d", "ci.yml.tmpl", k3d},
		{"ci minimal", "ci.yml.tmpl", minimal},
		{"proto-breaking", "proto-breaking.yml.tmpl", full},
		{"build-images", "build-images.yml.tmpl", BuildImagesWorkflowData{ProjectName: "demo", BuildEnv: "staging", VulnDocker: true}},
		// The hosted build job is a different document — no registry login,
		// a control-plane token instead — so it goes through prettier,
		// actionlint and the no-registry rules on its own.
		{"build-images hosted", "build-images.yml.tmpl", BuildImagesWorkflowData{ProjectName: "demo", BuildEnv: "staging", VulnDocker: true, Hosted: true}},
		{"release", "release.yml.tmpl", ReleaseWorkflowData{ProjectName: "demo", BuildEnv: "staging", Stages: ReleaseStages([]DeployEnv{{Name: "staging"}, {Name: "prod", Protection: true}}, nil)}},
		{"release mixed", "release.yml.tmpl", ReleaseWorkflowData{ProjectName: "demo", BuildEnv: "staging", Stages: ReleaseStages([]DeployEnv{{Name: "staging"}, {Name: "prod", Protection: true}}, map[string]bool{"prod": true})}},
		{"forge-deploy action", "forge-deploy-action.yml.tmpl", ReleaseWorkflowData{ProjectName: "demo", BuildEnv: "staging", Stages: ReleaseStages([]DeployEnv{{Name: "staging"}, {Name: "prod", Protection: true}}, nil)}},
		{"deploy", "deploy.yml.tmpl", DeployWorkflowData{ProjectName: "demo", Environments: envs, HasFrontends: true, FrontendPath: "frontends/web", Concurrency: true}},
		{"deploy lone env", "deploy.yml.tmpl", DeployWorkflowData{ProjectName: "demo", Environments: envs[1:]}},
		{"e2e", "e2e.yml.tmpl", E2EWorkflowData{ProjectName: "demo", Runtime: "docker-compose", HasFrontends: true, FrontendPath: "frontends/web"}},
		{"e2e no frontend", "e2e.yml.tmpl", E2EWorkflowData{ProjectName: "demo", Runtime: "k3d"}},
		// HasFrontends without a path is the setup-node fallback branch
		// (a fixed node-version), the one scalar the other cases miss.
		{"e2e frontend without path", "e2e.yml.tmpl", E2EWorkflowData{ProjectName: "demo", Runtime: "docker-compose", HasFrontends: true}},
		{"dependabot", "dependabot.yml.tmpl", DependabotData{FrontendName: "web", RunsProtocGenGo: true}},
	}
	out := map[string][]byte{}
	for _, c := range cases {
		b, err := CITemplates("github").Render(c.tmpl, c.data)
		if err != nil {
			t.Fatalf("render %s: %v", c.name, err)
		}
		out[c.name] = b
	}
	return out
}

// singleQuotedScalars returns the single-quoted YAML scalars in doc, with
// their line. prettier's YAML printer rewrites every one of them to
// double quotes (singleQuote defaults to false), so a scaffolded file that
// has any fails the pre-commit prettier hook. Quotes INSIDE a plain scalar —
// GitHub expressions like `if: github.event_name == 'push'` — are part of the
// value, not YAML quoting, and prettier leaves them alone; the parser's node
// style is what tells the two apart.
func singleQuotedScalars(t *testing.T, doc []byte) []string {
	t.Helper()
	var root yaml.Node
	if err := yaml.Unmarshal(doc, &root); err != nil {
		t.Fatalf("not valid YAML: %v\n%s", err, doc)
	}
	var found []string
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		if n.Kind == yaml.ScalarNode && n.Style&yaml.SingleQuotedStyle != 0 {
			found = append(found, fmt.Sprintf("line %d: '%s'", n.Line, n.Value))
		}
		for _, c := range n.Content {
			walk(c)
		}
	}
	walk(&root)
	return found
}

// Every scaffolded workflow is prettier-clean by construction: no
// single-quoted scalar anywhere. This is the check that runs everywhere,
// node or not.
func TestCIWorkflows_NoSingleQuotedScalars(t *testing.T) {
	for name, doc := range renderedWorkflows(t) {
		if found := singleQuotedScalars(t, doc); len(found) > 0 {
			t.Errorf("%s: single-quoted scalars prettier %s rewrites to double quotes:\n  %s",
				name, scaffoldPrettierVersion, strings.Join(found, "\n  "))
		}
	}
}

// The real check: the pinned prettier accepts every rendered workflow
// unchanged. Skipped without node (npx), and in -short mode because the
// first run downloads prettier.
func TestCIWorkflows_PrettierClean(t *testing.T) {
	RequirePrettierCheck(t, renderedWorkflows(t))
}

// RequirePrettierCheck writes files (name -> YAML) into a temp dir and runs
// `prettier@<scaffold version> --check` over them, failing with prettier's
// own diff of what it would rewrite.
func RequirePrettierCheck(t *testing.T, files map[string][]byte) {
	t.Helper()
	if testing.Short() {
		t.Skip("-short: skipping the prettier run (downloads prettier on first use); TestCIWorkflows_NoSingleQuotedScalars still guards the quoting")
	}
	npx := requirePrettierRunner(t)
	dir := t.TempDir()
	var paths []string
	for name, doc := range files {
		p := filepath.Join(dir, strings.NewReplacer(" ", "_", "/", "_").Replace(name)+".yml")
		if err := os.WriteFile(p, doc, 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	args := append([]string{"-y", "prettier@" + scaffoldPrettierVersion, "--no-config", "--check"}, paths...)
	cmd := exec.Command(npx, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("prettier %s would rewrite scaffolded YAML, so a fresh project fails its own pre-commit hook (%v):\n%s%s",
			scaffoldPrettierVersion, err, out, prettierDiffs(npx, paths))
	}
}

// requirePrettierRunner returns npx, or skips when node is absent on a
// developer machine — and FAILS under CI (or FORGE_E2E_REQUIRE_TOOLS=1),
// where a missing node is a provisioning bug, not a reason to report green
// for a check that never ran.
func requirePrettierRunner(t *testing.T) string {
	t.Helper()
	npx, lookErr := exec.LookPath("npx")
	// Shaped like internal/cli's requireTool: the skip is the guarded branch
	// and the failure the fall-through, so vacuousguard (and a reader) sees
	// exactly what the skip depends on.
	if lookErr != nil && os.Getenv("CI") == "" && os.Getenv("FORGE_E2E_REQUIRE_TOOLS") == "" {
		t.Skip("npx (node) not on PATH: cannot run prettier — skipped locally, a hard failure under CI; TestCIWorkflows_NoSingleQuotedScalars still guards the quoting")
	}
	if lookErr != nil {
		t.Fatalf("npx (node) not on PATH under CI: install node in the job so the prettier check actually runs")
	}
	return npx
}

// prettierDiffs shows, per file, the first lines prettier would rewrite.
func prettierDiffs(npx string, paths []string) string {
	var report strings.Builder
	for _, p := range paths {
		formatted, err := exec.Command(npx, "-y", "prettier@"+scaffoldPrettierVersion, "--no-config", p).Output()
		orig, _ := os.ReadFile(p)
		if err == nil && string(orig) != string(formatted) {
			report.WriteString("\n--- " + filepath.Base(p) + " as prettier writes it ---\n" + prettierLineDiff(string(orig), string(formatted)))
		}
	}
	return report.String()
}

// prettierLineDiff lists up to eight lines prettier rewrites: the scaffolded
// line (-) and what prettier writes instead (+).
func prettierLineDiff(a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	var out strings.Builder
	shown := 0
	for i := 0; i < len(al) && i < len(bl) && shown < 8; i++ {
		if al[i] != bl[i] {
			fmt.Fprintf(&out, "  line %d:\n    - %s\n    + %s\n", i+1, al[i], bl[i])
			shown++
		}
	}
	return out.String()
}
