// Copyright (c) 2025 Reliant Labs
package validation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

func workflowTimeoutFindings(t *testing.T, src string) (errs, warnings []string) {
	t.Helper()
	wf, err := wfyaml.ParseWorkflow([]byte(src))
	require.NoError(t, err)
	result := NewResult()
	validateWorkflowTimeout(wf, result)
	validateStructure(wf, nil, result)
	for _, e := range result.Errors() {
		if e.Field == "timeout" {
			errs = append(errs, e.Message)
		}
	}
	for _, w := range result.Warnings() {
		if w.Field == "timeout" {
			warnings = append(warnings, w.Message)
		}
	}
	return errs, warnings
}

func boundedWorkflow(timeout, nodeTimeout string) string {
	node := "\n  - id: gate\n    type: run\n    args:\n      command: ./scripts/gate.sh"
	if nodeTimeout != "" {
		node += "\n    timeout: " + nodeTimeout
	}
	return "name: bounded\ntimeout: " + timeout + "\nentry: [gate]\nnodes:" + node + "\n"
}

func TestWorkflowTimeout_ValidDurationsLoad(t *testing.T) {
	t.Parallel()
	for _, timeout := range []string{"30s", "3h", "4h", "12h", "168h"} {
		errs, warnings := workflowTimeoutFindings(t, boundedWorkflow(timeout, ""))
		assert.Empty(t, errs, "timeout %s", timeout)
		assert.Empty(t, warnings, "timeout %s", timeout)
	}
}

func TestWorkflowTimeout_InvalidValuesAreRejected(t *testing.T) {
	t.Parallel()
	for timeout, want := range map[string]string{
		"0s":                     "more than zero",
		"-1h":                    "more than zero",
		"169h":                   "at most 168h0m0s",
		"three-hours":            "invalid duration",
		"'{{ inputs.timeout }}'": "literal duration",
	} {
		errs, _ := workflowTimeoutFindings(t, boundedWorkflow(timeout, ""))
		require.Len(t, errs, 1, "timeout %s: %v", timeout, errs)
		assert.Contains(t, errs[0], want, "timeout %s", timeout)
	}
}

// A node whose own timeout is no shorter than the run's can never reach it.
func TestWorkflowTimeout_ANodeTimeoutPastTheRunsIsAWarning(t *testing.T) {
	t.Parallel()
	errs, warnings := workflowTimeoutFindings(t, boundedWorkflow("1h", "2h"))
	assert.Empty(t, errs)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], `node "gate"'s timeout (2h0m0s) is not shorter than the workflow's run timeout (1h0m0s)`)

	errs, warnings = workflowTimeoutFindings(t, boundedWorkflow("3h", "30m"))
	assert.Empty(t, errs)
	assert.Empty(t, warnings, "a node bounded inside the run is fine")
}

func TestWorkflowTimeout_AHandlerCannotDeclareOne(t *testing.T) {
	t.Parallel()
	errs, _ := workflowTimeoutFindings(t, `name: bounded
timeout: 1h
entry: [gate]
nodes:
  - id: gate
    type: run
    args:
      command: ./scripts/gate.sh
finally:
  timeout: 5m
  entry: [release]
  nodes:
    - id: release
      type: run
      args:
        command: ./release.sh
`)
	require.Len(t, errs, 1, "%v", errs)
	assert.Contains(t, errs[0], "a finally handler cannot declare a timeout")
}

// The rule is part of the validator every save and launch runs.
func TestWorkflowTimeout_StaticAnalysisChecksIt(t *testing.T) {
	t.Parallel()
	wf, err := wfyaml.ParseWorkflow([]byte(boundedWorkflow("0s", "")))
	require.NoError(t, err)
	result := StaticAnalysis(wf, nil)
	var found bool
	for _, e := range result.Errors() {
		found = found || e.Field == "timeout"
	}
	assert.True(t, found, "%v", result.Errors())
}
