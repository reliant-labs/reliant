package store

import (
	"context"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// The pipeline's enrolment and status reads (PipelineService, build plan task
// M2): what an admin enrols a repository by, and what `reliant pipeline
// status|queue` shows. Every query is one read; none locks.

// ListPipelineReposByRepository lists the enrolments of the provider's
// repositories last observed as owner/name (case-insensitive), by id. A name
// can have named another repository before a rename or a delete, so there
// may be more than one.
func ListPipelineReposByRepository(ctx context.Context, db orm.Context, provider, repository string) ([]*core.PipelineRepo, error) {
	var out []*core.PipelineRepo
	if err := db.Bun().NewRaw(`SELECT `+pipelineRepoColumns+` FROM pipeline_repos
		WHERE provider = ? AND lower(repository) = lower(?) ORDER BY id`, provider, repository).Scan(ctx, &out); err != nil {
		return nil, fmt.Errorf("list pipeline repos by name: %w", err)
	}
	return out, nil
}

// FreshIntegrationAccessByLabel lists the user's grants of the integration
// whose resource was last observed as label (owner/name, case-insensitive)
// and that were refreshed at or after freshAfter: the account and resource
// keys the user's own provider credential reaches that repository through.
func FreshIntegrationAccessByLabel(ctx context.Context, db orm.Context, userID, integrationID, label string, freshAfter time.Time) ([]core.IntegrationAccessGrant, error) {
	var out []core.IntegrationAccessGrant
	if err := db.Bun().NewRaw(`SELECT DISTINCT account_key, resource_key, resource_label FROM integration_event_access
		WHERE user_id = ? AND integration_id = ? AND lower(resource_label) = lower(?) AND refreshed_at >= ?
		ORDER BY account_key, resource_key`,
		userID, integrationID, label, freshAfter.UTC()).Scan(ctx, &out); err != nil {
		return nil, fmt.Errorf("read integration access by label: %w", err)
	}
	return out, nil
}

// CountPRPipelinesByState counts the repository's PRs in each pipeline
// state, in state order. A state with none is absent.
func CountPRPipelinesByState(ctx context.Context, db orm.Context, repoID string) ([]core.PRPipelineStateCount, error) {
	var out []core.PRPipelineStateCount
	if err := db.Bun().NewRaw(`SELECT state, count(*) AS count FROM pr_pipeline
		WHERE repo_id = ? GROUP BY state ORDER BY state`, repoID).Scan(ctx, &out); err != nil {
		return nil, fmt.Errorf("count pr pipelines by state: %w", err)
	}
	return out, nil
}

// ListActiveQueueEntries lists the repository's waiting and batched queue
// entries in pick order, each with its pull request's number.
func ListActiveQueueEntries(ctx context.Context, db orm.Context, repoID string) ([]*core.QueueEntryListing, error) {
	var out []*core.QueueEntryListing
	if err := db.Bun().NewRaw(`SELECT `+prefixColumns("e", queueEntryColumns)+`, r.number AS number
		FROM queue_entries AS e JOIN pr_records AS r ON r.id = e.record_id
		WHERE e.repo_id = ? AND e.state IN (?, ?)
		ORDER BY e.priority DESC, e.enqueued_at, e.id`,
		repoID, core.QueueEntryWaiting, core.QueueEntryBatched).Scan(ctx, &out); err != nil {
		return nil, fmt.Errorf("list active queue entries: %w", err)
	}
	return out, nil
}

// CurrentQueueBatch returns the repository's newest batch still building,
// gating or passed (not yet landed), or nil when there is none.
func CurrentQueueBatch(ctx context.Context, db orm.Context, repoID string) (*core.QueueBatch, error) {
	out, err := scanQueueBatch(ctx, db.Bun().NewRaw(`SELECT `+queueBatchColumns+` FROM queue_batches
		WHERE repo_id = ? AND state IN (?, ?, ?) ORDER BY started_at DESC, id DESC LIMIT 1`,
		repoID, core.QueueBatchBuilding, core.QueueBatchGating, core.QueueBatchPassed))
	if err != nil {
		return nil, fmt.Errorf("current queue batch: %w", err)
	}
	return out, nil
}
