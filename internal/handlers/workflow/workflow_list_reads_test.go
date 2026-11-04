// Copyright (c) 2025 Reliant Labs
package workflow

import (
	"testing"
	"testing/synctest"
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
// It is measured in synctest's virtual time, where the fake's 20ms sleep per
// read is the ONLY thing that advances the clock: the listing's own CPU
// (validating every workflow, under -race) costs nothing, however loaded the
// host. So the elapsed time is exactly the round trips on the listing's
// critical path times 20ms: reads in a row add up, concurrent ones overlap.
// A wall-clock version of this test measured the host instead — its
// no-latency control alone took 6.8s at load 100.
func TestListWorkflows_LatencyDoesNotScaleWithRefs(t *testing.T) {
	const roundTrip = 20 * time.Millisecond
	synctest.Test(t, func(t *testing.T) {
		ctx, repo := workflowlisttest.NewFixture(t, 12)
		svc := NewTestWorkflow(t, WithWorkflowDeps(Deps{Repo: repo}))
		req := connect.NewRequest(&reliantv1.ListWorkflowsRequest{ProjectId: repo.Project.ID, IncludeHidden: proto.Bool(true)})
		list := func(latency time.Duration) time.Duration {
			repo.Latency = latency
			start := time.Now()
			_, err := svc.ListWorkflows(ctx, req)
			require.NoError(t, err)
			return time.Since(start)
		}

		// The control: with no round-trip latency the listing takes no
		// virtual time at all, so nothing but the injected round trips is
		// on the clock below.
		require.Zero(t, list(0), "only the fake's round trips advance the clock")

		repo.ResetCounts()
		elapsed := list(roundTrip)
		trips := int(elapsed / roundTrip)
		require.Equal(t, time.Duration(trips)*roundTrip, elapsed, "virtual time moves only in whole round trips")
		// Before: 522 reads in a row, ~10s of round trips at 20ms each. Now a
		// handful in a row, the independent ones concurrent.
		assert.LessOrEqual(t, trips, maxListingRoundTripsInARow,
			"ListWorkflows made %d round trips in a row (%v at %v each; %d reads in all)",
			trips, elapsed, roundTrip, repo.TotalReads())
	})
}

// maxListingRoundTripsInARow is how many reads a listing may make one after
// another: a handful, against the fixture's 36 refs (the listing makes 2
// today: the project, then everything that needs it, concurrently).
const maxListingRoundTripsInARow = 4
