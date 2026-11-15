package modguard_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// forbiddenModules are forge's CONSUMERS. The dependency direction is
// one-way: control-plane and reliant import forge (pkg/deploy is the plug
// point the control plane consumes), and forge talks to them over the wire,
// never through a Go import. A forge→consumer import makes the OSS base of the
// stack uninstallable without private code, and it creates a cycle the moment
// the consumer imports forge back, which it already does.
var forbiddenModules = []string{
	"github.com/reliant-labs/control-plane",
	"github.com/reliant-labs/reliant",
}

// TestGoModDoesNotRequireConsumers checks forge's go.mod require list.
func TestGoModDoesNotRequireConsumers(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	for i, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) == 0 || strings.HasPrefix(fields[0], "//") {
			continue
		}
		mod := fields[0]
		if mod == "require" && len(fields) > 1 {
			mod = fields[1]
		}
		for _, bad := range forbiddenModules {
			if mod == bad || strings.HasPrefix(mod, bad+"/") {
				t.Errorf("go.mod:%d requires %s — forge must never depend on its consumers", i+1, mod)
			}
		}
	}
}

// TestNoPackageImportsConsumers is the stronger check: no package in forge's
// build graph comes from a consumer module. That covers a transitive import a
// go.mod scan cannot see.
func TestNoPackageImportsConsumers(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go list over the module (seconds); full mode only")
	}
	t.Parallel()
	cmd := exec.Command("go", "list", "-deps", "-test", "./...")
	cmd.Dir = repoRoot(t)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	for _, pkg := range strings.Split(string(out), "\n") {
		for _, bad := range forbiddenModules {
			if pkg == bad || strings.HasPrefix(pkg, bad+"/") {
				t.Errorf("forge's build graph contains %s", pkg)
			}
		}
	}
}
