package validation

import (
	"fmt"
	"strings"
	"testing"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/require"
)

func TestCrossWorkflowAnonymousInvocationsValidateEveryPath(t *testing.T) {
	for _, nesting := range []string{"siblings", "nested"} {
		for _, kind := range []string{"workflow", "loop"} {
			for _, argument := range []struct{ name, value, problem string }{
				{"template unknown", `yield: "{{inputs.flag}}"`, "unknown input(s): yield"},
				{"literal tool tag", `tools: [tag:default]`, `tool tag "default"`},
			} {
				t.Run(nesting+"/"+kind+"/"+argument.name, func(t *testing.T) {
					first := `  - id: first
    type: workflow
    inline:
      entry: call
      nodes:
        - id: call
          type: workflow
          ref: builtin://child
          args:
            tools: [tag:coding:default]
`
					second := fmt.Sprintf(`  - id: second
    type: %s
%s    inline:
      entry: bad
      nodes:
        - id: bad
          type: workflow
          ref: builtin://child
          args:
            %s
`, kind, map[string]string{"workflow": "", "loop": "    while: false\n"}[kind], argument.value)
					nodes := first + second
					wantPath := "parent.nodes.second.inline.nodes.bad"
					if nesting == "nested" {
						// Outer is the first anonymous body; its nested second body must not
						// be suppressed just because both parsed names are empty.
						nodes = "  - id: outer\n    type: workflow\n    inline:\n      entry: first\n      nodes:\n" + indentAnonymousNodes(nodes, 6)
						wantPath = "parent.nodes.outer.inline.nodes.second.inline.nodes.bad"
					}
					wf, err := wfyaml.ParseWorkflow([]byte("name: parent\ninputs:\n  flag:\n    type: boolean\n    default: false\nentry: " + map[string]string{"siblings": "first", "nested": "outer"}[nesting] + "\nnodes:\n" + nodes))
					require.NoError(t, err)
					child := &pb.Workflow{Name: "shared display name", Inputs: map[string]*pb.Input{"tools": {Type: "tools"}}, Entry: []string{"done"}, Nodes: []*pb.Node{{Id: "done", Type: "call_llm", Args: &pb.Node_CallLlm{CallLlm: &pb.CallLLMArgs{}}}}}
					result := &Result{}
					validateCrossWorkflow(wf, &ValidationOptions{WorkflowLoader: func(ref string) (*pb.Workflow, error) {
						if ref != "builtin://child" {
							return nil, fmt.Errorf("unexpected ref %s", ref)
						}
						return child, nil
					}}, result)
					if argument.name == "literal tool tag" {
						wantPath += ".args.tools"
					}
					var found bool
					for _, problem := range result.Errors() {
						if strings.Contains(problem.Message, argument.problem) {
							require.Equal(t, wantPath, strings.Join(problem.Path, "."))
							found = true
						}
					}
					require.True(t, found, "anonymous invocation escaped contract validation: %s", result.Error())
				})
			}
		}
	}
}

func TestCrossWorkflowSiblingDiagnosticPathsDoNotAlias(t *testing.T) {
	wf, err := wfyaml.ParseWorkflow([]byte(`name: parent
inputs:
  flag:
    type: boolean
    default: false
entry: outer
nodes:
  - id: outer
    type: workflow
    inline:
      entry: first
      nodes:
        - id: first
          type: workflow
          ref: builtin://child
          args:
            yield: "{{inputs.flag}}"
        - id: second
          type: workflow
          ref: builtin://child
          args:
            yield: "{{inputs.flag}}"
`))
	require.NoError(t, err)
	result := &Result{}
	validateCrossWorkflow(wf, &ValidationOptions{WorkflowLoader: func(string) (*pb.Workflow, error) {
		return &pb.Workflow{Name: "child", Inputs: map[string]*pb.Input{"tools": {Type: "tools"}}}, nil
	}}, result)
	var paths []string
	for _, problem := range result.Errors() {
		if strings.Contains(problem.Message, "unknown input(s): yield") {
			paths = append(paths, strings.Join(problem.Path, "."))
		}
	}
	require.Equal(t, []string{"parent.nodes.outer.inline.nodes.first", "parent.nodes.outer.inline.nodes.second"}, paths)
}

func TestCrossWorkflowInvocationCycleProtection(t *testing.T) {
	root, err := wfyaml.ParseWorkflow([]byte(`name: root
entry: call
nodes:
  - id: call
    type: workflow
    ref: builtin://cycle
`))
	require.NoError(t, err)
	child, err := wfyaml.ParseWorkflow([]byte(`name: same-display-name
entry: again
nodes:
  - id: again
    type: workflow
    ref: builtin://cycle
`))
	require.NoError(t, err)
	result := &Result{}
	validateCrossWorkflow(root, &ValidationOptions{WorkflowLoader: func(string) (*pb.Workflow, error) { return child, nil }}, result)
	require.True(t, result.HasErrors())
	require.Contains(t, result.Error(), "reference cycle")
}

func indentAnonymousNodes(text string, spaces int) string {
	return strings.Repeat(" ", spaces) + strings.ReplaceAll(strings.TrimSuffix(text, "\n"), "\n", "\n"+strings.Repeat(" ", spaces)) + "\n"
}
