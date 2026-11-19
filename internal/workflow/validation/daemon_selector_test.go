// Copyright (c) 2025 Reliant Labs
package validation

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// daemonFindings is every finding on a `daemon:` selector, as its one-line
// form (path.field: message).
func daemonFindings(t *testing.T, wf *reliantv1.Workflow) []string {
	t.Helper()
	var out []string
	for _, e := range StaticAnalysis(wf, nil).Errors() {
		if strings.HasPrefix(e.Field, "daemon") {
			out = append(out, e.Error())
		}
	}
	return out
}

// daemonAt builds a workflow with sel as the selector at one place: the
// workflow, a top-level node, a node inside an inline loop body, or a node of
// the on_failure handler.
func daemonAt(where, sel string) string {
	wfDaemon, nodeDaemon, loopDaemon, failDaemon := "", "", "", ""
	switch where {
	case "workflow":
		wfDaemon = "daemon: " + sel + "\n"
	case "node":
		nodeDaemon = "  daemon: " + sel + "\n"
	case "loop body":
		loopDaemon = "      daemon: " + sel + "\n"
	case "on_failure":
		failDaemon = "    daemon: " + sel + "\n"
	}
	return "name: pinned\n" + wfDaemon + `entry: [build]
nodes:
- id: build
  type: run
  timeout: 5m
` + nodeDaemon + `  args: {command: make build}
- id: fix
  type: loop
  while: iter.iteration < 3
  inline:
    entry: [retry]
    nodes:
    - id: retry
      type: run
      timeout: 5m
` + loopDaemon + `      args: {command: make test}
edges:
- {from: build, default: [fix]}
on_failure:
  entry: [notify]
  nodes:
  - id: notify
    type: run
    timeout: 1m
` + failDaemon + `    args: {command: echo failed}
`
}

func TestDaemonSelectorValidation(t *testing.T) {
	tests := []struct {
		name string
		sel  string
		// want is a substring of the one finding; "" means no finding.
		want string
	}{
		{"pool alone", "{pool: gate}", ""},
		{"labels alone", "{labels: {role: gate}}", ""},
		{"labels narrow a type", "{type: cloud, labels: {gpu: \"true\"}}", ""},
		{"name pin is unchanged", "box-1", ""},
		{"name mapping is unchanged", "{name: box-1}", ""},
		{"id pin is unchanged", "{id: 6f1c}", ""},
		{"type shorthand", "local", ""},
		{"CEL selector is not checked", `"{{ inputs.machine }}"`, ""},
		{"pool with name", "{pool: gate, name: box-1}", "daemon.pool: pool cannot be combined with name"},
		{"pool with id", "{pool: gate, id: 6f1c}", "daemon.pool: pool cannot be combined with id"},
		{"pool with type", "{pool: gate, type: cloud}", "daemon.pool: pool cannot be combined with type"},
		{"pool with labels", "{pool: gate, labels: {role: gate}}", "daemon.pool: pool cannot be combined with labels"},
		{"pool with several", "{pool: gate, name: box-1, labels: {a: b}}", "pool cannot be combined with name, labels"},
		// Labels keep their v1 meaning, so their keys are not newly checked.
		{"labels-only blank key is unchanged from v1", `{labels: {"": gate}}`, ""},
	}
	wherePath := map[string]string{
		"workflow":   "daemon.",
		"node":       "pinned.nodes.build.daemon.",
		"loop body":  "pinned.nodes.fix.inline.nodes.retry.daemon.",
		"on_failure": "pinned.on_failure.nodes.notify.daemon.",
	}
	for _, where := range []string{"workflow", "node", "loop body", "on_failure"} {
		for _, tc := range tests {
			t.Run(where+"/"+tc.name, func(t *testing.T) {
				wf, err := wfyaml.ParseWorkflow([]byte(daemonAt(where, tc.sel)))
				require.NoError(t, err)
				got := daemonFindings(t, wf)
				if tc.want == "" {
					assert.Empty(t, got)
					return
				}
				require.Len(t, got, 1, "%v", got)
				assert.Contains(t, got[0], tc.want)
				assert.True(t, strings.HasPrefix(got[0], wherePath[where]), "finding %q is not at %s", got[0], wherePath[where])
			})
		}
	}
}

// A whitespace-only pool cannot come from YAML (the parser refuses it) but
// can from any other producer of the proto.
func TestDaemonSelectorValidation_BlankPoolFromProto(t *testing.T) {
	wf, err := wfyaml.ParseWorkflow([]byte(daemonAt("", "")))
	require.NoError(t, err)
	wf.Daemon = &reliantv1.CelDaemonSelector{Value: &reliantv1.CelDaemonSelector_Literal{Literal: &reliantv1.DaemonSelectorProto{Pool: "  "}}}
	got := daemonFindings(t, wf)
	require.Len(t, got, 1, "%v", got)
	assert.Contains(t, got[0], "daemon.pool: pool is blank")
}

// The fix names the pool the author wrote.
func TestDaemonSelectorValidation_SuggestionNamesThePool(t *testing.T) {
	wf, err := wfyaml.ParseWorkflow([]byte(daemonAt("node", "{pool: gate, name: box-1}")))
	require.NoError(t, err)
	var suggestion string
	for _, e := range StaticAnalysis(wf, nil).Errors() {
		if e.Field == "daemon.pool" {
			suggestion = e.Suggestion
		}
	}
	assert.Contains(t, suggestion, "daemon: {pool: gate} alone")
}
