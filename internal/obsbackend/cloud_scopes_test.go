package obsbackend

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

type recordingDeclarer map[string]string

func (r recordingDeclarer) DeclareScope(scope string, procedures ...string) {
	for _, p := range procedures {
		r[p] = scope
	}
}

// Every control-plane procedure this package names, except the deploy ones
// the base credential may call, declares its scope: an undeclared one would
// still work, but each run would be refused once before elevating for it.
func TestEveryTelemetryProcedureDeclaresItsScope(t *testing.T) {
	declared := recordingDeclarer{}
	DeclareCloudScopes(declared)

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	seen := 0
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			proc, err := strconv.Unquote(lit.Value)
			if err != nil || !strings.HasPrefix(proc, "controlplane.v1.") || !strings.Contains(proc, "/") ||
				strings.HasPrefix(proc, "controlplane.v1.DeployService/") {
				return true
			}
			seen++
			if _, ok := declared[proc]; !ok {
				t.Errorf("%s (%s) names %s but DeclareCloudScopes does not declare its scope", name, fset.Position(lit.Pos()), proc)
			}
			return true
		})
	}
	if seen == 0 {
		t.Fatal("found no control-plane procedures: the scan is broken")
	}
	for _, triage := range []string{procUpdateIssueStatus, procAddIssueComment, procAssignIssue} {
		if declared[triage] != "obs:triage" {
			t.Errorf("%s declares %q, want obs:triage (obs:read does not imply it)", triage, declared[triage])
		}
	}
}
