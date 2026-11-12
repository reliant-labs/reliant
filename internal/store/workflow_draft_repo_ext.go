// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// The workflow_drafts queries (P5b lane L2): user-owned workflow definitions,
// available across all projects (project workflows come from
// .reliant/workflows/*.yaml and are never stored). A workflow is "usable" —
// offered in the agent selector, loadable at runtime — when its status is
// complete and it is not hidden; validity is never stored, it is computed on
// read and re-checked at run start.
//
// Every write bumps version, the optimistic-concurrency counter callers
// compare. workflow_drafts.chat_id is retired: nothing here writes it, and
// no core type carries it.
package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/uptrace/bun"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// newWorkflowDraftRow is the row a new draft is written as, at version 1.
// An unset status is a draft: an unset status must never make a workflow
// runnable (the column DEFAULT says the same).
func newWorkflowDraftRow(d *core.WorkflowDraft) *WorkflowDraft {
	return &WorkflowDraft{
		ID:          d.ID,
		UserID:      d.UserID,
		OrgID:       d.OrgID,
		Name:        d.Name,
		Slug:        d.Slug,
		Description: d.Description,
		Definition:  d.Definition,
		SourcePath:  d.SourcePath,
		ForkedFrom:  d.ForkedFrom,
		IsHidden:    d.IsHidden,
		CreatedAt:   d.CreatedAt,
		UpdatedAt:   d.UpdatedAt,
		Version:     1,
		Status:      d.Status,
	}
}

// InsertWorkflowDraft writes a new draft at version 1, with the caller's
// created_at and updated_at.
func InsertWorkflowDraft(ctx context.Context, db orm.Context, d *core.WorkflowDraft) error {
	return CreateWorkflowDraft(ctx, db, newWorkflowDraftRow(d))
}

// UpsertWorkflowDraftBySlug writes d, or, when the user already has a draft
// with its slug, updates that one's content in place and bumps its version.
// The existing row keeps its id, source_path, created_at and forked_from (an
// upsert must not rewrite a workflow's origin). Returns the stored row.
//
// It overwrites only the draft whose id is over ("": none), the row its
// caller locked and authorized (LockWorkflowDraftBySlug) under the owner's
// org lock: a conflict with any other row writes nothing and is
// ErrWorkflowDraftChanged.
func UpsertWorkflowDraftBySlug(ctx context.Context, db orm.Context, d *core.WorkflowDraft, over string) (*core.WorkflowDraft, error) {
	row := newWorkflowDraftRow(d)
	res, err := db.Bun().NewInsert().Model(row).
		ExcludeColumn(WorkflowDraftFieldChatID).
		On("CONFLICT (user_id, slug) DO UPDATE").
		Set("name = EXCLUDED.name").
		Set("description = EXCLUDED.description").
		Set("definition = EXCLUDED.definition").
		Set("status = EXCLUDED.status").
		Set("is_hidden = EXCLUDED.is_hidden").
		Set("updated_at = NOW()").
		Set("version = workflow_drafts.version + 1").
		Where("workflow_drafts.id = ?", over).
		Returning("?TableColumns").
		Exec(ctx)
	if err != nil {
		return nil, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return nil, err
	} else if n == 0 {
		return nil, ErrWorkflowDraftChanged
	}
	return workflowDraftToCore(row), nil
}

// ErrWorkflowDraftChanged refuses a draft write whose row is not the one its
// caller locked and authorized.
var ErrWorkflowDraftChanged = svcerr.Aborted("the workflow changed while it was being saved; try again")

// LockWorkflowDraftBySlug row-locks the user's draft with slug for the rest
// of the surrounding transaction and returns it, or nil when there is none.
// Its caller holds the owner's org lock first (LockUserOrgs).
func LockWorkflowDraftBySlug(ctx context.Context, db orm.Context, userID, slug string) (*core.WorkflowDraft, error) {
	return findWorkflowDraft(ctx, db, func(q *bun.SelectQuery) *bun.SelectQuery {
		return q.Where("user_id = ?", userID).Where("slug = ?", slug).For("UPDATE")
	})
}

// LockWorkflowDraft row-locks the draft with id and returns it; a miss is
// NotFound. Its caller holds the owner's org lock first (LockUserOrgs).
func LockWorkflowDraft(ctx context.Context, db orm.Context, id string) (*core.WorkflowDraft, error) {
	d, err := findWorkflowDraft(ctx, db, func(q *bun.SelectQuery) *bun.SelectQuery { return q.Where("id = ?", id).For("UPDATE") })
	if err == nil && d == nil {
		return nil, missing("workflow draft")
	}
	return d, err
}

