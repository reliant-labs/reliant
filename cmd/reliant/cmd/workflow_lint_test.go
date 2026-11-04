// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/lint"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A workflow with max_running whose command has no timeout: validation
// alone passes it; lint, run inside `workflow validate`, fails it.
const slotHoldingWorkflow = `name: gate
concurrency: {max_running: 1}
entry: [gate]
nodes:
%s- id: gate
  type: run
  args: {command: 'task ci; echo {{ nodes.gate.exit_code }}'}
`

func writeLintWorkflow(t *testing.T, body string) (dir, path string) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), ".reliant", "workflows")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	path = filepath.Join(dir, "gate.yaml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	return dir, path
}

func TestValidateFailsOnALintError(t *testing.T) {
	dir, path := writeLintWorkflow(t, fmt.Sprintf(slotHoldingWorkflow, ""))
	res := validateWorkflowFile(path, dir)
	assert.Empty(t, res.Errors, "the definition itself validates")
	assert.False(t, res.Valid, "a lint error fails validate")
	var rules []string
	for _, f := range res.Lint {
		rules = append(rules, f.Rule)
	}
	assert.Contains(t, rules, lint.RuleSlotUnboundedWait)
}

func TestValidatePassesAnAllowedFinding(t *testing.T) {
	dir, path := writeLintWorkflow(t, fmt.Sprintf(slotHoldingWorkflow, "# lint:allow slot-unbounded-wait — fixture: the gate bounds itself with timeout(1)\n"))
	res := validateWorkflowFile(path, dir)
	assert.True(t, res.Valid, "%v %v", res.Errors, res.Lint)
	assert.False(t, lint.HasErrors(res.Lint))
}

func TestWorkflowLintExitsNonZeroOnAnError(t *testing.T) {
	dir, _ := writeLintWorkflow(t, fmt.Sprintf(slotHoldingWorkflow, ""))
	require.ErrorContains(t, runWorkflowLint(nil, dir, true), "lint failed")
	dir, _ = writeLintWorkflow(t, fmt.Sprintf(slotHoldingWorkflow, "# lint:allow slot-unbounded-wait — fixture\n"))
	require.NoError(t, runWorkflowLint(nil, dir, true))
}
