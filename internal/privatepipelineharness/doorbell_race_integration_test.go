//go:build integration

package privatepipelineharness

import (
	"context"
	"github.com/reliant-labs/reliant/internal/runcontinuity"
	"github.com/reliant-labs/reliant/internal/store"
	enumspb "go.temporal.io/api/enums/v1"
	"testing"
)

func assertConsumedFailureBeforeDoorbell(t *testing.T, ctx context.Context, f *resources, state store.ReviewAuthorityStore, intentID, root, run string, generation int64) {
	t.Helper()
	publication, _, err := state.Publication(ctx, intentID)
	if err != nil {
		t.Fatal(err)
	}
	if publication.State != "confirmed" || publication.CheckID <= 0 || publication.Intent.Conclusion != "failure" {
		t.Fatal("unavailable error occurred without durable confirmed failure check")
	}
	var consumed int64
	var outcome string
	if err := f.SQL.QueryRowContext(ctx, `SELECT s.consumed_sequence,o.state FROM pr_review_wait_subscriptions s JOIN pr_review_wait_outcomes o ON o.binding_id=s.binding_id AND o.head_sha=s.head_sha WHERE s.binding_id='binding' AND o.provider_sequence=$1`, generation).Scan(&consumed, &outcome); err != nil {
		t.Fatal(err)
	}
	if consumed != generation || outcome != "unavailable" {
		t.Fatal("unavailable delivery error occurred before exact durable failure consumption")
	}
	proof, err := runcontinuity.Resolve(ctx, runcontinuity.TemporalReader{Service: f.Temporal.WorkflowService(), Namespace: f.Namespace}, runcontinuity.Execution{WorkflowID: root, RunID: run})
	if err != nil {
		t.Fatal(err)
	}
	if proof.Live || proof.Terminal != enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED || proof.Current.RunID != run {
		t.Fatal("unavailable error was not the completed exact original-author doorbell race")
	}
}
