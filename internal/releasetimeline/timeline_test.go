package releasetimeline

import (
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func at(min int) time.Time { return t0.Add(time.Duration(min) * time.Minute) }

func sha(c byte) string { return strings.Repeat(string(c), 40) }

// rel is a release cut at minute min whose range is based on base.
func rel(version string, min int, commit byte, base string, baseCommit byte) release.Release {
	r := release.Release{
		Version: version, CreatedAt: at(min),
		Git: release.Git{Commit: sha(commit)},
	}
	if base != "" {
		r.Commits = &release.CommitRange{
			BaseRelease: base, BaseCommit: sha(baseCommit), Total: 1,
			Commits: []release.Commit{{SHA: sha(commit), Subject: "x"}},
		}
	}
	return r
}

func promo(id, version string, min int) release.Promotion {
	return release.Promotion{ID: id, Env: "prod", Release: version, Kind: release.KindPromote, PromotedAt: at(min)}
}

// chain is v1 <- v2 <- v3, cut in that order, ranges chained by base.
func chain() []release.Release {
	return []release.Release{
		rel("v1", 0, 'a', "", 0),
		rel("v2", 10, 'b', "v1", 'a'),
		rel("v3", 20, 'c', "v2", 'b'),
	}
}

func TestAssemble_Directions(t *testing.T) {
	tests := []struct {
		name  string
		promo []release.Promotion
		want  []Direction // oldest first
	}{
		{"first promotion is initial", []release.Promotion{promo("p1", "v1", 30)}, []Direction{DirInitial}},
		{"forward moves are ahead", []release.Promotion{promo("p1", "v1", 30), promo("p2", "v2", 40), promo("p3", "v3", 50)},
			[]Direction{DirInitial, DirAhead, DirAhead}},
		{"promoting an older release is behind", []release.Promotion{promo("p1", "v3", 30), promo("p2", "v1", 40)},
			[]Direction{DirInitial, DirBehind}},
		{"roll forward after a rollback is ahead again", []release.Promotion{promo("p1", "v2", 30), promo("p2", "v1", 40), promo("p3", "v3", 50)},
			[]Direction{DirInitial, DirBehind, DirAhead}},
		{"re-promoting the bound release is same", []release.Promotion{promo("p1", "v2", 30), promo("p2", "v2", 40)},
			[]Direction{DirInitial, DirSame}},
		{"a release the ledger lacks is unknown, never guessed", []release.Promotion{promo("p1", "v2", 30), promo("p2", "ghost", 40)},
			[]Direction{DirInitial, DirUnknown}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tl := Assemble(Data{Env: "prod", Promotions: tc.promo, Releases: chain()}, time.Time{}, 0)
			if len(tl.Entries) != len(tc.want) {
				t.Fatalf("got %d entries, want %d", len(tl.Entries), len(tc.want))
			}
			for i, w := range tc.want {
				// Entries are newest first.
				if got := tl.Entries[len(tc.want)-1-i].Direction; got != w {
					t.Errorf("promotion %d direction = %q, want %q", i, got, w)
				}
			}
		})
	}
}

func TestAssemble_BehindRowCarriesTheRevertedRange(t *testing.T) {
	first := promo("p1", "v3", 30)
	first.BundleID = "b-p1"
	d := Data{Env: "prod", Releases: chain(), Promotions: []release.Promotion{first, promo("p2", "v1", 40)}}
	e := Assemble(d, time.Time{}, 0).Entries[0]
	if e.Direction != DirBehind || e.Range == nil {
		t.Fatalf("entry = %+v", e)
	}
	if !e.Range.Reverts || e.Range.FromRelease != "v3" || e.Range.ToRelease != "v1" || e.Range.Total != 2 {
		t.Errorf("range = %+v, want a reverting v3->v1 range of 2 commits", *e.Range)
	}
	if e.PreviousVersion != "v3" || e.PreviousBundleID != "b-p1" {
		t.Errorf("previous = %q", e.PreviousVersion)
	}
}

