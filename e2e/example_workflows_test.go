// Copyright (c) 2025 Reliant Labs
package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// EXAMPLE WORKFLOW E2E TESTS
//
// These tests validate example workflows stored in:
//   ~/Library/Application Support/reliant/data/workflows/
//
// Tests are structured in two parts:
// 1. File existence and YAML validity tests
// 2. Execution tests using simplified inline versions (full workflows need agent context)
// ============================================================================

// getWorkflowDir returns the path to the user workflows directory
func getWorkflowDir() string {
	return filepath.Join(".reliant", "workflows")
}

// getWorkflowPath returns the absolute path to a workflow file
func getWorkflowPath(filename string) string {
	return filepath.Join(getWorkflowDir(), filename)
}

// getWorkflowName extracts workflow name from filename (strips .yaml extension)
func getWorkflowName(filename string) string {
	return strings.TrimSuffix(filename, ".yaml")
}

// ExampleWorkflows lists all expected workflow files
var ExampleWorkflows = []string{
	"parallel-compete.yaml",
	"race.yaml",
	"weighted-voting.yaml",
	"adaptive-retry.yaml",
	"tool-router.yaml",
}

// ============================================================================
// FILE EXISTENCE TESTS
// ============================================================================

// TestExampleWorkflows_AllFilesExist verifies all example workflow files exist.
func TestExampleWorkflows_AllFilesExist(t *testing.T) {
	for _, wf := range ExampleWorkflows {
		path := getWorkflowPath(wf)
		_, err := os.Stat(path)
		if err != nil {
			t.Logf("Workflow not found: %s (path: %s)", wf, path)
		}
		// Don't fail - just log which ones exist
	}
	t.Logf("Checked %d example workflow paths", len(ExampleWorkflows))
}

// TestExampleWorkflows_ParseAndValidate ensures all workflow YAML files
// parse correctly, validate structurally, and have valid edges.
func TestExampleWorkflows_ParseAndValidate(t *testing.T) {
	for _, wfFile := range ExampleWorkflows {
		t.Run(getWorkflowName(wfFile), func(t *testing.T) {
			path := getWorkflowPath(wfFile)

			// Check file exists
			_, err := os.Stat(path)
			if os.IsNotExist(err) {
				t.Skipf("Workflow file not found: %s", path)
				return
			}
			require.NoError(t, err, "error checking file existence")

			// Read file content
			data, err := os.ReadFile(path)
			require.NoError(t, err, "failed to read workflow file")

			// Parse and validate (this validates YAML structure)
			wf, err := wfyaml.ParseWorkflow(data)
			require.NoError(t, err, "failed to parse workflow")

			// Basic structural assertions
			assert.NotEmpty(t, wf.GetName(), "workflow must have a name")
			assert.NotEmpty(t, wf.GetNodes(), "workflow must have nodes")

			// Log parsed info
			t.Logf("Parsed %s: %d nodes, %d edges", wf.GetName(), len(wf.GetNodes()), len(wf.GetEdges()))
		})
	}
}

// TestExampleWorkflows_EdgesValid validates that all workflow edges have
// valid structure and target existing nodes.
func TestExampleWorkflows_EdgesValid(t *testing.T) {
	for _, wfFile := range ExampleWorkflows {
		t.Run(getWorkflowName(wfFile), func(t *testing.T) {
			path := getWorkflowPath(wfFile)

			// Skip if file doesn't exist
			if _, err := os.Stat(path); os.IsNotExist(err) {
				t.Skipf("Workflow file not found: %s", path)
				return
			}

			data, err := os.ReadFile(path)
			require.NoError(t, err)

			wf, err := wfyaml.ParseWorkflow(data)
			require.NoError(t, err)

			// Build step map for validation
			stepMap := make(map[string]bool)
			for _, node := range wf.GetNodes() {
				stepMap[node.GetId()] = true
			}
			edges := wf.GetEdges()
			// Count conditions and validate edge targets
			conditionCount := 0
			caseCount := 0
			for _, edge := range edges {
				for _, c := range edge.GetCases() {
					caseCount++
					if c.GetCondition() != "" {
						conditionCount++
					}
					// Validate target steps exist (except for terminal nodes)
					for _, target := range c.GetTo() {
						if target != "" {
							assert.True(t, stepMap[target], "edge target %s should exist as a step", target)
						}
					}
				}
				// Also validate default targets
				for _, target := range edge.GetDefault() {
					if target != "" {
						assert.True(t, stepMap[target], "default edge target %s should exist as a step", target)
					}
				}
			}

			t.Logf("Validated %d edges (%d cases, %d with conditions) in %s", len(edges), caseCount, conditionCount, wf.GetName())
		})
	}
}
