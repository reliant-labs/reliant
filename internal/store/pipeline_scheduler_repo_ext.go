package store

import (
	"context"
	"fmt"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// The merge queue scheduler's reads of the PR records its entries name
// (internal/mergequeue). The queue reads only DB state to admit an entry
// (pipeline redesign §3.1): these are the record and its checks.

// GetQueuePRRecord returns the PR record with id, or nil when there is none.
func GetQueuePRRecord(ctx context.Context, db orm.Context, id string) (*core.PRRecord, error) {
	var rows []prRecordRow
	if err := db.Bun().NewRaw(`SELECT `+prRecordColumns+` FROM pr_records WHERE id = ?`, id).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("get queue pr record %s: %w", id, err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rows[0].core()
}

// ListStaleQueueEntries lists the repository's entries still in the queue
// (waiting or batched) whose PR record no longer holds them: the record is
// gone or not open (closed, merged), or its head is not the entry's. The
// scheduler ejects each one (closed, head_moved).
func ListStaleQueueEntries(ctx context.Context, db orm.Context, repoID string) ([]core.StaleQueueEntry, error) {
	var rows []staleQueueEntryRow
	if err := db.Bun().NewRaw(`SELECT `+prefixColumns("e", queueEntryColumns)+`,
			COALESCE(r.state, '') AS record_state, COALESCE(r.head_sha, '') AS record_head
		FROM queue_entries AS e LEFT JOIN pr_records AS r ON r.id = e.record_id
		WHERE e.repo_id = ? AND e.state IN ('waiting', 'batched')
		  AND (r.id IS NULL OR r.state <> 'open' OR r.head_sha <> e.head_sha)
		ORDER BY `+prefixColumns("e", queueEntryOrder), repoID).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("list stale queue entries: %w", err)
	}
	out := make([]core.StaleQueueEntry, len(rows))
	for i, r := range rows {
		e := r.QueueEntry
		out[i] = core.StaleQueueEntry{Entry: &e, RecordState: r.RecordState, RecordHead: r.RecordHead}
	}
	return out, nil
}

type staleQueueEntryRow struct {
	core.QueueEntry
	RecordState string
	RecordHead  string
}

// ListQueueHeadChecks lists the checks recorded on one head of a PR record's
// repository: the check tier's evidence (§3.3), as the webhook projection
// recorded it.
func ListQueueHeadChecks(ctx context.Context, db orm.Context, recordID, head string) ([]core.PRRecordCheck, error) {
	var out []core.PRRecordCheck
	if err := db.Bun().NewRaw(`SELECT c.id, c.provider, c.repository_id, c.head_sha, c.name, c.app_slug, c.check_run_id,
			c.status, c.conclusion, c.completed_at, c.observed_at
		FROM pr_record_checks AS c JOIN pr_records AS r ON r.provider = c.provider AND r.repository_id = c.repository_id
		WHERE r.id = ? AND c.head_sha = ?
		ORDER BY c.name, c.app_slug`, recordID, head).Scan(ctx, &out); err != nil {
		return nil, fmt.Errorf("list queue head checks: %w", err)
	}
	return out, nil
}
