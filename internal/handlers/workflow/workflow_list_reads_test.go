// Copyright (c) 2025 Reliant Labs
package workflow

import (
	"math"
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
	svc := NewTestWorkflow(t, WithWorkflowDeps(Deps{Repo: repo}))

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
//
// What is measured is the time the round trips ADD: the same listing with and
// without the 20ms latency, each the best of three. The listing's own CPU
// (validating every workflow, under -race) is in both and cancels out; on a
// loaded host it alone took 6s, and an absolute bound measured the host.
func TestListWorkflows_LatencyDoesNotScaleWithRefs(t *testing.T) {
	ctx, repo := workflowlisttest.NewFixture(t, 12)
	svc := NewTestWorkflow(t, WithWorkflowDeps(Deps{Repo: repo}))
	req := connect.NewRequest(&reliantv1.ListWorkflowsRequest{ProjectId: repo.Project.ID, IncludeHidden: proto.Bool(true)})
	// Warm the process-wide caches (parsed builtins, tool registry) so what
	// is measured is the listing's round trips, not first-use CPU.
	_, err := svc.ListWorkflows(ctx, req)
	require.NoError(t, err)

	fastest := func(latency time.Duration) time.Duration {
		repo.Latency = latency
		best := time.Duration(math.MaxInt64)
		for range 3 {
			start := time.Now()
			_, err := svc.ListWorkflows(ctx, req)
			require.NoError(t, err)
			best = min(best, time.Since(start))
		}
		return best
	}
	cpu := fastest(0)
	repo.ResetCounts()
	withRoundTrips := fastest(20 * time.Millisecond)
	// Before: 522 reads in a row, ~10s of round trips at 20ms each. Now a
	// handful, the independent ones concurrent.
	assert.Less(t, withRoundTrips-cpu, time.Second,
		"ListWorkflows's round trips added %v (%v with 20ms reads, %v without; %d reads over 3 listings)",
		withRoundTrips-cpu, withRoundTrips, cpu, repo.TotalReads())
}
