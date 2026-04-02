// Copyright (c) 2025 Reliant Labs
package workflow_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/simulator"
	"github.com/reliant-labs/reliant/internal/workflow/validation"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	// Register activity schemas for full validation
	_ "github.com/reliant-labs/reliant/internal/workflow/runtime/activities"
)

// ============================================================================
// PROJECT WORKFLOW TESTS
//
// Tests for custom project workflows in .reliant/workflows/.
// These workflows implement various AI coding methodologies:
//
//   - bmad-lite: Lightweight BMAD methodology workflow
//   - gsd: Get stuff done - fast execution workflow
//   - router: Dynamic workflow router
//   - simplify-first: Refactor before implementing
//   - spec-driven: Spec-first development (GitHub Spec Kit / Kiro style)
//   - superpowers: Enhanced multi-capability workflow
//
// Test strategy:
//   1. Parse: YAML → proto is valid
//   2. Validate: StaticAnalysis passes
//   3. Structure: nodes, edges, entry, inputs are correct
//   4. Semantics: workflow-specific invariants hold
// ============================================================================

// projectWorkflowsDir returns the path to the project workflows directory.
func projectWorkflowsDir() string {
	// Walk up from internal/workflow/ to project root
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		candidate := filepath.Join(dir, ".reliant", "workflows")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// loadProjectWorkflow reads and parses a project workflow by name.
func loadProjectWorkflow(t *testing.T, name string) (*reliantv1.Workflow, []byte) {
	t.Helper()
	dir := projectWorkflowsDir()
	require.NotEmpty(t, dir, "project workflows directory not found")

	path := filepath.Join(dir, name+".yaml")
	data, err := os.ReadFile(path)
	require.NoError(t, err, "failed to read %s.yaml", name)

	wf, err := runtime.ParseWorkflowProtoBytes(data)
	require.NoError(t, err, "failed to parse %s.yaml", name)
	require.NotNil(t, wf)

	return wf, data
}

// allProjectWorkflows lists all expected custom workflow names.
var allProjectWorkflows = []string{
	"router",
	"simplify-first",
	"spec-driven",
	"superpowers",
}

// =============================================================================
// PHASE 1: Parse + Validate all workflows
// =============================================================================

// TestProjectWorkflows_AllParseable ensures every project workflow YAML file
// can be read, parsed to proto, and passes basic structural validation.
func TestProjectWorkflows_AllParseable(t *testing.T) {
	dir := projectWorkflowsDir()
	if dir == "" {
		t.Skip("project workflows directory not found")
	}

	for _, name := range allProjectWorkflows {
		t.Run(name, func(t *testing.T) {
			wf, _ := loadProjectWorkflow(t, name)

			assert.Equal(t, name, wf.GetName(), "workflow name should match filename")
			assert.NotEmpty(t, wf.GetNodes(), "workflow should have nodes")
			assert.True(t,
				len(wf.GetEdges()) > 0 || len(wf.GetEntry()) > 0,
				"workflow should have edges or entry field")
		})
	}
}

// TestProjectWorkflows_StaticAnalysis runs the same validation the runtime uses.
// This catches unknown arg fields, invalid CEL expressions, missing required fields.
func TestProjectWorkflows_StaticAnalysis(t *testing.T) {
	dir := projectWorkflowsDir()
	if dir == "" {
		t.Skip("project workflows directory not found")
	}

	for _, name := range allProjectWorkflows {
		t.Run(name, func(t *testing.T) {
			wf, _ := loadProjectWorkflow(t, name)

			result := validation.StaticAnalysis(wf, nil)
			require.NoError(t, result.AsError(),
				"static analysis failed for %s: %v", name, result.AsError())
		})
	}
}

// TestProjectWorkflows_EdgesReferenceValidNodes checks that every edge target
// points to a node that actually exists in the workflow.
func TestProjectWorkflows_EdgesReferenceValidNodes(t *testing.T) {
	dir := projectWorkflowsDir()
	if dir == "" {
		t.Skip("project workflows directory not found")
	}

	for _, name := range allProjectWorkflows {
		t.Run(name, func(t *testing.T) {
			wf, _ := loadProjectWorkflow(t, name)

			nodeIDs := collectNodeIDs(wf.GetNodes())

			for _, edge := range wf.GetEdges() {
				// Check default targets
				for _, target := range edge.GetDefault() {
					assert.Contains(t, nodeIDs, target,
						"edge from %s has default target %q which is not a valid node",
						edge.GetFrom(), target)
				}
				// Check case targets
				for _, c := range edge.GetCases() {
					for _, target := range c.GetTo() {
						if target != "" {
							assert.Contains(t, nodeIDs, target,
								"edge from %s has case target %q which is not a valid node",
								edge.GetFrom(), target)
						}
					}
				}
			}

			// Check entry nodes exist
			for _, entryID := range wf.GetEntry() {
				assert.Contains(t, nodeIDs, entryID,
					"entry node %q does not exist", entryID)
			}
		})
	}
}

// TestProjectWorkflows_EntryNodesExist verifies each workflow has at least one
// entry point and all entry nodes reference real nodes.
func TestProjectWorkflows_EntryNodesExist(t *testing.T) {
	dir := projectWorkflowsDir()
	if dir == "" {
		t.Skip("project workflows directory not found")
	}

	for _, name := range allProjectWorkflows {
		t.Run(name, func(t *testing.T) {
			wf, _ := loadProjectWorkflow(t, name)
			require.NotEmpty(t, wf.GetEntry(), "workflow should have entry nodes")

			nodeIDs := collectNodeIDs(wf.GetNodes())
			for _, entry := range wf.GetEntry() {
				assert.Contains(t, nodeIDs, entry,
					"entry node %q not found in workflow nodes", entry)
			}
		})
	}
}

// TestProjectWorkflows_ResolveAndParse tests the full resolution path used at runtime.
func TestProjectWorkflows_ResolveAndParse(t *testing.T) {
	dir := projectWorkflowsDir()
	if dir == "" {
		t.Skip("project workflows directory not found")
	}

	for _, name := range allProjectWorkflows {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, name+".yaml")
			data, err := os.ReadFile(path)
			require.NoError(t, err)

			wf, err := runtime.ResolveAndParseWorkflow(data, map[string]interface{}{})
			require.NoError(t, err,
				"ResolveAndParseWorkflow failed for %s", name)
			require.NotNil(t, wf)
			assert.Equal(t, name, wf.GetName())
		})
	}
}

