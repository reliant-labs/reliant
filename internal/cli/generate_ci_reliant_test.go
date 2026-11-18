// Tests for `ci.provider: reliant` in the CI writers: `forge generate`'s CI
// step (generateCIWorkflows) and `forge project rescaffold`.
//
// Not t.Parallel: the birth ledger's in-memory cache is process-global.
package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator"
)

// reliantProject is a project directory that already has the GitHub
// workflow forge scaffolded for it (edited by the user since), with a config
// that has just been switched to ci.provider reliant.
func reliantProject(t *testing.T) (string, *config.ProjectConfig, []byte) {
	t.Helper()
	root := t.TempDir()
	checksums.ResetScaffoldLedgerCache()
	t.Cleanup(checksums.ResetScaffoldLedgerCache)
	cfg := &config.ProjectConfig{Name: "demo", ModulePath: "github.com/example/demo", Kind: config.ProjectKindService}
	config.ApplyDerivedDefaults(cfg)
	cfg.CI.Provider = config.CIProviderReliant

	const ciYML = ".github/workflows/ci.yml"
	userCI := []byte("# yours: scaffolded once, never touched again — forge will not overwrite this file.\nname: CI\n# the user's edits\n")
	if err := os.MkdirAll(filepath.Join(root, ".github", "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ciYML), userCI, 0o644); err != nil {
		t.Fatal(err)
	}
	checksums.RecordScaffold(root, ciYML)
	return root, cfg, userCI
}

// Switching ci.provider to reliant writes the Reliant workflows, leaves the
// GitHub ones exactly as they are (they are the user's), and says what to do
// next — once, on the run that wrote them.
func TestGenerateCIWorkflows_SwitchToReliantKeepsTheGitHubFiles(t *testing.T) {
	root, cfg, userCI := reliantProject(t)

	out := captureStdout(t, func() {
		if err := generateCIWorkflows(root, cfg, nil, false); err != nil {
			t.Fatalf("generateCIWorkflows: %v", err)
		}
	})
	for _, rel := range []string{generator.ReliantCIWorkflowPath, generator.ReliantReviewWorkflowPath} {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("%s not written: %v", rel, err)
		}
		if !strings.Contains(string(b), "check:\n  name: demo/"+generator.CIWorkflowName(rel)+"\n") {
			t.Errorf("%s does not post the check demo/%s", rel, generator.CIWorkflowName(rel))
		}
		if !checksums.ScaffoldRecorded(root, rel) {
			t.Errorf("%s has no birth record: deleting it would not stick", rel)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(root, ".github/workflows/ci.yml")); !bytes.Equal(got, userCI) {
		t.Errorf("the GitHub workflow changed on the provider switch:\n%s", got)
	}
	for _, rel := range []string{".github/workflows/proto-breaking.yml", ".github/dependabot.yml"} {
		if _, err := os.Stat(filepath.Join(root, rel)); err == nil {
			t.Errorf("ci.provider reliant still wrote %s", rel)
		}
	}
	for _, want := range []string{"ci.provider: reliant", "reliant trigger activate --workflow ci", "demo/ci", "skill load ci/reliant"} {
		if !strings.Contains(out, want) {
			t.Errorf("the switch prints no %q:\n%s", want, out)
		}
	}

	again := captureStdout(t, func() {
		if err := generateCIWorkflows(root, cfg, nil, false); err != nil {
			t.Fatalf("second generateCIWorkflows: %v", err)
		}
	})
	if strings.Contains(again, "reliant trigger activate") {
		t.Errorf("a run that wrote nothing repeats the switch guidance:\n%s", again)
	}
}

// A deleted Reliant workflow stays deleted under generate and comes back,
// byte-identical, through rescaffold. A file of the other provider is refused
// with the setting that would give it.
func TestRescaffold_ReliantWorkflows(t *testing.T) {
	root, cfg, _ := reliantProject(t)
	captureStdout(t, func() {
		if err := generateCIWorkflows(root, cfg, nil, false); err != nil {
			t.Fatalf("generateCIWorkflows: %v", err)
		}
	})
	rel := generator.ReliantCIWorkflowPath
	born := readRescaffoldFile(t, root, rel)
	removeRescaffoldFile(t, root, rel)
	captureStdout(t, func() {
		if err := generateCIWorkflows(root, cfg, nil, false); err != nil {
			t.Fatalf("generateCIWorkflows: %v", err)
		}
	})
	if rescaffoldFileExists(root, rel) {
		t.Fatal("generate re-created a Reliant workflow the user deleted")
	}

	gen := rescaffoldGenerate{ci: func() error { return generateCIWorkflows(root, cfg, nil, false) }}
	var w bytes.Buffer
	captureStdout(t, func() {
		if err := rescaffoldPaths(&w, root, cfg, []string{rel}, gen); err != nil {
			t.Fatalf("rescaffold %s: %v\n%s", rel, err, w.String())
		}
	})
	if got := readRescaffoldFile(t, root, rel); !bytes.Equal(got, born) {
		t.Errorf("rescaffolded %s differs from the scaffold's bytes", rel)
	}

	err := rescaffoldPaths(&w, root, cfg, []string{".github/workflows/e2e.yml"}, gen)
	if err == nil || !strings.Contains(err.Error(), "set ci.provider: github") {
		t.Errorf("rescaffold of a GitHub workflow under ci.provider reliant: err = %v, want a refusal naming ci.provider: github", err)
	}

	github := *cfg
	github.CI.Provider = ""
	removeRescaffoldFile(t, root, generator.ReliantReviewWorkflowPath)
	err = rescaffoldPaths(&w, root, &github, []string{generator.ReliantReviewWorkflowPath}, gen)
	if err == nil || !strings.Contains(err.Error(), "set ci.provider: reliant") {
		t.Errorf("rescaffold of a Reliant workflow under ci.provider github: err = %v, want a refusal naming ci.provider: reliant", err)
	}
}
