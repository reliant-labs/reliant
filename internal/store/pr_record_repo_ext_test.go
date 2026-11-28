// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

const (
	prShaA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	prShaB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

var prBase = time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

type prChange struct {
	key     string
	updated int // minutes after prBase
	head    string
	labels  []string
	add     string
	rm      string
	full    bool
	state   string
	delta   core.PRRecordDelta
}

// prIDs numbers delivery ids; atomic, because the tests run in parallel.
var prIDs atomic.Int64

func prNewID() string {
	return fmt.Sprintf("id-%d", prIDs.Add(1))
}

func (c prChange) change() core.PRRecordChange {
	state := c.state
	if state == "" {
		state = core.PRRecordOpen
	}
	d := c.delta
	if c.add != "" || c.rm != "" {
		d.LabelAdded, d.LabelRemoved = c.add, c.rm
	}
	return core.PRRecordChange{
		Delivery: core.PRRecordDelivery{Provider: "github", DeliveryKey: c.key, EventType: "pull_request.x", AppliedAt: prBase},
		Snapshot: &core.PRRecordSnapshot{
			ID: prNewID(), Provider: "github", AccountKey: "inst", RepositoryID: "42", Repository: "Acme/Widgets",
			Number: 7, State: state, HeadSHA: c.head, Labels: c.labels, Delta: d,
			SnapshotAt: prBase.Add(time.Duration(c.updated) * time.Minute), ObservedAt: prBase.Add(time.Hour), Full: c.full,
		},
		HeadID: prNewID(),
	}
}

func applyPR(t *testing.T, env *storetest.Env, c core.PRRecordChange) bool {
	t.Helper()
	ok, err := store.ApplyPRRecordChange(context.Background(), env.ORM, c)
	if err != nil {
		t.Fatalf("apply %s: %v", c.Delivery.DeliveryKey, err)
	}
	return ok
}

func getPR(t *testing.T, env *storetest.Env) *core.PRRecord {
	t.Helper()
	rec, err := store.GetPRRecord(context.Background(), env.ORM, "github", "acme/widgets", 7, []string{"42"})
	if err != nil || rec == nil {
		t.Fatalf("get: %v %v", rec, err)
	}
	return rec
}

// TestPRRecordSnapshotOrdering: a newer snapshot replaces, an older one
// delivered late changes nothing, a tie applies only its label delta, and a
// redelivery applies nothing.
func TestPRRecordSnapshotOrdering(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()

	if !applyPR(t, env, prChange{key: "d1", updated: 5, head: prShaA, labels: []string{"b", "a"}, full: true}.change()) {
		t.Fatal("first delivery not applied")
	}
	rec := getPR(t, env)
	if rec.HeadSHA != prShaA || !slices.Equal(rec.Labels, []string{"a", "b"}) || rec.Repository != "Acme/Widgets" {
		t.Fatalf("record = %+v", rec)
	}

	// A redelivery of d1, even carrying other content, is a no-op.
	if applyPR(t, env, prChange{key: "d1", updated: 50, head: prShaB, full: true}.change()) {
		t.Fatal("redelivery applied")
	}
	// An older snapshot delivered late changes nothing.
	applyPR(t, env, prChange{key: "d0", updated: 1, head: prShaB, labels: []string{"old"}, full: true}.change())
	if rec = getPR(t, env); rec.HeadSHA != prShaA || !slices.Equal(rec.Labels, []string{"a", "b"}) {
		t.Fatalf("stale snapshot applied: %+v", rec)
	}
	// Two label events in the same second: each applies its delta.
	applyPR(t, env, prChange{key: "d2", updated: 5, head: prShaA, labels: []string{"a", "b", "c"}, add: "c", full: true}.change())
	applyPR(t, env, prChange{key: "d3", updated: 5, head: prShaA, labels: []string{"b"}, rm: "a", full: true}.change())
	if rec = getPR(t, env); !slices.Equal(rec.Labels, []string{"b", "c"}) {
		t.Fatalf("tie deltas: labels = %v, want [b c]", rec.Labels)
	}
	// A slim snapshot (a review's) never overwrites an existing record.
	applyPR(t, env, prChange{key: "d4", updated: 30, head: prShaB, state: core.PRRecordClosed}.change())
	if rec = getPR(t, env); rec.State != core.PRRecordOpen || rec.HeadSHA != prShaA {
		t.Fatalf("slim snapshot applied: %+v", rec)
	}
	// A newer push moves the head; the old head is superseded.
	c := prChange{key: "d5", updated: 10, head: prShaB, full: true}.change()
	c.PreviousHeadSHA = prShaA
	applyPR(t, env, c)
	heads, err := store.ListPRRecordHeads(ctx, env.ORM, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(heads) != 2 {
		t.Fatalf("heads = %+v", heads)
	}
	for _, h := range heads {
		switch h.HeadSHA {
		case prShaA:
			if h.SupersededAt == nil {
				t.Errorf("old head not superseded: %+v", h)
			}
		case prShaB:
			if h.SupersededAt != nil || h.PreviousHeadSHA != prShaA {
				t.Errorf("new head = %+v", h)
			}
		}
	}

	list, err := store.ListPRRecords(ctx, env.ORM, core.PRRecordFilter{Provider: "github", Repository: "ACME/widgets", State: core.PRRecordOpen, Limit: 10})
	if err != nil || len(list) != 1 || list[0].HeadSHA != prShaB {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if n, err := store.PrunePRRecordDeliveries(ctx, env.ORM, prBase.Add(time.Minute)); err != nil || n != 6 {
		t.Fatalf("prune = %d, %v", n, err)
	}
}

const prShaC = "cccccccccccccccccccccccccccccccccccccccc"

// TestPRRecordSameSecondTransitions: GitHub's updated_at has one-second
// resolution, so two events of one pull request may carry the same snapshot
// time. Each applies its own transition, in either order, and a full snapshot
// completes a slim one of the same second.
func TestPRRecordSameSecondTransitions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	yes, no := true, false

	t.Run("a full snapshot completes a slim one", func(t *testing.T) {
		env := storetest.New(t)
		applyPR(t, env, prChange{key: "review", updated: 5, head: prShaA}.change())
		applyPR(t, env, prChange{key: "closed", updated: 5, head: prShaA, full: true, state: core.PRRecordMerged}.change())
		if rec := getPR(t, env); rec.State != core.PRRecordMerged {
			t.Fatalf("state = %s, want merged", rec.State)
		}
	})

	t.Run("close, draft and edit transitions", func(t *testing.T) {
		env := storetest.New(t)
		applyPR(t, env, prChange{key: "opened", updated: 5, head: prShaA, full: true, labels: []string{"x"}}.change())
		applyPR(t, env, prChange{key: "ready", updated: 5, head: prShaA, full: true, delta: core.PRRecordDelta{Draft: &no}}.change())
		applyPR(t, env, prChange{key: "draft", updated: 5, head: prShaA, full: true, delta: core.PRRecordDelta{Draft: &yes}}.change())
		applyPR(t, env, prChange{key: "edited", updated: 5, head: prShaA, full: true, delta: core.PRRecordDelta{Title: "renamed", BaseRef: "release"}}.change())
		merged := prBase.Add(5 * time.Minute)
		applyPR(t, env, prChange{key: "merged", updated: 5, head: prShaA, full: true, state: core.PRRecordMerged,
			delta: core.PRRecordDelta{State: core.PRRecordMerged, MergedAt: &merged, MergeCommit: prShaC}}.change())
		// A reopen of the same second never takes a record out of merged.
		applyPR(t, env, prChange{key: "reopened", updated: 5, head: prShaA, full: true, delta: core.PRRecordDelta{State: core.PRRecordOpen}}.change())
		rec := getPR(t, env)
		if rec.State != core.PRRecordMerged || rec.MergedAt == nil || rec.MergeCommit != prShaC || !rec.Draft ||
			rec.Title != "renamed" || rec.BaseRef != "release" || !slices.Equal(rec.Labels, []string{"x"}) {
			t.Fatalf("record = %+v", rec)
		}
	})

	push := func(key, from, to string) core.PRRecordChange {
		c := prChange{key: key, updated: 5, head: to, full: true, delta: core.PRRecordDelta{FromHead: from, HeadSHA: to}}.change()
		c.PreviousHeadSHA = from
		return c
	}
	// A force-push back to a sha the pull request had before, in the same
	// second as a snapshot at the head it replaced, must not follow the
	// earlier push out of that sha: that edge is history, not a pending push.
	t.Run("a same-second force-push back to an earlier head stays there", func(t *testing.T) {
		env := storetest.New(t)
		applyPR(t, env, prChange{key: "opened", updated: 3, head: prShaA, full: true}.change())
		ab := prChange{key: "ab", updated: 4, head: prShaB, full: true, delta: core.PRRecordDelta{FromHead: prShaA, HeadSHA: prShaB}}.change()
		ab.PreviousHeadSHA = prShaA
		applyPR(t, env, ab)
		applyPR(t, env, prChange{key: "labeled", updated: 5, head: prShaB, full: true, labels: []string{"x"}}.change())
		applyPR(t, env, push("ba", prShaB, prShaA))
		rec := getPR(t, env)
		if rec.HeadSHA != prShaA {
			t.Fatalf("head = %s, want the force-push's %s", rec.HeadSHA[:4], prShaA[:4])
		}
		heads, err := store.ListPRRecordHeads(ctx, env.ORM, rec.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, h := range heads {
			if (h.HeadSHA == prShaA) != (h.SupersededAt == nil) {
				t.Errorf("head %s superseded_at = %v", h.HeadSHA[:4], h.SupersededAt)
			}
		}
	})
	for _, order := range [][]string{{"ab", "bc"}, {"bc", "ab"}} {
		t.Run("two pushes in one second, "+order[0]+" first", func(t *testing.T) {
			env := storetest.New(t)
			applyPR(t, env, prChange{key: "opened", updated: 5, head: prShaA, full: true}.change())
			pushes := map[string]core.PRRecordChange{"ab": push("ab", prShaA, prShaB), "bc": push("bc", prShaB, prShaC)}
			for _, k := range order {
				applyPR(t, env, pushes[k])
			}
			rec := getPR(t, env)
			if rec.HeadSHA != prShaC {
				t.Fatalf("head = %s, want the last push's", rec.HeadSHA)
			}
			heads, err := store.ListPRRecordHeads(ctx, env.ORM, rec.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, h := range heads {
				if (h.HeadSHA == prShaC) != (h.SupersededAt == nil) {
					t.Errorf("head %s superseded_at = %v", h.HeadSHA[:4], h.SupersededAt)
				}
			}
		})
	}
}

// TestPRRecordConcurrentFirstDeliveries: deliveries of one new pull request
// racing each other create one record, and the newest snapshot wins.
func TestPRRecordConcurrentFirstDeliveries(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	var wg sync.WaitGroup
	changes := make([]core.PRRecordChange, 8)
	for i := range changes {
		changes[i] = prChange{key: fmt.Sprintf("c%d", i), updated: i, head: prShaA, labels: []string{fmt.Sprint(i)}, full: true}.change()
	}
	errs := make(chan error, len(changes))
	for _, c := range changes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := store.ApplyPRRecordChange(context.Background(), env.ORM, c); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if rec := getPR(t, env); !slices.Equal(rec.Labels, []string{"7"}) {
		t.Fatalf("labels = %v, want the newest snapshot's", rec.Labels)
	}
}

// TestPRRecordReviewsAndChecks: review markers are kept once, and the newest
// run of a check wins regardless of delivery order.
func TestPRRecordReviewsAndChecks(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()
	c := prChange{key: "r1", updated: 1, head: prShaA}.change()
	c.Reviews = []core.PRRecordReview{{
		ID: prNewID(), ReviewID: "900", CommitSHA: prShaA, MarkerSHA: prShaA, Kind: core.PRReviewPassed,
		AuthorLogin: "bot", SubmittedAt: prBase, ObservedAt: prBase,
	}}
	applyPR(t, env, c)
	// The same review delivered again under another delivery id adds no row.
	c2 := prChange{key: "r2", updated: 1, head: prShaA}.change()
	c2.Reviews = []core.PRRecordReview{c.Reviews[0]}
	c2.Reviews[0].ID = prNewID()
	applyPR(t, env, c2)
	rec := getPR(t, env)
	reviews, err := store.ListPRRecordReviews(ctx, env.ORM, rec.ID, []string{prShaA})
	if err != nil || len(reviews) != 1 || reviews[0].ReviewID != "900" || reviews[0].Kind != core.PRReviewPassed {
		t.Fatalf("reviews = %+v, %v", reviews, err)
	}

	done := prBase.Add(time.Minute)
	check := func(key string, run int64, conclusion string, completed *time.Time) core.PRRecordChange {
		return core.PRRecordChange{
			Delivery: core.PRRecordDelivery{Provider: "github", DeliveryKey: key, EventType: "check_run.completed", AppliedAt: prBase},
			Check: &core.PRRecordCheck{
				ID: prNewID(), Provider: "github", RepositoryID: "42", HeadSHA: prShaA, Name: "Reliant review",
				AppSlug: "github-actions", CheckRunID: run, Status: "completed", Conclusion: conclusion,
				CompletedAt: completed, ObservedAt: prBase,
			},
		}
	}
	applyPR(t, env, check("k2", 2, "success", &done))
	applyPR(t, env, check("k1", 1, "failure", &done)) // an older run, delivered late
	applyPR(t, env, check("k3", 2, "", nil))          // the same run's start, delivered late
	checks, err := store.ListPRRecordChecks(ctx, env.ORM, "github", "42", []string{prShaA})
	if err != nil || len(checks) != 1 || checks[0].CheckRunID != 2 || checks[0].Conclusion != "success" {
		t.Fatalf("checks = %+v, %v", checks, err)
	}
}

// TestHasFreshIntegrationAccess reads the routing grants.
func TestHasFreshIntegrationAccess(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()
	at := prBase
	if err := store.ReplaceIntegrationAccess(ctx, env.Repo.ORM(), storetest.SeedUserID, "github", "99", at,
		[]core.IntegrationAccessGrant{{AccountKey: "inst", ResourceKey: "42"}}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		resource string
		after    time.Time
		want     bool
	}{
		{"42", at.Add(-time.Minute), true},
		{"43", at.Add(-time.Minute), false},
		{"42", at.Add(time.Minute), false},
	} {
		got, err := store.HasFreshIntegrationAccess(ctx, env.ORM, storetest.SeedUserID, "github", "inst", tc.resource, tc.after)
		if err != nil || got != tc.want {
			t.Errorf("%+v: got %v, %v", tc, got, err)
		}
	}
}
