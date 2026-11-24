package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// PR records (dev-docs/pr-records.md). The tables are forge:no-entity, so the
// columns are named here, once per table, and every query reads through them.

const prRecordColumns = `id, provider, account_key, repository_id, repository, number, state, draft,
	head_sha, head_ref, base_ref, base_sha, author_id, author_login, title, labels,
	merged_at, merge_commit_sha, snapshot_at, snapshot_full, observed_at, created_at, updated_at`

type prRecordRow struct {
	ID, Provider, AccountKey, RepositoryID, Repository string
	Number                                             int
	State                                              string
	Draft                                              bool
	HeadSHA, HeadRef, BaseRef, BaseSHA                 string
	AuthorID, AuthorLogin, Title                       string
	Labels                                             []byte
	MergedAt                                           *time.Time
	MergeCommitSHA                                     string
	SnapshotAt                                         time.Time
	SnapshotFull                                       bool
	ObservedAt, CreatedAt, UpdatedAt                   time.Time
}

func (r prRecordRow) core() (*core.PRRecord, error) {
	var labels []string
	if len(r.Labels) > 0 {
		if err := json.Unmarshal(r.Labels, &labels); err != nil {
			return nil, fmt.Errorf("pr record %s labels: %w", r.ID, err)
		}
	}
	return &core.PRRecord{
		ID: r.ID, Provider: r.Provider, AccountKey: r.AccountKey, RepositoryID: r.RepositoryID,
		Repository: r.Repository, Number: r.Number, State: r.State, Draft: r.Draft,
		HeadSHA: r.HeadSHA, HeadRef: r.HeadRef, BaseRef: r.BaseRef, BaseSHA: r.BaseSHA,
		AuthorID: r.AuthorID, AuthorLogin: r.AuthorLogin, Title: r.Title, Labels: labels,
		MergedAt: r.MergedAt, MergeCommit: r.MergeCommitSHA, SnapshotAt: r.SnapshotAt,
		ObservedAt: r.ObservedAt, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}, nil
}

