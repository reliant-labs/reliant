package releasetimeline

import (
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

func atFixture(truncated bool) Timeline {
	d := Data{
		Env: "prod", Releases: chain(), Truncated: truncated,
		Promotions: []release.Promotion{promo("p1", "v1", 30), promo("p2", "v2", 40), promo("p3", "v3", 50)},
		Attempts: []Attempt{
			{PromotionID: "p1", Status: StatusSucceeded, StartedAt: at(30), FinishedAt: at(32)},
			{PromotionID: "p2", Status: StatusSucceeded, StartedAt: at(40), FinishedAt: at(48)},
		},
	}
	return Assemble(d, time.Time{}, 0)
}

func TestAt_Bracketing(t *testing.T) {
	tests := []struct {
		name                string
		when                time.Time
		live, prev, next    string
		before, unsettledIs bool
		unsettled           bool
	}{
		{name: "before the first release", when: at(10), next: "v1", before: true},
		{name: "exactly on a promotion", when: at(40), live: "v2", prev: "v1", next: "v3", unsettled: true},
		{name: "one second before a promotion", when: at(40).Add(-time.Second), live: "v1", next: "v2"},
		{name: "between releases, after the rollout landed", when: at(45 + 4), live: "v2", prev: "v1", next: "v3"},
		{name: "between releases, mid-rollout", when: at(44), live: "v2", prev: "v1", next: "v3", unsettled: true},
		{name: "after the last release is the live one", when: at(500), live: "v3", prev: "v2", unsettled: true},
		{name: "in another zone is the same instant", when: at(40).In(time.FixedZone("x", 5*3600)), live: "v2", prev: "v1", next: "v3", unsettled: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := atFixture(false).At(tc.when)
			name := func(e *Entry) string {
				if e == nil {
					return ""
				}
				return e.Version
			}
			if name(r.Live) != tc.live || name(r.Previous) != tc.prev || name(r.Next) != tc.next {
				t.Errorf("live/prev/next = %q/%q/%q, want %q/%q/%q",
					name(r.Live), name(r.Previous), name(r.Next), tc.live, tc.prev, tc.next)
			}
			if r.BeforeHistory != tc.before {
				t.Errorf("BeforeHistory = %v", r.BeforeHistory)
			}
			if r.Live != nil && r.Unsettled != tc.unsettled {
				t.Errorf("Unsettled = %v, want %v", r.Unsettled, tc.unsettled)
			}
		})
	}
}

func TestAt_BeforeFetchedHistoryIsNotBeforeHistory(t *testing.T) {
	r := atFixture(true).At(at(10))
	if r.BeforeHistory || !r.HistoryIncomplete || r.Live != nil {
		t.Errorf("a source that stopped early cannot say nothing was bound: %+v", r)
	}
}

func TestAt_NoPromotionsAtAll(t *testing.T) {
	r := Assemble(Data{Env: "prod"}, time.Time{}, 0).At(at(1))
	if !r.BeforeHistory || r.Live != nil || r.Next != nil {
		t.Errorf("%+v", r)
	}
}

func TestAt_IgnoresTheSinceFilter(t *testing.T) {
	d := Data{Env: "prod", Releases: chain(), Promotions: []release.Promotion{promo("p1", "v1", 30), promo("p2", "v2", 40)}}
	r := Assemble(d, at(100), 0).At(at(35))
	if r.Live == nil || r.Live.Version != "v1" {
		t.Errorf("live = %+v", r.Live)
	}
}

func TestAt_AFailedRolloutIsUnsettled(t *testing.T) {
	d := Data{
		Env: "prod", Promotions: []release.Promotion{promo("p1", "v1", 30)},
		Attempts: []Attempt{{PromotionID: "p1", Status: StatusFailed, FinishedAt: at(35)}},
	}
	if r := Assemble(d, time.Time{}, 0).At(at(60)); r.Live == nil || !r.Unsettled {
		t.Errorf("%+v", r)
	}
}
