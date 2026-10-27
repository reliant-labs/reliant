// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for PrHandoffBinding. The generated pr_handoff_binding_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: prHandoffBindingRepo (a *crud.Repo[PrHandoffBinding]) and the
//     package-level GetPrHandoffBindingByID / ListPrHandoffBinding / CreatePrHandoffBinding / UpdatePrHandoffBinding / DeletePrHandoffBinding
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     PrHandoffBinding struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindPrHandoffBindingBy(ctx context.Context, db orm.Context, /* args */) ([]*PrHandoffBinding, error) {
//		var rows []*PrHandoffBinding
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("owner_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActivePrHandoffBindings(ctx context.Context, db orm.Context) ([]*PrHandoffBinding, error) {
//		return ListPrHandoffBinding(ctx, db, orm.WhereEq("owner_id", true))
//	}
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/reliant/internal/prhandoff"
)

// PRHandoffStore implements the handoff's atomic persistence boundary. It is
// explicitly constructed, not wired into any running automation.
type PRHandoffStore struct{ DB orm.Context }

// RecordPublication commits a receipt and advances only bindings belonging to
// that exact original execution. CAS prevents old receipts regressing heads.
func (s PRHandoffStore) RecordPublication(ctx context.Context, p prhandoff.Publication) error {
	return s.DB.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
		var bindings []PrHandoffBinding
		if err := s.DB.Bun().NewSelect().Model(&bindings).Where("owner_id = ? AND repo_id = ? AND branch = ?", p.Author.OwnerID, p.Repo.ID, p.Branch).Scan(ctx); err != nil {
			return err
		}
		for _, b := range bindings {
			if b.RootID != p.Author.RootID || b.ExecutionID != p.Author.ExecutionID || b.ChatID != p.Author.ChatID || b.WorktreeID != p.Author.WorktreeID {
				return prhandoff.ErrRefused
			}
			if b.HeadSha != p.HeadSHA && b.HeadSha != p.PreviousHeadSHA {
				return fmt.Errorf("%w: publication predecessor conflict", prhandoff.ErrRefused)
			}
		}
		row := &PrHandoffPublication{ID: p.ID, OwnerID: p.Author.OwnerID, RootID: p.Author.RootID, ExecutionID: p.Author.ExecutionID, ChatID: p.Author.ChatID, WorktreeID: p.Author.WorktreeID, RepoID: p.Repo.ID, RepoName: p.Repo.Name, Branch: p.Branch, HeadSha: p.HeadSHA}
		if _, err := s.DB.Bun().NewInsert().Model(row).On("CONFLICT (id) DO NOTHING").Exec(ctx); err != nil {
			return err
		}
		for _, b := range bindings {
			if b.HeadSha == p.HeadSHA {
				continue
			} // exact idempotent replay
			result, err := s.DB.Bun().NewUpdate().Model((*PrHandoffBinding)(nil)).
				Set("head_sha = ?", p.HeadSHA).Set("publication_id = ?", p.ID).Set("updated_at = now()").
				Where("id = ? AND head_sha = ?", b.ID, p.PreviousHeadSHA).Exec(ctx)
			if err != nil {
				return err
			}
			changed, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if changed != 1 {
				return fmt.Errorf("%w: publication CAS conflict", prhandoff.ErrRefused)
			}
		}
		return nil
	})
}

// Enroll requires an exact matching persisted successful publication and never
// overwrites an existing author (even another root of the same chat).
func (s PRHandoffStore) Enroll(ctx context.Context, b prhandoff.Binding) error {
	return s.DB.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
		p, err := GetPrHandoffPublicationByID(ctx, s.DB, b.PublicationID)
		if err != nil {
			return fmt.Errorf("%w: no successful publication", prhandoff.ErrRefused)
		}
		a := b.Author
		// Publication may precede enrollment. Recheck all bindings on this
		// branch inside this transaction to close competing-root enrollment.
		var branchBindings []PrHandoffBinding
		if err := s.DB.Bun().NewSelect().Model(&branchBindings).Where("owner_id = ? AND repo_id = ? AND branch = ?", a.OwnerID, b.Pull.Repo.ID, b.Pull.Branch).Scan(ctx); err != nil {
			return err
		}
		for _, existing := range branchBindings {
			if existing.RootID != a.RootID || existing.ExecutionID != a.ExecutionID || existing.ChatID != a.ChatID || existing.WorktreeID != a.WorktreeID {
				return prhandoff.ErrRefused
			}
		}
		if p.OwnerID != a.OwnerID || p.RootID != a.RootID || p.ExecutionID != a.ExecutionID || p.ChatID != a.ChatID || p.WorktreeID != a.WorktreeID || p.RepoID != b.Pull.Repo.ID || p.Branch != b.Pull.Branch || p.HeadSha != b.Pull.HeadSHA {
			return prhandoff.ErrRefused
		}
		row := &PrHandoffBinding{ID: b.ID, OwnerID: a.OwnerID, RootID: a.RootID, ExecutionID: a.ExecutionID, ChatID: a.ChatID, WorktreeID: a.WorktreeID, RepoID: b.Pull.Repo.ID, RepoName: b.Pull.Repo.Name, PrNumber: b.Pull.Number, PrObjectID: b.Pull.ObjectID, HeadRepoID: b.Pull.HeadRepoID, Branch: b.Pull.Branch, HeadSha: b.Pull.HeadSHA, PublicationID: p.ID}
		if _, err := s.DB.Bun().NewInsert().Model(row).On("CONFLICT (owner_id, repo_id, pr_number) DO NOTHING").Exec(ctx); err != nil {
			return err
		}
		existing, err := s.Binding(ctx, a.OwnerID, b.Pull.Repo.ID, b.Pull.Number)
		if err != nil {
			return err
		}
		e := existing.Author
		if e.OwnerID != a.OwnerID || e.RootID != a.RootID || e.ExecutionID != a.ExecutionID || e.ChatID != a.ChatID || e.WorktreeID != a.WorktreeID || existing.Pull.ObjectID != b.Pull.ObjectID || existing.Pull.HeadRepoID != b.Pull.HeadRepoID || existing.Pull.Branch != b.Pull.Branch || existing.Pull.HeadSHA != b.Pull.HeadSHA || existing.PublicationID != b.PublicationID {
			return prhandoff.ErrRefused
		}
		return nil
	})
}

