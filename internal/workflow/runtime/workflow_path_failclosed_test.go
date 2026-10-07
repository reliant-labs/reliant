// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// These pin the two runtime halves of the parallel-compete P0 (2026-10-06):
// builtin parallel-compete ran `rsync -av --delete <winner>/ "{{workflow.path}}/"`
// with workflow.path never populated, so the destination rendered as "/" and
// the step deleted files across the user's machine.
//
//   - workflow.path must be the evaluating scope's working directory.
//   - a run node must never execute a command in which a plain value
//     reference rendered empty — that is how "/" was produced.

// runNodeFromYAML parses a one-node workflow and returns that `run` node, so the
// node goes through the same YAML → proto path a shipped workflow does.
func runNodeFromYAML(t *testing.T, command string) *reliantv1.Node {
	t.Helper()
	wf, err := wfyaml.ParseWorkflow([]byte(`
name: run-under-test
entry: [step]
nodes:
  - id: step
    type: run
    command: ` + quoteYAML(command) + `
`))
	require.NoError(t, err)
	require.Len(t, wf.GetNodes(), 1)
	return wf.GetNodes()[0]
}

// quoteYAML renders s as a YAML double-quoted scalar.
func quoteYAML(s string) string {
	out := []byte{'"'}
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"', '\\':
			out = append(out, '\\', s[i])
		default:
			out = append(out, s[i])
		}
	}
	return string(append(out, '"'))
}

func TestNodeConfig_WorkflowPathIsTheScopeWorkingDirectory(t *testing.T) {
	t.Parallel()
	node := runNodeFromYAML(t, `echo {{workflow.path}}`)

	resolved, err := EvaluateNodeConfig(node, map[string]interface{}{}, "wf-1", "wf",
		map[string]interface{}{}, nil, nil, &ExecutionContext{ProjectPath: "/work/project"})
	require.NoError(t, err)
	assert.Equal(t, "echo /work/project", model.CelStringValue(resolved.GetRun().GetCommand()),
		"workflow.path must render the scope's project directory (ExecutionContext.ProjectPath)")
}

func TestRunCommand_FailsClosedWhenAValueRendersEmpty(t *testing.T) {
	t.Parallel()

	// The incident command, minus the excludes.
	const incident = `rsync -av --delete "{{nodes.implementations._results[string(nodes.review.response.winner)].worktree_path}}/" "{{workflow.path}}/"`
	winnerOutputs := func(worktreePath string) map[string]interface{} {
		return map[string]interface{}{
			"implementations": map[string]interface{}{"_results": map[string]interface{}{
				"2": map[string]interface{}{"worktree_path": worktreePath},
			}},
			"review": map[string]interface{}{"response": map[string]interface{}{"winner": float64(2)}},
		}
	}

	cases := []struct {
		name        string
		command     string
		nodes       map[string]interface{}
		inputs      map[string]interface{}
		projectPath string
		wantErr     string // "" = must resolve
	}{
		{
			name:        "the incident: workflow.path unset renders the destination as /",
			command:     incident,
			nodes:       winnerOutputs("/home/u/.reliant/worktrees/p/compete-impl-2-abc"),
			projectPath: "",
			wantErr:     "workflow.path",
		},
		{
			name:        "an LLM-chosen candidate whose worktree path is empty",
			command:     incident,
			nodes:       winnerOutputs(""),
			projectPath: "/home/u/project",
			wantErr:     "worktree_path",
		},
		{
			name:        "an empty input used as the whole command",
			command:     `{{inputs.test_command}}`,
			inputs:      map[string]interface{}{"test_command": "  "},
			projectPath: "/home/u/project",
			wantErr:     "inputs.test_command",
		},
		{
			name:        "every value present",
			command:     incident,
			nodes:       winnerOutputs("/home/u/.reliant/worktrees/p/compete-impl-2-abc"),
			projectPath: "/home/u/project",
		},
		{
			name:        "an expression that says it may be empty is the author's call",
			command:     `git commit -m 'x' {{has(inputs.extra) ? inputs.extra : ''}}`,
			inputs:      map[string]interface{}{},
			projectPath: "/home/u/project",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			nodes := tc.nodes
			if nodes == nil {
				nodes = map[string]interface{}{}
			}
			inputs := tc.inputs
			if inputs == nil {
				inputs = map[string]interface{}{}
			}
			resolved, err := EvaluateNodeConfig(runNodeFromYAML(t, tc.command), nodes, "wf-1", "wf",
				inputs, nil, nil, &ExecutionContext{ProjectPath: tc.projectPath})
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err, "a run command must fail closed, not run; it rendered as %q",
				func() string {
					if resolved == nil {
						return ""
					}
					return model.CelStringValue(resolved.GetRun().GetCommand())
				}())
			assert.Contains(t, err.Error(), tc.wantErr, "the error must name the empty value")
		})
	}
}
