package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/config"
)

// TestScaffoldForgeResolution pins the decision that stops `forge project new`
// from writing a project that requires the RETIRED module
// github.com/reliant-labs/forge/pkg.
//
// Reproduced before the fix with `go build -trimpath ./cmd/forge` — the build
// `task install` makes — then `forge project new demo`: the scaffold pinned no
// forge (the build is on no proxy), had no source tree to bridge to (trimpath),
// and `go mod tidy` resolved github.com/reliant-labs/forge/pkg/forgepb to
// github.com/reliant-labs/forge/pkg v0.1.15. The first `forge generate` then
// refused the project with "requires the retired module".
func TestScaffoldForgeResolution(t *testing.T) {
	for _, tc := range []struct {
		name       string
		kind       string
		pinned     string
		bridgeRoot string
		linkable   string
		wantErr    bool
		wantFix    string
	}{
		{"service, neither pin nor bridge, no checkout", config.ProjectKindService, "", "", "", true, "task install:dev"},
		// A checkout exists but the user did not opt in: refuse, and say how.
		{"service, linkable but not linked", config.ProjectKindService, "", "", "/src/forge", true, "--link-forge"},
		{"service, published pin", config.ProjectKindService, "v0.1.17", "", "/src/forge", false, ""},
		{"service, linked", config.ProjectKindService, "", "/src/forge", "/src/forge", false, ""},
		{"service, both", config.ProjectKindService, "v0.1.17", "/src/forge", "/src/forge", false, ""},
		// CLI and library scaffolds import no forge package, so they have
		// nothing to resolve and must not be refused.
		{"cli, neither", config.ProjectKindCLI, "", "", "", false, ""},
		{"library, neither", config.ProjectKindLibrary, "", "", "", false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := scaffoldForgeResolution(tc.kind, tc.pinned, tc.bridgeRoot, tc.linkable)
			if (err != nil) != tc.wantErr {
				t.Fatalf("scaffoldForgeResolution(%q, %q, %q, %q) error = %v, wantErr %v",
					tc.kind, tc.pinned, tc.bridgeRoot, tc.linkable, err, tc.wantErr)
			}
			if err == nil {
				return
			}
			msg := err.Error()
			for _, want := range []string{"github.com/reliant-labs/forge/pkg", "Nothing was written", tc.wantFix} {
				if !strings.Contains(msg, want) {
					t.Errorf("error does not mention %q — it must name the failure and a fix:\n%s", want, msg)
				}
			}
		})
	}
}

// TestCheckScaffoldCanResolveForge_LinkWithoutCheckout: --link-forge on a
// binary that cannot locate its checkout is refused before anything is
// written, whatever the kind — there is nothing to bridge to.
func TestCheckScaffoldCanResolveForge_LinkWithoutCheckout(t *testing.T) {
	prevRoot := buildinfo.DevForgeRoot
	buildinfo.DevForgeRoot = ""
	t.Cleanup(func() { buildinfo.DevForgeRoot = prevRoot })
	buildinfo.SetDiscoveredForgeRoot("")
	t.Cleanup(buildinfo.ClearDiscoveredForgeRoot)

	t.Setenv(buildinfo.ForgeRootEnv, "")
	err := checkScaffoldCanResolveForge(config.ProjectKindCLI, true, nil)
	if err == nil || !strings.Contains(err.Error(), "cannot locate a forge checkout") || !strings.Contains(err.Error(), "--forge-root") {
		t.Fatalf("--link-forge without a checkout must be refused, got %v", err)
	}
	if err := checkScaffoldCanResolveForge(config.ProjectKindCLI, false, nil); err != nil {
		t.Errorf("a cli scaffold without --link-forge needs nothing: %v", err)
	}
}

// TestRunNew_RefusesUnresolvableForgeBeforeWriting drives the real command
// path: a service scaffold from a build that can neither pin nor bridge must
// fail, and must leave NO project directory behind.
func TestRunNew_RefusesUnresolvableForgeBeforeWriting(t *testing.T) {
	buildinfo.SetDevBuild(true)
	t.Cleanup(buildinfo.ClearDevBuild)
	prevRoot := buildinfo.DevForgeRoot
	buildinfo.DevForgeRoot = ""
	t.Cleanup(func() { buildinfo.DevForgeRoot = prevRoot })
	buildinfo.SetDiscoveredForgeRoot("")
	t.Cleanup(buildinfo.ClearDiscoveredForgeRoot)
	buildinfo.Set("dev", "", "unknown") // not installable: no proxy version to pin
	t.Cleanup(func() { buildinfo.Set("dev", "", "unknown") })

	parent := t.TempDir()
	err := runNew(t.Context(), "demo", parent, "github.com/example/demo", config.ProjectKindService,
		nil, nil, "", false, false, nil, "", true, "local", "", false, false, false, false, newFrontendOptions{})
	if err == nil {
		t.Fatal("runNew succeeded for a build that can neither pin nor bridge forge; " +
			"its go.mod would have required the retired forge/pkg module")
	}
	if _, statErr := os.Stat(filepath.Join(parent, "demo")); !os.IsNotExist(statErr) {
		t.Errorf("runNew refused but left %s behind (stat err = %v); the refusal must precede any write",
			filepath.Join(parent, "demo"), statErr)
	}
}
