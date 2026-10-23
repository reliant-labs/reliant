package lint

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/linter/contract"
)

// A package directive is a property of the package, declared from any file.
// The gate used to read the first REASONED marker by file name and honour only
// that marker's allowances, so which file declared the package's answer was an
// accident of naming: reliant moved approval.go (the file holding the
// outbound-io allowance) out of internal/grpc/services, connection.go's marker
// became "the" declaration, and the package was refused although nothing
// about it had changed. Now every repetition must agree, and two that do not
// are refused naming both.
func writePackageDeclarationFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	probe := `
import "net/http"

func Probe(url string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}
`
	files := map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.24\n",

		// The same declaration twice: one answer, accepted.
		"internal/dup/a.go": "//forge:exclude-contract: pure helpers\npackage dup\n\nfunc A() int { return 1 }\n",
		"internal/dup/b.go": "//forge:exclude-contract: pure helpers\npackage dup\n\nfunc B() int { return 2 }\n",

		// Two reasons: two answers.
		"internal/split/a.go": "//forge:exclude-contract: pure helpers\npackage split\n\nfunc A() int { return 1 }\n",
		"internal/split/b.go": "//forge:exclude-contract: vocabulary types\npackage split\n\nfunc B() int { return 2 }\n",

		// A bare marker beside a reasoned one used to be hidden by it.
		"internal/bareplus/a.go": "//forge:exclude-contract\npackage bareplus\n\nfunc A() int { return 1 }\n",
		"internal/bareplus/b.go": "//forge:exclude-contract: pure helpers\npackage bareplus\n\nfunc B() int { return 2 }\n",

		// The reliant shape: the allowance sits on a marker that does not
		// sort first. Before, the first marker's missing allowance decided,
		// and the package was refused for outbound I/O at a.go.
		"internal/halfallow/a.go": "//forge:exclude-contract: legacy probe\npackage halfallow\n",
		"internal/halfallow/b.go": "//forge:lint-disable-next-line forge-exclude-contract-outbound-io: reviewed, converting in #1\n" +
			"//forge:exclude-contract: legacy probe\npackage halfallow\n" + probe,

		// The recommended shape: one declaration, in doc.go, with its
		// allowance; the I/O lives in a file that sorts before it.
		"internal/docdecl/client.go": "package docdecl\n" + probe,
		"internal/docdecl/doc.go": "// Package docdecl probes a vendor.\n//\n" +
			"//forge:lint-disable-next-line forge-exclude-contract-outbound-io: reviewed, converting in #1\n" +
			"//forge:exclude-contract: legacy probe\npackage docdecl\n",

		// The same rule for not-a-component.
		"internal/nac/contract.go": "//forge:not-a-component: built by the CLI\npackage nac\n\ntype Service interface{ Run() error }\n",
		"internal/nac/other.go":    "//forge:not-a-component: built by the daemon\npackage nac\n",
	}
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestPackageDeclaration_OneAnswerFromAnyFile(t *testing.T) {
	if testing.Short() {
		t.Skip("loads a module with go/packages (type-checks net/http); skipped under -short")
	}
	t.Chdir(writePackageDeclarationFixture(t))
	diags, err := runContractAnalysisInProcess(context.Background(), []string{"./..."}, nil, contract.ExcludeGateOptions{})
	if err != nil {
		t.Fatalf("in-process contract analysis: %v", err)
	}
	byPkg := map[string][]contractDiagnostic{}
	for _, d := range diags {
		if strings.HasPrefix(d.Analyzer, "forge-exclude-contract") || strings.HasPrefix(d.Analyzer, "forge-not-a-component") ||
			strings.HasPrefix(d.Analyzer, "forge-suppression") {
			pkg := filepath.Base(filepath.Dir(d.Pos.Filename))
			byPkg[pkg] = append(byPkg[pkg], d)
		}
	}

	for _, pkg := range []string{"dup", "docdecl"} {
		if ds := byPkg[pkg]; len(ds) != 0 {
			t.Errorf("%s: one declaration (repeated identically, or in doc.go) was refused: %v", pkg, rulesOf(ds))
		}
	}

	for pkg, want := range map[string][]string{
		"split":     {"a.go:1", "b.go:1", `"pure helpers"`, `"vocabulary types"`},
		"bareplus":  {"a.go:1", "b.go:1", "no reason"},
		"halfallow": {"a.go:1", "b.go:2", "an allowance for forge-exclude-contract-outbound-io", "with no allowance"},
	} {
		ds := byPkg[pkg]
		if len(ds) != 1 || ds[0].Analyzer != contract.RuleExcludeConflict {
			t.Errorf("%s: want exactly one %s, got %v", pkg, contract.RuleExcludeConflict, rulesOf(ds))
			continue
		}
		for _, w := range want {
			if !strings.Contains(ds[0].Message, w) {
				t.Errorf("%s: the refusal must name %q: %s", pkg, w, ds[0].Message)
			}
		}
		if !strings.Contains(ds[0].FixHint, "doc.go") || ds[0].Warning {
			t.Errorf("%s: want a gating refusal pointing at doc.go, got %+v", pkg, ds[0])
		}
	}

	ds := byPkg["nac"]
	if len(ds) != 1 || ds[0].Analyzer != contract.RuleNotAComponentConflict ||
		!strings.Contains(ds[0].Message, "contract.go:1") || !strings.Contains(ds[0].Message, "other.go:1") {
		t.Errorf("nac: want one %s naming both files, got %+v", contract.RuleNotAComponentConflict, ds)
	}
}
