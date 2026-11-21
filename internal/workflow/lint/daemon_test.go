// Copyright (c) 2025 Reliant Labs
package lint

import (
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/validation"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pinSource is a workflow with sel as the workflow-level selector, or as the
// selector of node `gate` (and of a node in an inline loop body).
func pinSource(level, sel string) string {
	wfDaemon, nodeDaemon, bodyDaemon := "", "", ""
	switch level {
	case "workflow":
		wfDaemon = "daemon: " + sel + "\n"
	case "node":
		nodeDaemon = "  daemon: " + sel + "\n"
	case "body":
		bodyDaemon = "      daemon: " + sel + "\n"
	}
	return "name: fixture\n" + wfDaemon + `entry: [gate]
nodes:
- id: gate
  type: run
  timeout: 30m
` + nodeDaemon + `  args: {command: 'make test; echo {{ nodes.gate.exit_code }}'}
- id: fix
  type: loop
  while: iter.iteration < 3
  inline:
    entry: [retry]
    nodes:
    - id: retry
      type: run
      timeout: 5m
` + bodyDaemon + `      args: {command: 'make test; echo {{ nodes.retry.exit_code }}'}
edges:
- {from: gate, default: [fix]}
`
}

func TestDaemonMachinePin(t *testing.T) {
	tests := []struct {
		name string
		sel  string
		// pinned is what the message names; "" means no finding.
		pinned string
	}{
		{"name shorthand", "ci-box-1", "ci-box-1"},
		{"name mapping", "{name: ci-box-1}", "ci-box-1"},
		{"id", "{id: 6f1c0d2e}", "id 6f1c0d2e"},
		{"name with labels", "{name: ci-box-1, labels: {gpu: \"true\"}}", "ci-box-1"},
		{"name with type", "{type: cloud, name: ci-box-1}", "ci-box-1"},
		{"pool", "{pool: ci}", ""},
		{"labels only", "{labels: {role: gate}}", ""},
		{"type shorthand local", "local", ""},
		{"type shorthand cloud", "cloud", ""},
		{"type shorthand any", "any", ""},
		{"type mapping", "{type: cloud}", ""},
		{"type with labels", "{type: cloud, labels: {gpu: \"true\"}}", ""},
		{"CEL", `"{{ inputs.machine }}"`, ""},
	}
	wantNode := map[string]string{"workflow": "", "node": "gate", "body": "fix.retry"}
	for _, level := range []string{"workflow", "node", "body"} {
		for _, tc := range tests {
			t.Run(level+"/"+tc.name, func(t *testing.T) {
				src := []byte(pinSource(level, tc.sel))
				wf, err := wfyaml.ParseWorkflow(src)
				require.NoError(t, err)

				// A name pin is not a validation problem: only lint speaks.
				for _, e := range validation.StaticAnalysis(wf, nil).Errors() {
					assert.False(t, strings.HasPrefix(e.Field, "daemon"), "validation: %v", e)
				}

				got := findingsOf(Check(Input{Workflow: wf, Source: src}), RuleDaemonMachinePin)
				if tc.pinned == "" {
					assert.Empty(t, got)
					return
				}
				require.Len(t, got, 1, "%v", got)
				f := got[0]
				assert.Equal(t, SeverityWarning, f.Severity)
				assert.Equal(t, wantNode[level], f.Node)
				assert.Contains(t, f.Message, "one machine ("+tc.pinned+")")
				assert.Contains(t, f.Message, "stalls")
				assert.Contains(t, f.Fix, "daemon: {pool: <name>}")
				assert.Equal(t, DocsBase+"#"+RuleDaemonMachinePin, f.Docs)
			})
		}
	}
}

// A judged pin is silenced where it is written, with the reason.
func TestDaemonMachinePinAllowance(t *testing.T) {
	for name, src := range map[string]string{
		"workflow": "name: fixture\n# lint:allow daemon-machine-pin — the release key lives on this one box\ndaemon: release-box\nentry: [gate]\nnodes:\n- id: gate\n  type: run\n  timeout: 1m\n  args: {command: 'echo {{ nodes.gate.exit_code }}'}\n",
		"node":     "name: fixture\nentry: [gate]\nnodes:\n# lint:allow daemon-machine-pin — the release key lives on this one box\n- id: gate\n  type: run\n  timeout: 1m\n  daemon: release-box\n  args: {command: 'echo {{ nodes.gate.exit_code }}'}\n",
	} {
		t.Run(name, func(t *testing.T) {
			wf, err := wfyaml.ParseWorkflow([]byte(src))
			require.NoError(t, err)
			fs := Check(Input{Workflow: wf, Source: []byte(src)})
			assert.Empty(t, findingsOf(fs, RuleDaemonMachinePin))
			assert.Empty(t, findingsOf(fs, RuleAllowInvalid))
		})
	}
}