// WorkflowDraftOwner is the user who owns the draft with id; a miss is
// NotFound. The owner never changes, so it is read before the owner's org
// lock is taken.
func WorkflowDraftOwner(ctx context.Context, db orm.Context, id string) (string, error) {
	var owners []string
	if err := db.Bun().NewRaw(`SELECT user_id FROM workflow_drafts WHERE id = ?`, id).Scan(ctx, &owners); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if len(owners) == 0 {
		return "", missing("workflow draft")
	}
	return owners[0], nil
}

// LoadWorkflowDraft reads one draft by id; a miss is sql.ErrNoRows.
func LoadWorkflowDraft(ctx context.Context, db orm.Context, id string) (*core.WorkflowDraft, error) {
	d, err := findWorkflowDraft(ctx, db, func(q *bun.SelectQuery) *bun.SelectQuery { return q.Where("id = ?", id) })
	if err == nil && d == nil {
		return nil, missing("workflow draft")
	}
	return d, err
}

// FindWorkflowDraftBySlug is the user's draft with slug, or nil.
func FindWorkflowDraftBySlug(ctx context.Context, db orm.Context, userID, slug string) (*core.WorkflowDraft, error) {
	return findWorkflowDraft(ctx, db, func(q *bun.SelectQuery) *bun.SelectQuery {
		return q.Where("user_id = ?", userID).Where("slug = ?", slug)
	})
}

// FindWorkflowDraftByName is the user's draft whose name matches, ignoring
// case, or nil: the duplicate-name check, distinct from the slug check.
func FindWorkflowDraftByName(ctx context.Context, db orm.Context, userID, name string) (*core.WorkflowDraft, error) {
	return findWorkflowDraft(ctx, db, func(q *bun.SelectQuery) *bun.SelectQuery {
		return q.Where("user_id = ?", userID).Where("LOWER(name) = LOWER(?)", name)
	})
}

// FindUsableWorkflowBySlug is the user's draft with slug if it is usable —
// complete and not hidden — or nil, for runtime loading.
func FindUsableWorkflowBySlug(ctx context.Context, db orm.Context, userID, slug string) (*core.WorkflowDraft, error) {
	return findWorkflowDraft(ctx, db, func(q *bun.SelectQuery) *bun.SelectQuery {
		return q.Where("user_id = ?", userID).Where("slug = ?", slug).
			Where("status = ?", core.WorkflowDraftStatusComplete).
			Where("NOT is_hidden")
	})
}

// findWorkflowDraft is the one row the predicate selects, or nil. Every
// lookup it serves is on a unique key.
func findWorkflowDraft(ctx context.Context, db orm.Context, where func(*bun.SelectQuery) *bun.SelectQuery) (*core.WorkflowDraft, error) {
	row := new(WorkflowDraft)
	err := where(db.Bun().NewSelect().Model(row)).Limit(1).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return workflowDraftToCore(row), nil
}

// ListUserWorkflowDrafts is every draft of a user, most recently updated
// first.
func ListUserWorkflowDrafts(ctx context.Context, db orm.Context, userID string) ([]*core.WorkflowDraft, error) {
	var rows []*WorkflowDraft
	if err := db.Bun().NewSelect().Model(&rows).
		Where("user_id = ?", userID).
		Order("updated_at DESC").
		Scan(ctx); err != nil {
		return nil, err
	}
	out := make([]*core.WorkflowDraft, len(rows))
	for i, row := range rows {
		out[i] = workflowDraftToCore(row)
	}
	return out, nil
}

// WorkflowSlugExists reports whether the user has a draft with slug.
func WorkflowSlugExists(ctx context.Context, db orm.Context, userID, slug string) (bool, error) {
	return db.Bun().NewSelect().Model((*WorkflowDraft)(nil)).
		Where("user_id = ?", userID).Where("slug = ?", slug).
		Exists(ctx)
}

// updateWorkflowDraft applies set to draft id, bumping its version and
// updated_at, and returns the stored row; a missing draft is sql.ErrNoRows.
func updateWorkflowDraft(ctx context.Context, db orm.Context, id string, set func(*bun.UpdateQuery) *bun.UpdateQuery) (*core.WorkflowDraft, error) {
	row := new(WorkflowDraft)
	err := set(db.Bun().NewUpdate().Model(row)).
		Set("updated_at = NOW()").
		Set("version = version + 1").
		Where("id = ?", id).
		Returning("?TableColumns").
		Scan(ctx)
	if err != nil {
		return nil, err
	}
	return workflowDraftToCore(row), nil
}

