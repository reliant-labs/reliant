package obsbuiltin

import (
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/dashspec"
)

// newUsersSeed is one environment per case of how New is decided. The range
// under test is the 30 days ending 2026-11-13 00:00 UTC (from 10-14 00:00);
// the Overview's week is 11-06..11-13, its previous week 10-30..11-06.
//
//	fresh     reports no sign-ups; its data begins 10-24 14:03, inside the
//	          range, so new users are decided from 10-31 14:03. f1 (first
//	          seen at the start, back 11-10) and f2 (10-27) count returning;
//	          f3 (11-05) and f4 (11-12) are new.
//	mature    reports no sign-ups; its data begins 09-01, before the range,
//	          so the whole range is decided. m1 (09-01, back 11-10) and m3
//	          (10-10, back 11-01) were first seen before it; m2 (10-20) is new.
//	signups   reports sign-ups: unchanged. g1 signed up at the very start of
//	          its data (10-24 14:03) and is new; g2 never signed up; g3
//	          signed up 09-01.
//	brandnew  reports no sign-ups; its data begins 11-10, so nothing in the
//	          range is decided.
//	empty     no events.
//	signedout only a sign-out: rows in scope, none of them UserActivityEvidence.
const newUsersSeed = `
INSERT INTO forge_obsbuiltin_newusers.product_events (Timestamp, ServiceName, EnvId, UserId, EventName, Props, Client) VALUES
    ('2026-10-24 14:03:00', 'api', 'fresh', 'f1', 'task_created', {}, 'backend'),
    ('2026-11-10 09:00:00', 'api', 'fresh', 'f1', 'task_created', {}, 'backend'),
    ('2026-10-27 09:00:00', 'api', 'fresh', 'f2', 'task_created', {}, 'backend'),
    ('2026-11-05 09:00:00', 'api', 'fresh', 'f3', 'task_created', {}, 'backend'),
    ('2026-11-12 09:00:00', 'api', 'fresh', 'f4', 'task_created', {}, 'backend'),
    ('2026-09-01 09:00:00', 'api', 'mature', 'm1', 'task_created', {}, 'backend'),
    ('2026-11-10 09:00:00', 'api', 'mature', 'm1', 'task_created', {}, 'backend'),
    ('2026-10-20 09:00:00', 'api', 'mature', 'm2', 'task_created', {}, 'backend'),
    ('2026-10-10 09:00:00', 'api', 'mature', 'm3', 'task_created', {}, 'backend'),
    ('2026-11-01 09:00:00', 'api', 'mature', 'm3', 'task_created', {}, 'backend'),
    ('2026-10-24 14:03:00', 'api', 'signups', 'g1', 'auth_signed_up', {'method': 'password'}, 'backend'),
    ('2026-10-30 09:00:00', 'api', 'signups', 'g2', 'task_created', {}, 'backend'),
    ('2026-09-01 09:00:00', 'api', 'signups', 'g3', 'auth_signed_up', {'method': 'password'}, 'backend'),
    ('2026-11-01 09:00:00', 'api', 'signups', 'g3', 'task_created', {}, 'backend'),
    ('2026-11-10 09:00:00', 'api', 'brandnew', 'b1', 'task_created', {}, 'backend'),
    ('2026-11-11 09:00:00', 'api', 'brandnew', 'b2', 'task_created', {}, 'backend'),
    ('2026-11-10 09:00:00', 'api', 'signedout', 's1', 'auth_signed_out', {}, 'backend');
`

