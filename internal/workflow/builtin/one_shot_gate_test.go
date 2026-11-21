// Copyright (c) 2025 Reliant Labs
package builtin_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOneShotGateHoldsAnUnattendedRunToItsDecisionsSection: the unattended
// charter tells the agent to record every decision it made without the user
// under "## Decisions made without the user" in FORGE_PLAN.md, and the gate now
// checks it (north-star AW-36): a run that skipped the section used to pass.
// The gate's own command is rendered as the run renders it, with forge's
// generate and lint replaced by `true`, and run against a plan with and without
// the section.
func TestOneShotGateHoldsAnUnattendedRunToItsDecisionsSection(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not on PATH")
	}
	args := mapAt(t, nodeByID(t, loadWorkflowYAML(t, "forge-one-shot.yaml"), "build_app"), "args")
	template, ok := args["lint_command"].(string)
	require.True(t, ok)
	require.Contains(t, template, "&& reliant forge generate && reliant forge lint")
	template = strings.Replace(template, "&& reliant forge generate && reliant forge lint", "&& true", 1)

	run := func(unattended bool, plan string) (string, error) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "FORGE_PLAN.md"), []byte(plan), 0o644))
		inputs := scaffoldInputs(unattended)
		inputs["working_directory"] = dir
		cmd := renderPrompt(t, template, inputs, nil, nil, 0)
		out, err := exec.Command("bash", "-c", cmd).CombinedOutput()
		return string(out), err
	}
	const withSection = "# Plan\n\n## Decisions made without the user\n- Chose Postgres: the default.\n"
	const without = "# Plan\n\nEntities: Task.\n"

	_, err := run(true, withSection)
	assert.NoError(t, err, "an unattended plan with the section passes")
	out, err := run(true, without)
	require.Error(t, err, "an unattended plan without the section fails the gate")
	assert.Contains(t, out, "Decisions made without the user")
	_, err = run(false, without)
	assert.NoError(t, err, "a run with a person needs no such section")
}
