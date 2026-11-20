// Copyright (c) 2025 Reliant Labs
package prrecords

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
)

const (
	shaA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	shaB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

type fakeStore struct {
	changes []core.PRRecordChange
	err     error
}

func (f *fakeStore) ApplyPRRecordChange(_ context.Context, c core.PRRecordChange) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	f.changes = append(f.changes, c)
	return true, nil
}

func pr(number int, head string, labels []any, extra map[string]any) map[string]any {
	m := map[string]any{
		"number": int64(number), "title": "t", "state": "open", "draft": false, "merged": false,
		"updated_at": "2026-11-20T10:00:00Z",
		"head":       map[string]any{"ref": "feat/x", "sha": head},
		"base":       map[string]any{"ref": "main", "sha": shaB},
		"user":       map[string]any{"login": "octo", "id": int64(7)},
		"labels":     labels,
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func event(typ string, data map[string]any) Event {
	return Event{
		Type: typ, AccountKey: "inst-1", ResourceKey: "42",
		Attributes: map[string]string{"repository": "Reliant-Labs/Reliant"}, Data: data,
	}
}

func project(t *testing.T, events ...Event) []core.PRRecordChange {
	t.Helper()
	store := &fakeStore{}
	p := New(Deps{Store: store, Scope: NewScope([]string{"reliant-labs/reliant"})}).(*projector)
	p.now = func() time.Time { return time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC) }
	if err := p.Apply(context.Background(), Delivery{Provider: ProviderGitHub, Key: "d-1", Events: events}); err != nil {
		t.Fatal(err)
	}
	return store.changes
}

func TestProjectorPullRequestSnapshot(t *testing.T) {
	changes := project(t, event("pull_request.synchronize", map[string]any{
		"number": int64(5), "before": strings.ToUpper(shaB), "after": shaA,
		"pull_request": pr(5, shaA, []any{"review:pending"}, nil),
	}))
	if len(changes) != 1 {
		t.Fatalf("changes = %d, want 1", len(changes))
	}
	c := changes[0]
	s := c.Snapshot
	if s == nil || s.Number != 5 || s.HeadSHA != shaA || s.State != core.PRRecordOpen || !s.Full ||
		s.Repository != "Reliant-Labs/Reliant" || s.RepositoryID != "42" || s.AuthorID != "7" ||
		len(s.Labels) != 1 || s.Labels[0] != "review:pending" {
		t.Fatalf("snapshot = %+v", s)
	}
	if c.PreviousHeadSHA != shaB {
		t.Errorf("previous head = %q, want lower-cased before", c.PreviousHeadSHA)
	}
	if d := s.Delta; d.FromHead != shaB || d.HeadSHA != shaA || d.HeadRef != "feat/x" {
		t.Errorf("synchronize delta = %+v", d)
	}
	if c.Delivery.DeliveryKey != "d-1" || c.Delivery.EventIndex != 0 || c.Delivery.EventType != "pull_request.synchronize" {
		t.Errorf("delivery = %+v", c.Delivery)
	}
}

func TestProjectorMergedAndLabelDelta(t *testing.T) {
	changes := project(t,
		event("pull_request.closed", map[string]any{"pull_request": pr(5, shaA, nil, map[string]any{
			"state": "closed", "merged": true, "merged_at": "2026-11-20T11:00:00Z", "merge_commit_sha": shaB,
		})}),
		event("pull_request.labeled", map[string]any{"label": "hold", "pull_request": pr(6, shaA, []any{"hold"}, nil)}),
		event("pull_request.unlabeled", map[string]any{"label": "hold", "pull_request": pr(6, shaA, []any{}, nil)}),
	)
	if len(changes) != 3 {
		t.Fatalf("changes = %d, want 3", len(changes))
	}
	if s := changes[0].Snapshot; s.State != core.PRRecordMerged || s.MergedAt == nil || s.MergeCommit != shaB ||
		s.Delta.State != core.PRRecordMerged || s.Delta.MergeCommit != shaB {
		t.Errorf("merged snapshot = %+v", s)
	}
	if changes[1].Snapshot.Delta.LabelAdded != "hold" || changes[2].Snapshot.Delta.LabelRemoved != "hold" {
		t.Errorf("label deltas = %q / %q", changes[1].Snapshot.Delta.LabelAdded, changes[2].Snapshot.Delta.LabelRemoved)
	}
	if changes[1].Delivery.EventIndex != 1 || changes[2].Delivery.EventIndex != 2 {
		t.Errorf("event indexes = %d, %d", changes[1].Delivery.EventIndex, changes[2].Delivery.EventIndex)
	}
}

func TestProjectorReviewMarkers(t *testing.T) {
	slim := pr(5, shaA, nil, nil)
	delete(slim, "merged")
	// Numbers read back from stored JSON are float64; both shapes decode.
	var review map[string]any
	if err := json.Unmarshal([]byte(`{"id": 9001, "commit_id": "`+shaA+`", "state": "commented",
		"submitted_at": "2026-11-20T10:05:00Z", "user": {"login": "reliant-labs[bot]", "id": 99},
		"markers": [
			{"name": "review-passed", "attrs": {"sha": "`+shaA+`"}},
			{"name": "review-rereview", "attrs": {"sha": "`+shaA+`", "of": "8000"}},
			{"name": "merge-regen", "attrs": {"of": "`+shaB+`"}},
			{"name": "review-changes", "attrs": {"sha": "not-a-sha"}}
		]}`), &review); err != nil {
		t.Fatal(err)
	}
	changes := project(t, event("pull_request_review.submitted", map[string]any{"pull_request": slim, "review": review}))
	if len(changes) != 1 {
		t.Fatalf("changes = %d", len(changes))
	}
	c := changes[0]
	if c.Snapshot.Full {
		t.Error("a review's pull request lacks `merged`: the snapshot must not be full")
	}
	if len(c.Reviews) != 2 {
		t.Fatalf("reviews = %+v, want passed + rereview", c.Reviews)
	}
	if r := c.Reviews[0]; r.Kind != core.PRReviewPassed || r.ReviewID != "9001" || r.CommitSHA != shaA || r.MarkerSHA != shaA || r.AuthorID != "99" {
		t.Errorf("passed = %+v", r)
	}
	if r := c.Reviews[1]; r.Kind != core.PRReviewRereview || r.OfReviewID != "8000" {
		t.Errorf("rereview = %+v", r)
	}
}

func TestProjectorCheckRun(t *testing.T) {
	changes := project(t, event("check_run.completed", map[string]any{"check_run": map[string]any{
		"id": int64(77), "name": "Reliant review", "status": "completed", "conclusion": "success",
		"head_sha": shaA, "completed_at": "2026-11-20T10:06:00Z", "app": map[string]any{"slug": "github-actions"},
	}}))
	if len(changes) != 1 || changes[0].Check == nil || changes[0].Snapshot != nil {
		t.Fatalf("changes = %+v", changes)
	}
	if c := changes[0].Check; c.CheckRunID != 77 || c.AppSlug != "github-actions" || c.Conclusion != "success" || c.CompletedAt == nil {
		t.Errorf("check = %+v", c)
	}
}

func TestProjectorSkips(t *testing.T) {
	other := event("pull_request.opened", map[string]any{"pull_request": pr(1, shaA, nil, nil)})
	other.Attributes = map[string]string{"repository": "someone/else"}
	noNumber := event("pull_request.opened", map[string]any{"pull_request": map[string]any{"updated_at": "2026-11-20T10:00:00Z"}})
	if got := project(t,
		other,
		noNumber,
		event("issues.opened", map[string]any{}),
		event("pull_request_review.dismissed", map[string]any{"pull_request": pr(1, shaA, nil, nil)}),
	); len(got) != 0 {
		t.Fatalf("changes = %+v, want none", got)
	}
	// Records are opt-in: an empty scope records nothing.
	store := &fakeStore{}
	if err := New(Deps{Store: store, Scope: NewScope(nil)}).Apply(context.Background(), Delivery{
		Provider: ProviderGitHub, Key: "d", Events: []Event{event("pull_request.opened", map[string]any{"pull_request": pr(1, shaA, nil, nil)})},
	}); err != nil || len(store.changes) != 0 {
		t.Fatalf("empty scope: err=%v changes=%d", err, len(store.changes))
	}
}

func TestProjectorStoreErrorIsReturned(t *testing.T) {
	store := &fakeStore{err: errors.New("db down")}
	p := New(Deps{Store: store, Scope: NewScope([]string{"reliant-labs/reliant"})})
	err := p.Apply(context.Background(), Delivery{Provider: ProviderGitHub, Key: "d", Events: []Event{
		event("pull_request.opened", map[string]any{"pull_request": pr(1, shaA, nil, nil)}),
	}})
	if err == nil || !strings.Contains(err.Error(), "db down") {
		t.Fatalf("err = %v", err)
	}
}

func rv(id, kind, author string, at int, commit, marker string) core.PRRecordReview {
	return core.PRRecordReview{
		ReviewID: id, Kind: kind, AuthorLogin: author, CommitSHA: commit, MarkerSHA: marker,
		SubmittedAt: time.Date(2026, 11, 20, 10, at, 0, 0, time.UTC),
	}
}

func TestHeadVerdict(t *testing.T) {
	policy := Policy{TrustedAuthors: []string{"reliant-labs[bot]", "reliant-01"}, RereviewAuthors: []string{"reliant-labs[bot]"}}
	bot := "reliant-labs[bot]"
	cases := []struct {
		name    string
		reviews []core.PRRecordReview
		want    Verdict
	}{
		{"none", nil, Verdict{}},
		{"first marker wins", []core.PRRecordReview{
			rv("20", core.PRReviewChanges, bot, 1, shaA, shaA),
			rv("21", core.PRReviewPassed, bot, 2, shaA, shaA),
		}, Verdict{Kind: core.PRReviewChanges, ReviewID: "20"}},
		{"submission order, not row order", []core.PRRecordReview{
			rv("21", core.PRReviewPassed, bot, 2, shaA, shaA),
			rv("20", core.PRReviewChanges, bot, 1, shaA, shaA),
		}, Verdict{Kind: core.PRReviewChanges, ReviewID: "20"}},
		{"numeric review id breaks a tie", []core.PRRecordReview{
			rv("100", core.PRReviewPassed, bot, 1, shaA, shaA),
			rv("99", core.PRReviewChanges, bot, 1, shaA, shaA),
		}, Verdict{Kind: core.PRReviewChanges, ReviewID: "99"}},
		{"untrusted author ignored", []core.PRRecordReview{
			rv("20", core.PRReviewPassed, "mallory", 1, shaA, shaA),
		}, Verdict{}},
		{"marker for another head ignored", []core.PRRecordReview{
			rv("20", core.PRReviewPassed, bot, 1, shaA, shaB),
		}, Verdict{}},
		{"review on another commit ignored", []core.PRRecordReview{
			rv("20", core.PRReviewPassed, bot, 1, shaB, shaB),
		}, Verdict{}},
		{"newest re-review decides", []core.PRRecordReview{
			rv("20", core.PRReviewChanges, bot, 1, shaA, shaA),
			rv("30", core.PRReviewPassed, bot, 5, shaA, shaA),
			rv("30", core.PRReviewRereview, bot, 5, shaA, shaA),
			rv("25", core.PRReviewChanges, bot, 3, shaA, shaA),
			rv("25", core.PRReviewRereview, bot, 3, shaA, shaA),
		}, Verdict{Kind: core.PRReviewPassed, ReviewID: "30", Rereview: true}},
		{"re-review by a non re-review author is not one", []core.PRRecordReview{
			rv("20", core.PRReviewChanges, "reliant-01", 1, shaA, shaA),
			rv("30", core.PRReviewPassed, "reliant-01", 5, shaA, shaA),
			rv("30", core.PRReviewRereview, "reliant-01", 5, shaA, shaA),
		}, Verdict{Kind: core.PRReviewChanges, ReviewID: "20"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := HeadVerdict(tc.reviews, shaA, policy)
			if got.Kind != tc.want.Kind || got.ReviewID != tc.want.ReviewID || got.Rereview != tc.want.Rereview {
				t.Fatalf("verdict = %+v, want %+v", got, tc.want)
			}
		})
	}
	// An author matches by numeric id too.
	r := rv("20", core.PRReviewPassed, "renamed", 1, shaA, shaA)
	r.AuthorID = "99"
	if got := HeadVerdict([]core.PRRecordReview{r}, shaA, Policy{TrustedAuthors: []string{"99"}}); got.Kind != core.PRReviewPassed {
		t.Fatalf("by id: %+v", got)
	}
}

func TestClassify(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	done := now.Add(-time.Hour)
	lp := DefaultLabelPolicy()
	rec := func(labels ...string) *core.PRRecord {
		return &core.PRRecord{Number: 1, HeadSHA: shaA, State: core.PRRecordOpen, Labels: labels, ObservedAt: now.Add(-time.Hour)}
	}
	check := func(conclusion string) []core.PRRecordCheck {
		return []core.PRRecordCheck{{HeadSHA: shaA, Name: "Reliant review", CheckRunID: 1, Conclusion: conclusion, CompletedAt: &done}}
	}
	passed, changes := Verdict{Kind: core.PRReviewPassed}, Verdict{Kind: core.PRReviewChanges}
	cases := []struct {
		name   string
		rec    *core.PRRecord
		v      Verdict
		checks []core.PRRecordCheck
		want   string
	}{
		{"pending agrees", rec("review:pending"), Verdict{}, nil, DiffAgree},
		{"passed agrees", rec("review:passed", "merge:queue"), passed, check("success"), DiffAgree},
		{"changes agrees", rec("review:changes"), changes, check("failure"), DiffAgree},
		{"label missing", rec(), passed, nil, DiffLabelMissing},
		{"stale passed label", rec("review:passed"), Verdict{}, nil, DiffLabelStale},
		{"wrong label", rec("review:passed"), changes, nil, DiffLabelMissing},
		{"check disagrees", rec("review:passed"), passed, check("failure"), DiffCheckDisagrees},
		{"held", rec("hold"), passed, nil, DiffHeld},
		{"draft", &core.PRRecord{Draft: true, HeadSHA: shaA}, passed, nil, DiffDraft},
		{"settling", &core.PRRecord{HeadSHA: shaA, ObservedAt: now.Add(-time.Minute)}, passed, nil, DiffSettling},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.rec, tc.v, tc.checks, lp, now); got.Class != tc.want {
				t.Fatalf("class = %s, want %s (%+v)", got.Class, tc.want, got)
			}
		})
	}
}
