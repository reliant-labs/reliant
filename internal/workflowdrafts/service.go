// yours: scaffolded once, never touched again — forge will not overwrite this file
// Service implementation; canonical pattern in `forge skill load service-layer`.
package workflowdrafts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/pkg/config"
)

// ErrSlugTaken reports that the owner already has a workflow with the slug.
// It is deliberately not retryable: the conflict is permanent, not a race.
var ErrSlugTaken = svcerr.AlreadyExists("workflow slug already taken")

// ErrVersionConflict reports that a draft was written since the version the
// caller read: its write was computed from content that is no longer
// current, so it was not applied.
var ErrVersionConflict = svcerr.Aborted("workflow draft changed since it was read")

// Deps holds dependencies for the workflowdrafts package.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config

	Tx      TxRunner
	Locks   OrgLocks
	Updates UserUpdates
	Drafts  Drafts
}

// txPolicy is the level every draft write runs at: each is one single-row
// write plus the user_updates insert, which allocates from the per-user
// counter row and so needs READ COMMITTED (under SERIALIZABLE it would abort
// against any concurrent update for the same user). The Repo's ladder still
// re-runs it on the failures it retries.
var txPolicy = orm.ReadCommitted.WithMaxAttempts(orm.DefaultTxMaxAttempts)

// The package is the one writer of workflow_drafts and the one reader that
// decides what its caller may see of a row: every read is scoped to the
// owner's live org membership (authz.CanReadWorkflow) and every write
// re-decides on the row it locked, under the owner's org lock
// (authz.CanWriteWorkflow). A handler that reads the store directly skips
// both.

//forge:contract
type service struct {
	deps Deps
}

// New creates a new workflowdrafts service instance.
//
// forge:constructor
func New(deps Deps) (Service, error) {
	if deps.Tx == nil || deps.Locks == nil || deps.Updates == nil || deps.Drafts == nil {
		return nil, errors.New("workflowdrafts: Deps.Tx, Locks, Updates and Drafts are required")
	}
	return &service{deps: deps}, nil
}

// mapSlugConflict turns a unique violation on workflow_drafts into
// ErrSlugTaken. The PgError is not wrapped (%w) so the retry ladder does not
// see 23505 and walk it for a permanent conflict.
func mapSlugConflict(err error, slug string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "workflow_drafts_user_id_slug_key" {
		return fmt.Errorf("%w: %q", ErrSlugTaken, slug)
	}
	return err
}

func (s *service) run(ctx context.Context, f func(txCtx context.Context) error) error {
	return s.deps.Tx.RunTx(ctx, txPolicy, f)
}

// publish announces a draft write to the owner's open editors. It runs
// inside the write's transaction: a rolled-back write never announces
// itself. deleted marks the draft's removal; the version is then the last.
func (s *service) publish(ctx context.Context, draft *core.WorkflowDraft, deleted bool) error {
	if draft == nil {
		return nil
	}
	payload := map[string]any{"draft_id": draft.ID, "slug": draft.Slug, "version": draft.Version}
	if deleted {
		payload["deleted"] = true
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal workflow draft update: %w", err)
	}
	return s.deps.Updates.CreateUserUpdate(ctx, &core.UserUpdate{
		UserID:     draft.UserID,
		UpdateType: reliantv1.UserUpdateType_USER_UPDATE_TYPE_WORKFLOW_DRAFT_UPDATED,
		EntityType: reliantv1.EntityType_ENTITY_TYPE_WORKFLOW_DRAFT,
		EntityID:   draft.ID,
		Data:       data,
	})
}

func (s *service) publishByID(ctx context.Context, id string) error {
	row, err := s.deps.Drafts.LoadWorkflowDraft(ctx, id)
	if err != nil {
		return err
	}
	return s.publish(ctx, row, false)
}

