package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/linter/contract"
)

func TestVettoolPrintsContractFixHints(t *testing.T) {
	if testing.Short() {
		t.Skip("builds contractlint and runs go vet with the built tool")
	}
	root := t.TempDir()
	tool := filepath.Join(root, "contractlint")
	if runtime.GOOS == "windows" {
		tool += ".exe"
	}
	if out, err := exec.CommandContext(t.Context(), "go", "build", "-o", tool, ".").CombinedOutput(); err != nil {
		t.Fatalf("build vettool: %v\n%s", err, out)
	}
	for name, body := range map[string]string{
		"go.mod": "module example.com/demo\n\ngo 1.23\n",
		"internal/missing/service.go": `package missing

type Service struct{}
func (Service) Run() {}
`,
		"internal/extra/contract.go": `package extra

type Service interface { Run() }
`,
		"internal/extra/service.go": `package extra

type implementation struct{}
func (implementation) Run() {}
func (implementation) Extra() {}
`,
	} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.CommandContext(t.Context(), "go", "vet", "-vettool="+tool, "./internal/...")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("vettool accepted both violations:\n%s", out)
	}
	for _, want := range []string{
		"package missing has exported methods but no contract.go",
		"exported method Extra on type implementation is not declared",
		"Fix: " + contract.RequireContractFixHint,
		"Fix: " + contract.FixHint,
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("vettool output lacks %q:\n%s", want, out)
		}
	}
}