// ReplaceWorkflowDraft writes d's editable fields — name, slug,
// description, definition, status, hidden — over draft d.ID.
func ReplaceWorkflowDraft(ctx context.Context, db orm.Context, d *core.WorkflowDraft) error {
	_, err := updateWorkflowDraft(ctx, db, d.ID, func(q *bun.UpdateQuery) *bun.UpdateQuery {
		return q.Set("name = ?", d.Name).
			Set("slug = ?", d.Slug).
			Set("description = ?", d.Description).
			Set("definition = ?", d.Definition).
			Set("status = ?", draftStatus(d.Status)).
			Set("is_hidden = ?", d.IsHidden)
	})
	return err
}

// SetWorkflowDraftDefinition writes a draft's definition with the name, slug
// and status derived from it.
func SetWorkflowDraftDefinition(ctx context.Context, db orm.Context, id, name, slug, definition string, status core.WorkflowDraftStatus) error {
	_, err := updateWorkflowDraft(ctx, db, id, func(q *bun.UpdateQuery) *bun.UpdateQuery {
		return q.Set("name = ?", name).
			Set("slug = ?", slug).
			Set("definition = ?", definition).
			Set("status = ?", draftStatus(status))
	})
	return err
}

// SetWorkflowDraftStatus moves a draft between draft and complete. The
// caller validates before marking it complete; this only records the
// decision.
func SetWorkflowDraftStatus(ctx context.Context, db orm.Context, id string, status core.WorkflowDraftStatus) (*core.WorkflowDraft, error) {
	return updateWorkflowDraft(ctx, db, id, func(q *bun.UpdateQuery) *bun.UpdateQuery {
		return q.Set("status = ?", draftStatus(status))
	})
}

// SetWorkflowDraftHidden hides or shows a draft.
func SetWorkflowDraftHidden(ctx context.Context, db orm.Context, id string, hidden bool) (*core.WorkflowDraft, error) {
	return updateWorkflowDraft(ctx, db, id, func(q *bun.UpdateQuery) *bun.UpdateQuery {
		return q.Set("is_hidden = ?", hidden)
	})
}

// SetWorkflowDraftForkedFrom records the origin a draft was forked from.
func SetWorkflowDraftForkedFrom(ctx context.Context, db orm.Context, id, forkedFrom string) (*core.WorkflowDraft, error) {
	return updateWorkflowDraft(ctx, db, id, func(q *bun.UpdateQuery) *bun.UpdateQuery {
		return q.Set("forked_from = ?", forkedFrom)
	})
}

// RemoveWorkflowDraft deletes the draft with id and returns it; a miss is
// NotFound.
func RemoveWorkflowDraft(ctx context.Context, db orm.Context, id string) (*core.WorkflowDraft, error) {
	return deleteWorkflowDraft(ctx, db, func(q *bun.DeleteQuery) *bun.DeleteQuery {
		return q.Where("id = ?", id)
	})
}

func deleteWorkflowDraft(ctx context.Context, db orm.Context, where func(*bun.DeleteQuery) *bun.DeleteQuery) (*core.WorkflowDraft, error) {
	row := new(WorkflowDraft)
	if err := where(db.Bun().NewDelete().Model(row)).Returning("?TableColumns").Scan(ctx); err != nil {
		return nil, classifyMissing(err, "workflow draft")
	}
	return workflowDraftToCore(row), nil
}

// draftStatus is the status a write stores: unset is a draft, never
// runnable.
func draftStatus(s core.WorkflowDraftStatus) core.WorkflowDraftStatus {
	if s == "" {
		return core.WorkflowDraftStatusDraft
	}
	return s
}

func workflowDraftToCore(row *WorkflowDraft) *core.WorkflowDraft {
	return &core.WorkflowDraft{
		ID:          row.ID,
		UserID:      row.UserID,
		OrgID:       row.OrgID,
		Name:        row.Name,
		Slug:        row.Slug,
		Description: row.Description,
		Definition:  row.Definition,
		Status:      row.Status,
		SourcePath:  row.SourcePath,
		ForkedFrom:  row.ForkedFrom,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
		IsHidden:    row.IsHidden,
		Version:     row.Version,
		Visibility:  visibilityOf(row.Visibility),
		SharedAt:    row.SharedAt,
	}
}

// WorkflowDraftExtra carries fields on WorkflowDraft that are NOT columns — computed
// values, transient state, anything derived rather than stored. WorkflowDraft is
// embedded on WorkflowDraftExtra (see workflow_draft_orm.go), so a field added here is a field on
// every WorkflowDraft value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type WorkflowDraftExtra struct {
}
