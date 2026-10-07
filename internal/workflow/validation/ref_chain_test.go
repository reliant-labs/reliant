// Copyright (c) 2025 Reliant Labs
package validation

import (
	"fmt"
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func refChainWorkflow(t *testing.T, name, ref string) *reliantv1.Workflow {
	t.Helper()
	wf, err := wfyaml.ParseWorkflow([]byte(fmt.Sprintf(`name: %s
entry: [next]
nodes:
  - id: next
    type: workflow
    ref: %s
`, name, ref)))
	require.NoError(t, err)
	return wf
}

// Issue #623: following refs must terminate. A ref cycle used to recurse in
// core.Compile until the stack overflowed — on the API server, a crash any
// user could trigger by saving two workflows that reference each other.
func TestStaticAnalysis_RefCycleIsReportedNotFollowedForever(t *testing.T) {
	byRef := map[string]*reliantv1.Workflow{
		"project://a": refChainWorkflow(t, "a", "project://b"),
		"project://b": refChainWorkflow(t, "b", "project://a"),
	}
	calls := 0
	loader := func(ref string) (*reliantv1.Workflow, error) {
		calls++
		if calls > 100 {
			t.Fatalf("loader called %d times: the ref cycle a → b → a is being followed forever", calls)
		}
		return byRef[ref], nil
	}

	res := StaticAnalysis(byRef["project://a"], loader)
	require.True(t, res.HasErrors())
	msg := res.Error()
	assert.Contains(t, msg, "a → project://b → project://a")
	assert.Contains(t, msg, "cycle")
}

// A broken ref two levels down is reported with every hop that reached it.
func TestStaticAnalysis_NestedBrokenRefNamesTheChain(t *testing.T) {
	byRef := map[string]*reliantv1.Workflow{
		"project://b": refChainWorkflow(t, "b", "project://c"),
	}
	loader := func(ref string) (*reliantv1.Workflow, error) {
		if wf, ok := byRef[ref]; ok {
			return wf, nil
		}
		return nil, fmt.Errorf("no project workflow is named %q", strings.TrimPrefix(ref, "project://"))
	}

	res := StaticAnalysis(refChainWorkflow(t, "a", "project://b"), loader)
	require.True(t, res.HasErrors())
	assert.Contains(t, res.Error(), `a → project://b → project://c: no project workflow is named "c"`)
}
