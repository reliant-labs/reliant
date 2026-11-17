package obsquery

import (
	"flag"
	"os"
	"strings"
	"testing"
	"time"
)

var updateUserActivity = flag.Bool("update-user-activity", false, "rewrite testdata/user_activity*.golden.sql")

// golden compares got with testdata/<name>, or rewrites it under
// -update-user-activity. The fragments are shared with the control plane's
// GetUserActivity and the generated dashboards, so a change to them is a
// reviewed diff, not a silent one.
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := "testdata/" + name
	if *updateUserActivity {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update-user-activity to create it)", err)
	}
	if got != string(want) {
		t.Errorf("%s changed (run with -update-user-activity if that is intended):\n%s", path, got)
	}
}

func userActivityQuery() UserActivityQuery {
	end := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	return UserActivityQuery{
		Scope: Scope{Database: "t_org", EnvRestricted: true, EnvIDs: []string{"env-prod"}, Services: []string{"api"},
			Start: end.Add(-7 * 24 * time.Hour), End: end},
		SeriesDays: 30, WantSeenTotal: true,
	}
}

func TestUserActivityFragmentsAreGolden(t *testing.T) {
	golden(t, "user_activity_fragments.golden.sql",
		"-- UserActivityEvidence\n"+UserActivityEvidence+"\n\n-- NewUsersSource\n"+
			NewUsersSource("product_events", "{to:DateTime64(9)}", "(empty({env_ids:Array(String)}) OR has({env_ids:Array(String)}, EnvId))")+"\n")
}

func TestUserActivityStatementIsGolden(t *testing.T) {
	st, err := userActivityQuery().Compile()
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "user_activity.golden.sql", st.SQL+"\n")
	for _, frag := range []string{UserActivityEvidence, "max(has_signup) OVER (PARTITION BY EnvId)"} {
		if !strings.Contains(st.SQL, frag) {
			t.Errorf("the statement does not compile from the shared fragment %q", frag)
		}
	}
}

func TestUserActivityWindow(t *testing.T) {
	q := userActivityQuery()
	w := q.window()
	if !w.prevFrom.Equal(q.Scope.Start.Add(-7*24*time.Hour)) || !w.curFrom.Equal(q.Scope.Start) {
		t.Errorf("previous window = [%v, %v)", w.prevFrom, w.curFrom)
	}
	// 30 days ending with the day of End (2026-11-10).
	if got := w.seriesFrom.Format(time.DateOnly); got != "2026-10-12" || !w.readFrom.Equal(w.seriesFrom) {
		t.Errorf("series from %s, read from %v", got, w.readFrom)
	}
	// An End on midnight belongs to the day before it.
	q.Scope.End = time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	q.Scope.Start = q.Scope.End.Add(-24 * time.Hour)
	q.SeriesDays = 1
	if got := q.window().seriesFrom.Format(time.DateOnly); got != "2026-11-09" {
		t.Errorf("one-day series from %s, want 2026-11-09", got)
	}
}

func TestUserActivityRefusesWhatItCannotBound(t *testing.T) {
	q := userActivityQuery()
	q.SeriesDays = MaxUserActivitySeriesDays + 1
	if _, err := q.Compile(); err == nil {
		t.Error("a series past the cap compiled")
	}
	q = userActivityQuery()
	q.Scope.Start = q.Scope.End.Add(-MaxRangeUserActivity - time.Hour)
	if _, err := q.Compile(); err == nil {
		t.Error("a range past the cap compiled")
	}
	q = userActivityQuery()
	q.Scope.Database = "default"
	if _, err := q.Compile(); err == nil {
		t.Error("an org scope on the server's default database compiled")
	}
}

func TestUserActivityWithoutSeenTotalReadsNoRetention(t *testing.T) {
	q := userActivityQuery()
	q.WantSeenTotal = false
	st, err := q.Compile()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(st.SQL, "'seen'") {
		t.Error("the retention-wide read ran without WantSeenTotal")
	}
}

