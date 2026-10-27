// Copyright (c) 2025 Reliant Labs
package prhandoff

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

var shaPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// ErrRefused means the available evidence cannot authorize this association.
var ErrRefused = errors.New("PR handoff evidence refused")

// ErrNotFound means no future-only handoff binding exists.
var ErrNotFound = errors.New("PR handoff binding not found")

type service struct{ deps Deps }

// New constructs the disabled-by-wiring registry/router, requiring every seam.
func New(deps Deps) (Service, error) {
	if deps.Store == nil || deps.Authors == nil || deps.GitHub == nil {
		return nil, errors.New("PR handoff requires store, authors and trusted GitHub reader")
	}
	return &service{deps: deps}, nil
}

func (s *service) Published(ctx context.Context, in PublishInput) error {
	author, err := s.deps.Authors.Resolve(ctx, in.Site)
	if err != nil {
		return err
	}
	if !validAuthor(author) || !filepath.IsAbs(in.WorktreePath) || !bounded(in.Branch) || !shaPattern.MatchString(in.HeadSHA) {
		return ErrRefused
	}
	repo, err := s.deps.GitHub.Repository(ctx, author.OwnerID, in.Repository)
	if err != nil {
		return err
	}
	if !validRepo(repo) {
		return ErrRefused
	}
	checkout, err := s.deps.Authors.Checkout(ctx, author, repo)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(checkout) || filepath.Clean(checkout) != filepath.Clean(in.WorktreePath) {
		return ErrRefused
	}
	head, err := s.deps.GitHub.BranchHead(ctx, author.OwnerID, repo, in.Branch)
	if err != nil {
		return err
	}
	if head != in.HeadSHA {
		return fmt.Errorf("%w: published head no longer current", ErrRefused)
	}
	p := Publication{Author: author, Repo: repo, Branch: in.Branch, HeadSHA: in.HeadSHA, PreviousHeadSHA: in.PreviousHeadSHA}
	p.ID = key(author.OwnerID, author.RootID, author.ExecutionID, repo.ID, p.Branch, p.HeadSHA)
	return s.deps.Store.RecordPublication(ctx, p)
}

func (s *service) Created(ctx context.Context, in CreateInput) error {
	author, err := s.deps.Authors.Resolve(ctx, in.Site)
	if err != nil {
		return err
	}
	if !validAuthor(author) || in.Number <= 0 || in.ObjectID <= 0 || !shaPattern.MatchString(in.HeadSHA) {
		return ErrRefused
	}
	repo, err := s.deps.GitHub.Repository(ctx, author.OwnerID, in.Repository)
	if err != nil {
		return err
	}
	pull, err := s.deps.GitHub.Pull(ctx, author.OwnerID, repo, in.Number)
	if err != nil {
		return err
	}
	if !validPull(pull) || pull.Repo != repo || pull.Number != in.Number || pull.ObjectID != in.ObjectID || pull.HeadSHA != in.HeadSHA {
		return ErrRefused
	}
	// Store.Enroll must find this exact successful publication. A PR response
	// alone cannot claim that another run's branch belongs to its caller.
	publicationID := key(author.OwnerID, author.RootID, author.ExecutionID, repo.ID, pull.Branch, pull.HeadSHA)
	return s.deps.Store.Enroll(ctx, Binding{ID: key(author.OwnerID, repo.ID, pull.Number), Author: author, Pull: pull, PublicationID: publicationID})
}

func (s *service) Reviewed(ctx context.Context, in ReviewInput) (Review, error) {
	if !bounded(in.OwnerID) || in.RepositoryID <= 0 || in.Number <= 0 || !bounded(in.ReviewID) {
		return Review{}, ErrRefused
	}
	b, err := s.deps.Store.Binding(ctx, in.OwnerID, in.RepositoryID, in.Number)
	if err != nil {
		return Review{}, err
	}
	if b.Author.OwnerID != in.OwnerID || b.Pull.Repo.ID != in.RepositoryID || b.Pull.Number != in.Number {
		return Review{}, ErrRefused
	}
	completion, err := s.deps.GitHub.Review(ctx, in.OwnerID, b.Pull.Repo, in.Number, in.ReviewID)
	if err != nil {
		return Review{}, err
	}
	if completion.ReviewID != in.ReviewID || !validPull(completion.Pull) || completion.Pull.Repo.ID != b.Pull.Repo.ID || completion.Pull.Number != b.Pull.Number || completion.Pull.ObjectID != b.Pull.ObjectID || completion.Pull.HeadRepoID != b.Pull.HeadRepoID || completion.Pull.Branch != b.Pull.Branch || (completion.Verdict != "approved" && completion.Verdict != "changes_requested") || !shaPattern.MatchString(completion.HeadSHA) {
		return Review{}, ErrRefused
	}
	r := Review{ID: key(b.ID, completion.ReviewID), BindingID: b.ID, ReviewID: completion.ReviewID, HeadSHA: completion.HeadSHA, Verdict: completion.Verdict, State: "pending_wait", Reason: "durable author wait and exact-execution delivery not implemented; no message sent"}
	if completion.HeadSHA != b.Pull.HeadSHA || completion.Pull.HeadSHA != b.Pull.HeadSHA {
		r.State, r.Reason = "stale", "review source head is not the current enrolled head"
	} else {
		available, err := s.deps.Authors.Available(ctx, b.Author)
		if err != nil {
			return Review{}, err
		}
		if !available {
			r.State, r.Reason = "needs_human", "original execution completed, cancelled, or unavailable; explicit reassignment required"
		}
	}
	return s.deps.Store.RecordReview(ctx, r)
}

func validAuthor(a Author) bool {
	return bounded(a.OwnerID) && bounded(a.RootID) && bounded(a.ExecutionID) && bounded(a.ChatID) && bounded(a.WorktreeID)
}
func validRepo(r Repository) bool {
	return r.ID > 0 && bounded(r.Name) && strings.Count(r.Name, "/") == 1
}
func validPull(p Pull) bool {
	return validRepo(p.Repo) && p.Open && p.Number > 0 && p.ObjectID > 0 && p.HeadRepoID == p.Repo.ID && bounded(p.Branch) && shaPattern.MatchString(p.HeadSHA)
}
func bounded(s string) bool { return len(s) > 0 && len(s) <= 255 && !strings.ContainsRune(s, '\x00') }

func key(parts ...any) string {
	b, _ := json.Marshal(parts)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