// =============================================================================
// PHASE 2: Workflow-specific structural tests
// =============================================================================

// TestSimplifyFirst_Structure validates the simplify-first workflow has
// the required phases: research → refactor → implement, with optional verification.
func TestSimplifyFirst_Structure(t *testing.T) {
	wf, _ := loadProjectWorkflow(t, "simplify-first")

	// Must have core inputs
	inputs := wf.GetInputs()
	requireInput(t, inputs, "model", "model")
	requireInput(t, inputs, "mode", "enum")

	// Must have these phases represented in nodes
	nodeIDs := collectNodeIDs(wf.GetNodes())
	require.Contains(t, nodeIDs, "refactor",
		"simplify-first must have a 'refactor' node")
	require.Contains(t, nodeIDs, "implement",
		"simplify-first must have an 'implement' node")

	// Refactor must come before implement in the edge graph
	assertEdgeExists(t, wf, "refactor", "implement",
		"refactor must flow to implement")
}

// TestSpecDriven_Structure validates the spec-driven workflow follows
// the specify → plan → tasks → implement pattern.
func TestSpecDriven_Structure(t *testing.T) {
	wf, _ := loadProjectWorkflow(t, "spec-driven")

	inputs := wf.GetInputs()
	requireInput(t, inputs, "model", "model")

	nodeIDs := collectNodeIDs(wf.GetNodes())

	// Must have the core spec-driven phases
	require.Contains(t, nodeIDs, "specify",
		"spec-driven must have a 'specify' node")
	require.Contains(t, nodeIDs, "plan",
		"spec-driven must have a 'plan' node")
	require.Contains(t, nodeIDs, "implement",
		"spec-driven must have an 'implement' node")

	// Specify must come before plan, plan before implement
	assertEdgeExists(t, wf, "specify", "plan",
		"specify must flow to plan")
	assertEdgeExists(t, wf, "plan", "implement",
		"plan must flow to implement")
}

