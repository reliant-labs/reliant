package lint

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/linter/contract"
)

// requirecontract through the lane `forge lint` runs: it covers the module's
// root internal/ only, and its finding names every way out. Red before: the
// SDK's pkg/internal/conn was flagged too, and the finding carried the
// generic analyzer hint ("declare the exported method in the contract
// interface, or unexport it"), which names no contract to declare it in and
// neither opt-out.
func TestContractLane_RequireContractScopeAndRemedies(t *testing.T) {
	if testing.Short() {
		t.Skip("loads a module with go/packages; skipped under -short")
	}
	dir := t.TempDir()
	for name, body := range map[string]string{
		"go.mod": "module example.com/sdk\n\ngo 1.24\n",
		"internal/svc/svc.go": `package svc

type Service struct{}

func (s *Service) DoWork() error { return nil }
`,
		"pkg/internal/conn/conn.go": `package conn

type Conn struct{}

func (c *Conn) Dial() error { return nil }
`,
	} {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)
	diags, err := runContractAnalysisInProcess(context.Background(), []string{"./..."}, nil, contract.ExcludeGateOptions{})
	if err != nil {
		t.Fatalf("in-process contract analysis: %v", err)
	}
	var found []contractDiagnostic
	for _, d := range diags {
		if d.Analyzer == contract.RequireContractAnalyzer.Name {
			found = append(found, d)
		}
	}
	if len(found) != 1 || filepath.Base(filepath.Dir(found[0].Pos.Filename)) != "svc" {
		t.Fatalf("requirecontract findings = %v, want exactly one, on internal/svc", found)
	}
	for _, want := range []string{
		"//forge:contract", "contract.go", "unexport",
		"//forge:exclude-contract: <why>", "outbound I/O", "two implementations",
		"contracts.exclude",
	} {
		if !strings.Contains(found[0].FixHint, want) {
			t.Errorf("requirecontract remediation does not name %q: %s", want, found[0].FixHint)
		}
	}
}
