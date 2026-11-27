package ciguard

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	yaml "gopkg.in/yaml.v3"
)

// #2816 added e2e tests that requireTool(t, "task", ...), and the suite job
// never installed Task: under CI a missing tool fails the test, so shards 2
// and 3 went red on every main push from 2026-11-26. The suite's "Verify
// every required tool resolves" step exists to catch exactly that in five
// seconds, but its list was kept equal to requireTool's call sites by hand.
// This pins it: every tool an e2e test asks requireTool for must be in that
// step's list, so a new requirement without its install fails here, in the
// merge tier, rather than on main.

var cliPackageDir = filepath.Join("..", "cli")

// requiredE2ETools returns every string literal passed to requireTool in the
// e2e test files of dir.
func requiredE2ETools(t *testing.T, dir string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*_e2e_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("no *_e2e_test.go under %s", dir)
	}
	seen := map[string]bool{}
	fset := token.NewFileSet()
	for _, f := range files {
		file, err := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if id, ok := call.Fun.(*ast.Ident); !ok || id.Name != "requireTool" {
				return true
			}
			for _, arg := range call.Args {
				lit, ok := arg.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				if s, err := strconv.Unquote(lit.Value); err == nil {
					seen[s] = true
				}
			}
			return true
		})
	}
	tools := make([]string, 0, len(seen))
	for s := range seen {
		tools = append(tools, s)
	}
	sort.Strings(tools)
	return tools
}

var verifyToolLoop = regexp.MustCompile(`for tool in ([^;]+);`)

// suiteVerifiedTools returns the tools the suite job's verify step checks.
func suiteVerifiedTools(raw []byte) ([]string, error) {
	var wf stepsWorkflow
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		return nil, err
	}
	for _, s := range wf.Jobs["suite"].Steps {
		if s.Name != "Verify every required tool resolves" {
			continue
		}
		m := verifyToolLoop.FindStringSubmatch(s.Run)
		if m == nil {
			return nil, nil
		}
		return strings.Fields(m[1]), nil
	}
	return nil, nil
}

func TestE2ESuiteVerifiesEveryRequiredTool(t *testing.T) {
	raw, err := os.ReadFile(e2eSuiteWorkflow)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := suiteVerifiedTools(raw)
	if err != nil {
		t.Fatalf("e2e-suite.yml does not parse: %v", err)
	}
	if len(verified) == 0 {
		t.Fatal(`e2e-suite.yml's suite job has no "Verify every required tool resolves" step with a "for tool in ...;" loop`)
	}
	required := requiredE2ETools(t, cliPackageDir)
	if len(required) == 0 {
		t.Fatal("found no requireTool call in internal/cli/*_e2e_test.go; the parser no longer sees them")
	}
	for _, tool := range required {
		if !slices.Contains(verified, tool) {
			t.Errorf("an e2e test calls requireTool(t, %q), but e2e-suite.yml's verify step does not check it: install %s in the suite job and add it to that step's list", tool, tool)
		}
	}
}

// The check itself: a verify list missing a required tool is found.
func TestSuiteVerifiedToolsReadsTheLoop(t *testing.T) {
	wf := `jobs:
  suite:
    steps:
      - name: Verify every required tool resolves
        run: |
          for tool in go node buf; do
            command -v "$tool"
          done
`
	got, err := suiteVerifiedTools([]byte(wf))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"go", "node", "buf"}; !slices.Equal(got, want) {
		t.Fatalf("verified = %q, want %q", got, want)
	}
	if slices.Contains(got, "task") {
		t.Fatal("task is not in the list, yet was reported as verified")
	}
}