// The first-seen rule keeps the users of the data's first FirstSeenGraceDays
// out of New, from the same fragment the decided-from moment comes from.
func TestNewUsersSourceExcludesTheDataStart(t *testing.T) {
	src := NewUsersSource("product_events", "{to:DateTime64(9)}", "")
	for _, frag := range []string{
		"min(first_at) OVER (PARTITION BY EnvId) AS data_from",
		"reports = 0 AND first_at >= data_from + INTERVAL 7 DAY",
	} {
		if !strings.Contains(src, frag) {
			t.Errorf("NewUsersSource lacks %q:\n%s", frag, src)
		}
	}
	decided := NewUsersDecidedFrom("product_events", "{to:DateTime64(9)}", "")
	if !strings.Contains(decided, "min(first_at) + INTERVAL 7 DAY) AS DecidedFrom") {
		t.Errorf("NewUsersDecidedFrom does not add the grace to the data's start:\n%s", decided)
	}
	if per := newUsersPerUser("product_events", "{to:DateTime64(9)}", "", UserActivityEvidence); !strings.Contains(src, per) || !strings.Contains(decided, per) {
		t.Error("NewUsersSource and NewUsersDecidedFrom read different rows")
	}
}

func TestNewDecided(t *testing.T) {
	from := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	to := from.Add(30 * 24 * time.Hour)
	cases := []struct {
		name        string
		decidedFrom time.Time
		want        NewDecision
	}{
		{"data start inside the range", time.Date(2026, 10, 31, 14, 3, 0, 0, time.UTC), NewDecidedPart},
		{"data start before the range", time.Date(2026, 9, 8, 9, 0, 0, 0, time.UTC), NewDecidedAll},
		{"decided exactly at the range's start", from, NewDecidedAll},
		{"sign-up event present", time.Time{}, NewDecidedAll},
		{"no events", time.Time{}, NewDecidedAll},
		{"decided at the range's end", to, NewDecidedNone},
		{"data start after the range", to.Add(48 * time.Hour), NewDecidedNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NewDecided(tc.decidedFrom, from, to); got != tc.want {
				t.Errorf("NewDecided(%v) = %s, want %s", tc.decidedFrom, got, tc.want)
			}
		})
	}
}

// Parse reads each window's decided-from moment off the rule rows.
func TestUserActivityParseReadsNewDecidedFrom(t *testing.T) {
	epoch := time.Unix(0, 0).UTC()
	decided := time.Date(2026, 10, 31, 14, 3, 0, 0, time.UTC)
	prevDecided := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	rule := func(kind string, at time.Time, reports uint8) []any {
		return []any{kind, "env-prod", "", uint64(0), uint64(0), uint64(0), []string{}, at, uint32(0), reports}
	}
	cur := func(active, nw, returning uint64) []any {
		return []any{"cur", "env-prod", "", active, nw, returning, []string{}, epoch, uint32(0), uint8(0)}
	}
	cases := []struct {
		name          string
		rows          [][]any
		rule          NewUsersRule
		decided, prev time.Time
		current       UserActivityCounts
	}{
		{name: "data start inside the range",
			rows: [][]any{cur(4, 2, 2), rule("rule", decided, 0), rule("rule_prev", prevDecided, 0)},
			rule: NewUsersFirstSeen, decided: decided, prev: prevDecided, current: UserActivityCounts{Active: 4, New: 2, Returning: 2}},
		{name: "data start before the range",
			rows: [][]any{cur(3, 1, 2), rule("rule", prevDecided, 0)},
			rule: NewUsersFirstSeen, decided: prevDecided, current: UserActivityCounts{Active: 3, New: 1, Returning: 2}},
		{name: "sign-up event present",
			rows: [][]any{cur(3, 1, 2), rule("rule", epoch, 1), rule("rule_prev", epoch, 1)},
			rule: NewUsersSignups, current: UserActivityCounts{Active: 3, New: 1, Returning: 2}},
		{name: "no events", rule: NewUsersFirstSeen},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := userActivityQuery().Parse(&fakeRows{rows: tc.rows})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 {
				t.Fatalf("envs = %+v", got)
			}
			e := got[0]
			if e.NewRule != tc.rule || !e.NewDecidedFrom.Equal(tc.decided) || !e.PreviousNewDecidedFrom.Equal(tc.prev) || e.Current != tc.current {
				t.Errorf("got rule %s decided %v prev %v current %+v; want %s %v %v %+v",
					e.NewRule, e.NewDecidedFrom, e.PreviousNewDecidedFrom, e.Current, tc.rule, tc.decided, tc.prev, tc.current)
			}
		})
	}
}
