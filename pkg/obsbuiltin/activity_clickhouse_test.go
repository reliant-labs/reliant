package obsbuiltin

import (
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/dashspec"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

// The activity rules' population. The range is the 30 days ending 2026-03-16
// 12:00 UTC, environment e1, no account snapshot (so Accounts falls back to
// the users seen in 90 days):
//
//	ua  signed up 03-15 10:00, then a page view: new and active.
//	ub  staff: signed up 03-15 11:00, a page view, then the internal_user
//	    marker. Excluded from every count, its sign-up and history included.
//	uc  only billing_subscription_state rows (03-14): a fact about the user,
//	    declared not_activity, so never active, new or seen.
//	ud  a page view 03-14 and none of a sign-up in the window: returning.
const activitySeed = `
INSERT INTO forge_obsbuiltin_activity.product_events
    (Timestamp, ServiceName, EnvId, UserId, EventName, Props, Client) VALUES
    ('2026-03-15 10:00:00', 'api', 'e1', 'ua', 'auth_signed_up', {'method': 'password'}, 'backend'),
    ('2026-03-15 10:05:00', 'web', 'e1', 'ua', 'page_viewed', {'route': '/'}, 'web'),
    ('2026-03-15 11:00:00', 'api', 'e1', 'ub', 'auth_signed_up', {'method': 'password'}, 'backend'),
    ('2026-03-15 11:05:00', 'web', 'e1', 'ub', 'page_viewed', {'route': '/'}, 'web'),
    ('2026-03-15 11:06:00', 'api', 'e1', 'ub', 'internal_user', {}, 'backend'),
    ('2026-03-14 09:00:00', 'api', 'e1', 'uc', 'billing_subscription_state', {'plan': 'pro'}, 'backend'),
    ('2026-03-14 10:00:00', 'web', 'e1', 'ud', 'page_viewed', {'route': '/'}, 'web');
`

// TestActivityRulesRemoveUsersFromEveryCountOnClickHouse runs the Overview's
// Users card and the Users dashboard on ClickHouse, with and without a
// project's activity rules: a user with an exclude_users_with marker is gone
// from active, new (their sign-up included), returning, daily active and the
// Accounts fallback, and a user whose only rows are not_activity events is
// never counted.
func TestActivityRulesRemoveUsersFromEveryCountOnClickHouse(t *testing.T) {
	ch := newClickHouse(t, "forge_obsbuiltin_activity", activitySeed)
	end := time.Date(2026, 3, 16, 12, 0, 0, 0, time.UTC)
	start := end.Add(-30 * 24 * time.Hour)
	rows := func(r ...string) string { return strings.Join(r, "\n") }

	dashboards := func(a obsquery.Activity) map[string]dashspec.Dashboard {
		in := fullInput()
		in.Activity = a
		byID := map[string]dashspec.Dashboard{}
		for _, d := range Dashboards(in) {
			byID[d.ID] = d
		}
		byID["overview"] = ExtendOverview(dashspec.Dashboard{ID: "overview", Title: "shop: overview"}, in)
		return byID
	}
	check := func(byID map[string]dashspec.Dashboard, label, dash, id string, trend bool, want string) {
		t.Helper()
		p := panel(t, byID[dash], id)
		sql := p.SQL
		if trend {
			sql = p.TrendSQL
		}
		if got := ch.run(sql, start, end, "e1"); got != want {
			t.Errorf("%s: %s/%s (trend %v) =\n%s\nwant\n%s", label, dash, id, trend, got, want)
		}
	}

	// Without rules every user with a user-attributed row counts: ua, ub, uc
	// and ud are active; ua and ub are new.
	plain := dashboards(obsquery.Activity{})
	check(plain, "no rules", "overview", "active-7d", false, "4\t0")
	check(plain, "no rules", "overview", "new-7d", false, "2\t0")
	check(plain, "no rules", "overview", "returning-7d", false, "2\t0")
	check(plain, "no rules", "overview", "accounts", false, "4")
	check(plain, "no rules", "overview", "dau-30d", false, rows("2026-03-14\t2", "2026-03-15\t2"))

	ruled := dashboards(obsquery.Activity{NotActivity: []string{"billing_subscription_state"}, ExcludeUsersWith: []string{"internal_user"}})
	check(ruled, "rules", "overview", "active-7d", false, "2\t0")
	check(ruled, "rules", "overview", "new-7d", false, "1\t0")
	check(ruled, "rules", "overview", "returning-7d", false, "1\t0")
	check(ruled, "rules", "overview", "accounts", false, "2")
	check(ruled, "rules", "overview", "dau-30d", false, rows("2026-03-14\t1", "2026-03-15\t1"))

	check(ruled, "rules", "users", "active", false, "2")
	check(ruled, "rules", "users", "active", true, rows("2026-03-14\t1", "2026-03-15\t1"))
	check(ruled, "rules", "users", "new", false, "1")
	check(ruled, "rules", "users", "new", true, rows("2026-03-15\t1"))
	check(ruled, "rules", "users", "returning", false, "1")
	check(ruled, "rules", "users", "accounts", false, "2")
	// Sign-up counts and cohorts (suCTE) leave the marked user's sign-up out:
	// ua and ub signed up by password, ub is staff.
	check(plain, "no rules", "users", "signups-by-method", false, "2026-03-15\tpassword\t2")
	check(ruled, "rules", "users", "signups-by-method", false, "2026-03-15\tpassword\t1")
}

// The excluded-id and state-sign-in population, 03-15 in environment e1:
//
//	ua  a person: a credential sign-in and a page view.
//	uf  a synthetic monitor signing in under observe.WithStateEvents: its
//	    sign-in carries forge.kind = state, so it is never a user's.
//	ue  staff: a credential sign-in and a page view, excluded by id
//	    (exclude_user_ids).
const excludedIDSeed = `
INSERT INTO forge_obsbuiltin_excluded_ids.product_events
    (Timestamp, ServiceName, EnvId, UserId, EventName, Props, Client) VALUES
    ('2026-03-15 10:00:00', 'api', 'e1', 'ua', 'auth_signed_in', {'kind': 'credential', 'method': 'password'}, 'backend'),
    ('2026-03-15 10:05:00', 'web', 'e1', 'ua', 'page_viewed', {'route': '/'}, 'web'),
    ('2026-03-15 10:01:00', 'api', 'e1', 'uf', 'auth_signed_in', {'kind': 'credential', 'method': 'password', 'forge.kind': 'state'}, 'backend'),
    ('2026-03-15 10:02:00', 'api', 'e1', 'ue', 'auth_signed_in', {'kind': 'credential', 'method': 'password'}, 'backend'),
    ('2026-03-15 10:06:00', 'web', 'e1', 'ue', 'page_viewed', {'route': '/'}, 'web');
`

// TestExcludedUserIDsAndStateSignInsOnClickHouse: a sign-in recorded as a
// state event counts no user and no sign-in, and a user excluded by id is
// gone from the user counts, Most active users and Sign-ins by method.
func TestExcludedUserIDsAndStateSignInsOnClickHouse(t *testing.T) {
	ch := newClickHouse(t, "forge_obsbuiltin_excluded_ids", excludedIDSeed)
	end := time.Date(2026, 3, 16, 12, 0, 0, 0, time.UTC)
	start := end.Add(-30 * 24 * time.Hour)
	rows := func(r ...string) string { return strings.Join(r, "\n") }
	check := func(a obsquery.Activity, dash, id, want string) {
		t.Helper()
		in := fullInput()
		in.Activity = a
		d := Users(in)
		if dash == "overview" {
			d = ExtendOverview(dashspec.Dashboard{ID: "overview", Title: "shop: overview"}, in)
		}
		if got := ch.run(panel(t, d, id).SQL, start, end, "e1"); got != want {
			t.Errorf("%+v: %s/%s =\n%s\nwant\n%s", a, dash, id, got, want)
		}
	}

	none := obsquery.Activity{}
	check(none, "users", "active", "2")
	check(none, "users", "signins-by-method", "2026-03-15\tpassword\t2")
	check(none, "users", "most-active-users", rows(
		"ua\t2\t1\t2\t2026-03-15 10:05:00",
		"ue\t2\t1\t2\t2026-03-15 10:06:00"))

	staff := obsquery.Activity{ExcludeUserIDs: []string{"ue"}}
	check(staff, "users", "active", "1")
	check(staff, "overview", "active-7d", "1\t0")
	check(staff, "users", "signins-by-method", "2026-03-15\tpassword\t1")
	check(staff, "users", "most-active-users", "ua\t2\t1\t2\t2026-03-15 10:05:00")
}
