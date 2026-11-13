package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// goTidyMissingConfig is go's own report from the P3b pilot's failure shape:
// a scaffolded handler importing a pkg/config the project does not have,
// beside a gen package buf did not write.
const goTidyMissingConfig = `go: finding module for package example.com/f6/pkg/config
go: example.com/f6/internal/handlers/greeter imports
	example.com/f6/pkg/config: cannot find module providing package example.com/f6/pkg/config: unrecognized import path "example.com/f6/pkg/config": reading https://example.com/f6/pkg/config?go-get=1: 404 Not Found
go: example.com/f6/internal/handlers/greeter imports
	example.com/f6/gen/f6/v1: no matching versions for query "latest"
go: example.com/f6/internal/handlers/greeter imports
	github.com/acme/missing: module github.com/acme/missing: reading https://proxy.golang.org/github.com/acme/missing/@v/list: 404 Not Found
`

func TestTidyFailureCause_NamesTheMissingPackages(t *testing.T) {
	got := tidyFailureCause(goTidyMissingConfig, "example.com/f6")
	for _, want := range []string{
		"code imports packages that do not exist in this module (example.com/f6), so no go.mod or go.sum change can fix it",
		"example.com/f6/internal/handlers/greeter imports example.com/f6/pkg/config — typed config",
		"example.com/f6/internal/handlers/greeter imports example.com/f6/gen/f6/v1 — generated stubs",
		"a dependency cannot be resolved",
		"github.com/acme/missing",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("cause lacks %q:\n%s", want, got)
		}
	}
	if tidyFailureCause("go: errors parsing go.mod:\ngo.mod:3: unknown directive: requir\n", "example.com/f6") != "" {
		t.Error("a go.mod problem names no import; the go.mod advice stands for it")
	}
}

// The real tidy, end to end: its error names the missing in-module package
// instead of advising a go.mod or go.sum edit.
func TestRunGoModTidyRoot_ErrorNamesTheMissingPackage(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: runs `go mod tidy`; runs in task test")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not on PATH")
	}
	dir := t.TempDir()
	for rel, body := range map[string]string{
		"go.mod":                               "module example.com/app\n\ngo 1.24\n",
		"internal/handlers/greeter/service.go": "package greeter\n\nimport _ \"example.com/app/pkg/config\"\n",
	} {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOFLAGS", "-mod=mod")
	t.Setenv("GOWORK", "off")
	err := runGoModTidyRoot(dir)
	if err == nil {
		t.Fatal("tidy must fail on a missing in-module import")
	}
	msg := err.Error()
	if !strings.Contains(msg, "example.com/app/internal/handlers/greeter imports example.com/app/pkg/config") {
		t.Errorf("the error must name the import, got: %s", msg)
	}
	if strings.Contains(msg, "fix go.mod or go.sum") {
		t.Errorf("the error must not blame go.mod/go.sum: %s", msg)
	}
}

func TestMissingHarnessSeam(t *testing.T) {
	errs := []string{"/p/internal/handlers/q/helpers_gen_test.go:152:72: undefined: middleware.ContextWithClaims"}
	if got := missingHarnessSeam(errs); !strings.Contains(got, "pkg/middleware.ContextWithClaims") || strings.Contains(got, "forge codegen bug") {
		t.Errorf("a missing ContextWithClaims is the project's seam, got %q", got)
	}
	if missingHarnessSeam([]string{"/p/x_gen_test.go:1:1: undefined: foo"}) != "" {
		t.Error("other harness errors keep the codegen-bug wording")
	}
}
