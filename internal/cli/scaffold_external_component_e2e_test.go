//go:build e2e

package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestE2EExternalComponentStaleDepsKeyIsDropped is the issue's repro (#747),
// run through the real generate pipeline and its validate build.
//
// A scaffolded adapter is composed by internal/app/compose.go. The author
// then marks it //forge:external-component and removes a Deps field. The
// stale-key drop only measured discovered components' literals, so the key
// survived in compose.go — which still constructs the package — and every
// generate after that failed and rolled back:
//
//	❌ ROOT CAUSE: go build failed: internal/app/compose.go:62:3: unknown field BaseURL in struct literal of type alpaca.Deps
//
// The field removed here is one the test adds itself, so the repro does not
// depend on what the adapter template's Deps happens to declare.
func TestE2EExternalComponentStaleDepsKeyIsDropped(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "extapp", "--mod", "example.com/extapp", "--service", "orders")
	projectDir := filepath.Join(dir, "extapp")
	addCorpusForgePkgReplace(t, projectDir)

	runCmd(t, projectDir, forgeBin, "scaffold", "adapter", "alpaca")

	adapterPath := filepath.Join(projectDir, "internal", "alpaca", "adapter.go")
	const depsOpen = "type Deps struct {\n"
	const region = "\tRegion string\n"
	adapter := readFileE2E(t, adapterPath)
	if !strings.Contains(adapter, depsOpen) {
		t.Fatalf("adapter.go has no %q:\n%s", depsOpen, adapter)
	}
	writeFileE2E(t, adapterPath, strings.Replace(adapter, depsOpen, depsOpen+region, 1))
	runCmd(t, projectDir, forgeBin, "generate")

	composePath := filepath.Join(projectDir, "internal", "app", "compose.go")
	if compose := readFileE2E(t, composePath); !strings.Contains(compose, "alpaca.Deps{") || !strings.Contains(compose, "Region:") {
		t.Fatalf("precondition: compose.go constructs alpaca with a Region key:\n%s", compose)
	}

	// The author takes the adapter out of the composition and drops Region.
	contractPath := filepath.Join(projectDir, "internal", "alpaca", "contract.go")
	contract := readFileE2E(t, contractPath)
	if !strings.Contains(contract, "// forge:outbound-io\n") {
		t.Fatalf("contract.go has no forge:outbound-io marker to anchor on:\n%s", contract)
	}
	writeFileE2E(t, contractPath, strings.Replace(contract, "// forge:outbound-io\n", "// forge:outbound-io\n//forge:external-component\n", 1))
	writeFileE2E(t, adapterPath, strings.Replace(readFileE2E(t, adapterPath), region, "", 1))

	out, err := runCmdAllowFail(t, projectDir, forgeBin, "generate")
	if err != nil {
		t.Fatalf("generate after marking the adapter external and removing a Deps field failed:\n%s", out)
	}
	compose := readFileE2E(t, composePath)
	if strings.Contains(compose, "Region:") {
		t.Fatalf("compose.go still keys the removed Region field:\n%s", compose)
	}
	if !strings.Contains(compose, "alpaca.Deps{") {
		t.Fatalf("the construction is the author's code and must be kept:\n%s", compose)
	}
	const warning = "internal/app/compose.go still constructs internal/alpaca, which is now //forge:external-component"
	if !strings.Contains(out, warning) {
		t.Fatalf("generate must say compose.go still constructs the external adapter (%q); output:\n%s", warning, out)
	}
	runCmd(t, projectDir, "go", "build", "./...")
}
