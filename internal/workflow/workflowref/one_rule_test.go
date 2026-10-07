// Copyright (c) 2025 Reliant Labs
package workflowref

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Issue #623 happened because the rule "which workflow does this ref name, and
// where are its scenarios" was written out eight times, and the copies
// drifted: the CLI resolved project://X by file name, the app by name:, the
// runtime not at all. These tests keep it written ONCE.
//
// They read the repository's Go source (opened by this process, so Go's test
// cache sees every file they depend on).

const repoRoot = "../../.."

// entryPoints are where each surface turns a ref into a workflow, or finds a
// project's scenarios, and the shared function each must go through. A
// surface that grows its own loader instead fails here with this table's
// reason, rather than drifting silently.
var entryPoints = []struct {
	file, mustCall, surface string
}{
	{"cmd/reliant/commands/workflow.go", "workflowref.Resolve(", "CLI: validate, validate-tree and the scenario runner resolve refs"},
	{"cmd/reliant/commands/workflow.go", "workflowref.ReadLayout(", "CLI: workflow and scenario discovery"},
	{"internal/toolexec/daemonruntime/runtime.go", "workflowref.ReadLayout(", "app: the daemon indexes .reliant/workflows for the server"},
	{"internal/workflow/workflowsource/workflowsource.go", "workflowref.Resolve(", "app: every server-side resolution"},
	{"internal/workflow/runtime/activities/handlers/load_workflow.go", "workflowsource.Resolve(", "app: the runtime's LoadWorkflow activity"},
	{"internal/launch/workflows.go", "workflowsource.Resolve(", "app: run start"},
	{"internal/grpc/services/scenario_workflow_loader.go", "workflowsource.Loader(", "app: the scenario RPCs"},
	{"internal/llm/tools/scenario_tools.go", "workflowsource.Loader(", "app: the scenario agent tools"},
	{"internal/grpc/services/scenario.go", "workflowref.ScenarioPath(", "app: UploadScenario writes where the locator reads"},
	{"internal/workflow/scenario/runner/backend.go", "workflowref.Resolve(", "scenario runner: builtin refs"},
}

func TestOneRule_EverySurfaceCallsTheSharedResolver(t *testing.T) {
	for _, ep := range entryPoints {
		src, err := os.ReadFile(filepath.Join(repoRoot, ep.file))
		require.NoError(t, err, ep.file)
		assert.Contains(t, string(src), ep.mustCall,
			"%s: %s must go through %s — a private copy of the rule is how #623 happened", ep.file, ep.surface, strings.TrimSuffix(ep.mustCall, "("))
	}
}

// secondCopies are the fingerprints of the rule written out again.
var secondCopies = []struct {
	pattern *regexp.Regexp
	what    string
}{
	{regexp.MustCompile(`"project://"`), "handles the project:// scheme (use workflowref.Parse / Resolve)"},
	{regexp.MustCompile(`[(,]\s*"scenarios"\s*[,)]`), `builds a scenarios path (use workflowref.ScenarioDir / ScenarioPath / ReadLayout)`},
	{regexp.MustCompile("`\\[\\^a-z0-9-\\]`"), "re-implements the workflow slug (use workflowref.Slug)"},
}

func TestOneRule_NoSecondCopy(t *testing.T) {
	var offenders []string
	for _, dir := range []string{"cmd", "internal", "tools"} {
		err := filepath.WalkDir(filepath.Join(repoRoot, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "node_modules" || d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			rel, _ := filepath.Rel(repoRoot, path)
			rel = filepath.ToSlash(rel)
			if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") ||
				strings.HasPrefix(rel, "internal/workflow/workflowref/") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, c := range secondCopies {
				if c.pattern.Match(src) {
					offenders = append(offenders, rel+": "+c.what)
				}
			}
			return nil
		})
		require.NoError(t, err)
	}
	assert.Empty(t, offenders, "the ref / scenario-layout rule lives in internal/workflow/workflowref only")
}
