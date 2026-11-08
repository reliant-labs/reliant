package lint

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/linter/contract"
)

// The not-a-component gate (internal/linter/contract/not_a_component_directive.go),
// driven through the in-process contract lane `forge lint` runs. One module
// holds a package per verdict, keyed by its directory's base name.
func writeNotAComponentFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	contractSrc := func(pkg, header string) string {
		return header + "package " + pkg + "\n\ntype Service interface{ Do() error }\n\ntype Deps struct{}\n\nfunc New(d Deps) Service { return nil }\n"
	}
	files := map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.24\n",

		// Reasoned, on an internal contract package that does outbound I/O:
		// accepted. The marker removes the package from the server, not from
		// the contract system, so the exclusion gate's I/O refusal is not
		// its business.
		"internal/cliauth/contract.go": contractSrc("cliauth", "//forge:not-a-component: CLI-only: built by the login command\n"),
		"internal/cliauth/http.go": `package cliauth

import "net/http"

func ping(url string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}
`,
		// Bare: refused for the missing reason only.
		"internal/bareauth/contract.go": contractSrc("bareauth", "//forge:not-a-component\n"),
		// A handler is a server component by definition.
		"internal/handlers/orders/service.go": "//forge:not-a-component: misplaced\npackage orders\n\ntype Service struct{}\n",
		// A contract package nested BELOW a handler is an ordinary internal
		// package to composition, which honours the marker there.
		"internal/handlers/orders/pricing/contract.go": contractSrc("pricing", "//forge:not-a-component: only the importer CLI prices offline\n"),
		// A worker is a server component by definition.
		"internal/workers/sweep/worker.go": "//forge:not-a-component: misplaced\npackage sweep\n",
		// Contradicts external-component.
		"internal/both/contract.go": contractSrc("both", "//forge:external-component\n//forge:not-a-component: who knows\n"),
		// No contract.go: never composed anyway.
		"internal/nocontract/x.go": "//forge:not-a-component: nothing to compose\npackage nocontract\n\nfunc X() int { return 1 }\n",
		// Already out of composition via exclude-contract.
		"internal/excluded/contract.go": contractSrc("excluded", "//forge:exclude-contract: pure data\n//forge:not-a-component: belt and braces\n"),
		// Outside internal/: forge composes nothing there.
		"pkg/lib/contract.go": contractSrc("lib", "//forge:not-a-component: library\n"),
	}
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func notAComponentDiags(t *testing.T, gate contract.ExcludeGateOptions) map[string][]contractDiagnostic {
	t.Helper()
	if testing.Short() {
		t.Skip("loads a module with go/packages (type-checks net/http); skipped under -short")
	}
	t.Chdir(writeNotAComponentFixture(t))
	diags, err := runContractAnalysisInProcess(context.Background(), []string{"./..."}, nil, gate)
	if err != nil {
		t.Fatalf("in-process contract analysis: %v", err)
	}
	byPkg := map[string][]contractDiagnostic{}
	for _, d := range diags {
		if !strings.HasPrefix(d.Analyzer, "forge-not-a-component") {
			continue
		}
		pkg := filepath.Base(filepath.Dir(d.Pos.Filename))
		byPkg[pkg] = append(byPkg[pkg], d)
	}
	return byPkg
}

func TestNotAComponentGate(t *testing.T) {
	byPkg := notAComponentDiags(t, contract.ExcludeGateOptions{})

	for _, pkg := range []string{"cliauth", "pricing"} {
		if ds := byPkg[pkg]; len(ds) != 0 {
			t.Errorf("%s: a reasoned marker on an internal contract package must be accepted, got %v", pkg, rulesOf(ds))
		}
	}

	t.Run("bare marker is refused", func(t *testing.T) {
		ds := byPkg["bareauth"]
		if len(ds) != 1 || ds[0].Analyzer != contract.RuleNotAComponentReason {
			t.Fatalf("bare marker: got %v, want only %s", rulesOf(ds), contract.RuleNotAComponentReason)
		}
		if ds[0].Warning || !strings.Contains(ds[0].FixHint, "//forge:not-a-component: <why>") {
			t.Errorf("refusal must gate and name the reasoned spelling: %+v", ds[0])
		}
	})

	for pkg, want := range map[string]string{
		"orders":     "service handler",
		"sweep":      "worker",
		"both":       "external-component",
		"nocontract": "no contract.go",
		"excluded":   "exclude-contract",
		"lib":        "only contract packages under internal/",
	} {
		t.Run("misplaced in "+pkg, func(t *testing.T) {
			ds := byPkg[pkg]
			if !hasRule(ds, contract.RuleNotAComponentScope) {
				t.Fatalf("%s: misplaced marker not reported: %v", pkg, rulesOf(ds))
			}
			if !strings.Contains(ds[0].Message, want) {
				t.Errorf("%s: message should say %q: %s", pkg, want, ds[0].Message)
			}
		})
	}
}

// Without forge.yaml there is no composition for the marker to be misplaced
// in, so only the reason is gated.
func TestNotAComponentGate_CodegenUnavailableKeepsOnlyTheReason(t *testing.T) {
	byPkg := notAComponentDiags(t, contract.ExcludeGateOptions{CodegenUnavailable: true})
	for pkg, ds := range byPkg {
		for _, d := range ds {
			if d.Analyzer == contract.RuleNotAComponentScope {
				t.Errorf("%s: placement must not be gated without codegen: %s", pkg, d.Message)
			}
		}
	}
	if !hasRule(byPkg["bareauth"], contract.RuleNotAComponentReason) {
		t.Errorf("a bare marker is refused everywhere, codegen or not")
	}
}
