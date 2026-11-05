package lint

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

func rulesCfg(rules map[string]string) *config.ProjectConfig {
	return &config.ProjectConfig{Lint: config.LintConfig{Rules: rules}}
}

// gradeLane is the one place lint.rules decides a verdict: each case is a
// dial a project turns, and what the lane's verdict must do.
func TestGradeLane(t *testing.T) {
	warn := lintJSONFinding{Severity: lintSevWarning, Rule: "forge-config-deps", File: "internal/x/contract.go"}
	errF := lintJSONFinding{Severity: lintSevError, Rule: "forgeconv-handler-file-size", File: "internal/handlers/a/a.go"}
	external := lintJSONFinding{Severity: lintSevError, Rule: "external", Message: "golangci-lint: errcheck"}

	for name, tc := range map[string]struct {
		rules     map[string]string
		in        []lintJSONFinding
		gated     bool
		wantGated bool
		wantSev   []string
	}{
		"an advisory lane's rule raised to error gates": {
			rules: map[string]string{"forge-config-deps": "error"}, in: []lintJSONFinding{warn},
			wantGated: true, wantSev: []string{lintSevError},
		},
		"a gating lane's only error lowered to warn passes": {
			rules: map[string]string{"forgeconv-handler-file-size": "warn"}, in: []lintJSONFinding{errF}, gated: true,
			wantGated: false, wantSev: []string{lintSevWarning},
		},
		"off drops the finding and its verdict": {
			rules: map[string]string{"forgeconv-handler-file-size": "off"}, in: []lintJSONFinding{errF}, gated: true,
			wantGated: false, wantSev: nil,
		},
		"another tool's finding is never graded, even by *": {
			rules: map[string]string{"*": "warn"}, in: []lintJSONFinding{external}, gated: true,
			wantGated: true, wantSev: []string{lintSevError},
		},
		"* grades forge's rules": {
			rules: map[string]string{"*": "error"}, in: []lintJSONFinding{warn},
			wantGated: true, wantSev: []string{lintSevError},
		},
		"an exact rule beats *": {
			rules: map[string]string{"*": "error", "forge-config-deps": "warn"}, in: []lintJSONFinding{warn},
			wantGated: false, wantSev: []string{lintSevWarning},
		},
		"a lane that failed without a finding still fails": {
			rules: map[string]string{"forge-config-deps": "warn"}, in: []lintJSONFinding{warn}, gated: true,
			wantGated: true, wantSev: []string{lintSevWarning},
		},
		"no dial leaves the lane as it was": {
			rules: nil, in: []lintJSONFinding{warn}, wantGated: false, wantSev: []string{lintSevWarning},
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, gated := gradeLane(rulesCfg(tc.rules).Lint.EffectiveRules(), tc.in, tc.gated)
			if gated != tc.wantGated {
				t.Errorf("gated = %v, want %v", gated, tc.wantGated)
			}
			var sevs []string
			for _, f := range got {
				sevs = append(sevs, f.Severity)
			}
			if strings.Join(sevs, ",") != strings.Join(tc.wantSev, ",") {
				t.Errorf("severities = %v, want %v", sevs, tc.wantSev)
			}
		})
	}
}

// The P7b defect, end to end through the engine: `lint.rules:
// {forge-config-deps: error}` was accepted and did nothing, because the lane
// is advisory by construction. Now the engine grades it like every lane, and
// every output mode renders that one outcome.
func TestLintRulesGradeAnAdvisoryLane(t *testing.T) {
	advisory := linterStep{
		name: "config-deps lint", gates: false, scope: scopeByFile,
		shouldRun: func(*lintRunCtx) (bool, string) { return true, "" },
		collect: func(*lintRunCtx) ([]lintJSONFinding, bool, error) {
			return []lintJSONFinding{{Severity: lintSevWarning, Rule: "forge-config-deps", File: "internal/x/contract.go", Line: 3,
				Message: "Deps.BaseURL is a naked scalar"}}, false, nil
		},
	}
	run := func(rules map[string]string) ([]lintStepOutcome, error) {
		rc := &lintRunCtx{ctx: t.Context(), cfg: rulesCfg(rules)}
		outcomes := collectLintOutcomesFrom(rc, []linterStep{advisory, cleanGatingStep("contract linter")})
		var out strings.Builder
		return outcomes, renderLintOutcomes(&out, outcomes, false, nil)
	}

	outcomes, err := run(nil)
	if err != nil || outcomes[0].gated {
		t.Fatalf("by default the lane is advisory: gated=%v err=%v", outcomes[0].gated, err)
	}
	outcomes, err = run(map[string]string{"forge-config-deps": "error"})
	if err == nil || !strings.Contains(err.Error(), "config-deps lint") {
		t.Fatalf("lint.rules forge-config-deps: error must fail the run naming the lane, got %v", err)
	}
	if findings, gated := flattenLintOutcomes(outcomes); !gated || findings[0].Severity != lintSevError {
		t.Fatalf("--json must report the same verdict: gated=%v %+v", gated, findings)
	}
}

