// Copyright (c) 2025 Reliant Labs
package prhandoffadapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"

	"github.com/reliant-labs/reliant/internal/prhandoff"
	"github.com/reliant-labs/reliant/internal/runcontinuity"
)

// BranchBindingReader resolves only authenticated owner/provider-ID/branch.
type BranchBindingReader interface {
	PRHandoffBranchBinding(context.Context, string, int64, string) (prhandoff.Binding, error)
}

// ContinuationProof is shared runcontinuity resolution, not a chat pointer.
type ContinuationProof interface {
	Resolve(context.Context, runcontinuity.Execution) (runcontinuity.Result, error)
}

// ContinuationPublisher is an optional server receipt hook. It preserves the
// original author through proven internal rollovers without weakening storage
// ownership. No app root installs it in this stage.
type ContinuationPublisher struct {
	Store      prhandoff.Store
	Bindings   BranchBindingReader
	Authors    prhandoff.Authors
	GitHub     prhandoff.GitHub
	Continuity ContinuationProof
	Initial    prhandoff.Service
}

func publicationID(a prhandoff.Author, repoID int64, branch, head string) string {
	b, _ := json.Marshal([]any{a.OwnerID, a.RootID, a.ExecutionID, repoID, branch, head})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Published validates the actual activity first, then normalizes only the
// immutable storage author after proving the original→current execution edge.
func (p ContinuationPublisher) Published(ctx context.Context, in prhandoff.PublishInput) error {
	if p.Store == nil || p.Bindings == nil || p.Authors == nil || p.GitHub == nil || p.Continuity == nil || p.Initial == nil {
		return prhandoff.ErrRefused
	}
	site, err := ActivitySite(ctx, in.Site.RootID, in.Site.ChatID)
	if err != nil || site != in.Site {
		return prhandoff.ErrRefused
	}
	current, err := p.Authors.Resolve(ctx, site)
	if err != nil {
		return err
	}
	repo, err := p.GitHub.Repository(ctx, current.OwnerID, in.Repository)
	if err != nil {
		return err
	}
	binding, err := p.Bindings.PRHandoffBranchBinding(ctx, current.OwnerID, repo.ID, in.Branch)
	if errors.Is(err, prhandoff.ErrNotFound) {
		return p.Initial.Published(ctx, in)
	}
	if err != nil {
		return err
	}
	original := binding.Author
	if current.OwnerID != original.OwnerID || current.RootID != original.RootID || current.ChatID != original.ChatID || current.WorktreeID != original.WorktreeID {
		return prhandoff.ErrRefused
	}
	proof, err := p.Continuity.Resolve(ctx, runcontinuity.Execution{WorkflowID: original.RootID, RunID: original.ExecutionID})
	if err != nil {
		return err
	}
	if !proof.Live || proof.Current != (runcontinuity.Execution{WorkflowID: site.RootID, RunID: site.ExecutionID}) {
		return prhandoff.ErrRefused
	}
	checkout, err := p.Authors.Checkout(ctx, current, repo)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(in.WorktreePath) || !filepath.IsAbs(checkout) || filepath.Clean(in.WorktreePath) != filepath.Clean(checkout) {
		return prhandoff.ErrRefused
	}
	head, err := p.GitHub.BranchHead(ctx, current.OwnerID, repo, in.Branch)
	if err != nil {
		return err
	}
	if head != in.HeadSHA {
		return prhandoff.ErrRefused
	}
	// Keep the same canonical receipt identity the core derives. No fake original
	// activity context or substitution of the persisted binding execution occurs.
	return p.Store.RecordPublication(ctx, prhandoff.Publication{ID: publicationID(original, repo.ID, in.Branch, in.HeadSHA), Author: original, Repo: repo, Branch: in.Branch, HeadSHA: in.HeadSHA, PreviousHeadSHA: in.PreviousHeadSHA})
}
