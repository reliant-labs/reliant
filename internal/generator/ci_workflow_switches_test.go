package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
)

// ci.workflows switches single workflows off. A switched-off workflow is gone
// from every writer's list; the gate still plans from ci.yml's data.
func TestCIWorkflows_SwitchedOffWorkflowsAreNotScaffolded(t *testing.T) {
	root := t.TempDir()
	cfg := serviceCfg()
	if ciFile(CIWorkflowsFor(root, cfg, CIInputs{}), ".github/workflows/deploy.yml") == nil {
		t.Fatal("precondition: a service project has deploy.yml")
	}
	cfg.CI.Workflows = map[string]bool{"deploy": false, "build-images": false, "ci": false, "dependabot": true}
	files := CIWorkflowsFor(root, cfg, CIInputs{})
	for _, off := range []string{".github/workflows/deploy.yml", ".github/workflows/build-images.yml", ".github/workflows/ci.yml"} {
		if ciFile(files, off) != nil {
			t.Errorf("%s is switched off but still scaffolded", off)
		}
	}
	if ciFile(files, ".github/dependabot.yml") == nil {
		t.Error("dependabot: true must keep dependabot.yml")
	}
	if data := CIWorkflowDataFor(root, cfg, CIInputs{}); !data.VerifyGenerated {
		t.Error("ci: false must not empty the gate's plan: CIWorkflowDataFor reads the full set")
	}
	if got := CIWorkflowAbsenceReasonFor(cfg, ".github/workflows/deploy.yml"); !strings.Contains(got, "ci.workflows: {deploy: false}") {
		t.Errorf("absence reason = %q, want it to name the switch", got)
	}
}

func TestCIWorkflowName(t *testing.T) {
	for rel, want := range map[string]string{
		".github/workflows/deploy.yml":         "deploy",
		".github/workflows/proto-breaking.yml": "proto-breaking",
		".github/dependabot.yml":               "dependabot",
		ForgeDeployActionPath:                  "release",
		"README.md":                            "",
	} {
		if got := CIWorkflowName(rel); got != want {
			t.Errorf("CIWorkflowName(%q) = %q, want %q", rel, got, want)
		}
	}
}

// A deploy.yml the repository wrote itself is never adopted silently, and a
// workflow forge scaffolded is never mistaken for one.
func TestForeignCIWorkflow(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "forge.yaml"), []byte("name: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	checksums.ResetScaffoldLedgerCache()
	t.Cleanup(checksums.ResetScaffoldLedgerCache)

	write(".github/workflows/deploy.yml", "name: Deploy\non: push\n")
	write(".github/workflows/ci.yml", "# yours: scaffolded once, never touched again — forge will not overwrite this file.\nname: CI\n")
	write(".github/workflows/release.yml", "# Release — no banner, but forge wrote it\n")
	checksums.RecordScaffold(root, ".github/workflows/release.yml")

	if !ForeignCIWorkflow(root, ".github/workflows/deploy.yml") {
		t.Error("a banner-less deploy.yml forge never scaffolded is the project's own")
	}
	if ForeignCIWorkflow(root, ".github/workflows/ci.yml") {
		t.Error("a file with forge's scaffold banner is forge's")
	}
	if ForeignCIWorkflow(root, ".github/workflows/release.yml") {
		t.Error("a file the birth ledger records is forge's, banner or not")
	}
	if ForeignCIWorkflow(root, ".github/workflows/e2e.yml") {
		t.Error("an absent file is not foreign")
	}
	if n := ForeignCIWorkflowNotice(".github/workflows/deploy.yml"); !strings.Contains(n, "ci.workflows: {deploy: false}") {
		t.Errorf("notice %q does not name the switch", n)
	}
}
