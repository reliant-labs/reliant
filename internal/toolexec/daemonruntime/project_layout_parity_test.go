// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/workflow/workflowref"
	"github.com/reliant-labs/reliant/internal/workflow/workflowsource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Issue #623: the CLI and the app must see one project. The CLI reads
// .reliant/workflows from disk with workflowref.ReadLayout and resolves refs
// with workflowref.Resolve (pinned by workflowref.TestOneRule_*). The app sees
// the same directory only through this daemon: the snapshot it syncs, the JSON
// the server stores, and workflowsource reading that back. These tests drive
// both paths over one project and require the same answer from each — for
// every ref, every problem, and every scenario.

type storedRecord struct{ record *db.ProjectConfigRecord }

func (s storedRecord) GetProjectConfigRecord(context.Context, string) (*db.ProjectConfigRecord, error) {
	return s.record, nil
}

// The caller has no workflows of their own: only the project answers, on both
// surfaces.
func (storedRecord) GetUsableWorkflowBySlug(context.Context, string, string) (*db.WorkflowDraft, error) {
	return nil, nil
}

func (storedRecord) GetWorkflowDraftBySlug(context.Context, string, string) (*db.WorkflowDraft, error) {
	return nil, nil
}

func writeProject(t *testing.T, files map[string]string) string {
	t.Helper()
	project := t.TempDir()
	for rel, content := range files {
		path := filepath.Join(project, filepath.FromSlash(workflowref.Dir), filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
	return project
}

func parityWorkflow(name string) string {
	return "name: " + name + "\napiVersion: \"1.0\"\nentry: [x]\nnodes:\n  - id: x\n    type: save_message\n    args: {role: assistant, content: hi}\n"
}

func TestProjectLayout_AppResolvesEveryRefAsTheCLIDoes(t *testing.T) {
	project := writeProject(t, map[string]string{
		"blog.yaml":         parityWorkflow("blog-content-pipeline"),
		"content-next.yaml": parityWorkflow("content-next"),
		"a.yaml":            parityWorkflow("deploy"),
		"b.yaml":            parityWorkflow("Deploy"),
		"nameless.yaml":     "entry: [x]\nnodes: []\n",
		"x.yaml":            parityWorkflow("y"),
		"y.yaml":            parityWorkflow("z"),
	})

	// The CLI's path: the directory itself.
	cli, err := workflowref.ReadLayout(os.DirFS(filepath.Join(project, workflowref.Dir)))
	require.NoError(t, err)

	// The app's path: what the daemon syncs, as the server stores and reads it.
	snapshot, err := buildProjectSnapshot(project)
	require.NoError(t, err)
	store := storedRecord{record: &db.ProjectConfigRecord{ProjectWorkflowsJSON: flattenWorkflows(snapshot.GetWorkflows())}}
	appIndex, err := workflowsource.ProjectIndex(context.Background(), store, "project")
	require.NoError(t, err)

	for _, ref := range []string{
		"project://blog-content-pipeline", // by name: found
		"blog-content-pipeline",           // bare: the same
		"project://Content Next",          // any spelling of the name
		"project://blog",                  // a file name: a miss that names blog.yaml
		"project://deploy",                // two files, one name
		"project://nameless",              // a file with no name:
		"project://y",                     // a name that is another file's file name
		"project://z",                     // the crossed file, by its own name
		"project://missing",
		"builtin://agent",
	} {
		t.Run(ref, func(t *testing.T) {
			fromCLI, cliErr := workflowref.Resolve(ref, workflowref.Sources{Project: cli.Workflows})
			fromApp, appErr := workflowsource.Resolve(context.Background(), store,
				workflowsource.Options{UserID: "user", ProjectID: "project"}, ref)
			if cliErr != nil || appErr != nil {
				require.Error(t, cliErr, "the app failed (%v) where the CLI resolved", appErr)
				require.Error(t, appErr, "the CLI failed (%v) where the app resolved", cliErr)
				assert.Equal(t, cliErr.Error(), appErr.Error(), "the same miss, explained the same way")
				return
			}
			assert.Equal(t, fromCLI.Source, fromApp.Source)
			assert.Equal(t, fromCLI.Path, fromApp.Path, "the same file")
			assert.Equal(t, fromCLI.YAML, fromApp.YAML)
		})
	}

	t.Run("the same problems", func(t *testing.T) {
		problems := func(ix *workflowref.Index) map[string]string {
			out := map[string]string{}
			for _, e := range ix.Entries() {
				if e.Problem != nil {
					out[e.Path] = e.Problem.Error()
				}
			}
			return out
		}
		assert.Equal(t, problems(cli.Workflows), problems(appIndex))
		assert.Len(t, problems(appIndex), 4, "a.yaml, b.yaml (shared name), nameless.yaml, x.yaml (crosses y.yaml)")
	})
}

func TestProjectLayout_AppListsTheScenariosTheCLIRuns(t *testing.T) {
	project := writeProject(t, map[string]string{
		"blog.yaml":                                   parityWorkflow("blog-content-pipeline"),
		"content-next.yaml":                           parityWorkflow("content-next"),
		"content-next/scenarios/happy.yaml":           "name: happy\n",
		"content-next/scenarios/sad.yml":              "name: sad\n",
		"blog-content-pipeline/scenarios/drafts.yaml": "name: drafts\n",
		// Retired layouts: neither surface reads them.
		"scenarios/blog/old.yaml": "name: old\n",
		"blog_scenarios.yaml":     "name: older\n",
	})

	cli, err := workflowref.ReadLayout(os.DirFS(filepath.Join(project, workflowref.Dir)))
	require.NoError(t, err)
	var fromCLI []string
	for _, s := range cli.Scenarios {
		fromCLI = append(fromCLI, s.WorkflowSlug+"/"+s.Name)
	}

	snapshot, err := buildProjectSnapshot(project)
	require.NoError(t, err)
	stored, err := config.ParseStoredScenarios(flattenScenarios(snapshot.GetScenarios()))
	require.NoError(t, err)
	var fromApp []string
	for _, s := range stored {
		fromApp = append(fromApp, s.WorkflowSlug+"/"+s.Name)
	}
	sort.Strings(fromCLI)
	sort.Strings(fromApp)

	assert.Equal(t, []string{"blog-content-pipeline/drafts", "content-next/happy", "content-next/sad"}, fromApp)
	assert.Equal(t, fromCLI, fromApp, "the app lists exactly the scenarios the CLI runs")

	var syncedWorkflows []string
	for _, w := range snapshot.GetWorkflows() {
		syncedWorkflows = append(syncedWorkflows, w.GetRelativePath())
	}
	assert.NotContains(t, syncedWorkflows, ".reliant/workflows/blog_scenarios.yaml",
		"a retired scenario file is not synced as a workflow")
}
