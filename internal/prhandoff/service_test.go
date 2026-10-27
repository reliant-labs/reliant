// Copyright (c) 2025 Reliant Labs
package prhandoff

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type authorsFake struct {
	a         Author
	available bool
}

func (a authorsFake) Resolve(_ context.Context, site Site) (Author, error) {
	if site.RootID != a.a.RootID || site.ExecutionID != a.a.ExecutionID || site.ChatID != a.a.ChatID {
		return Author{}, ErrRefused
	}
	return a.a, nil
}
func (a authorsFake) Checkout(context.Context, Author, Repository) (string, error) {
	return a.a.WorktreePath, nil
}
func (a authorsFake) Available(context.Context, Author) (bool, error) { return a.available, nil }

type ghFake struct {
	pull       Pull
	completion Completion
}

func (g ghFake) Repository(context.Context, string, string) (Repository, error) {
	return g.pull.Repo, nil
}
func (g ghFake) BranchHead(context.Context, string, Repository, string) (string, error) {
	return g.pull.HeadSHA, nil
}
func (g ghFake) Pull(context.Context, string, Repository, int64) (Pull, error) { return g.pull, nil }
func (g ghFake) Review(context.Context, string, Repository, int64, string) (Completion, error) {
	return g.completion, nil
}

type memoryStore struct {
	p       Publication
	b       Binding
	reviews map[string]Review
}

func (m *memoryStore) RecordPublication(_ context.Context, p Publication) error { m.p = p; return nil }
func (m *memoryStore) Enroll(_ context.Context, b Binding) error {
	if m.p.ID != b.PublicationID {
		return ErrRefused
	}
	m.b = b
	return nil
}
func (m *memoryStore) Binding(_ context.Context, o string, r, n int64) (Binding, error) {
	if m.b.Author.OwnerID != o || m.b.Pull.Repo.ID != r || m.b.Pull.Number != n {
		return Binding{}, ErrNotFound
	}
	return m.b, nil
}
func (m *memoryStore) RecordReview(_ context.Context, r Review) (Review, error) {
	if old, ok := m.reviews[r.ID]; ok {
		return old, nil
	}
	m.reviews[r.ID] = r
	return r, nil
}

func TestReceiptEnrollmentAndReview(t *testing.T) {
	ctx := context.Background()
	a := Author{OwnerID: "owner", RootID: "root", ExecutionID: "execution", ChatID: "chat", WorktreeID: "wt", WorktreePath: "/owned/wt"}
	site := Site{RootID: a.RootID, ExecutionID: a.ExecutionID, ChatID: a.ChatID}
	pull := Pull{Repo: Repository{ID: 42, Name: "org/repo"}, Number: 5, ObjectID: 55, HeadRepoID: 42, Branch: "fix", HeadSHA: strings.Repeat("a", 40), Open: true}
	for _, tt := range []struct {
		name       string
		available  bool
		reviewHead string
		want       string
	}{{"event before park stays durable", true, pull.HeadSHA, "pending_wait"}, {"completed author", false, pull.HeadSHA, "needs_human"}, {"stale review", true, strings.Repeat("b", 40), "stale"}} {
		t.Run(tt.name, func(t *testing.T) {
			store := &memoryStore{reviews: map[string]Review{}}
			gh := ghFake{pull: pull, completion: Completion{Pull: pull, ReviewID: "review", HeadSHA: tt.reviewHead, Verdict: "changes_requested"}}
			svc, err := New(Deps{Store: store, Authors: authorsFake{a, tt.available}, GitHub: gh})
			if err != nil {
				t.Fatal(err)
			}
			// A forged PR-body identity has no field in the API. A create receipt
			// without the exact original execution's publication cannot enroll.
			create := CreateInput{Site: site, Repository: "org/repo", Number: 5, ObjectID: 55, HeadSHA: pull.HeadSHA}
			if err := svc.Created(ctx, create); !errors.Is(err, ErrRefused) {
				t.Fatalf("unpublished create: %v", err)
			}
			if err := svc.Published(ctx, PublishInput{Site: site, Repository: "org/repo", WorktreePath: a.WorktreePath, Branch: "fix", HeadSHA: pull.HeadSHA}); err != nil {
				t.Fatal(err)
			}
			wrong := create
			wrong.HeadSHA = strings.Repeat("c", 40)
			if err := svc.Created(ctx, wrong); !errors.Is(err, ErrRefused) {
				t.Fatalf("provider mismatch: %v", err)
			}
			if err := svc.Created(ctx, create); err != nil {
				t.Fatal(err)
			}
			input := ReviewInput{OwnerID: "owner", RepositoryID: 42, Number: 5, ReviewID: "review"}
			got, err := svc.Reviewed(ctx, input)
			if err != nil || got.State != tt.want {
				t.Fatalf("got %+v %v", got, err)
			}
			again, err := svc.Reviewed(ctx, input)
			if err != nil || again != got || len(store.reviews) != 1 {
				t.Fatalf("duplicate %+v %v", again, err)
			}
			input.OwnerID = "stranger"
			if _, err := svc.Reviewed(ctx, input); !errors.Is(err, ErrNotFound) {
				t.Fatalf("cross owner: %v", err)
			}
			input.OwnerID = "owner"
			input.RepositoryID = 999
			if _, err := svc.Reviewed(ctx, input); !errors.Is(err, ErrNotFound) {
				t.Fatalf("cross repo: %v", err)
			}
		})
	}
}