func TestAssemble_DirectionFallsBackToCutTimeWhenRangesDoNotChain(t *testing.T) {
	// No recorded ranges at all: only cut time can place them.
	rels := []release.Release{rel("20261001.old", 0, 'a', "", 0), rel("v9", 10, 'b', "", 0)}
	d := Data{Env: "prod", Releases: rels, Promotions: []release.Promotion{promo("p1", "v9", 30), promo("p2", "20261001.old", 40)}}
	tl := Assemble(d, time.Time{}, 0)
	if got := tl.Entries[0].Direction; got != DirBehind {
		t.Errorf("direction = %q, want behind (older cut time)", got)
	}
	if !tl.Entries[0].Range.Truncated {
		t.Error("a range not derived from recorded ranges must say it is incomplete")
	}
}

func TestAssemble_LiveIsTheNewestPromotion(t *testing.T) {
	d := Data{Env: "prod", Releases: chain(), Promotions: []release.Promotion{promo("p1", "v1", 30), promo("p2", "v2", 40)}}
	tl := Assemble(d, time.Time{}, 0)
	if !tl.Entries[0].Live || tl.Entries[1].Live {
		t.Errorf("live flags = %v %v", tl.Entries[0].Live, tl.Entries[1].Live)
	}
}

func TestAssemble_SameTimestampKeepsLedgerOrder(t *testing.T) {
	// The file ledger stamps to the second: two promotions in one second
	// must not swap, or the newer one would not be live.
	d := Data{Env: "prod", Releases: chain(), Promotions: []release.Promotion{promo("p1", "v2", 30), promo("p2", "v1", 30)}}
	tl := Assemble(d, time.Time{}, 0)
	if tl.Entries[0].Version != "v1" || !tl.Entries[0].Live || tl.Entries[0].Direction != DirBehind {
		t.Errorf("newest = %+v", tl.Entries[0])
	}
}

func TestAssemble_FailedAndTimedOutDeploysAreRows(t *testing.T) {
	d := Data{
		Env: "prod", Releases: chain(),
		Promotions: []release.Promotion{promo("p1", "v1", 30), promo("p2", "v2", 40)},
		Attempts: []Attempt{
			{ID: "a1", PromotionID: "p1", Status: StatusSucceeded, StartedAt: at(30), FinishedAt: at(33)},
			{ID: "a2", PromotionID: "p2", Status: StatusFailed, StartedAt: at(40), FinishedAt: at(46), Reason: "HealthCheckFailed", Cluster: "c1"},
			{ID: "a3", PromotionID: "p2", Status: StatusTimedOut, StartedAt: at(50), FinishedAt: at(60)},
		},
	}
	tl := Assemble(d, time.Time{}, 0)
	var kinds []EntryKind
	for _, e := range tl.Entries {
		kinds = append(kinds, e.Kind)
	}
	want := []EntryKind{KindFailedDeploy, KindFailedDeploy, KindPromotion, KindPromotion}
	if len(kinds) != len(want) {
		t.Fatalf("kinds = %v", kinds)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("kinds = %v, want %v", kinds, want)
		}
	}
	timedOut, failed := tl.Entries[0], tl.Entries[1]
	if timedOut.Rollout.Status != StatusTimedOut || timedOut.Rollout.DurationSeconds != 600 {
		t.Errorf("timed-out row = %+v", *timedOut.Rollout)
	}
	if failed.Version != "v2" || failed.Rollout.Reason != "HealthCheckFailed" || failed.Rollout.DurationSeconds != 360 {
		t.Errorf("failed row = %+v / %+v", failed, *failed.Rollout)
	}
	// The promotion that did succeed carries its rollout duration.
	p1 := tl.Entries[3]
	if p1.Rollout == nil || p1.Rollout.Status != StatusSucceeded || p1.Rollout.DurationSeconds != 180 {
		t.Errorf("p1 rollout = %+v", p1.Rollout)
	}
	// p2 never succeeded: no rollout on the promotion row, only the failure rows.
	if tl.Entries[2].Rollout != nil {
		t.Errorf("p2 must not claim a rollout: %+v", tl.Entries[2].Rollout)
	}
}

func TestAssemble_AttemptForAnUnseenPromotionStillShows(t *testing.T) {
	d := Data{Env: "prod", Attempts: []Attempt{{ID: "a", PromotionID: "gone", Status: StatusFailed, FinishedAt: at(5)}}}
	tl := Assemble(d, time.Time{}, 0)
	if len(tl.Entries) != 1 || tl.Entries[0].Kind != KindFailedDeploy {
		t.Fatalf("entries = %+v", tl.Entries)
	}
}

