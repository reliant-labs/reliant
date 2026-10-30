// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for Worktree. The generated worktree_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: worktreeRepo (a *crud.Repo[Worktree]) and the
//     package-level GetWorktreeByID / ListWorktree / CreateWorktree / UpdateWorktree / DeleteWorktree
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     Worktree struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindWorktreeBy(ctx context.Context, db orm.Context, /* args */) ([]*Worktree, error) {
//		var rows []*Worktree
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("name = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveWorktrees(ctx context.Context, db orm.Context) ([]*Worktree, error) {
//		return ListWorktree(ctx, db, orm.WhereEq("name", true))
//	}
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/uptrace/bun"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// WorktreeExtra carries fields on Worktree that are NOT columns — computed
// values, transient state, anything derived rather than stored. Worktree is
// embedded on WorktreeExtra (see worktree_orm.go), so a field added here is a field on
// every Worktree value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type WorktreeExtra struct {
}

// worktrees.deleted_at is the ARCHIVE stamp, not a soft-delete tombstone
// (COMMENT ON COLUMN … 'forge:no-soft-delete'): an archived workspace is
// still a row callers read, update, unarchive and sweep, so no query here
// filters it out implicitly. One that wants live rows only says
// deleted_at IS NULL itself.

// liveNameIndex is the partial unique index that holds a worktree name for
// the project's unarchived rows.
const liveNameIndex = WorktreeConstraintProjectIDNameLiveKey

// nameTakenOr turns a violation of the live-name index into
// core.ErrWorktreeNameTaken and leaves any other error alone.
func nameTakenOr(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" && pg.ConstraintName == liveNameIndex {
		return core.ErrWorktreeNameTaken
	}
	return err
}

// InsertWorktree stores a new worktree with the timestamps the caller set.
// A builder insert rather than CreateWorktree, so the two columns only later
// writes set are left out and take their DEFAULTs. A second live worktree
// with the same name in the project is core.ErrWorktreeNameTaken.
func InsertWorktree(ctx context.Context, db orm.Context, w *core.Worktree) error {
	// cleanup_metadata and failure_reason are written only by the later
	// settle and sweep writes; a new row takes their column defaults.
	_, err := db.Bun().NewInsert().Model(worktreeFromCore(w)).
		ExcludeColumn(WorktreeFieldCleanupMetadata, WorktreeFieldFailureReason).
		Exec(ctx)
	return nameTakenOr(err)
}

// GetWorktree returns the worktree, archived or not; a missing one wraps
// core.ErrWorktreeNotFound.
func GetWorktree(ctx context.Context, db orm.Context, id string) (*core.Worktree, error) {
	w, err := firstWorktree(ctx, db, func(q *bun.SelectQuery) { q.Where("id = ?", id) })
	if err != nil {
		return nil, fmt.Errorf("failed to get worktree: %w", err)
	}
	if w == nil {
		return nil, fmt.Errorf("%w: %s", core.ErrWorktreeNotFound, id)
	}
	return w, nil
}

// GetWorktreeByPath returns a worktree, archived or not, at path.
func GetWorktreeByPath(ctx context.Context, db orm.Context, path string) (*core.Worktree, error) {
	w, err := firstWorktree(ctx, db, func(q *bun.SelectQuery) { q.Where("path = ?", path) })
	if err != nil {
		return nil, fmt.Errorf("failed to get worktree by path: %w", err)
	}
	if w == nil {
		return nil, fmt.Errorf("%w: %s", core.ErrWorktreeNotFound, path)
	}
	return w, nil
}

// GetWorktreeByIdempotencyKey returns the worktree a previous create already
// produced for this key, or nil when there is none. Scoped by project
// because the key is client-generated.
func GetWorktreeByIdempotencyKey(ctx context.Context, db orm.Context, projectID, key string) (*core.Worktree, error) {
	return firstWorktree(ctx, db, func(q *bun.SelectQuery) {
		q.Where("project_id = ?", projectID).Where("idempotency_key = ?", key)
	})
}

