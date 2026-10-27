// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/prhandoff"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

func TestPRHandoffPublicationBeforeCompetingEnroll(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	s := store.PRHandoffStore{DB: env.ORM}
	a := prhandoff.Author{OwnerID: "o", RootID: "a", ExecutionID: "ea", ChatID: "ca", WorktreeID: "w"}
	b := a
	b.RootID = "b"
	b.ExecutionID = "eb"
	b.ChatID = "cb"
	repo := prhandoff.Repository{ID: 42, Name: "org/repo"}
	sha := strings.Repeat("a", 40)
	pa := prhandoff.Publication{ID: "pa", Author: a, Repo: repo, Branch: "same", HeadSHA: sha}
	pb := pa
	pb.ID = "pb"
	pb.Author = b
	if err := s.RecordPublication(ctx, pa); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordPublication(ctx, pb); err != nil {
		t.Fatal(err)
	}
	ba := prhandoff.Binding{ID: "ba", Author: a, PublicationID: "pa", Pull: prhandoff.Pull{Repo: repo, Number: 1, ObjectID: 11, HeadRepoID: 42, Branch: "same", HeadSHA: sha, Open: true}}
	bb := ba
	bb.ID = "bb"
	bb.Author = b
	bb.PublicationID = "pb"
	bb.Pull.Number = 2
	bb.Pull.ObjectID = 22
	if err := s.Enroll(ctx, ba); err != nil {
		t.Fatal(err)
	}
	if err := s.Enroll(ctx, bb); !errors.Is(err, prhandoff.ErrRefused) {
		t.Fatalf("competing prepublished root enrolled: %v", err)
	}
}

func TestPRHandoffImmutableAuthorCASAndReviewDedup(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	s := store.PRHandoffStore{DB: env.ORM}
	a := prhandoff.Author{OwnerID: "owner", RootID: "root", ExecutionID: "execution", ChatID: "chat", WorktreeID: "wt"}
	repo := prhandoff.Repository{ID: 42, Name: "org/repo"}
	sha := func(c string) string { return strings.Repeat(c, 40) }
	p := prhandoff.Publication{ID: "p1", Author: a, Repo: repo, Branch: "fix", HeadSHA: sha("a")}
	b := prhandoff.Binding{ID: "binding", Author: a, Pull: prhandoff.Pull{Repo: repo, Number: 1, ObjectID: 11, HeadRepoID: 42, Branch: "fix", HeadSHA: p.HeadSHA, Open: true}, PublicationID: p.ID}
	if err := s.Enroll(ctx, b); !errors.Is(err, prhandoff.ErrRefused) {
		t.Fatalf("missing receipt: %v", err)
	}
	if err := s.RecordPublication(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := s.Enroll(ctx, b); err != nil {
		t.Fatal(err)
	}
	// Another execution cannot replace the original, even in the same chat.
	other := p
	other.ID = "other"
	other.Author.ExecutionID = "replacement"
	if err := s.RecordPublication(ctx, other); !errors.Is(err, prhandoff.ErrRefused) {
		t.Fatalf("replacement publication: %v", err)
	}
	stolen := b
	stolen.Author = other.Author
	stolen.PublicationID = other.ID
	if err := s.Enroll(ctx, stolen); !errors.Is(err, prhandoff.ErrRefused) {
		t.Fatalf("ownership stolen: %v", err)
	}
	if _, err := s.Binding(ctx, "stranger", 42, 1); !errors.Is(err, prhandoff.ErrNotFound) {
		t.Fatalf("foreign binding: %v", err)
	}
	p2 := p
	p2.ID = "p2"
	p2.HeadSHA = sha("b")
	p2.PreviousHeadSHA = p.HeadSHA
	if err := s.RecordPublication(ctx, p2); err != nil {
		t.Fatal(err)
	}
	// Exact replay succeeds; an old receipt cannot regress or claim success.
	if err := s.RecordPublication(ctx, p2); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordPublication(ctx, p); !errors.Is(err, prhandoff.ErrRefused) {
		t.Fatalf("old receipt: %v", err)
	}
	bad := p2
	bad.ID = "bad"
	bad.HeadSHA = sha("c")
	bad.PreviousHeadSHA = sha("d")
	if err := s.RecordPublication(ctx, bad); !errors.Is(err, prhandoff.ErrRefused) {
		t.Fatalf("wrong predecessor: %v", err)
	}
	var receipts int
	if err := env.SQL.QueryRowContext(ctx, "SELECT count(*) FROM pr_handoff_publications WHERE id = 'bad'").Scan(&receipts); err != nil || receipts != 0 {
		t.Fatalf("conflicting receipt committed: %d %v", receipts, err)
	}
	if err := s.Enroll(ctx, b); !errors.Is(err, prhandoff.ErrRefused) {
		t.Fatalf("old create replay after head advanced: %v", err)
	}
	current, err := s.Binding(ctx, "owner", 42, 1)
	if err != nil || current.Pull.HeadSHA != p2.HeadSHA || current.Author.ExecutionID != a.ExecutionID {
		t.Fatalf("CAS: %+v %v", current, err)
	}
	review := prhandoff.Review{ID: "review1", BindingID: b.ID, ReviewID: "provider-review", HeadSHA: p.HeadSHA, Verdict: "changes_requested", State: "pending_wait", Reason: "wait missing"}
	got, err := s.RecordReview(ctx, review)
	if err != nil || got.State != "stale" {
		t.Fatalf("concurrent refresh: %+v %v", got, err)
	}
	review.HeadSHA = p2.HeadSHA
	dup, err := s.RecordReview(ctx, review)
	if err != nil || dup != got {
		t.Fatalf("duplicate mutated: %+v %v", dup, err)
	}
	var count int
	if err := env.SQL.QueryRowContext(ctx, "SELECT count(*) FROM pr_handoff_reviews").Scan(&count); err != nil || count != 1 {
		t.Fatalf("count %d: %v", count, err)
	}
}