// TestNewUsersPanelsDecideNewFromFirstSeen runs the new and returning panels
// over each case: first-seen new users exclude the first week of the data,
// and a range with nothing decided shows no figure (\N) instead of a guess.
func TestNewUsersPanelsDecideNewFromFirstSeen(t *testing.T) {
	ch := newClickHouse(t, "forge_obsbuiltin_newusers", newUsersSeed)
	end := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	start := end.Add(-30 * 24 * time.Hour)
	in := fullInput()
	byID := map[string]dashspec.Dashboard{}
	for _, d := range Dashboards(in) {
		byID[d.ID] = d
	}
	byID["overview"] = ExtendOverview(dashspec.Dashboard{ID: "overview", Title: "shop: overview"}, in)
	const plainNote = "sign-ups not reported: new users are the users first seen\t0\t0\t0\t0"

	cases := []struct {
		name, env string
		// want is each panel's TSV answer, by dashboard/panel; "/trend" reads
		// the panel's TrendSQL.
		want map[string]string
	}{
		{name: "data start inside the range", env: "fresh", want: map[string]string{
			"users/active":    "4",
			"users/new":       "2",
			"users/new/trend": "2026-11-05\t1\n2026-11-12\t1",
			"users/returning": "2",
			"users/auth-sources": "sign-ups not reported: new users are the users first seen from 2026-10-31 14:03 UTC, " +
				"7 days after the earliest event retained; a user first seen before then may be returning and counts as returning\t0\t0\t0\t0",
			// This week: f1 back, f4 new; the week before: f3 new.
			"overview/new-7d":       "1\t1",
			"overview/returning-7d": "1\t0",
		}},
		{name: "data start before the range", env: "mature", want: map[string]string{
			"users/active":          "3",
			"users/new":             "1",
			"users/new/trend":       "2026-10-20\t1",
			"users/returning":       "2",
			"users/auth-sources":    plainNote,
			"overview/new-7d":       "0\t0",
			"overview/returning-7d": "1\t1",
		}},
		{name: "sign-up event present", env: "signups", want: map[string]string{
			"users/active":          "3",
			"users/new":             "1",
			"users/new/trend":       "2026-10-24\t1",
			"users/returning":       "2",
			"users/auth-sources":    "backend\t1\t0\t0\t0",
			"overview/new-7d":       "0\t0",
			"overview/returning-7d": "0\t2",
		}},
		{name: "no part of the range decided", env: "brandnew", want: map[string]string{
			"users/active":       "2",
			"users/new":          `\N`,
			"users/returning":    `\N`,
			"users/auth-sources": strings.Replace(plainNote, "first seen", "first seen from 2026-11-17 09:00 UTC, 7 days after the earliest event retained; a user first seen before then may be returning and counts as returning", 1),
			// The week before holds no rows, so it is decided: nobody.
			"overview/new-7d":       "\\N\t0",
			"overview/returning-7d": "\\N\t0",
		}},
		{name: "no events", env: "empty", want: map[string]string{
			"users/active":          "0",
			"users/new":             "0",
			"users/new/trend":       "",
			"users/returning":       "0",
			"users/auth-sources":    plainNote,
			"overview/new-7d":       "0\t0",
			"overview/returning-7d": "0\t0",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for key, want := range tc.want {
				parts := strings.Split(key, "/")
				p := panel(t, byID[parts[0]], parts[1])
				sql := p.SQL
				if len(parts) == 3 {
					sql = p.TrendSQL
				}
				got, err := ch.try(sql, start, end, tc.env)
				if err != nil {
					t.Errorf("%s: %v", key, err)
					continue
				}
				if got != want {
					t.Errorf("%s (%s) =\n%s\nwant\n%s", key, tc.env, got, want)
				}
			}
		})
	}
}

// TestNewUsersUndecidedGuardIsScopedToTheRangesActivity pins when New and
// Returning have no figure: only when no environment active in the range
// decides any of it. An undecided environment nobody was active in during the
// range contributes a determinate 0, and with several environments in scope
// one that decides the range keeps the figure, each environment contributing
// its own count.
func TestNewUsersUndecidedGuardIsScopedToTheRangesActivity(t *testing.T) {
	ch := newClickHouse(t, "forge_obsbuiltin_newusers", newUsersSeed)
	end := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	month := end.Add(-30 * 24 * time.Hour)
	users := Users(fullInput())
	newP, retP := panel(t, users, "new"), panel(t, users, "returning")

	for _, tc := range []struct {
		name       string
		start      time.Time
		envs       []string
		new, retur string
	}{
		// brandnew's users were first seen 11-10 and 11-11, so it decides
		// nothing before 11-17; on 11-12 nobody was active there, so that
		// day's answer is a determinate nothing, not a missing figure.
		{"undecided env, nobody active in the range", end.Add(-24 * time.Hour), []string{"brandnew"}, "0", "0"},
		{"undecided env, active in the range", end.Add(-3 * 24 * time.Hour), []string{"brandnew"}, `\N`, `\N`},
		// Rows in scope, but no UserActivityEvidence row in the 90-day window.
		{"only a sign-out in scope", month, []string{"signedout"}, "0", "0"},
		// One environment that decides the range keeps the figure; the
		// undecided one counts its users returning (b1, b2).
		{"decided and undecided env together", month, []string{"mature", "brandnew"}, "1", "4"},
		{"partly decided and undecided env together", month, []string{"fresh", "brandnew"}, "2", "4"},
		// An env with no events does not decide anything for the other.
		{"undecided env beside an empty one", month, []string{"brandnew", "empty"}, `\N`, `\N`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ch.run(newP.SQL, tc.start, end, tc.envs...); got != tc.new {
				t.Errorf("users/new over %v from %s = %q, want %q", tc.envs, tc.start.Format(time.DateOnly), got, tc.new)
			}
			if got := ch.run(retP.SQL, tc.start, end, tc.envs...); got != tc.retur {
				t.Errorf("users/returning over %v from %s = %q, want %q", tc.envs, tc.start.Format(time.DateOnly), got, tc.retur)
			}
		})
	}
}