// lockWritable takes the draft's owner's org lock, then its row, and decides
// on the locked row: it may be written only while its owner may still change
// it. A denied draft is NotFound, as a missing one.
func (s *service) lockWritable(ctx context.Context, id string) (*core.WorkflowDraft, error) {
	owner, err := s.deps.Drafts.WorkflowDraftOwner(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.deps.Locks.LockUserOrgs(ctx, owner); err != nil {
		return nil, err
	}
	row, err := s.deps.Drafts.LockWorkflowDraft(ctx, id)
	if err != nil {
		return nil, err
	}
	if !authz.CanWriteWorkflow(ctx, owner, row) {
		return nil, svcerr.NotFound("workflow draft")
	}
	return row, nil
}

// visible confirms a draft an owner-filtered read returned through authz: a
// draft of an org its owner has left reads as no draft, so no lookup by slug
// or name can reach what the lookup by id refuses.
func visible(ctx context.Context, userID string, draft *core.WorkflowDraft, err error) (*core.WorkflowDraft, error) {
	if err != nil || draft == nil {
		return draft, err
	}
	if !authz.CanReadWorkflow(ctx, userID, draft) {
		return nil, nil
	}
	return draft, nil
}

// notRunnable is the answer when no usable workflow has the slug and draft
// (visible to the caller, or nil) does: a visible draft that is not complete
// is reported, anything else is nothing.
func notRunnable(slug string, draft *core.WorkflowDraft) error {
	if draft != nil && !draft.IsHidden && draft.Status != core.WorkflowDraftStatusComplete {
		return &core.WorkflowDraftNotRunnableError{Slug: slug}
	}
	return nil
}

// CreateWorkflowDraft inserts a new draft at version 1 under its owner's org
// lock, so inserts of one user's slugs serialize.
func (s *service) CreateWorkflowDraft(ctx context.Context, draft *core.WorkflowDraft) error {
	if draft == nil {
		return fmt.Errorf("draft cannot be nil")
	}
	return s.run(ctx, func(txCtx context.Context) error {
		if err := s.deps.Locks.LockUserOrgs(txCtx, draft.UserID); err != nil {
			return err
		}
		if err := s.deps.Drafts.InsertWorkflowDraft(txCtx, draft); err != nil {
			return mapSlugConflict(err, draft.Slug)
		}
		return s.publishByID(txCtx, draft.ID)
	})
}

// UpsertWorkflowDraft writes the draft, or updates the content of the
// caller's draft with its slug. Under the owner's org lock the slug's row is
// locked and decided on as it is now; a denied draft's slug is taken, the
// upsert never overwrites it.
func (s *service) UpsertWorkflowDraft(ctx context.Context, draft *core.WorkflowDraft) (*core.WorkflowDraft, error) {
	if draft == nil {
		return nil, fmt.Errorf("draft cannot be nil")
	}
	var saved *core.WorkflowDraft
	err := s.run(ctx, func(txCtx context.Context) error {
		if err := s.deps.Locks.LockUserOrgs(txCtx, draft.UserID); err != nil {
			return err
		}
		existing, err := s.deps.Drafts.LockWorkflowDraftBySlug(txCtx, draft.UserID, draft.Slug)
		if err != nil {
			return err
		}
		over := ""
		if existing != nil {
			if !authz.CanWriteWorkflow(txCtx, draft.UserID, existing) {
				return fmt.Errorf("%w: %q", ErrSlugTaken, draft.Slug)
			}
			over = existing.ID
		}
		row, err := s.deps.Drafts.UpsertWorkflowDraftBySlug(txCtx, draft, over)
		if err != nil {
			return mapSlugConflict(err, draft.Slug)
		}
		saved = row
		return s.publish(txCtx, row, false)
	})
	if err != nil {
		return nil, err
	}
	return saved, nil
}

// GetWorkflowDraft reads one draft by id. It does not scope to a caller: the
// handler that holds a user decides with authz.CanReadWorkflow or
// CanWriteWorkflow.
func (s *service) GetWorkflowDraft(ctx context.Context, id string) (*core.WorkflowDraft, error) {
	row, err := s.deps.Drafts.LoadWorkflowDraft(ctx, id)
	if err != nil {
		if svcerr.IsNotFound(err) {
			return nil, fmt.Errorf("%w: %s", svcerr.NotFound("workflow draft"), id)
		}
		return nil, err
	}
	return row, nil
}

// GetWorkflowDraftBySlug is the user's draft with slug, or nil: a draft of an
// org the user has left is nil.
func (s *service) GetWorkflowDraftBySlug(ctx context.Context, userID, slug string) (*core.WorkflowDraft, error) {
	draft, err := s.deps.Drafts.FindWorkflowDraftBySlug(ctx, userID, slug)
	return visible(ctx, userID, draft, err)
}

// GetWorkflowDraftByName is the user's draft whose name matches, ignoring
// case, or nil: a draft of an org the user has left is nil.
func (s *service) GetWorkflowDraftByName(ctx context.Context, userID, name string) (*core.WorkflowDraft, error) {
	draft, err := s.deps.Drafts.FindWorkflowDraftByName(ctx, userID, name)
	return visible(ctx, userID, draft, err)
}

// GetUsableWorkflowBySlug returns the runnable (complete, visible) workflow
// with this slug, or (nil, nil) when there is none. A visible DRAFT with the
// slug is reported as *core.WorkflowDraftNotRunnableError rather than hidden
// behind "not found": the workflow exists, and the fix is to mark it
// complete.
func (s *service) GetUsableWorkflowBySlug(ctx context.Context, userID, slug string) (*core.WorkflowDraft, error) {
	usable, err := s.deps.Drafts.FindUsableWorkflowBySlug(ctx, userID, slug)
	if err != nil {
		return nil, err
	}
	if usable != nil {
		return visible(ctx, userID, usable, nil)
	}
	draft, err := s.GetWorkflowDraftBySlug(ctx, userID, slug)
	if err != nil {
		return nil, err
	}
	return nil, notRunnable(slug, draft)
}

// DraftsBySlug is one user's drafts of a set of slugs, read in one query
// (GetWorkflowDraftsBySlugs). For each slug it was read for, it gives exactly
// what GetWorkflowDraftBySlug and GetUsableWorkflowBySlug give, so a reader
// that resolves many workflows asks once instead of twice per name.
type DraftsBySlug struct {
	userID string
	bySlug map[string]*core.WorkflowDraft
}

// Draft is GetWorkflowDraftBySlug's answer for slug, which must be one of the
// slugs b was read for.
func (b *DraftsBySlug) Draft(ctx context.Context, slug string) (*core.WorkflowDraft, error) {
	return visible(ctx, b.userID, b.bySlug[slug], nil)
}

// Usable is GetUsableWorkflowBySlug's answer for slug, which must be one of
// the slugs b was read for. The usable row is the slug's one row when it is
// complete and not hidden.
func (b *DraftsBySlug) Usable(ctx context.Context, slug string) (*core.WorkflowDraft, error) {
	row := b.bySlug[slug]
	if row != nil && row.Status == core.WorkflowDraftStatusComplete && !row.IsHidden {
		return visible(ctx, b.userID, row, nil)
	}
	draft, err := b.Draft(ctx, slug)
	if err != nil {
		return nil, err
	}
	return nil, notRunnable(slug, draft)
}

// GetWorkflowDraftsBySlugs reads userID's drafts of slugs in one query.
func (s *service) GetWorkflowDraftsBySlugs(ctx context.Context, userID string, slugs []string) (*DraftsBySlug, error) {
	rows, err := s.deps.Drafts.ListWorkflowDraftsBySlugs(ctx, userID, slugs)
	if err != nil {
		return nil, err
	}
	out := &DraftsBySlug{userID: userID, bySlug: make(map[string]*core.WorkflowDraft, len(rows))}
	for _, row := range rows {
		out.bySlug[row.Slug] = row
	}
	return out, nil
}

// ListWorkflowDraftsByUser is the drafts userID owns and may still see: a
// draft of an org they have left is not listed (authz decides each row).
func (s *service) ListWorkflowDraftsByUser(ctx context.Context, userID string) ([]*core.WorkflowDraft, error) {
	drafts, err := s.deps.Drafts.ListUserWorkflowDrafts(ctx, userID)
	if err != nil {
		return nil, err
	}
	return authz.Visible(ctx, userID, drafts, authz.CanReadWorkflow), nil
}

// UpdateWorkflowDraft rewrites a draft's editable fields.
func (s *service) UpdateWorkflowDraft(ctx context.Context, draft *core.WorkflowDraft) error {
	if draft == nil {
		return fmt.Errorf("draft cannot be nil")
	}
	return s.run(ctx, func(txCtx context.Context) error {
		if _, err := s.lockWritable(txCtx, draft.ID); err != nil {
			return err
		}
		if err := s.deps.Drafts.ReplaceWorkflowDraft(txCtx, draft); err != nil {
			return mapSlugConflict(err, draft.Slug)
		}
		return s.publishByID(txCtx, draft.ID)
	})
}

// UpdateWorkflowDraftDefinition writes a draft's definition with the name,
// slug and status derived from it.
func (s *service) UpdateWorkflowDraftDefinition(ctx context.Context, id string, name string, slug string, definition string, status core.WorkflowDraftStatus) error {
	return s.updateDefinition(ctx, id, nil, name, slug, definition, status)
}

// UpdateWorkflowDraftDefinitionAtVersion is UpdateWorkflowDraftDefinition
// applied only while the draft is still at version: a compare-and-swap under
// the draft's row lock. A draft written since returns ErrVersionConflict and
// is left as it is, so two edits computed from the same read cannot both
// report success while one is lost.
func (s *service) UpdateWorkflowDraftDefinitionAtVersion(ctx context.Context, id string, version int64, name string, slug string, definition string, status core.WorkflowDraftStatus) error {
	return s.updateDefinition(ctx, id, &version, name, slug, definition, status)
}

func (s *service) updateDefinition(ctx context.Context, id string, version *int64, name, slug, definition string, status core.WorkflowDraftStatus) error {
	return s.run(ctx, func(txCtx context.Context) error {
		row, err := s.lockWritable(txCtx, id)
		if err != nil {
			return err
		}
		if version != nil && row.Version != *version {
			return ErrVersionConflict
		}
		if err := s.deps.Drafts.SetWorkflowDraftDefinition(txCtx, id, name, slug, definition, status); err != nil {
			return mapSlugConflict(err, slug)
		}
		return s.publishByID(txCtx, id)
	})
}

// SetWorkflowDraftStatus moves a draft between draft and complete.
func (s *service) SetWorkflowDraftStatus(ctx context.Context, id string, status core.WorkflowDraftStatus) (*core.WorkflowDraft, error) {
	return s.setOne(ctx, id, func(txCtx context.Context) (*core.WorkflowDraft, error) {
		return s.deps.Drafts.SetWorkflowDraftStatus(txCtx, id, status)
	})
}

// SetWorkflowDraftHidden hides or shows a draft.
func (s *service) SetWorkflowDraftHidden(ctx context.Context, id string, isHidden bool) (*core.WorkflowDraft, error) {
	return s.setOne(ctx, id, func(txCtx context.Context) (*core.WorkflowDraft, error) {
		return s.deps.Drafts.SetWorkflowDraftHidden(txCtx, id, isHidden)
	})
}

// UpdateWorkflowForkedFrom records the origin a draft was forked from.
func (s *service) UpdateWorkflowForkedFrom(ctx context.Context, draftID string, forkedFrom string) (*core.WorkflowDraft, error) {
	return s.setOne(ctx, draftID, func(txCtx context.Context) (*core.WorkflowDraft, error) {
		return s.deps.Drafts.SetWorkflowDraftForkedFrom(txCtx, draftID, forkedFrom)
	})
}

// setOne is the shape of the by-id single-field writes: lock and decide on
// the row, write, announce.
func (s *service) setOne(ctx context.Context, id string, write func(txCtx context.Context) (*core.WorkflowDraft, error)) (*core.WorkflowDraft, error) {
	var saved *core.WorkflowDraft
	err := s.run(ctx, func(txCtx context.Context) error {
		if _, err := s.lockWritable(txCtx, id); err != nil {
			return err
		}
		row, err := write(txCtx)
		if err != nil {
			return err
		}
		saved = row
		return s.publish(txCtx, row, false)
	})
	if err != nil {
		return nil, err
	}
	return saved, nil
}

// DeleteWorkflowDraftBySlug deletes the user's draft with slug. Under the
// owner's org lock the slug's row is locked and decided on as it is now; a
// denied draft is deleted like a missing one: not at all.
func (s *service) DeleteWorkflowDraftBySlug(ctx context.Context, userID, slug string) error {
	return s.run(ctx, func(txCtx context.Context) error {
		if err := s.deps.Locks.LockUserOrgs(txCtx, userID); err != nil {
			return err
		}
		existing, err := s.deps.Drafts.LockWorkflowDraftBySlug(txCtx, userID, slug)
		if err != nil || existing == nil || !authz.CanWriteWorkflow(txCtx, userID, existing) {
			return err
		}
		deleted, err := s.deps.Drafts.RemoveWorkflowDraft(txCtx, existing.ID)
		if svcerr.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		return s.publish(txCtx, deleted, true)
	})
}
