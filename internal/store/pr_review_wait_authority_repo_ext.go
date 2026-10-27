// Copyright (c) 2025 Reliant Labs
package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/reliant/internal/prhandoff"
	"github.com/reliant-labs/reliant/internal/prreviewwait"
)

// PrReviewWaitAuthorityExtra contains only optional non-column extensions.
type PrReviewWaitAuthorityExtra struct{}

// BindingByID scopes the authoritative binding lookup to an authenticated owner.
func (s PRReviewWaitStore) BindingByID(ctx context.Context, owner, id string) (prhandoff.Binding, error) {
	if owner == "" || id == "" {
		return prhandoff.Binding{}, prhandoff.ErrRefused
	}
	row, err := GetPrHandoffBindingByID(ctx, s.DB, id)
	if err != nil {
		return prhandoff.Binding{}, err
	}
	if row.OwnerID != owner {
		return prhandoff.Binding{}, prhandoff.ErrRefused
	}
	return s.Binding(ctx, owner, row.RepoID, row.PrNumber)
}

// BindAuthority installs policy authority only while owner/binding/head still
// match. Arm and Put call bindAuthority inside their own Serializable write.
func (s PRReviewWaitStore) BindAuthority(ctx context.Context, b prhandoff.Binding, a prreviewwait.Authority) error {
	return s.DB.RunTx(ctx, orm.Serializable, func(ctx context.Context) error { return s.bindAuthority(ctx, b, a) })
}
func (s PRReviewWaitStore) bindAuthority(ctx context.Context, b prhandoff.Binding, a prreviewwait.Authority) error {
	if a.ID == "" || len(a.ID) > 255 || a.PolicyRevision <= 0 || b.Author.OwnerID == "" {
		return prhandoff.ErrRefused
	}
	current, err := s.BindingByID(ctx, b.Author.OwnerID, b.ID)
	if err != nil {
		return err
	}
	if current.Pull.HeadSHA != b.Pull.HeadSHA || current.Pull.Repo.ID != b.Pull.Repo.ID || current.Pull.Number != b.Pull.Number {
		return prhandoff.ErrRefused
	}
	digest := sha256.Sum256([]byte(b.ID + "\x00" + b.Pull.HeadSHA))
	row := &PrReviewWaitAuthority{ID: hex.EncodeToString(digest[:]), BindingID: b.ID, HeadSha: b.Pull.HeadSHA, AuthorityID: a.ID, PolicyRevision: a.PolicyRevision}
	if _, err := s.DB.Bun().NewInsert().Model(row).On("CONFLICT (binding_id,head_sha) DO NOTHING").Exec(ctx); err != nil {
		return err
	}
	persisted, err := s.authority(ctx, b.ID, b.Pull.HeadSHA)
	if err != nil {
		return err
	}
	if persisted.AuthorityID != a.ID || persisted.PolicyRevision != a.PolicyRevision {
		return prhandoff.ErrRefused
	}
	return nil
}
func (s PRReviewWaitStore) authority(ctx context.Context, binding, head string) (*PrReviewWaitAuthority, error) {
	var row PrReviewWaitAuthority
	if err := s.DB.Bun().NewSelect().Model(&row).Where("binding_id = ? AND head_sha = ?", binding, head).Scan(ctx); err != nil {
		return nil, err
	}
	return &row, nil
}