// GetLiveWorktreeByName returns the one unarchived worktree holding a name,
// or nil; worktrees_project_id_name_live_key guarantees there is at most
// one.
func GetLiveWorktreeByName(ctx context.Context, db orm.Context, projectID, name string) (*core.Worktree, error) {
	return firstWorktree(ctx, db, func(q *bun.SelectQuery) {
		q.Where("project_id = ?", projectID).Where("name = ?", name).Where("deleted_at IS NULL")
	})
}

// ListWorktreesByBranch returns every worktree, archived included, recorded
// against a branch: an archived row's checkout or branch may outlive the
// archive.
func ListWorktreesByBranch(ctx context.Context, db orm.Context, projectID, branch string) ([]*core.Worktree, error) {
	var rows []*Worktree
	err := db.Bun().NewSelect().Model(&rows).
		Where("project_id = ?", projectID).
		Where("branch = ?", branch).
		Scan(ctx)
	if err != nil {
		return nil, err
	}
	return worktreesToCore(rows), nil
}

// ListWorktrees lists live workspaces by default. IncludeArchived adds the
// archived ones after them; ArchivedOnly lists only the archived ones, most
// recently archived first; MainOnly narrows to the project's main
// workspace. A zero limit is 100.
func ListWorktrees(ctx context.Context, db orm.Context, f core.WorktreeFilters) ([]*core.Worktree, error) {
	limit := f.Limit
	if limit == 0 {
		limit = 100
	}
	var rows []*Worktree
	q := db.Bun().NewSelect().Model(&rows)
	if f.ProjectID != nil {
		q.Where("project_id = ?", *f.ProjectID)
	}
	if f.ChatID != nil {
		q.Where("chat_id = ?", *f.ChatID)
	}
	if f.Status != nil {
		q.Where("status = ?", *f.Status)
	}
	if f.MainOnly {
		q.Where("is_main")
	}
	switch {
	case f.ArchivedOnly:
		q.Where("deleted_at IS NOT NULL")
	case !f.IncludeArchived:
		q.Where("deleted_at IS NULL")
	}
	err := q.OrderExpr("(deleted_at IS NOT NULL), is_main DESC, COALESCE(deleted_at, last_active) DESC, id").
		Limit(int64(limit)).
		Offset(int64(f.Offset)).
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list worktrees: %w", err)
	}
	return worktreesToCore(rows), nil
}

// ListWorktreesByIDs returns the named worktrees, archived ones included: a
// chat's workspace being archived must not hide the chat.
func ListWorktreesByIDs(ctx context.Context, db orm.Context, ids []string) ([]*core.Worktree, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var rows []*Worktree
	err := db.Bun().NewSelect().Model(&rows).
		Where("id IN (?)", bun.List(ids)).
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list worktrees by id: %w", err)
	}
	return worktreesToCore(rows), nil
}

// ListMainWorktreesForUser returns the live main workspace of each project
// userID owns, or of the one project when projectID is set. Ownership is part
// of the query: a project id that is not the caller's yields nothing.
func ListMainWorktreesForUser(ctx context.Context, db orm.Context, userID string, projectID *string) ([]*core.Worktree, error) {
	var rows []*Worktree
	q := db.Bun().NewSelect().Model(&rows).
		Where("is_main").
		Where("deleted_at IS NULL").
		Where("project_id IN (SELECT id FROM projects WHERE user_id = ?)", userID)
	if projectID != nil {
		q = q.Where("project_id = ?", *projectID)
	}
	if err := q.OrderExpr("project_id, id").Scan(ctx); err != nil {
		return nil, fmt.Errorf("failed to list main worktrees: %w", err)
	}
	return worktreesToCore(rows), nil
}

