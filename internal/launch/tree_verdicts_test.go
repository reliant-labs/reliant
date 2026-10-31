package launch

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/workflow/validation"
)

const (
	verdictRoot       = "name: root\nentry: [child]\nnodes:\n  - {id: child, type: workflow, ref: child}\n"
	verdictValidChild = "name: child\nentry: [a]\nnodes:\n  - id: a\n    type: approval\n    args: {title: ok}\n"
	// Parses, but its condition is not CEL: only the tree's analysis can say so.
	verdictBrokenChild = "name: child\nentry: [a]\nnodes:\n  - id: a\n    type: approval\n    args: {title: ok}\n    condition: \"nodes.a.output ===\"\n"
)

// verdictStore serves the user's own workflows from defs.
func verdictStore(defs map[string]string) *MockWorkflowResolver {
	return &MockWorkflowResolver{
		GetUsableWorkflowBySlugFunc: func(_ context.Context, _, slug string) (*db.WorkflowDraft, error) {
			def, ok := defs[slug]
			if !ok {
				return nil, nil
			}
			return &db.WorkflowDraft{Definition: def}, nil
		},
	}
}

// countAnalyses counts the tree analyses run for the rest of the test.
func countAnalyses(t *testing.T) *int {
	t.Helper()
	n := new(int)
	prev := analyzeTree
	analyzeTree = func(wf *reliantv1.Workflow, opts *validation.ValidationOptions) *validation.Result {
		*n++
		return prev(wf, opts)
	}
	t.Cleanup(func() { analyzeTree = prev })
	return n
}

// ListInbox resolves every activated workflow on every read: an unchanged tree
// is analysed once, and every later resolution reuses that verdict.
func TestValidatedTree_UnchangedTreeIsAnalysedOnce(t *testing.T) {
	analyses := countAnalyses(t)
	user := uuid.NewString()
	store := verdictStore(map[string]string{"root": verdictRoot, "child": verdictValidChild})

	for range 3 {
		wf, err := ResolveValidatedRunWorkflow(context.Background(), store, user, "root", "")
		require.NoError(t, err)
		require.Equal(t, "root", wf.GetName())
	}
	require.Equal(t, 1, *analyses)
}

// The memo is keyed by the root, but its verdict depends on the children too:
// a child that changes after a valid verdict is analysed, and fails, rather
// than riding the root's cached pass. Fixing it passes again.
func TestValidatedTree_ChangedChildIsReanalysed(t *testing.T) {
	analyses := countAnalyses(t)
	user := uuid.NewString()
	defs := map[string]string{"root": verdictRoot, "child": verdictValidChild}
	store := verdictStore(defs)
	ctx := context.Background()

	_, err := ResolveValidatedRunWorkflow(ctx, store, user, "root", "")
	require.NoError(t, err)

	defs["child"] = verdictBrokenChild
	_, err = ResolveValidatedRunWorkflow(ctx, store, user, "root", "")
	var invalid *ValidationError
	require.ErrorAs(t, err, &invalid, "the broken child must fail the root, cached pass or not")
	require.Contains(t, invalid.Reason, "workflow tree validation failed for 'root'")
	require.Equal(t, 2, *analyses)

	// The failing verdict is memoized as well, and each caller gets its own error.
	_, again := ResolveValidatedRunWorkflow(ctx, store, user, "root", "")
	require.EqualError(t, again, err.Error())
	require.NotSame(t, err, again)
	require.Equal(t, 2, *analyses)

	defs["child"] = verdictValidChild
	_, err = ResolveValidatedRunWorkflow(ctx, store, user, "root", "")
	require.NoError(t, err)
}

// A child that cannot be read is not a verdict: nothing is memoized, so the
// next resolution, after the store recovers, analyses the tree.
func TestValidatedTree_ChildLookupFailureIsNotMemoized(t *testing.T) {
	analyses := countAnalyses(t)
	ctx, user := context.Background(), uuid.NewString()
	_, err := ResolveValidatedRunWorkflow(ctx, admissionStore{}, user, "root", "project")
	var lookup *WorkflowLookupError
	require.ErrorAs(t, err, &lookup)
	_, err = ResolveValidatedRunWorkflow(ctx, admissionStore{}, user, "root", "project")
	require.ErrorAs(t, err, &lookup)
	require.Equal(t, 2, *analyses)
}
