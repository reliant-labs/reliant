package obsbackend

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/obsquery"
)

func TestLocalUserActivityRefusesWhatDoesNotApply(t *testing.T) {
	l := localWith(&fakeRunner{})
	if _, err := l.UserActivity(context.Background(), UserActivityRequest{Narrow: Narrow{Range: rng().Range, Filters: []Filter{{Key: "k", Value: "v"}}}}); exitOf(err) != ExitWrong || !strings.Contains(err.Error(), "--filter") {
		t.Errorf("a filter on users: %v", err)
	}
	for _, days := range []int{-1, obsquery.MaxUserActivitySeriesDays + 1} {
		if _, err := l.UserActivity(context.Background(), UserActivityRequest{Narrow: rng(), SeriesDays: days}); exitOf(err) != ExitWrong || !strings.Contains(err.Error(), "--series must be 0-") {
			t.Errorf("series %d: %v", days, err)
		}
	}
}

func TestLocalUserActivityExplainsAndMapsTheAnswer(t *testing.T) {
	fr := &fakeRunner{}
	res, err := localWith(fr).UserActivity(context.Background(), UserActivityRequest{Narrow: explainNarrow()})
	if err != nil || len(res.Statements) != 1 || res.Statements[0].Purpose != "users" || len(fr.ran) != 0 {
		t.Errorf("explain = %+v, %v", res, err)
	}

	first := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	row := func(kind, env, day string, active, nw, ret int, firstAt time.Time, retention int, flag int) []any {
		return []any{kind, env, day, num64(itoaN(active)), num64(itoaN(nw)), num64(itoaN(ret)), []any{"web", "ios"}, firstAt.Format("2006-01-02 15:04:05"), num64(itoaN(retention)), num64(itoaN(flag))}
	}
	fr = &fakeRunner{answers: []fakeAnswer{{"product_events", Result{Rows: [][]any{
		row("cur", "env-1", "", 10, 4, 6, time.Unix(0, 0).UTC(), 0, 0),
		row("prev", "env-1", "", 8, 2, 6, time.Unix(0, 0).UTC(), 0, 0),
		row("env", "env-1", "", 0, 0, 0, first, 90, 0),
		row("rule", "env-1", "", 0, 0, 0, first, 0, 1),
		row("seen", "env-1", "", 25, 0, 0, first, 90, 0),
	}}}}}
	to := time.Date(2026, 10, 22, 11, 0, 0, 0, time.UTC)
	res, err = localWith(fr).UserActivity(context.Background(), UserActivityRequest{
		Narrow:        Narrow{Range: Range{From: to.Add(-7 * 24 * time.Hour), To: to}},
		SeriesDays:    2,
		WantSeenTotal: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Envs) != 1 {
		t.Fatalf("envs = %+v", res.Envs)
	}
	e := res.Envs[0]
	if e.EnvID != "env-1" || e.Current != (UserCounts{Active: 10, New: 4, Returning: 6}) || e.Previous != (UserCounts{Active: 8, New: 2, Returning: 6}) ||
		e.SeenTotal == nil || *e.SeenTotal != 25 || e.RetentionDays != 90 || !e.FirstEventAt.Equal(first) || e.NewRule != "signups" ||
		strings.Join(e.Clients, ",") != "ios,web" || len(e.Days) != 2 {
		t.Errorf("env = %+v", e)
	}
}

func itoaN(n int) string { return strconv.Itoa(n) }

func TestLocalUserActivityMapsFailuresOntoTheExitTable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		exit int
		msg  string
	}{
		{"missing table", Undetermined("Code: 60 (UNKNOWN_TABLE)"), ExitUndetermined, "no product_events table"},
		{"the other wording", Undetermined("Unknown table"), ExitUndetermined, "no product_events table"},
		{"a backend error passes through", Undetermined("the container is down"), ExitUndetermined, "the container is down"},
		{"invalid", obsquery.ErrInvalid, ExitWrong, ""},
		{"anything else", errors.New("boom"), ExitUndetermined, "user activity forge cannot read"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := localWith(&fakeRunner{err: tc.err}).UserActivity(context.Background(), UserActivityRequest{Narrow: rng()})
			if exitOf(err) != tc.exit || !strings.Contains(err.Error(), tc.msg) {
				t.Errorf("err = %v (exit %d), want exit %d containing %q", err, exitOf(err), tc.exit, tc.msg)
			}
		})
	}
	if usersErr(nil) != nil {
		t.Error("usersErr(nil) must be nil")
	}
}

func TestCloudUserActivityMapsTheWireAnswer(t *testing.T) {
	f := &fakeCaller{replies: map[string]string{procGetUserActivity: `{"envs":[
		{"envId":"env-1","current":{"active":"10","new":"4","returning":"6"},"previous":{"active":8,"new":2,"returning":6},
		 "days":[{"day":"2026-10-21","active":"3","new":"1","returning":"2"}],
		 "seenTotal":"25","retentionDays":90,"firstEventAt":"2026-09-01T10:00:00+02:00","clients":["web"]},
		{"envId":"env-2","current":{},"previous":{}}]}`}}
	res, err := cloudWith(f).UserActivity(context.Background(), UserActivityRequest{Narrow: rng(), SeriesDays: 7, WantSeenTotal: true})
	if err != nil || len(res.Envs) != 2 {
		t.Fatalf("UserActivity = %+v, %v", res, err)
	}
	e := res.Envs[0]
	if e.Current != (UserCounts{Active: 10, New: 4, Returning: 6}) || e.Previous.Active != 8 || *e.SeenTotal != 25 || e.RetentionDays != 90 ||
		!e.FirstEventAt.Equal(time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)) || e.FirstEventAt.Location() != time.UTC ||
		len(e.Days) != 1 || e.Days[0].Day != "2026-10-21" || e.Days[0].Active != 3 || len(e.Clients) != 1 {
		t.Errorf("env = %+v", e)
	}
	if bare := res.Envs[1]; bare.SeenTotal != nil || !bare.FirstEventAt.IsZero() || len(bare.Days) != 0 {
		t.Errorf("an env with nothing reported = %+v", bare)
	}
	req := f.reqs[procGetUserActivity]
	if req["seriesDays"] != float64(7) || req["wantSeenTotal"] != true {
		t.Errorf("request = %v", req)
	}

	if _, err := cloudWith(f).UserActivity(context.Background(), UserActivityRequest{Narrow: Narrow{Range: rng().Range, Filters: []Filter{{Key: "k"}}}}); exitOf(err) != ExitWrong {
		t.Errorf("a filter on users: %v", err)
	}
	failing := &fakeCaller{errs: map[string]error{procGetUserActivity: errors.New("down")}}
	if _, err := cloudWith(failing).UserActivity(context.Background(), UserActivityRequest{Narrow: rng()}); err == nil {
		t.Error("a failed call must be an error")
	}
	// --explain compiles locally and never calls.
	idle := &fakeCaller{}
	res, err = cloudWith(idle).UserActivity(context.Background(), UserActivityRequest{Narrow: explainNarrow()})
	if err != nil || len(res.Statements) == 0 || len(idle.reqs) != 0 {
		t.Errorf("explain = %+v, %v; calls %v", res, err, idle.reqs)
	}
}