// ReplaceWorktree rewrites a worktree's mutable columns, archived or not,
// and stamps updated_at. Path is among them because asynchronous creation
// only learns the on-disk location after the daemon reports it: the row is
// inserted as CREATING with an empty path and settled here — as is
// failure_reason, when it settles FAILED. A missing worktree is not an
// error.
func ReplaceWorktree(ctx context.Context, db orm.Context, w *core.Worktree) error {
	_, err := db.Bun().NewUpdate().Model(worktreeFromCore(w)).
		Column(WorktreeFieldName, WorktreeFieldBranch, WorktreeFieldStatus, WorktreeFieldBaseBranch,
			WorktreeFieldBaseBranches, WorktreeFieldLastActive, WorktreeFieldPath, WorktreeFieldFailureReason).
		Set("updated_at = NOW()").
		Where("id = ?", w.ID).
		Exec(ctx)
	return recoveryAdmissionError(err)
}

// SetWorktreeCleanupMetadata replaces a worktree's cleanup metadata (nil
// clears it) and stamps updated_at.
func SetWorktreeCleanupMetadata(ctx context.Context, db orm.Context, id string, metadata *core.CleanupMetadata) error {
	encoded, err := encodeCleanupMetadata(metadata)
	if err != nil {
		return err
	}
	_, err = db.Bun().NewUpdate().Model((*Worktree)(nil)).
		Set("cleanup_metadata = ?", encoded).
		Set("updated_at = NOW()").
		Where("id = ?", id).
		Exec(ctx)
	return err
}

// MergeWorktreeCleanupMetadata is a read-modify-write of one row's
// cleanup_metadata under SELECT ... FOR UPDATE, so settle, sweep and clean-up
// running on different replicas never lose each other's updates. fn gets the
// stored metadata and whether the row is archived; it returns false to write
// nothing.
//
// The row lock is the whole guarantee, so the transaction is READ COMMITTED,
// as the hand SQL's BeginTx(nil) was (internal/db/SCHEMA.md rule 7): a merge
// queued behind another writer's lock then reads that writer's committed
// value. Under the ORM's SERIALIZABLE default the same read fails with 40001,
// and under contention the retries run out and an update is lost
// (TestMergeWorktreeCleanupMetadataLoserReadsTheWinnersWrite). fn may still
// run more than once — a deadlock (40P01) is retried — each time on a fresh
// copy of the stored value.
func MergeWorktreeCleanupMetadata(ctx context.Context, db orm.Context, id string, fn func(m *core.CleanupMetadata, archived bool) bool) error {
	return db.RunTx(ctx, orm.ReadCommitted.WithMaxAttempts(orm.DefaultTxMaxAttempts), func(ctx context.Context) error {
		var row Worktree
		err := db.Bun().NewSelect().Model(&row).
			Column(WorktreeFieldCleanupMetadata, WorktreeFieldDeletedAt).
			Where("id = ?", id).
			For("UPDATE").
			Scan(ctx)
		if err != nil {
			return err
		}
		meta := core.CleanupMetadata{}
		if row.CleanupMetadata != nil && *row.CleanupMetadata != "" {
			_ = json.Unmarshal([]byte(*row.CleanupMetadata), &meta)
		}
		if !fn(&meta, row.DeletedAt != nil) {
			return nil
		}
		return SetWorktreeCleanupMetadata(ctx, db, id, &meta)
	})
}

// cleanupMetadataPending is true while a row's directory may still be on
// disk: it has no cleanup metadata saying directory_deleted. The jsonb cast
// sits inside a CASE so a hand-edited value cannot fail the whole listing.
const cleanupMetadataPending = `(CASE WHEN worktrees.cleanup_metadata IS NULL OR worktrees.cleanup_metadata = '' THEN false
	ELSE COALESCE((worktrees.cleanup_metadata::jsonb ->> 'directory_deleted')::boolean, false) END) = false`

