// Copyright (c) 2025 Reliant Labs
package workflow_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/validation"

	// Register activity schemas so StaticAnalysis runs at full strength.
	_ "github.com/reliant-labs/reliant/internal/workflow/runtime/activities"
)

// Structural guard over the workflows reliant SHIPS.
//
// These files are the product: `forge-one-shot`, `get-it-right`, the routers.
// They are hand-edited YAML with no compiler behind them, so a typo'd edge
// target or a renamed node is invisible until a run reaches that node — which,
// for a phase deep in a long workflow, can be many minutes of real work in.
//
// This replaces a suite that pointed at `.reliant/workflows/*.yaml` and named
// four sample workflows deleted from this repo in #84. It had been asserting on
// files that no longer existed, and only appeared to pass because its guard
// skipped when the DIRECTORY was absent — an empty directory (which is what a
// checkout ends up with) sailed past the skip and hard-failed. Project
// workflows remain a live product feature for USER projects; this repo simply
// has none, so the machinery is pointed at content that actually ships.
//
// Discovery is from the embedded FS rather than a hardcoded list, so a new
// builtin workflow is covered the moment it is added and none can be dropped
// from coverage by omission.

// builtinWorkflowFiles returns every embedded builtin workflow YAML.
// Fails when empty: every assertion below is a loop, and a loop over nothing
// passes — the exact way the suite this replaces went quietly dead.
func builtinWorkflowFiles(t *testing.T) []string {
	t.Helper()
	entries, err := builtin.BuiltinWorkflowsFS.ReadDir(".")
	require.NoError(t, err, "read embedded builtin workflows")

	var names []string
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
			continue
		}
		names = append(names, e.Name())
	}
	require.NotEmpty(t, names, "no builtin workflow YAML found in the embedded FS — "+
		"every check in this file is a loop, so an empty set would pass vacuously")
	return names
}

// parseBuiltinWorkflow reads and parses one embedded builtin workflow.
func parseBuiltinWorkflow(t *testing.T, name string) *reliantv1.Workflow {
	t.Helper()
	data, err := builtin.BuiltinWorkflowsFS.ReadFile(name)
	require.NoError(t, err, "read %s", name)

	wf, err := runtime.ParseWorkflowProtoBytes(data)
	require.NoError(t, err, "parse %s", name)
	require.NotNil(t, wf, "parse %s returned nil", name)
	return wf
}

// TestBuiltinWorkflows_Parse pins that every shipped workflow is readable and
// parses to proto, and that its declared name matches its filename — the name
// is how a workflow is addressed (`reliant workflow run <name>`), so a mismatch
// makes it unrunnable under the name users can see.
func TestBuiltinWorkflows_Parse(t *testing.T) {
	for _, file := range builtinWorkflowFiles(t) {
		t.Run(file, func(t *testing.T) {
			wf := parseBuiltinWorkflow(t, file)
			want := strings.TrimSuffix(file, filepath.Ext(file))
			assert.Equal(t, want, wf.GetName(),
				"workflow name must match its filename — the filename is how it is addressed")
		})
	}
}

// TestBuiltinWorkflows_StaticAnalysis runs the same validation pass the runtime
// applies, so a shipped workflow cannot fail the product's own checks.
func TestBuiltinWorkflows_StaticAnalysis(t *testing.T) {
	for _, file := range builtinWorkflowFiles(t) {
		t.Run(file, func(t *testing.T) {
			wf := parseBuiltinWorkflow(t, file)
			result := validation.StaticAnalysis(wf, nil)
			require.NoError(t, result.AsError(), "static analysis failed for %s", file)
		})
	}
}

// TestBuiltinWorkflows_EdgesReferenceValidNodes catches the highest-cost typo:
// an edge pointing at a node that does not exist. Nothing fails at load time —
// the run simply proceeds until it tries to traverse that edge, which for a
// late phase means the failure surfaces after the expensive work is done.
func TestBuiltinWorkflows_EdgesReferenceValidNodes(t *testing.T) {
	for _, file := range builtinWorkflowFiles(t) {
		t.Run(file, func(t *testing.T) {
			wf := parseBuiltinWorkflow(t, file)
			nodeIDs := builtinNodeIDs(wf.GetNodes())

			for _, edge := range wf.GetEdges() {
				for _, target := range edge.GetDefault() {
					assert.Contains(t, nodeIDs, target,
						"edge from %q has default target %q, which is not a node in this workflow",
						edge.GetFrom(), target)
				}
				for _, c := range edge.GetCases() {
					for _, target := range c.GetTo() {
						if target == "" {
							continue // an empty target terminates the branch
						}
						assert.Contains(t, nodeIDs, target,
							"edge from %q has case target %q, which is not a node in this workflow",
							edge.GetFrom(), target)
					}
				}
			}
		})
	}
}

// TestBuiltinWorkflows_EntryNodesExist requires a reachable starting point:
// at least one entry, each naming a real node. A workflow with a stale entry
// cannot start at all.
func TestBuiltinWorkflows_EntryNodesExist(t *testing.T) {
	for _, file := range builtinWorkflowFiles(t) {
		t.Run(file, func(t *testing.T) {
			wf := parseBuiltinWorkflow(t, file)
			require.NotEmpty(t, wf.GetEntry(), "%s declares no entry node, so it can never start", file)

			nodeIDs := builtinNodeIDs(wf.GetNodes())
			for _, entry := range wf.GetEntry() {
				assert.Contains(t, nodeIDs, entry,
					"entry node %q is not a node in this workflow", entry)
			}
		})
	}
}

// builtinNodeIDs indexes a workflow's node IDs for membership checks.
func builtinNodeIDs(nodes []*reliantv1.Node) map[string]bool {
	ids := make(map[string]bool, len(nodes))
	for _, node := range nodes {
		ids[node.GetId()] = true
	}
	return ids
}