func TestAssemble_GatesClaimedAndRecordedWithWorstVerdict(t *testing.T) {
	p := promo("p1", "v1", 30)
	p.Gates = []release.Gate{{Name: "lint", Status: release.GateStatusPassed}}
	p.RecordedGates = []release.Gate{
		{Name: "smoke", Status: release.GateStatusFailed, Summary: "502"},
		{Name: "legacy", Status: release.GateStatusError, RawStatus: "green"},
	}
	e := Assemble(Data{Env: "prod", Promotions: []release.Promotion{p}}, time.Time{}, 0).Entries[0]
	if len(e.Gates) != 3 {
		t.Fatalf("gates = %+v", e.Gates)
	}
	if e.Gates[0].Phase != PhaseClaimed || e.Gates[1].Phase != PhaseRecorded {
		t.Errorf("phases = %q %q", e.Gates[0].Phase, e.Gates[1].Phase)
	}
	if e.GateVerdict != "failed" {
		t.Errorf("verdict = %q, want failed", e.GateVerdict)
	}

	cases := []struct {
		name  string
		gates []release.Gate
		want  string
	}{
		{"none", nil, ""},
		{"all pass", []release.Gate{{Name: "a", Status: release.GateStatusPassed}}, "passed"},
		{"skipped alone is not a pass", []release.Gate{{Name: "a", Status: release.GateStatusSkipped}}, "skipped"},
		{"an unrecognised stored status never reads as a pass",
			[]release.Gate{{Name: "a", Status: release.GateStatusPassed}, {Name: "b", Status: release.GateStatusError, RawStatus: "ok!"}}, "error"},
		{"a failure outranks an error",
			[]release.Gate{{Name: "a", Status: release.GateStatusError}, {Name: "b", Status: release.GateStatusFailed}}, "failed"},
	}
	for _, tc := range cases {
		q := promo("p", "v1", 1)
		q.Gates = tc.gates
		got := Assemble(Data{Env: "prod", Promotions: []release.Promotion{q}}, time.Time{}, 0).Entries[0].GateVerdict
		if got != tc.want {
			t.Errorf("%s: verdict = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestAssemble_WhoAndViaFromActivity(t *testing.T) {
	ps := []release.Promotion{promo("p1", "v1", 30), promo("p2", "v2", 40), promo("p3", "v3", 50)}
	ps[0].PromotedBy = release.Actor{User: "u-1"}
	events := []Activity{
		{Verb: "promote", ActorName: "alice", Via: "cli", Detail: map[string]string{"promotion_id": "p1"}, At: at(30)},
		// No promotion id: joined by release and time.
		{Verb: "promote", ActorName: "ci-bot", Via: "ci", TargetID: "v2", At: at(40).Add(20 * time.Second)},
		// Right release, wrong time: not this promotion's event.
		{Verb: "promote", ActorName: "mallory", Via: "api", TargetID: "v3", At: at(50).Add(time.Hour)},
		{Verb: "scale", ActorName: "bob", TargetID: "v3", At: at(50)},
	}
	tl := Assemble(Data{Env: "prod", Promotions: ps, Activity: events}, time.Time{}, 0)
	got := map[string][2]string{}
	for _, e := range tl.Entries {
		got[e.Version] = [2]string{e.Actor, e.Via}
	}
	if got["v1"] != [2]string{"alice", "cli"} || got["v2"] != [2]string{"ci-bot", "ci"} || got["v3"] != [2]string{"", ""} {
		t.Errorf("who/via = %v", got)
	}
}

func TestAssemble_SinceAndLimitMarkTruncation(t *testing.T) {
	d := Data{Env: "prod", Releases: chain(), Promotions: []release.Promotion{promo("p1", "v1", 30), promo("p2", "v2", 40), promo("p3", "v3", 50)}}
	tl := Assemble(d, at(35), 0)
	if len(tl.Entries) != 2 || tl.Truncated {
		t.Errorf("since: %d entries truncated=%v", len(tl.Entries), tl.Truncated)
	}
	// The first row inside the window still knows what it replaced.
	if tl.Entries[1].Version != "v2" || tl.Entries[1].Direction != DirAhead {
		t.Errorf("v2 = %+v", tl.Entries[1])
	}
	tl = Assemble(d, time.Time{}, 1)
	if len(tl.Entries) != 1 || !tl.Truncated || tl.Entries[0].Version != "v3" {
		t.Errorf("limit: %+v truncated=%v", tl.Entries, tl.Truncated)
	}
}

func TestAssemble_SourceTruncationLeavesTheOldestDirectionUnknown(t *testing.T) {
	d := Data{Env: "prod", Truncated: true, Releases: chain(), Promotions: []release.Promotion{promo("p2", "v2", 40), promo("p3", "v3", 50)}}
	tl := Assemble(d, time.Time{}, 0)
	if !tl.Truncated || tl.Entries[1].Direction != DirUnknown {
		t.Errorf("truncated=%v oldest=%q", tl.Truncated, tl.Entries[1].Direction)
	}
}

func TestAssemble_FirstPromotionShowsTheReleaseOwnRange(t *testing.T) {
	d := Data{Env: "prod", Releases: chain(), Promotions: []release.Promotion{promo("p1", "v2", 30)}}
	e := Assemble(d, time.Time{}, 0).Entries[0]
	if e.Direction != DirInitial || e.Range == nil || e.Range.FromRelease != "v1" || e.Range.Total != 1 {
		t.Errorf("entry = %+v range=%+v", e, e.Range)
	}
}

func TestFind(t *testing.T) {
	d := Data{Env: "prod", Releases: chain(), Promotions: []release.Promotion{promo("p1", "v1", 30), promo("p2", "v2", 40), promo("p3", "v1", 50)}}
	tl := Assemble(d, at(45), 0) // find reads past the since filter
	for ref, wantID := range map[string]string{"p2": "p2", "v1": "p3", "v2": "p2"} {
		e, err := tl.Find(ref)
		if err != nil || e.PromotionID != wantID {
			t.Errorf("Find(%q) = %q, %v; want %q", ref, e.PromotionID, err, wantID)
		}
	}
	if _, err := tl.Find("nope"); err == nil {
		t.Error("Find of an unknown ref must fail")
	}
}

func TestAttachHealth(t *testing.T) {
	d := Data{Env: "prod", Promotions: []release.Promotion{promo("p1", "v1", 30), promo("p2", "v2", 40)}}
	tl := Assemble(d, time.Time{}, 0)
	tl.AttachHealth(map[string]Health{"p2": {Verdict: "regressed"}})
	if tl.Entries[0].Health == nil || tl.Entries[0].Health.Verdict != "regressed" {
		t.Errorf("p2 health = %+v", tl.Entries[0].Health)
	}
	if tl.Entries[1].Health != nil {
		t.Errorf("p1 has no verdict and must show none, got %+v", tl.Entries[1].Health)
	}
	if got, _ := tl.Find("p2"); got.Health == nil {
		t.Error("Find must see attached health")
	}
}

func TestFailuresOf_FindsTheDeploysThatNeverLanded(t *testing.T) {
	d := Data{
		Env: "prod", Releases: chain(),
		Promotions: []release.Promotion{promo("p1", "v1", 30), promo("p2", "v2", 40)},
		Attempts: []Attempt{
			{PromotionID: "p2", Status: StatusFailed, FinishedAt: at(41), Reason: "first"},
			{PromotionID: "p2", Status: StatusTimedOut, FinishedAt: at(50)},
			{PromotionID: "p1", Status: StatusSucceeded, FinishedAt: at(32)},
		},
	}
	tl := Assemble(d, at(100), 0) // every row filtered out; FailuresOf still sees them
	got := tl.FailuresOf("p2")
	if len(got) != 2 || got[0].Rollout.Status != StatusTimedOut || got[1].Rollout.Reason != "first" {
		t.Errorf("got %+v", got)
	}
	if len(tl.FailuresOf("p1")) != 0 || len(tl.FailuresOf("")) != 0 {
		t.Error("a promotion that landed, or no promotion, has no failures")
	}
}