// sortedLabels is a label set in its stored form: unique and sorted, so two
// snapshots of one set compare equal.
func sortedLabels(labels []string) []string {
	seen := make(map[string]bool, len(labels))
	out := make([]string, 0, len(labels))
	for _, l := range labels {
		if l != "" && !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	sort.Strings(out)
	return out
}

// InsertPRRecordDelivery claims one event of a delivery for projection. It
// returns false when the event was already applied: a retried delivery
// changes nothing. Run it in the transaction that applies the event, so a
// failed apply leaves the event unclaimed.
func InsertPRRecordDelivery(ctx context.Context, db orm.Context, d core.PRRecordDelivery) (bool, error) {
	return prRecordChanged(db.Bun().NewRaw(`INSERT INTO pr_record_deliveries
		(provider, delivery_key, event_index, event_type, record_id, outcome, applied_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (provider, delivery_key, event_index) DO NOTHING`,
		d.Provider, d.DeliveryKey, d.EventIndex, d.EventType, d.RecordID, d.Outcome, d.AppliedAt).Exec(ctx))
}

// SettlePRRecordDelivery records what applying a claimed event did.
func SettlePRRecordDelivery(ctx context.Context, db orm.Context, d core.PRRecordDelivery) error {
	_, err := db.Bun().NewRaw(`UPDATE pr_record_deliveries SET record_id = ?, outcome = ?
		WHERE provider = ? AND delivery_key = ? AND event_index = ?`,
		d.RecordID, d.Outcome, d.Provider, d.DeliveryKey, d.EventIndex).Exec(ctx)
	return err
}

// PrunePRRecordDeliveries deletes delivery receipts applied before before,
// at most 1000 per call.
func PrunePRRecordDeliveries(ctx context.Context, db orm.Context, before time.Time) (int64, error) {
	res, err := db.Bun().NewRaw(`DELETE FROM pr_record_deliveries WHERE (provider, delivery_key, event_index) IN (
		SELECT provider, delivery_key, event_index FROM pr_record_deliveries WHERE applied_at < ?
		LIMIT 1000 FOR UPDATE SKIP LOCKED)`, before).Exec(ctx)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ApplyPRRecordChange applies one decoded event to the records, once: it
// returns false, changing nothing, when this (provider, delivery key, event
// index) was applied before. READ COMMITTED is correct because every write
// is one upsert whose conflict arm re-checks its guard against the row it
// locked (the snapshot's age, a check's run id), and the receipt's primary
// key serializes two workers applying the same event.
func ApplyPRRecordChange(ctx context.Context, db orm.Context, c core.PRRecordChange) (bool, error) {
	var applied bool
	err := db.RunTx(ctx, orm.ReadCommitted, func(tx context.Context) error {
		applied = false
		d := c.Delivery
		d.Outcome = core.PRDeliveryIgnored
		claimed, err := InsertPRRecordDelivery(tx, db, d)
		if err != nil || !claimed {
			return err
		}
		applied = true
		if c.Snapshot != nil {
			res, err := ApplyPRRecordSnapshot(tx, db, *c.Snapshot)
			if err != nil {
				return err
			}
			d.RecordID = res.RecordID
			d.Outcome = core.PRDeliveryStale
			if res.Applied {
				d.Outcome = core.PRDeliveryApplied
			}
			if c.Snapshot.HeadSHA != "" {
				h := core.PRRecordHead{
					ID: c.HeadID, RecordID: res.RecordID, HeadSHA: c.Snapshot.HeadSHA,
					PreviousHeadSHA: c.PreviousHeadSHA, FirstSeenAt: c.Snapshot.ObservedAt,
				}
				if c.PreviousHeadSHA != "" {
					at := c.Snapshot.SnapshotAt
					h.PushedSnapshotAt = &at
				}
				if err := EnsurePRRecordHead(tx, db, h); err != nil {
					return err
				}
			}
			// A same-second push that moved the head may have been preceded by
			// the push over it, which could not apply then: follow it.
			if res.Tied && c.Snapshot.Delta.HeadSHA != "" && res.HeadSHA == c.Snapshot.Delta.HeadSHA {
				if res.HeadSHA, err = AdvancePRRecordHead(tx, db, res.RecordID); err != nil {
					return err
				}
			}
			if res.HeadSHA != "" {
				if err := SupersedePRRecordHeads(tx, db, res.RecordID, res.HeadSHA, c.Snapshot.ObservedAt); err != nil {
					return err
				}
			}
			for _, rv := range c.Reviews {
				rv.RecordID = res.RecordID
				if err := InsertPRRecordReview(tx, db, rv); err != nil {
					return err
				}
				d.Outcome = core.PRDeliveryApplied
			}
		}
		if c.Check != nil {
			if err := UpsertPRRecordCheck(tx, db, *c.Check); err != nil {
				return err
			}
			d.Outcome = core.PRDeliveryApplied
		}
		if d.Outcome == core.PRDeliveryIgnored {
			return nil
		}
		return SettlePRRecordDelivery(tx, db, d)
	})
	return applied && err == nil, err
}

// PRRecordApplied is what applying one snapshot did to its record.
type PRRecordApplied struct {
	RecordID string
	// HeadSHA is the record's current head after the apply, which may be
	// another snapshot's when this one was stale.
	HeadSHA string
	// Applied is true when the snapshot replaced the record, or when it tied
	// the record's snapshot time and its delta was applied.
	Applied bool
	// Tied is true when the snapshot was exactly as new as the record's.
	Tied bool
}

// ApplyPRRecordSnapshot applies a pull request snapshot to its record, in one
// statement per outcome so concurrent deliveries of one pull request order
// themselves on the row lock:
//
//   - a record that does not exist is created from any snapshot;
//   - a full snapshot newer than the record (by the pull request's own
//     updated_at), or exactly as new as a slim one, replaces its state,
//     head, base, title and labels;
//   - a full snapshot exactly as new as a full one applies only its own
//     transition (core.PRRecordDelta): GitHub's timestamps have one-second
//     resolution, so neither snapshot of a same-second pair is the later;
//   - anything older only marks the record observed.
func ApplyPRRecordSnapshot(ctx context.Context, db orm.Context, s core.PRRecordSnapshot) (PRRecordApplied, error) {
	labels, err := json.Marshal(sortedLabels(s.Labels))
	if err != nil {
		return PRRecordApplied{}, err
	}
	var out struct {
		ID, HeadSHA string
		Tied        bool
	}
	err = db.Bun().NewRaw(`INSERT INTO pr_records AS r (`+prRecordColumns+`)
		VALUES (?0, ?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12, ?13, ?14, ?15::jsonb, ?16, ?17, ?18, ?20, ?19, ?19, ?19)
		ON CONFLICT (provider, repository_id, number) DO UPDATE SET
			account_key = EXCLUDED.account_key, repository = EXCLUDED.repository,
			state = EXCLUDED.state, draft = EXCLUDED.draft,
			head_sha = EXCLUDED.head_sha, head_ref = EXCLUDED.head_ref,
			base_ref = EXCLUDED.base_ref, base_sha = EXCLUDED.base_sha,
			author_id = EXCLUDED.author_id, author_login = EXCLUDED.author_login,
			title = EXCLUDED.title, labels = EXCLUDED.labels,
			merged_at = EXCLUDED.merged_at, merge_commit_sha = EXCLUDED.merge_commit_sha,
			snapshot_at = EXCLUDED.snapshot_at, snapshot_full = EXCLUDED.snapshot_full,
			observed_at = GREATEST(r.observed_at, EXCLUDED.observed_at), updated_at = EXCLUDED.observed_at
		WHERE EXCLUDED.snapshot_full AND (r.snapshot_at < EXCLUDED.snapshot_at
			OR (r.snapshot_at = EXCLUDED.snapshot_at AND NOT r.snapshot_full))
		RETURNING r.id, r.head_sha`,
		s.ID, s.Provider, s.AccountKey, s.RepositoryID, s.Repository, s.Number, s.State, s.Draft,
		s.HeadSHA, s.HeadRef, s.BaseRef, s.BaseSHA, s.AuthorID, s.AuthorLogin, s.Title, string(labels),
		s.MergedAt, s.MergeCommit, s.SnapshotAt.UTC(), s.ObservedAt.UTC(), s.Full).
		Scan(ctx, &out.ID, &out.HeadSHA)
	if err == nil {
		return PRRecordApplied{RecordID: out.ID, HeadSHA: out.HeadSHA, Applied: true}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return PRRecordApplied{}, fmt.Errorf("apply pr record snapshot: %w", err)
	}
	// The record is at least as new. On a tie of two full snapshots, apply
	// this event's own transition; either way, mark the record seen. Every
	// right-hand side reads the row as it was before this statement.
	d := s.Delta
	draftSet, draft := d.Draft != nil, d.Draft != nil && *d.Draft
	err = db.Bun().NewRaw(`UPDATE pr_records AS r SET
			labels = CASE WHEN ?3 AND r.snapshot_at = ?4 AND (?5 <> '' OR ?6 <> '') THEN (
				SELECT coalesce(jsonb_agg(DISTINCT x ORDER BY x), '[]'::jsonb)
				FROM jsonb_array_elements_text(r.labels || CASE WHEN ?5 = '' THEN '[]'::jsonb ELSE jsonb_build_array(?5::text) END) AS t(x)
				WHERE ?6 = '' OR x <> ?6)
				ELSE r.labels END,
			state = CASE WHEN ?3 AND r.snapshot_at = ?4 AND ?7 <> '' AND r.state <> 'merged' THEN ?7 ELSE r.state END,
			merged_at = CASE WHEN ?3 AND r.snapshot_at = ?4 AND ?7 = 'merged' AND r.state <> 'merged' THEN ?8 ELSE r.merged_at END,
			merge_commit_sha = CASE WHEN ?3 AND r.snapshot_at = ?4 AND ?7 = 'merged' AND r.state <> 'merged' THEN ?9 ELSE r.merge_commit_sha END,
			draft = CASE WHEN ?3 AND r.snapshot_at = ?4 AND ?10 THEN ?11 ELSE r.draft END,
			head_sha = CASE WHEN ?3 AND r.snapshot_at = ?4 AND ?13 <> '' AND r.head_sha = ?12 THEN ?13 ELSE r.head_sha END,
			head_ref = CASE WHEN ?3 AND r.snapshot_at = ?4 AND ?13 <> '' AND r.head_sha = ?12 AND ?14 <> '' THEN ?14 ELSE r.head_ref END,
			title = CASE WHEN ?3 AND r.snapshot_at = ?4 AND ?15 <> '' THEN ?15 ELSE r.title END,
			base_ref = CASE WHEN ?3 AND r.snapshot_at = ?4 AND ?16 <> '' THEN ?16 ELSE r.base_ref END,
			observed_at = GREATEST(r.observed_at, ?17)
		WHERE r.provider = ?0 AND r.repository_id = ?1 AND r.number = ?2
		RETURNING r.id, r.head_sha, (?3 AND r.snapshot_at = ?4)`,
		s.Provider, s.RepositoryID, s.Number, s.Full, s.SnapshotAt.UTC(),
		d.LabelAdded, d.LabelRemoved, d.State, d.MergedAt, d.MergeCommit, draftSet, draft,
		d.FromHead, d.HeadSHA, d.HeadRef, d.Title, d.BaseRef, s.ObservedAt.UTC()).
		Scan(ctx, &out.ID, &out.HeadSHA, &out.Tied)
	if err != nil {
		return PRRecordApplied{}, fmt.Errorf("observe pr record: %w", err)
	}
	return PRRecordApplied{RecordID: out.ID, HeadSHA: out.HeadSHA, Tied: out.Tied, Applied: out.Tied && d != (core.PRRecordDelta{})}, nil
}

// AdvancePRRecordHead moves a record's head one push forward when exactly one
// known head was pushed over it in the record's own snapshot second: the push
// that arrived first of two in one second, which could not apply until the
// one it replaced had. An edge an older push recorded is history, not a
// pending push (a force-push back to a sha the pull request had before must
// not follow that sha's old successor). It returns the head after the move.
func AdvancePRRecordHead(ctx context.Context, db orm.Context, recordID string) (string, error) {
	var head string
	err := db.Bun().NewRaw(`UPDATE pr_records AS r SET head_sha = n.head_sha
		FROM (SELECT min(h.head_sha) AS head_sha FROM pr_record_heads h JOIN pr_records p ON p.id = h.record_id
			WHERE h.record_id = ?0 AND h.previous_head_sha = p.head_sha AND h.head_sha <> p.head_sha
				AND h.pushed_snapshot_at = p.snapshot_at
			HAVING count(*) = 1) AS n
		WHERE r.id = ?0
		RETURNING r.head_sha`, recordID).Scan(ctx, &head)
	if errors.Is(err, sql.ErrNoRows) {
		err = db.Bun().NewRaw(`SELECT head_sha FROM pr_records WHERE id = ?`, recordID).Scan(ctx, &head)
	}
	if err != nil {
		return "", fmt.Errorf("advance pr record head: %w", err)
	}
	return head, nil
}

// EnsurePRRecordHead records a head of a pull request. A head already known
// keeps its row; it learns the head a push replaced (and that push's snapshot
// time) if it did not know it.
func EnsurePRRecordHead(ctx context.Context, db orm.Context, h core.PRRecordHead) error {
	var pushedAt *time.Time
	if h.PushedSnapshotAt != nil {
		at := h.PushedSnapshotAt.UTC()
		pushedAt = &at
	}
	_, err := db.Bun().NewRaw(`INSERT INTO pr_record_heads AS h
		(id, record_id, head_sha, previous_head_sha, pushed_snapshot_at, first_seen_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (record_id, head_sha) DO UPDATE SET
			previous_head_sha = CASE WHEN h.previous_head_sha = '' THEN EXCLUDED.previous_head_sha ELSE h.previous_head_sha END,
			pushed_snapshot_at = CASE WHEN h.previous_head_sha = '' THEN EXCLUDED.pushed_snapshot_at ELSE h.pushed_snapshot_at END,
			first_seen_at = LEAST(h.first_seen_at, EXCLUDED.first_seen_at)`,
		h.ID, h.RecordID, h.HeadSHA, h.PreviousHeadSHA, pushedAt, h.FirstSeenAt.UTC()).Exec(ctx)
	if err != nil {
		return fmt.Errorf("ensure pr record head: %w", err)
	}
	return nil
}

// SupersedePRRecordHeads marks every head of the record but current
// superseded (at at, unless it already was), and current not superseded.
func SupersedePRRecordHeads(ctx context.Context, db orm.Context, recordID, current string, at time.Time) error {
	_, err := db.Bun().NewRaw(`UPDATE pr_record_heads SET
			superseded_at = CASE WHEN head_sha = ?1 THEN NULL ELSE coalesce(superseded_at, ?2) END
		WHERE record_id = ?0 AND ((head_sha = ?1) = (superseded_at IS NOT NULL))`,
		recordID, current, at.UTC()).Exec(ctx)
	if err != nil {
		return fmt.Errorf("supersede pr record heads: %w", err)
	}
	return nil
}

// InsertPRRecordReview records one marker a review carried. A review is
// immutable once submitted, so a redelivery changes nothing.
func InsertPRRecordReview(ctx context.Context, db orm.Context, r core.PRRecordReview) error {
	_, err := db.Bun().NewRaw(`INSERT INTO pr_record_reviews
		(id, record_id, review_id, commit_sha, marker_sha, kind, of_review_id, author_id, author_login, submitted_at, observed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (record_id, review_id, kind) DO NOTHING`,
		r.ID, r.RecordID, r.ReviewID, r.CommitSHA, r.MarkerSHA, r.Kind, r.OfReviewID,
		r.AuthorID, r.AuthorLogin, r.SubmittedAt.UTC(), r.ObservedAt.UTC()).Exec(ctx)
	if err != nil {
		return fmt.Errorf("insert pr record review: %w", err)
	}
	return nil
}

// UpsertPRRecordCheck keeps the newest run of a check on a commit: a higher
// run id replaces the row, and for one run a completion replaces a start,
// never the other way round.
func UpsertPRRecordCheck(ctx context.Context, db orm.Context, c core.PRRecordCheck) error {
	_, err := db.Bun().NewRaw(`INSERT INTO pr_record_checks AS c
		(id, provider, repository_id, head_sha, name, app_slug, check_run_id, status, conclusion, completed_at, observed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (provider, repository_id, head_sha, name, app_slug) DO UPDATE SET
			check_run_id = EXCLUDED.check_run_id, status = EXCLUDED.status,
			conclusion = EXCLUDED.conclusion, completed_at = EXCLUDED.completed_at,
			observed_at = EXCLUDED.observed_at
		WHERE c.check_run_id < EXCLUDED.check_run_id
			OR (c.check_run_id = EXCLUDED.check_run_id AND c.completed_at IS NULL)`,
		c.ID, c.Provider, c.RepositoryID, c.HeadSHA, c.Name, c.AppSlug, c.CheckRunID,
		c.Status, c.Conclusion, c.CompletedAt, c.ObservedAt.UTC()).Exec(ctx)
	if err != nil {
		return fmt.Errorf("upsert pr record check: %w", err)
	}
	return nil
}

// GetPRRecord returns the record of one pull request, looked up by the
// repository's owner/name (case-insensitive, as the provider treats it)
// among the given repository identities only, or nil when there is none. A
// name can have named a deleted repository whose pull requests share
// numbers with the re-created one's: the caller resolves which identities it
// may read first (as a list does), so a record it cannot read never hides
// one it can. No identities is no record. Of two readable identities with
// the number, the more recently observed wins.
func GetPRRecord(ctx context.Context, db orm.Context, provider, repository string, number int, repositoryIDs []string) (*core.PRRecord, error) {
	if len(repositoryIDs) == 0 {
		return nil, nil
	}
	var rows []prRecordRow
	err := db.Bun().NewRaw(`SELECT `+prRecordColumns+` FROM pr_records
		WHERE provider = ?0 AND lower(repository) = lower(?1) AND number = ?2 AND repository_id = ANY(?3::text[])
		ORDER BY observed_at DESC, repository_id LIMIT 1`, provider, repository, number, orm.Arg(repositoryIDs)).Scan(ctx, &rows)
	if err != nil {
		return nil, fmt.Errorf("get pr record: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rows[0].core()
}

// ListPRRecordRepositories lists the repository identities records were
// kept under a repository name (case-insensitive).
func ListPRRecordRepositories(ctx context.Context, db orm.Context, provider, repository string) ([]core.PRRecordRepository, error) {
	var out []core.PRRecordRepository
	err := db.Bun().NewRaw(`SELECT DISTINCT account_key, repository_id FROM pr_records
		WHERE provider = ? AND lower(repository) = lower(?)
		ORDER BY repository_id, account_key`, provider, repository).Scan(ctx, &out)
	if err != nil {
		return nil, fmt.Errorf("list pr record repositories: %w", err)
	}
	return out, nil
}

// ListPRRecords lists one repository's records in (number, repository id)
// order.
func ListPRRecords(ctx context.Context, db orm.Context, f core.PRRecordFilter) ([]*core.PRRecord, error) {
	if f.Limit <= 0 {
		return nil, nil
	}
	ids := f.RepositoryIDs
	if ids == nil {
		ids = []string{}
	}
	var rows []prRecordRow
	err := db.Bun().NewRaw(`SELECT `+prRecordColumns+` FROM pr_records
		WHERE provider = ?0 AND lower(repository) = lower(?1) AND (?2 = '' OR state = ?2)
			AND (cardinality(?3::text[]) = 0 OR repository_id = ANY(?3::text[]))
			AND (number, repository_id) > (?4, ?5)
		ORDER BY number, repository_id LIMIT ?6`,
		f.Provider, f.Repository, f.State, orm.Arg(ids), f.AfterNumber, f.AfterRepositoryID, f.Limit).Scan(ctx, &rows)
	if err != nil {
		return nil, fmt.Errorf("list pr records: %w", err)
	}
	out := make([]*core.PRRecord, 0, len(rows))
	for _, r := range rows {
		rec, err := r.core()
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, nil
}

// ListPRRecordHeads lists a record's heads, oldest first.
func ListPRRecordHeads(ctx context.Context, db orm.Context, recordID string) ([]core.PRRecordHead, error) {
	var out []core.PRRecordHead
	err := db.Bun().NewRaw(`SELECT id, record_id, head_sha, previous_head_sha, pushed_snapshot_at, first_seen_at, superseded_at
		FROM pr_record_heads WHERE record_id = ? ORDER BY first_seen_at, head_sha`, recordID).Scan(ctx, &out)
	if err != nil {
		return nil, fmt.Errorf("list pr record heads: %w", err)
	}
	return out, nil
}

// ListPRRecordReviews lists the markers reviews of the record carried on the
// given commits, in submission order (review id breaks a tie).
func ListPRRecordReviews(ctx context.Context, db orm.Context, recordID string, commits []string) ([]core.PRRecordReview, error) {
	if len(commits) == 0 {
		return nil, nil
	}
	var out []core.PRRecordReview
	err := db.Bun().NewRaw(`SELECT id, record_id, review_id, commit_sha, marker_sha, kind, of_review_id,
			author_id, author_login, submitted_at, observed_at
		FROM pr_record_reviews WHERE record_id = ? AND commit_sha = ANY(?)
		ORDER BY submitted_at, length(review_id), review_id, kind`, recordID, orm.Arg(commits)).Scan(ctx, &out)
	if err != nil {
		return nil, fmt.Errorf("list pr record reviews: %w", err)
	}
	return out, nil
}

// ListPRRecordChecks lists the checks on the given commits of a repository.
func ListPRRecordChecks(ctx context.Context, db orm.Context, provider, repositoryID string, shas []string) ([]core.PRRecordCheck, error) {
	if len(shas) == 0 {
		return nil, nil
	}
	var out []core.PRRecordCheck
	err := db.Bun().NewRaw(`SELECT id, provider, repository_id, head_sha, name, app_slug, check_run_id,
			status, conclusion, completed_at, observed_at
		FROM pr_record_checks WHERE provider = ? AND repository_id = ? AND head_sha = ANY(?)
		ORDER BY head_sha, name, app_slug`, provider, repositoryID, orm.Arg(shas)).Scan(ctx, &out)
	if err != nil {
		return nil, fmt.Errorf("list pr record checks: %w", err)
	}
	return out, nil
}

// HasFreshIntegrationAccess reports whether the user holds a grant for
// (account, resource) of the integration refreshed at or after freshAfter.
func HasFreshIntegrationAccess(ctx context.Context, db orm.Context, userID, integrationID, account, resource string, freshAfter time.Time) (bool, error) {
	var n int
	err := db.Bun().NewRaw(`SELECT count(*) FROM integration_event_access
		WHERE user_id = ? AND integration_id = ? AND account_key = ? AND resource_key = ? AND refreshed_at >= ?`,
		userID, integrationID, account, resource, freshAfter.UTC()).Scan(ctx, &n)
	if err != nil {
		return false, fmt.Errorf("read integration access: %w", err)
	}
	return n > 0, nil
}

// prRecordChanged is whether a statement changed a row.
func prRecordChanged(res sql.Result, err error) (bool, error) {
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}