// ListWorktreesForReclaim returns every workspace whose directory a daemon
// may still hold: not main, has a path, and not already known to be
// removed. CREATING and FAILED rows have no directory yet. Archived rows are
// the ones to remove; the rest are locked as reliant-owned. owner_user_id
// lets the sweep find the daemon's connection. Unbounded.
func ListWorktreesForReclaim(ctx context.Context, db orm.Context) ([]*core.ReclaimCandidate, error) {
	var rows []worktreeWithProject
	err := db.Bun().NewSelect().Model(&rows).
		ColumnExpr("?TableColumns").
		ColumnExpr("p.user_id AS owner_user_id").
		ColumnExpr("p.path AS project_path").
		Join("JOIN projects AS p ON p.id = worktrees.project_id").
		Where("worktrees.is_main = false").
		Where("worktrees.path <> ''").
		Where("worktrees.status NOT IN (?, ?)",
			reliantv1.WorktreeStatus_WORKTREE_STATUS_CREATING, reliantv1.WorktreeStatus_WORKTREE_STATUS_FAILED).
		Where(cleanupMetadataPending).
		OrderExpr("worktrees.last_active DESC").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list worktrees for reclaim: %w", err)
	}
	out := make([]*core.ReclaimCandidate, len(rows))
	for i := range rows {
		out[i] = &core.ReclaimCandidate{
			Worktree:    worktreeToCore(&rows[i].Worktree),
			OwnerUserID: rows[i].OwnerUserID,
			ProjectPath: rows[i].ProjectPath,
		}
	}
	return out, nil
}

// ListHeldWorktreesForUser returns the user's archived workspaces the daemon
// declined to remove on its own (a held_reason, directory still there),
// oldest archive first, for the storage inbox item.
func ListHeldWorktreesForUser(ctx context.Context, db orm.Context, userID string) ([]*core.HeldWorktree, error) {
	var rows []worktreeWithProject
	err := db.Bun().NewSelect().Model(&rows).
		ColumnExpr("?TableColumns").
		ColumnExpr("p.name AS project_name").
		ColumnExpr("p.path AS project_path").
		Join("JOIN projects AS p ON p.id = worktrees.project_id").
		Where("p.user_id = ?", userID).
		Where("worktrees.is_main = false").
		Where("worktrees.deleted_at IS NOT NULL").
		Where(`(CASE WHEN worktrees.cleanup_metadata IS NULL OR worktrees.cleanup_metadata = '' THEN ''
			ELSE COALESCE(worktrees.cleanup_metadata::jsonb ->> 'held_reason', '') END) <> ''`).
		Where(cleanupMetadataPending).
		OrderExpr("worktrees.deleted_at").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list held worktrees: %w", err)
	}
	out := make([]*core.HeldWorktree, len(rows))
	for i := range rows {
		out[i] = &core.HeldWorktree{
			Worktree:    worktreeToCore(&rows[i].Worktree),
			ProjectName: rows[i].ProjectName,
			ProjectPath: rows[i].ProjectPath,
		}
	}
	return out, nil
}

// worktreeWithProject is a worktree row plus the columns of its project the
// sweep's listings join in.
type worktreeWithProject struct {
	Worktree    `bun:",extend"`
	OwnerUserID string `bun:"owner_user_id,scanonly"`
	ProjectName string `bun:"project_name,scanonly"`
	ProjectPath string `bun:"project_path,scanonly"`
}