// A lint.rules key forge cannot honour is an error, never a silent no-op.
func TestLintRulesOutcome(t *testing.T) {
	if o := lintRulesOutcome(nil); o.ran {
		t.Fatal("no config: nothing to check")
	}
	if o := lintRulesOutcome(rulesCfg(map[string]string{"forge-config-deps": "error", "*": "warn"})); o.ran || o.gated {
		t.Fatalf("every key honoured: the check must not report, got %+v", o)
	}

	o := lintRulesOutcome(rulesCfg(map[string]string{
		"forge-conifg-deps":           "error", // misspelled
		"errcheck":                    "off",   // golangci's
		"forge-frontend-typecheck":    "warn",  // another dial's
		"golangci-lint-unavailable":   "off",   // a could-not-run headline
		"forgeconv-activity-declared": "loud",  // a bad severity
	}))
	if !o.ran || !o.gated || !o.gates {
		t.Fatalf("unhonourable keys must fail the run, got ran=%v gated=%v", o.ran, o.gated)
	}
	byKey := map[string]lintJSONFinding{}
	for _, f := range o.findings {
		if f.Severity != lintSevError || f.Rule != ruleLintRules || f.File != "" {
			t.Errorf("finding must be an unattributed %s error: %+v", ruleLintRules, f)
		}
		for _, k := range []string{"forge-conifg-deps", "errcheck", "forge-frontend-typecheck", "golangci-lint-unavailable", "forgeconv-activity-declared"} {
			if strings.Contains(f.Message, strconv.Quote(k)) {
				byKey[k] = f
			}
		}
	}
	for key, want := range map[string]string{
		"forge-conifg-deps":           `did you mean "forge-config-deps"`,
		"errcheck":                    ".golangci.yml",
		"forge-frontend-typecheck":    "lint.frontend.typecheck",
		"golangci-lint-unavailable":   "lint.allow_skip",
		"forgeconv-activity-declared": "is not a severity",
	} {
		f, ok := byKey[key]
		if !ok {
			t.Errorf("no finding for %q in %+v", key, o.findings)
			continue
		}
		if !strings.Contains(f.Message+" "+f.FixHint, want) {
			t.Errorf("%q: want %q in %q / %q", key, want, f.Message, f.FixHint)
		}
	}

	// The outcome both entry points lead with: present only when it reports.
	if got := lintRulesOutcomes(rulesCfg(map[string]string{"nope": "off"})); len(got) != 1 || got[0].name != lintRulesLane || !got[0].gated {
		t.Errorf("an unhonourable key must yield one failing %s outcome, got %+v", lintRulesLane, got)
	}
	if got := lintRulesOutcomes(rulesCfg(map[string]string{"forge-config-deps": "error"})); len(got) != 0 {
		t.Errorf("a clean dial must add nothing to the report, got %+v", got)
	}
}

// The catalog is the set lint.rules accepts AND the set it grades, so a rule
// a lane emits that the catalog misses would be both refused as a key and
// left ungraded. This reads every rule ID the lane code declares — a
// `Rule: "<id>"` field or a rule-named string constant — and requires each
// to be gradable or owned by another dial.
func TestEveryEmittedRuleIsGradable(t *testing.T) {
	root := moduleRootForTest(t)
	dirs := []string{
		"internal/cli/lint", "internal/linter/forgeconv", "internal/linter/migrationlint",
		"internal/linter/scaffolds", "internal/linter/staticexport", "internal/linter/slowtest",
		"internal/linter/tracinglint", "internal/lint/secrets", "internal/commitpolicy",
		"internal/contractcheck",
	}
	files := map[string]bool{"internal/codegen/composition_graph_lint.go": true, "pkg/release/fluxsafe.go": true}
	for _, d := range dirs {
		entries, err := os.ReadDir(filepath.Join(root, d))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".go") && !strings.HasSuffix(e.Name(), "_test.go") {
				files[d+"/"+e.Name()] = true
			}
		}
	}
	ruleShape := regexp.MustCompile(`^[a-z][a-z0-9-]*(/[a-z0-9-]+)?$`)
	ruleName := regexp.MustCompile(`(?i)rule`)
	gradable := gradableRuleSet()
	missing := map[string]string{}
	fset := token.NewFileSet()
	for rel := range files {
		f, err := parser.ParseFile(fset, filepath.Join(root, rel), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		check := func(lit ast.Expr) {
			bl, ok := lit.(*ast.BasicLit)
			if !ok || bl.Kind != token.STRING {
				return
			}
			id, err := strconv.Unquote(bl.Value)
			if err != nil || !ruleShape.MatchString(id) || gradable[id] || ownedElsewhere[id] != "" || isGapRule(id) {
				return
			}
			missing[id] = rel
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.KeyValueExpr:
				if k, ok := n.Key.(*ast.Ident); ok && k.Name == "Rule" {
					check(n.Value)
				}
			case *ast.ValueSpec:
				for i, name := range n.Names {
					if ruleName.MatchString(name.Name) && i < len(n.Values) {
						check(n.Values[i])
					}
				}
			}
			return true
		})
	}
	if len(missing) > 0 {
		ids := make([]string, 0, len(missing))
		for id := range missing {
			ids = append(ids, id+" ("+missing[id]+")")
		}
		sort.Strings(ids)
		t.Errorf("rule IDs a lane declares but lint.rules neither grades nor routes elsewhere — add each to "+
			"gradableRules (or ownedElsewhere, naming the dial that owns it):\n  %s", strings.Join(ids, "\n  "))
	}
}

// moduleRootForTest walks up from the package directory to go.mod.
func moduleRootForTest(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test")
		}
		dir = parent
	}
}
