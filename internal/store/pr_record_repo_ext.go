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
	merged_at, merge_commit_sha, snapshot_at, observed_at, created_at, updated_at`

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
	SnapshotAt, ObservedAt, CreatedAt, UpdatedAt       time.Time
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

// PRRecordApplied is what applying one snapshot did to its record.
type PRRecordApplied struct {
	RecordID string
	// HeadSHA is the record's current head after the apply, which may be
	// another snapshot's when this one was stale.
	HeadSHA string
	// Applied is false when the snapshot was older than the record (or a
	// slim snapshot of a record that exists) and changed nothing but, on a
	// tie, its label delta.
	Applied bool
}

// ApplyPRRecordSnapshot applies a pull request snapshot to its record, in one
// statement per outcome so concurrent deliveries of one pull request order
// themselves on the row lock:
//
//   - a record that does not exist is created from any snapshot;
//   - a full snapshot strictly newer than the record (by the pull request's
//     own updated_at) replaces its state, head, base, title and labels;
//   - a snapshot exactly as new applies only its label delta (two label
//     events in one second carry different full sets, so neither set wins);
//   - anything older only marks the record observed.
func ApplyPRRecordSnapshot(ctx context.Context, db orm.Context, s core.PRRecordSnapshot) (PRRecordApplied, error) {
	labels, err := json.Marshal(sortedLabels(s.Labels))
	if err != nil {
		return PRRecordApplied{}, err
	}
	var out struct {
		ID, HeadSHA string
	}
	err = db.Bun().NewRaw(`INSERT INTO pr_records AS r (`+prRecordColumns+`)
		VALUES (?0, ?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12, ?13, ?14, ?15::jsonb, ?16, ?17, ?18, ?19, ?19, ?19)
		ON CONFLICT (provider, repository_id, number) DO UPDATE SET
			account_key = EXCLUDED.account_key, repository = EXCLUDED.repository,
			state = EXCLUDED.state, draft = EXCLUDED.draft,
			head_sha = EXCLUDED.head_sha, head_ref = EXCLUDED.head_ref,
			base_ref = EXCLUDED.base_ref, base_sha = EXCLUDED.base_sha,
			author_id = EXCLUDED.author_id, author_login = EXCLUDED.author_login,
			title = EXCLUDED.title, labels = EXCLUDED.labels,
			merged_at = EXCLUDED.merged_at, merge_commit_sha = EXCLUDED.merge_commit_sha,
			snapshot_at = EXCLUDED.snapshot_at,
			observed_at = GREATEST(r.observed_at, EXCLUDED.observed_at), updated_at = EXCLUDED.observed_at
		WHERE ?20 AND r.snapshot_at < EXCLUDED.snapshot_at
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
	// The record is at least as new: apply a tie's label delta and mark it seen.
	err = db.Bun().NewRaw(`UPDATE pr_records AS r SET
			labels = CASE WHEN r.snapshot_at = ?3 AND (?4 <> '' OR ?5 <> '') THEN (
				SELECT coalesce(jsonb_agg(DISTINCT x ORDER BY x), '[]'::jsonb)
				FROM jsonb_array_elements_text(r.labels || CASE WHEN ?4 = '' THEN '[]'::jsonb ELSE jsonb_build_array(?4::text) END) AS t(x)
				WHERE ?5 = '' OR x <> ?5)
				ELSE r.labels END,
			observed_at = GREATEST(r.observed_at, ?6)
		WHERE r.provider = ?0 AND r.repository_id = ?1 AND r.number = ?2
		RETURNING r.id, r.head_sha`,
		s.Provider, s.RepositoryID, s.Number, s.SnapshotAt.UTC(), s.LabelAdded, s.LabelRemoved, s.ObservedAt.UTC()).
		Scan(ctx, &out.ID, &out.HeadSHA)
	if err != nil {
		return PRRecordApplied{}, fmt.Errorf("observe pr record: %w", err)
	}
	return PRRecordApplied{RecordID: out.ID, HeadSHA: out.HeadSHA}, nil
}

// EnsurePRRecordHead records a head of a pull request. A head already known
// keeps its row; it learns the head a push replaced if it did not know it.
func EnsurePRRecordHead(ctx context.Context, db orm.Context, h core.PRRecordHead) error {
	_, err := db.Bun().NewRaw(`INSERT INTO pr_record_heads AS h
		(id, record_id, head_sha, previous_head_sha, first_seen_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (record_id, head_sha) DO UPDATE SET
			previous_head_sha = CASE WHEN h.previous_head_sha = '' THEN EXCLUDED.previous_head_sha ELSE h.previous_head_sha END,
			first_seen_at = LEAST(h.first_seen_at, EXCLUDED.first_seen_at)`,
		h.ID, h.RecordID, h.HeadSHA, h.PreviousHeadSHA, h.FirstSeenAt.UTC()).Exec(ctx)
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
// repository's owner/name (case-insensitive, as the provider treats it), or
// nil when there is none.
func GetPRRecord(ctx context.Context, db orm.Context, provider, repository string, number int) (*core.PRRecord, error) {
	var rows []prRecordRow
	err := db.Bun().NewRaw(`SELECT `+prRecordColumns+` FROM pr_records
		WHERE provider = ? AND lower(repository) = lower(?) AND number = ?
		ORDER BY observed_at DESC LIMIT 1`, provider, repository, number).Scan(ctx, &rows)
	if err != nil {
		return nil, fmt.Errorf("get pr record: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rows[0].core()
}

// ListPRRecords lists one repository's records in number order.
func ListPRRecords(ctx context.Context, db orm.Context, f core.PRRecordFilter) ([]*core.PRRecord, error) {
	if f.Limit <= 0 {
		return nil, nil
	}
	var rows []prRecordRow
	err := db.Bun().NewRaw(`SELECT `+prRecordColumns+` FROM pr_records
		WHERE provider = ?0 AND lower(repository) = lower(?1) AND (?2 = '' OR state = ?2) AND number > ?3
		ORDER BY number LIMIT ?4`, f.Provider, f.Repository, f.State, f.AfterNumber, f.Limit).Scan(ctx, &rows)
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
	err := db.Bun().NewRaw(`SELECT id, record_id, head_sha, previous_head_sha, first_seen_at, superseded_at
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