// AdoptWorktreeDaemon records the daemon that found a workspace's directory
// on its disk, which proves it owns it. Only a row created before daemon_id
// was recorded is filled in, once.
//
// The same statement moves the chats in that workspace that are pinned to
// another of their owner's machines onto the owner, as worktree_owner_backfill
// does for owners recorded before it ran: a chat's tools run where its
// checkout is, and a pin naming another machine is one StartChat and
// SetChatDaemon refuse today. Every reader of a chat's pin (tool routing,
// queued delivery, the machine's catalog) then agrees with the owner. A
// no-machine chat has no pin and is untouched; updated_at does not move.
//
// It returns the owner the row records afterwards, which is daemonID only
// when this call won: two resolvers (separate services or API replicas) can
// nominate different machines for one row, and only the persisted one may be
// routed to. The fallback read is its own statement, so under READ COMMITTED
// it sees a concurrent winner that committed while the UPDATE waited on the
// row. "" means the row is gone.
func AdoptWorktreeDaemon(ctx context.Context, db orm.Context, id, daemonID string) (string, error) {
	var adopted []string
	err := db.Bun().NewRaw(`
WITH adopted AS (
    UPDATE worktrees SET daemon_id = ?
     WHERE id = ? AND daemon_id IS NULL
    RETURNING id, daemon_id
), moved AS (
    UPDATE chats c SET active_daemon_id = a.daemon_id
      FROM adopted a
      JOIN daemons d ON d.id = a.daemon_id
     WHERE c.worktree_id = a.id
       AND d.user_id = c.user_id
       AND c.active_daemon_id IS NOT NULL
       AND c.active_daemon_id <> a.daemon_id
       AND NOT c.no_machine
    RETURNING c.id
)
SELECT daemon_id FROM adopted`, daemonID, id).Scan(ctx, &adopted)
	if err != nil {
		return "", err
	}
	if len(adopted) == 1 {
		return adopted[0], nil
	}
	var owner []*string
	if err := db.Bun().NewSelect().Model((*Worktree)(nil)).
		Column("daemon_id").
		Where("id = ?", id).
		Scan(ctx, &owner); err != nil {
		return "", err
	}
	if len(owner) == 0 || owner[0] == nil {
		return "", nil
	}
	return *owner[0], nil
}

// ListLiveWorktreePathsForUser returns every path one user's unarchived
// worktree rows claim, with the project that says where each row's checkouts
// sit. Scoped by user because another tenant's rows say nothing about this
// user's directories.
func ListLiveWorktreePathsForUser(ctx context.Context, db orm.Context, userID string) ([]core.WorktreePath, error) {
	var rows []*Worktree
	err := db.Bun().NewSelect().Model(&rows).
		Column(WorktreeFieldID, WorktreeFieldPath, WorktreeFieldProjectID).
		Join("JOIN projects AS p ON p.id = worktrees.project_id").
		Where("p.user_id = ?", userID).
		Where("worktrees.deleted_at IS NULL").
		Where("worktrees.path <> ''").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list live worktree paths: %w", err)
	}
	out := make([]core.WorktreePath, len(rows))
	for i, r := range rows {
		out[i] = core.WorktreePath{ID: r.ID, Path: r.Path, ProjectID: r.ProjectID}
	}
	return out, nil
}

// RemoveWorktree hard-deletes the worktree row, archived or not; a missing
// one is not an error.
func RemoveWorktree(ctx context.Context, db orm.Context, id string) error {
	_, err := db.Bun().NewDelete().Model((*Worktree)(nil)).
		Where("id = ?", id).
		Exec(ctx)
	return recoveryAdmissionError(err)
}

// ArchiveWorktree stamps the worktree archived as of now.
func ArchiveWorktree(ctx context.Context, db orm.Context, id string) error {
	_, err := db.Bun().NewUpdate().Model((*Worktree)(nil)).
		Set("deleted_at = NOW()").
		Set("updated_at = NOW()").
		Where("id = ?", id).
		Exec(ctx)
	return recoveryAdmissionError(err)
}

// UnarchiveWorktree makes the worktree live again; a live worktree that
// already holds its name is core.ErrWorktreeNameTaken.
func UnarchiveWorktree(ctx context.Context, db orm.Context, id string) error {
	_, err := db.Bun().NewUpdate().Model((*Worktree)(nil)).
		Set("deleted_at = NULL").
		Set("updated_at = NOW()").
		Where("id = ?", id).
		Exec(ctx)
	return nameTakenOr(recoveryAdmissionError(err))
}

