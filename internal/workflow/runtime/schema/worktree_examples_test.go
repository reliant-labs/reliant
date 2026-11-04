package schema_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
	workflowyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/require"
)

// Read the builder's actual proto-derived examples and feed them through the
// YAML parser. An object example must become a map, not a quoted JSON string;
// expected_head must remain a CEL expression, not a made-up secret or commit.
func TestCreateWorktreeNewFieldExamplesKeepTheirTypes(t *testing.T) {
	examples := map[string]string{}
	for _, node := range schema.ListVisibleActivities() {
		if node.ID == "create_worktree" {
			for _, field := range node.InputFields {
				examples[field.Name] = field.Example
			}
		}
	}
	for _, name := range []string{"target_repo", "base_branches", "fetch_branch", "expected_head"} {
		require.NotEmpty(t, examples[name], name)
	}
	var bases map[string]string
	require.NoError(t, json.Unmarshal([]byte(examples["base_branches"]), &bases), "map placeholder must be a JSON object")
	require.Equal(t, map[string]string{"forge": "main"}, bases)
	source := fmt.Sprintf(`name: worktree-example
entry: [wt]
nodes:
  - id: wt
    type: create_worktree
    args:
      name: review-example
      target_repo: %q
      base_branches: %s
      fetch_branch: %q
      expected_head: %q
`, examples["target_repo"], examples["base_branches"], examples["fetch_branch"], examples["expected_head"])
	wf, err := workflowyaml.ParseWorkflow([]byte(source))
	require.NoError(t, err)
	args := wf.Nodes[0].GetCreateWorktree()
	require.NotNil(t, args)
	require.Equal(t, "control-plane", model.CelStringValue(args.GetTargetRepo()))
	require.Equal(t, bases, args.GetBaseBranches())
	require.Equal(t, "feat/review-repair", model.CelStringValue(args.GetFetchBranch()))
	require.Equal(t, "{{ nodes.pr.data.head.sha }}", args.GetExpectedHead().GetExpr())
	require.Empty(t, args.GetExpectedHead().GetLiteral(), "the SHA example must not become a literal placeholder commit")
}