// Binding reads only within the authenticated owner's repository/PR scope.
func (s PRHandoffStore) Binding(ctx context.Context, owner string, repoID, number int64) (prhandoff.Binding, error) {
	var row PrHandoffBinding
	err := s.DB.Bun().NewSelect().Model(&row).Where("owner_id = ? AND repo_id = ? AND pr_number = ?", owner, repoID, number).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return prhandoff.Binding{}, prhandoff.ErrNotFound
	}
	if err != nil {
		return prhandoff.Binding{}, err
	}
	return prhandoff.Binding{ID: row.ID, Author: prhandoff.Author{OwnerID: row.OwnerID, RootID: row.RootID, ExecutionID: row.ExecutionID, ChatID: row.ChatID, WorktreeID: row.WorktreeID}, Pull: prhandoff.Pull{Repo: prhandoff.Repository{ID: row.RepoID, Name: row.RepoName}, Number: row.PrNumber, ObjectID: row.PrObjectID, HeadRepoID: row.HeadRepoID, Branch: row.Branch, HeadSHA: row.HeadSha, Open: true}, PublicationID: row.PublicationID}, nil
}

// RecordReview deduplicates by provider review identity and rechecks the head
// inside the same serializable transaction as the write.
func (s PRHandoffStore) RecordReview(ctx context.Context, r prhandoff.Review) (prhandoff.Review, error) {
	var out prhandoff.Review
	err := s.DB.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
		binding, err := GetPrHandoffBindingByID(ctx, s.DB, r.BindingID)
		if err != nil {
			return err
		}
		state, reason := r.State, r.Reason
		if binding.HeadSha != r.HeadSHA {
			state, reason = "stale", "enrolled head advanced before review was recorded"
		}
		row := &PrHandoffReview{ID: r.ID, BindingID: r.BindingID, ReviewID: r.ReviewID, HeadSha: r.HeadSHA, Verdict: r.Verdict, State: state, Reason: reason}
		if _, err := s.DB.Bun().NewInsert().Model(row).On("CONFLICT (binding_id, review_id) DO NOTHING").Exec(ctx); err != nil {
			return err
		}
		var saved PrHandoffReview
		if err := s.DB.Bun().NewSelect().Model(&saved).Where("binding_id = ? AND review_id = ?", r.BindingID, r.ReviewID).Scan(ctx); err != nil {
			return err
		}
		out = prhandoff.Review{ID: saved.ID, BindingID: saved.BindingID, ReviewID: saved.ReviewID, HeadSHA: saved.HeadSha, Verdict: saved.Verdict, State: saved.State, Reason: saved.Reason}
		return nil
	})
	return out, err
}

// PRHandoffBranchBinding resolves an existing enrolled branch within the
// authenticated owner's provider repository identity. Multiple PR targets are
// permitted only when they agree on immutable original ownership.
func (s PRHandoffStore) PRHandoffBranchBinding(ctx context.Context, owner string, repoID int64, branch string) (prhandoff.Binding, error) {
	var rows []PrHandoffBinding
	if err := s.DB.Bun().NewSelect().Model(&rows).Where("owner_id = ? AND repo_id = ? AND branch = ?", owner, repoID, branch).Scan(ctx); err != nil {
		return prhandoff.Binding{}, err
	}
	if len(rows) == 0 {
		return prhandoff.Binding{}, prhandoff.ErrNotFound
	}
	first, err := s.Binding(ctx, owner, repoID, rows[0].PrNumber)
	if err != nil {
		return prhandoff.Binding{}, err
	}
	for _, r := range rows {
		if r.RootID != first.Author.RootID || r.ExecutionID != first.Author.ExecutionID || r.ChatID != first.Author.ChatID || r.WorktreeID != first.Author.WorktreeID {
			return prhandoff.Binding{}, prhandoff.ErrRefused
		}
	}
	return first, nil
}

// PrHandoffBindingExtra carries fields on PrHandoffBinding that are NOT columns — computed
// values, transient state, anything derived rather than stored. PrHandoffBinding is
// embedded on PrHandoffBindingExtra (see pr_handoff_binding_orm.go), so a field added here is a field on
// every PrHandoffBinding value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type PrHandoffBindingExtra struct {
}