// =============================================================================
// PHASE 3: Semantic invariants
// =============================================================================

// TestSimplifyFirst_RefactorBeforeImplement verifies that the refactoring
// phase uses a fork so it doesn't pollute the implementation context.
func TestSimplifyFirst_RefactorBeforeImplement(t *testing.T) {
	wf, _ := loadProjectWorkflow(t, "simplify-first")

	for _, node := range wf.GetNodes() {
		if node.GetId() == "refactor" {
			// Refactor should use a fork so its conversation stays isolated
			subArgs := model.GetSubWorkflowArgs(node)
			if subArgs != nil {
				thread := subArgs.GetThread()
				assert.Equal(t, "fork", thread.GetMode(),
					"refactor node should fork to isolate refactoring conversation")
			}
		}
	}
}

// =============================================================================
// PHASE 4: All workflows reference only valid builtin refs
// =============================================================================

// TestProjectWorkflows_BuiltinRefsAreValid checks that all workflow/loop nodes
// that reference builtin:// workflows point to workflows that actually exist.
func TestProjectWorkflows_BuiltinRefsAreValid(t *testing.T) {
	dir := projectWorkflowsDir()
	if dir == "" {
		t.Skip("project workflows directory not found")
	}

	validBuiltins := map[string]bool{
		"builtin://agent":            true,
		"builtin://structured-agent": true,
		"builtin://auditing-agent":   true,
		"builtin://discovery-relay":  true,
		"builtin://get-it-right":     true,
		"builtin://one-ring":         true,
		"builtin://parallel-compete": true,
		"builtin://ralph-wiggum":     true,
	}

	for _, name := range allProjectWorkflows {
		t.Run(name, func(t *testing.T) {
			wf, _ := loadProjectWorkflow(t, name)
			validateBuiltinRefs(t, wf.GetNodes(), validBuiltins, name)
		})
	}
}

// =============================================================================
// Helpers
// =============================================================================

// collectNodeIDs returns a map of all node IDs in a workflow, including inline sub-workflows.
func collectNodeIDs(nodes []*reliantv1.Node) map[string]bool {
	ids := make(map[string]bool)
	for _, node := range nodes {
		ids[node.GetId()] = true
	}
	return ids
}

// requireInput asserts that the workflow has an input with the given name and type.
func requireInput(t *testing.T, inputs map[string]*reliantv1.Input, name, expectedType string) {
	t.Helper()
	input, ok := inputs[name]
	require.True(t, ok, "workflow must have input %q", name)
	assert.Equal(t, expectedType, input.GetType(),
		"input %q should be type %q", name, expectedType)
}

// assertEdgeExists checks that there's a path from sourceID to targetID in the edge graph.
// It checks both direct default edges and case edges.
func assertEdgeExists(t *testing.T, wf *reliantv1.Workflow, sourceID, targetID, msg string) {
	t.Helper()

	// Build adjacency: source → set of reachable targets (one hop)
	adj := make(map[string]map[string]bool)
	for _, edge := range wf.GetEdges() {
		from := edge.GetFrom()
		if adj[from] == nil {
			adj[from] = make(map[string]bool)
		}
		for _, target := range edge.GetDefault() {
			adj[from][target] = true
		}
		for _, c := range edge.GetCases() {
			for _, target := range c.GetTo() {
				adj[from][target] = true
			}
		}
	}

	// BFS to find path
	visited := make(map[string]bool)
	queue := []string{sourceID}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if current == targetID {
			return // Found path
		}
		if visited[current] {
			continue
		}
		visited[current] = true
		for next := range adj[current] {
			queue = append(queue, next)
		}
	}

	t.Errorf("%s: no path from %q to %q in edge graph", msg, sourceID, targetID)
}

// =============================================================================
// PHASE 4: Scenario simulation tests
// =============================================================================

