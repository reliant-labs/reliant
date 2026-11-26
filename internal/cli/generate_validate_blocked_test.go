package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

func TestValidate_BlockedConsumersStillReportContractMismatches(t *testing.T) {
	testkit.Slow(t, "compiles a temp module and type-checks its blocked packages from source")
	dir := writeValidateFixture(t, map[string]string{
		"store/contract_gen.go": `package store
// Service is the regenerated contract. Read used to take no arguments.
type Service interface { Read(within int) int }
`,
		"api/caller.go": `package api
import "example.com/validateapp/store"
func Read(s store.Service) int { return s.Read() }
`,
		"assigner/deps.go": `package assigner
type Store interface { Read() int }
type Deps struct { Store Store }
`,
		"loadsource/deps.go": `package loadsource
type Store interface { Read() int }
type Deps struct { Store Store }
`,
		"app/compose.go": `package app
import (
    "example.com/validateapp/api"
    "example.com/validateapp/assigner"
    "example.com/validateapp/store"
)
func Compose(s store.Service) (int, assigner.Deps) {
    return api.Read(s), assigner.Deps{Store: s}
}
`,
		"worker/compose.go": `package worker
import (
    "example.com/validateapp/api"
    "example.com/validateapp/loadsource"
    "example.com/validateapp/store"
)
func Compose(s store.Service) (int, loadsource.Deps) {
    return api.Read(s), loadsource.Deps{Store: s}
}
`,
		"ok/ok.go": `package ok
import "example.com/validateapp/api"
var Read = api.Read
`,
	})

	_, restore := captureStderr(t)
	err := runGoBuildValidate(dir)
	restore()
	if err == nil || !strings.Contains(err.Error(), "api/caller.go:3:") {
		t.Fatalf("the actual compiler failure must remain the root cause, got %v", err)
	}
	out := errOutputOf(err)
	for _, want := range []string{
		"# example.com/validateapp/app (type-checked from source; dependency did not compile)", "app/compose.go:8:", "assigner.Store",
		"# example.com/validateapp/worker (type-checked from source; dependency did not compile)", "worker/compose.go:8:", "loadsource.Store",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("validation output missing %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "api/caller.go:3:") != 1 || strings.Contains(out, "# example.com/validateapp/ok") {
		t.Errorf("must not repeat the original error or blame an unaffected package:\n%s", out)
	}
}

func TestBlockedBuildPackages(t *testing.T) {
	root := t.TempDir()
	upstream := []*listedPackageError{{Err: "a dependency failed"}}
	pkgs := []*listedPackage{
		{ImportPath: "example.com/blocked", Dir: filepath.Join(root, "blocked"), DepsErrors: upstream},
		{ImportPath: "example.com/good", Dir: filepath.Join(root, "good")},
		{ImportPath: "example.com/already-reported", Dir: root, DepsErrors: upstream, Error: &listedPackageError{Err: "own failure"}},
		{ImportPath: "example.com/only-dependency", Dir: root, DepOnly: true, DepsErrors: upstream},
		{ImportPath: "example.com/test-variant", Dir: root, ForTest: "example.com/test-variant", DepsErrors: upstream},
		{ImportPath: "example.com/outside", Dir: filepath.Join(root, "..", "external"), DepsErrors: upstream},
	}
	got := blockedBuildPackages(root, pkgs)
	if len(got) != 1 || got[0] != "example.com/blocked" {
		t.Fatalf("blockedBuildPackages = %v, want only the unchecked in-project root", got)
	}
}
