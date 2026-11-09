package generator

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// pre-commit.yml runs on pushes to main as well as on pull requests, so it
// follows the rule every forge workflow does (internal/templates'
// TestCIWorkflows_CancelInProgressOnlyOnPullRequests says why): a newer run
// cancels an older one only on a pull request. Its old `cancel-in-progress:
// true` let each merge cancel the main run before it.
func TestPreCommitWorkflow_CancelsOnlyPullRequestRuns(t *testing.T) {
	g := &ProjectGenerator{Name: "demo", Path: t.TempDir()}
	if err := g.generatePreCommitWorkflow(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(g.Path, ".github", "workflows", "pre-commit.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Concurrency map[string]any `yaml:"concurrency"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("pre-commit.yml is not valid YAML: %v", err)
	}
	const want = "${{ github.event_name == 'pull_request' }}"
	if got := doc.Concurrency["cancel-in-progress"]; got != want {
		t.Errorf("pre-commit.yml concurrency = %v, want cancel-in-progress: %s", doc.Concurrency, want)
	}
}
