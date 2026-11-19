package wfyaml

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/testing/protocmp"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// An alias or a merge key parses exactly as the text it points to, written in
// place: a {{ }} template behind one is an expression, not literal text. The
// first case is control-plane pr-review's disarm step, whose `command: *disarm`
// ran `n='{{ inputs.pr_number }}'` verbatim when pr-review ran as a child.
func TestAliasesParseAsTheValueTheyPointTo(t *testing.T) {
	cases := []struct {
		name    string
		aliased string
		literal string
	}{
		{
			name: "alias to a templated scalar",
			aliased: `
name: t
nodes:
  - id: arm
    type: run
    args:
      command: &disarm "n='{{ inputs.pr_number }}'; gh pr edit $n"
  - id: disarm
    type: run
    args:
      command: *disarm
`,
			literal: `
name: t
nodes:
  - id: arm
    type: run
    args:
      command: "n='{{ inputs.pr_number }}'; gh pr edit $n"
  - id: disarm
    type: run
    args:
      command: "n='{{ inputs.pr_number }}'; gh pr edit $n"
`,
		},
		{
			name: "alias to a mapping",
			aliased: `
name: t
nodes:
  - id: a
    type: save_message
    args: &msg
      role: assistant
      content: "pr {{ inputs.pr_number }}"
  - id: b
    type: save_message
    args: *msg
`,
			literal: `
name: t
nodes:
  - id: a
    type: save_message
    args:
      role: assistant
      content: "pr {{ inputs.pr_number }}"
  - id: b
    type: save_message
    args:
      role: assistant
      content: "pr {{ inputs.pr_number }}"
`,
		},
		{
			name: "alias inside a mapping",
			aliased: `
name: t
inputs:
  pr_number: {type: string, default: &pr "{{ 7 }}"}
nodes:
  - id: a
    type: save_message
    args: {role: assistant, content: *pr}
`,
			literal: `
name: t
inputs:
  pr_number: {type: string, default: "{{ 7 }}"}
nodes:
  - id: a
    type: save_message
    args: {role: assistant, content: "{{ 7 }}"}
`,
		},
		{
			name: "merge key; an explicit key wins wherever it is written",
			aliased: `
name: t
nodes:
  - id: a
    type: save_message
    args: &base
      role: assistant
      content: "base {{ inputs.x }}"
  - id: b
    type: save_message
    args:
      content: "mine {{ inputs.y }}"
      <<: *base
`,
			literal: `
name: t
nodes:
  - id: a
    type: save_message
    args:
      role: assistant
      content: "base {{ inputs.x }}"
  - id: b
    type: save_message
    args:
      content: "mine {{ inputs.y }}"
      role: assistant
`,
		},
		{
			name: "merge list; the earlier mapping wins",
			aliased: `
name: t
nodes:
  - id: a
    type: save_message
    args: &first {content: "first {{ inputs.x }}"}
  - id: b
    type: save_message
    args: &second {role: assistant, content: second}
  - id: c
    type: save_message
    args:
      <<: [*first, *second]
`,
			literal: `
name: t
nodes:
  - id: a
    type: save_message
    args: {content: "first {{ inputs.x }}"}
  - id: b
    type: save_message
    args: {role: assistant, content: second}
  - id: c
    type: save_message
    args:
      content: "first {{ inputs.x }}"
      role: assistant
`,
		},
		{
			name: "merge key whose base merges in turn",
			aliased: `
name: t
nodes:
  - id: a
    type: save_message
    args: &role {role: "{{ inputs.role }}"}
  - id: b
    type: save_message
    args: &msg {<<: *role, content: "{{ inputs.c }}"}
  - id: c
    type: save_message
    args: {<<: *msg}
`,
			literal: `
name: t
nodes:
  - id: a
    type: save_message
    args: {role: "{{ inputs.role }}"}
  - id: b
    type: save_message
    args: {role: "{{ inputs.role }}", content: "{{ inputs.c }}"}
  - id: c
    type: save_message
    args: {role: "{{ inputs.role }}", content: "{{ inputs.c }}"}
`,
		},
		{
			name: "alias to a whole node, in an inline child workflow",
			aliased: `
name: t
nodes:
  - &step
    id: a
    type: run
    args: {command: "echo {{ inputs.v }}"}
  - id: child
    type: workflow
    inline:
      entry: [a]
      nodes: [*step]
`,
			literal: `
name: t
nodes:
  - id: a
    type: run
    args: {command: "echo {{ inputs.v }}"}
  - id: child
    type: workflow
    inline:
      entry: [a]
      nodes:
        - id: a
          type: run
          args: {command: "echo {{ inputs.v }}"}
`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want, err := ParseWorkflow([]byte(tc.literal))
			require.NoError(t, err)
			for name, parse := range map[string]func([]byte) (*reliantv1.Workflow, error){
				"ParseWorkflow":      ParseWorkflow,
				"ParseDraftWorkflow": ParseDraftWorkflow,
			} {
				got, err := parse([]byte(tc.aliased))
				require.NoError(t, err, name)
				require.Empty(t, cmp.Diff(want, got, protocmp.Transform()), name)
			}
		})
	}
}

// The disarm shape, asserted directly: the aliased command is an expression.
func TestAliasToTemplateIsAnExpression(t *testing.T) {
	wf, err := ParseWorkflow([]byte(`
name: t
nodes:
  - id: arm
    type: run
    args: {command: &disarm "n='{{ inputs.pr_number }}'"}
  - id: disarm
    type: run
    args: {command: *disarm}
`))
	require.NoError(t, err)
	cmd := wf.GetNodes()[1].GetRun().GetCommand()
	require.Equal(t, "n='{{ inputs.pr_number }}'", cmd.GetExpr())
	require.Empty(t, cmd.GetLiteral())
}

func TestAliasExpansionRefusesBadDocuments(t *testing.T) {
	var bomb strings.Builder
	bomb.WriteString("name: t\nx0: &x0 [a, a, a, a, a, a, a, a, a, a]\n")
	for i := 1; i <= 8; i++ {
		fmt.Fprintf(&bomb, "x%d: &x%d [", i, i)
		for j := 0; j < 10; j++ {
			if j > 0 {
				bomb.WriteString(", ")
			}
			fmt.Fprintf(&bomb, "*x%d", i-1)
		}
		bomb.WriteString("]\n")
	}
	cases := []struct {
		name, yaml, want string
	}{
		{"merge of a scalar", "name: t\nx: &s hi\nnodes:\n  - {<<: *s, id: a}\n", "merge key (<<) takes a mapping"},
		{"merge list holding a scalar", "name: t\nx: &s hi\nnodes:\n  - {<<: [*s], id: a}\n", "may hold only mappings"},
		{"alias to its own ancestor", "name: t\nx: &loop {a: [*loop]}\n", "contains an alias to itself"},
		{"alias bomb", bomb.String(), "aliases expand to more than"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseWorkflow([]byte(tc.yaml))
			require.ErrorContains(t, err, tc.want)
		})
	}
}

// A quoted "<<" is a plain string key, not a merge key, exactly as yaml.v3
// decodes it.
func TestQuotedMergeKeyIsAString(t *testing.T) {
	_, err := ParseWorkflow([]byte(`
name: t
nodes:
  - id: a
    type: save_message
    args: &b {role: assistant}
  - id: b
    type: save_message
    args: {"<<": *b, content: hi}
`))
	require.ErrorContains(t, err, `"<<"`)
}
