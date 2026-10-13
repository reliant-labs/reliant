// Copyright (c) 2025 Reliant Labs
package services

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/workflowlisttest"
)

// A listing resolves every ref of every workflow it lists, twice over (once to
// validate each of the user's workflows, once to say what each needs a machine
// for). Each resolution used to read the project's synced workflows and look
// the ref up among the user's own, so a listing's reads grew with the number
// of refs: in prod each project read was an 18 MB row and ListWorkflows took
// 5–10s at p90. Every distinct read is now made once per listing.
func TestListWorkflows_ReadsEachSourceOncePerListing(t *testing.T) {
	ctx, repo := workflowlisttest.NewFixture(t, 12)
	svc := NewWorkflowService(repo, nil)

	resp, err := svc.ListWorkflows(ctx, connect.NewRequest(&reliantv1.ListWorkflowsRequest{
		ProjectId: repo.Project.ID, IncludeHidden: proto.Bool(true),
	}))
	require.NoError(t, err)

	listed := map[string]bool{}
	for _, wf := range resp.Msg.GetWorkflows() {
		listed[wf.GetName()] = true
	}
	require.True(t, listed["mine-0"], "the user's workflows are listed")
	require.True(t, listed["team-flow-0"], "the project's workflows are listed")
	require.True(t, listed["builtin://agent"], "the builtins are listed")

	assert.Equal(t, 1, repo.CallCount("GetProjectWorkflowsJSON"), "the project's synced workflows are read once")
	assert.Equal(t, 1, repo.CallCount("GetProjectWithUserCheck"), "the project is read once")
	assert.Zero(t, repo.CallCount("GetProject"), "the ownership check already returned the project")
	// One lookup per distinct user slug a ref names: mine-0..mine-11 and the
	// three team-flow refs that miss among the user's own.
	assert.LessOrEqual(t, repo.CallCount("GetUsableWorkflowBySlug"), 12+3,
		"each slug is looked up among the user's workflows at most once")
	assert.LessOrEqual(t, repo.TotalReads(), 6+12+3,
		"a listing's reads do not grow with the number of refs it resolves")
}

// The read count is what made the latency: with a realistic round trip per
// read, a listing must cost a handful of round trips, not one per ref.
func TestListWorkflows_LatencyDoesNotScaleWithRefs(t *testing.T) {
	ctx, repo := workflowlisttest.NewFixture(t, 12)
	svc := NewWorkflowService(repo, nil)
	req := connect.NewRequest(&reliantv1.ListWorkflowsRequest{ProjectId: repo.Project.ID, IncludeHidden: proto.Bool(true)})
	// Warm the process-wide caches (parsed builtins, tool registry) so what
	// is measured is the listing's round trips, not first-use CPU.
	_, err := svc.ListWorkflows(ctx, req)
	require.NoError(t, err)

	repo.ResetCounts()
	repo.Latency = 20 * time.Millisecond
	start := time.Now()
	_, err = svc.ListWorkflows(ctx, req)
	elapsed := time.Since(start)
	require.NoError(t, err)
	// Before: 522 reads in a row, ~10s at 20ms each. Now a handful, the
	// independent ones concurrent.
	assert.Less(t, elapsed, time.Second,
		"ListWorkflows made %d reads at 20ms each", repo.TotalReads())
}
