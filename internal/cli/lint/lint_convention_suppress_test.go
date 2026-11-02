package lint

// The forge-convention lane (`forge lint --conventions`, and step 8 of the
// full pipeline) over a tree shaped like reliant's (H-RELIANT-CI): Go
// packages under internal/ with a hand-written contract.go, run with and
// without a forge.yaml.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cli/factory"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/linter/forgeconv"
	"github.com/reliant-labs/forge/internal/linter/suppress"
	"github.com/reliant-labs/forge/internal/projectstore"
)

// writeConventionTree lays down the reliant shapes: a contract.go whose
// constructor returns a concrete type from ANOTHER file (accesstokenclient),
// and a Deps struct with a concrete field (mcpserver.Deps.Limiter).
// directive, when non-empty, is written above each offending declaration.
func writeConventionTree(t *testing.T, directive func(rule string) string) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.24\n",
		"internal/tokenclient/contract.go": `package tokenclient

import "context"

// Service is the adapter's surface.
type Service interface {
	Introspect(ctx context.Context, token string) error
}

// Deps are the adapter's collaborators.
type Deps struct{}
`,
		"internal/tokenclient/client.go": `package tokenclient

import "context"

// Client implements Service.
type Client struct{}

func (c *Client) Introspect(context.Context, string) error { return nil }

` + directive("forgeconv-internal-package-contract-names") + `// New builds the client; callers want the concrete type.
func New(deps Deps) *Client { return &Client{} }
`,
		"internal/limitsrv/contract.go": `package limitsrv

// Service serves.
type Service interface{ Serve() error }

// Limiter throttles.
type Limiter struct{ n int }

// Deps are the server's collaborators.
type Deps struct {
` + directive("forgeconv-deps-are-interfaces") + `	Limiter *Limiter
}

func New(deps Deps) Service { return nil }
`,
	}
	for name, body := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(root)
	// No forge.yaml: the loader reports "not a forge project", exactly as
	// the real one does for reliant.
	factory.SetProjectStoreLoader(func() (*projectstore.Store, error) {
		return nil, ErrProjectConfigNotFound
	})
	return root
}

func noDirective(string) string { return "" }

func conventionFindings(t *testing.T, strict bool) []forgeconv.Finding {
	t.Helper()
	res, _, _, err := collectConventionFindings(forgeconv.LintOptions{Strict: strict})
	if err != nil {
		t.Fatalf("collectConventionFindings: %v", err)
	}
	return res.Findings
}

func findingFor(fs []forgeconv.Finding, rule string) (forgeconv.Finding, bool) {
	for _, f := range fs {
		if f.Rule == rule {
			return f, true
		}
	}
	return forgeconv.Finding{}, false
}

// contractcheck findings honour a reasoned forge:lint-disable written above
// the declaration they name. Red before: collectConventionFindings never
// applied suppression, so neither rule had any escape.
func TestConventionLane_HonorsReasonedSuppression(t *testing.T) {
	writeConventionTree(t, func(rule string) string {
		return "//forge:lint-disable-next-line " + rule + ": reliant is not a forge project; tracked in #1\n"
	})
	for _, f := range conventionFindings(t, true) {
		if strings.HasPrefix(f.Rule, "forgeconv-") || f.Rule == suppress.RuleMissingReason {
			t.Errorf("reasoned suppression did not apply: %+v", f)
		}
	}
}

// A reasonless suppression of an error-severity rule is itself an error.
func TestConventionLane_ReasonlessSuppressionGates(t *testing.T) {
	writeConventionTree(t, func(rule string) string {
		return "//forge:lint-disable-next-line " + rule + "\n"
	})
	fs := conventionFindings(t, true)
	if !(forgeconv.Result{Findings: fs}).HasErrors() {
		t.Fatalf("a reasonless suppression must gate; got %+v", fs)
	}
	if _, ok := findingFor(fs, suppress.RuleMissingReason); !ok {
		t.Fatalf("want %s, got %+v", suppress.RuleMissingReason, fs)
	}
}

// With no forge.yaml there is no bootstrap to break, so contract-names is a
// warning that says why; deps-are-interfaces keeps gating (mock-based
// testability does not depend on codegen); --strict restores the error.
// Red before: contract-names gated a tree forge never generates for.
func TestConventionLane_NoForgeYAMLDowngradesContractNames(t *testing.T) {
	writeConventionTree(t, noDirective)

	fs := conventionFindings(t, false)
	names, ok := findingFor(fs, "forgeconv-internal-package-contract-names")
	if !ok {
		t.Fatalf("contract-names finding missing: %+v", fs)
	}
	if names.Severity != forgeconv.SeverityWarning || !strings.Contains(names.Remediation, "no forge.yaml") {
		t.Errorf("without forge.yaml contract-names must warn and say why: %+v", names)
	}
	if deps, ok := findingFor(fs, "forgeconv-deps-are-interfaces"); !ok || deps.Severity != forgeconv.SeverityError {
		t.Errorf("deps-are-interfaces must keep gating without codegen: %+v", deps)
	}

	strict := conventionFindings(t, true)
	if names, _ := findingFor(strict, "forgeconv-internal-package-contract-names"); names.Severity != forgeconv.SeverityError {
		t.Errorf("--strict must keep contract-names gating: %+v", names)
	}
}

// The constructor finding names the file the constructor is IN, so the
// line number is right and a directive above it can reach it. Red before:
// it said contract.go with client.go's line number.
func TestConventionLane_ContractNamesAnchorsOnTheDeclaration(t *testing.T) {
	writeConventionTree(t, noDirective)
	names, ok := findingFor(conventionFindings(t, true), "forgeconv-internal-package-contract-names")
	if !ok {
		t.Fatal("contract-names finding missing")
	}
	if filepath.Base(names.File) != "client.go" {
		t.Errorf("finding should point at client.go where New is declared, got %s:%d", names.File, names.Line)
	}
}

// `lint.rules` is the repo-wide severity dial for EVERY forge convention
// rule, not only the frontend one it was first wired to. Red before:
// collectConventionFindings applied it to forgeconv-frontend-process-env
// alone, so a downgrade of any other rule was accepted and ignored. The
// outcome engine grades every lane now (lint_rules.go); this pins it on the
// conventions lane's real findings.
func TestConventionLane_LintRulesRegradesEveryRule(t *testing.T) {
	writeConventionTree(t, noDirective)
	rules := config.LintConfig{Rules: map[string]string{
		"forgeconv-deps-are-interfaces":             "warn",
		"forgeconv-internal-package-contract-names": "off",
	}}.EffectiveRules()

	js, gated, err := collectConventionsJSON(forgeconv.LintOptions{Strict: true})
	if err != nil {
		t.Fatal(err)
	}
	fs, _ := gradeLane(rules, js, gated)
	var deps *lintJSONFinding
	for i, f := range fs {
		switch f.Rule {
		case "forgeconv-deps-are-interfaces":
			deps = &fs[i]
		case "forgeconv-internal-package-contract-names":
			t.Errorf("lint.rules off must drop contract-names: %+v", f)
		}
	}
	if deps == nil || deps.Severity != lintSevWarning {
		t.Errorf("lint.rules warn must downgrade deps-are-interfaces: %+v", deps)
	}
}