// firstWorktree returns the first worktree, archived or not, the filter
// matches, or nil when none does.
func firstWorktree(ctx context.Context, db orm.Context, filter func(*bun.SelectQuery)) (*core.Worktree, error) {
	var rows []*Worktree
	q := db.Bun().NewSelect().Model(&rows).Limit(1)
	filter(q)
	if err := q.Scan(ctx); err != nil || len(rows) == 0 {
		return nil, err
	}
	return worktreeToCore(rows[0]), nil
}

func worktreeFromCore(w *core.Worktree) *Worktree {
	status := reliantv1.WorktreeStatus(w.Status)
	return &Worktree{
		ID:             w.ID,
		Name:           w.Name,
		Path:           w.Path,
		Branch:         w.Branch,
		BaseBranch:     w.BaseBranch,
		BaseBranches:   encodeBaseBranches(w.BaseBranches),
		ProjectID:      w.ProjectID,
		ChatID:         w.ChatID,
		DaemonID:       w.DaemonID,
		Status:         &status,
		IdempotencyKey: w.IdempotencyKey,
		IsMain:         w.IsMain,
		CreatedAt:      w.CreatedAt,
		UpdatedAt:      w.UpdatedAt,
		LastActive:     w.LastActive,
		DeletedAt:      w.DeletedAt,
		FailureReason:  w.FailureReason,
	}
}

func worktreeToCore(row *Worktree) *core.Worktree {
	var cleanupMetadata *core.CleanupMetadata
	if row.CleanupMetadata != nil {
		var parsed core.CleanupMetadata
		if err := json.Unmarshal([]byte(*row.CleanupMetadata), &parsed); err == nil {
			cleanupMetadata = &parsed
		}
	}
	var status int32
	if row.Status != nil {
		status = int32(*row.Status)
	}
	return &core.Worktree{
		ID:                   row.ID,
		Name:                 row.Name,
		Path:                 row.Path,
		Branch:               row.Branch,
		BaseBranch:           row.BaseBranch,
		BaseBranches:         decodeBaseBranches(row.BaseBranches),
		ProjectID:            row.ProjectID,
		ChatID:               row.ChatID,
		DaemonID:             row.DaemonID,
		Status:               status,
		IdempotencyKey:       row.IdempotencyKey,
		IsMain:               row.IsMain,
		CreatedAt:            row.CreatedAt,
		UpdatedAt:            row.UpdatedAt,
		LastActive:           row.LastActive,
		DeletedAt:            row.DeletedAt,
		CleanupMetadata:      cleanupMetadata,
		FailureReason:        row.FailureReason,
		ActivityGeneration:   row.ActivityGeneration,
		RecoveryFinalizingID: row.RecoveryFinalizingID,
	}
}

func worktreesToCore(rows []*Worktree) []*core.Worktree {
	out := make([]*core.Worktree, len(rows))
	for i, row := range rows {
		out[i] = worktreeToCore(row)
	}
	return out
}

// encodeBaseBranches stores a per-repo base-branch map as JSON; an empty map
// is NULL. A map[string]string always marshals.
func encodeBaseBranches(m map[string]string) *string {
	if len(m) == 0 {
		return nil
	}
	encoded, _ := json.Marshal(m)
	s := string(encoded)
	return &s
}

func decodeBaseBranches(s *string) map[string]string {
	if s == nil || *s == "" {
		return nil
	}
	var parsed map[string]string
	if err := json.Unmarshal([]byte(*s), &parsed); err != nil {
		return nil
	}
	return parsed
}

// encodeCleanupMetadata stores cleanup metadata as JSON; nil is NULL.
func encodeCleanupMetadata(m *core.CleanupMetadata) (*string, error) {
	if m == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal cleanup metadata: %w", err)
	}
	s := string(encoded)
	return &s, nil
}