// TestProjectWorkflows_Scenarios runs scenario files through the simulator engine
// for each project workflow that has scenarios in .reliant/workflows/scenarios/<name>/.
func TestProjectWorkflows_Scenarios(t *testing.T) {
	dir := projectWorkflowsDir()
	if dir == "" {
		t.Skip("project workflows directory not found")
	}

	scenariosDir := filepath.Join(dir, "scenarios")
	if _, err := os.Stat(scenariosDir); os.IsNotExist(err) {
		t.Skip("no scenarios directory found")
	}

	// Find all workflow directories under scenarios/
	wfDirs, err := os.ReadDir(scenariosDir)
	require.NoError(t, err)

	for _, wfDir := range wfDirs {
		if !wfDir.IsDir() {
			continue
		}

		workflowName := wfDir.Name()

		// Load the workflow
		workflowPath := filepath.Join(dir, workflowName+".yaml")
		workflowData, err := os.ReadFile(workflowPath)
		if err != nil {
			t.Logf("Skipping %s: workflow file not found", workflowName)
			continue
		}

		wf, err := runtime.ParseWorkflowProtoBytes(workflowData)
		if err != nil {
			t.Errorf("Failed to parse workflow %s: %v", workflowName, err)
			continue
		}

		// Load all scenario files for this workflow
		scenarioFiles, err := os.ReadDir(filepath.Join(scenariosDir, workflowName))
		if err != nil {
			continue
		}

		t.Run(workflowName, func(t *testing.T) {
			engine := simulator.NewEngine(wf)

			for _, sf := range scenarioFiles {
				if sf.IsDir() || !strings.HasSuffix(sf.Name(), ".yaml") {
					continue
				}

				sfPath := filepath.Join(scenariosDir, workflowName, sf.Name())
				data, err := os.ReadFile(sfPath)
				if err != nil {
					t.Errorf("Failed to read scenario file %s: %v", sfPath, err)
					continue
				}

				var scenario simulator.Scenario
				if err := yaml.Unmarshal(data, &scenario); err != nil {
					t.Errorf("Failed to parse scenario %s: %v", sf.Name(), err)
					continue
				}

				if scenario.Name == "" {
					continue
				}

				t.Run(scenario.Name, func(t *testing.T) {
					result := engine.RunScenario(&scenario)

					if result.Status != simulator.StatusPassed {
						t.Logf("Scenario: %s", scenario.Name)
						t.Logf("Description: %s", scenario.Description)
						t.Logf("Outcome: %s", result.Execution.Outcome)
						t.Logf("Nodes reached: %v", result.Execution.NodesReached)
						if result.Execution.Error != nil {
							t.Logf("Error: %s (node: %s)", result.Execution.Error.Message, result.Execution.Error.Node)
						}
						for _, mismatch := range result.Mismatches {
							t.Errorf("Mismatch: %s", mismatch)
						}
					}

					assert.Equal(t, simulator.StatusPassed, result.Status,
						"Scenario %q failed with %d mismatches", scenario.Name, len(result.Mismatches))
				})
			}
		})
	}
}

// validateBuiltinRefs recursively checks nodes for builtin:// references.
func validateBuiltinRefs(t *testing.T, nodes []*reliantv1.Node, validBuiltins map[string]bool, workflowName string) {
	t.Helper()
	for _, node := range nodes {
		ref := model.NodeRef(node)
		if ref != "" && strings.HasPrefix(ref, "builtin://") {
			// Skip CEL template expressions
			if strings.Contains(ref, "{{") {
				continue
			}
			assert.True(t, validBuiltins[ref],
				"workflow %s node %s references %q which is not a valid builtin workflow",
				workflowName, node.GetId(), ref)
		}

		// Check inline sub-workflows recursively
		if subArgs := model.GetSubWorkflowArgs(node); subArgs != nil && subArgs.GetInline() != nil {
			validateBuiltinRefs(t, subArgs.GetInline().GetNodes(), validBuiltins, workflowName)
		}
		if loopArgs := model.GetLoopArgs(node); loopArgs != nil && loopArgs.GetInline() != nil {
			validateBuiltinRefs(t, loopArgs.GetInline().GetNodes(), validBuiltins, workflowName)
		}
	}
}
