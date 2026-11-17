package obsbuiltin

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/dashspec"
)

// The population every built-in panel is checked against. The range under
// test is the 30 days ending Monday 2026-03-16 12:00 UTC. Environment e1:
//
// Visitors (web, hashed by the edge; country / device / browser / OS):
//
//	h1   03-16 09:00 from news.ycombinator.com/item: /, /pricing, add_to_cart,
//	     /orders signed in as u4, a flag exposure (new_checkout=on). The
//	     backend's auth_signed_up and auth_signed_in of u4 carry h1 (the
//	     edge stamped the request). US desktop Chrome macOS.
//	h2   03-16 10:00 newsletter (utm email/spring) on /, a flag exposure
//	     (new_checkout=off), back 60 minutes later on /pricing: two visits, two
//	     bounces. DE mobile Safari iOS.
//	h3   03-16 11:30 paid google click on /, /signup, then the BROWSER's
//	     auth_signed_up of u5 (the backend's copy a second later has no hash).
//	     US desktop Firefox Windows.
//	h4   a bot: never counted.
//	h5   03-16 08:00 from www.google.com, under GPC: counted, never linked.
//	     FR desktop Chrome Linux.
//	h6   03-16 11:58 from chatgpt.com: the one visitor in the last 5 minutes.
//	     GB desktop Edge Windows.
//	h7   03-13 14:00 from t.co, then /orders signed in as u1. US desktop
//	     Chrome macOS.
//	h8   03-13 15:00 /orders as u2, then as u3: one office NAT, two users, so
//	     it links nobody. US desktop Chrome Windows.
//	h10  03-01 10:00 partner (utm partner/referral, a custom channel rule);
//	     u6's backend sign-up carries h10. CA tablet Safari iPadOS.
//	h11  03-14 23:59:30 / and 03-15 00:00:10 /docs: both rows carry the hash
//	     of the batch's day, so one visitor and ONE visit across midnight.
//	     US mobile Chrome Android.
//
// Users (backend events): u1 signed up in 2025 (outside the 90-day window),
// active 03-07, 03-13, 03-16; u2 signed up 02-20 (magic link, activated in 30
// minutes), active 03-02 and 03-13; u3 signed up 02-24 (invite), active 03-13;
// u4 signed up 03-16 (password, activated 8 minutes later, purchased); u5
// signed up 03-16 (social google, unverified, never activated); u6 signed up
// 03-01 (password, activated 03-05), active 03-13; u7 only signed out. The
// platform's account snapshots say 40 then 42. Two browser errors.
//
// e2 holds one user and one visitor whose hash string equals h1's, and no
// sign-up: a different site, never mixed in. e3 is the week-4 retention
// cohort (seeded by w4Cohort).
const populationSeed = `
INSERT INTO forge_obsbuiltin_population.product_events
    (Timestamp, ServiceName, EnvId, UserId, EventName, Props, Client, DayVisitor, Country, DeviceType, Browser, OS, Privacy) VALUES
    ('2026-03-16 09:00:00', 'shop-web', 'e1', '',   'page_viewed', {'route': '/', 'entry': 'true', 'referrer_host': 'news.ycombinator.com', 'referrer_path': '/item'}, 'web', 'h1', 'US', 'desktop', 'Chrome', 'macOS', ''),
    ('2026-03-16 09:05:00', 'shop-web', 'e1', '',   'page_viewed', {'route': '/pricing'}, 'web', 'h1', 'US', 'desktop', 'Chrome', 'macOS', ''),
    ('2026-03-16 09:10:00', 'shop-web', 'e1', '',   'add_to_cart', {}, 'web', 'h1', 'US', 'desktop', 'Chrome', 'macOS', ''),
    ('2026-03-16 09:15:00', 'shop-web', 'e1', 'u4', 'page_viewed', {'route': '/orders'}, 'web', 'h1', 'US', 'desktop', 'Chrome', 'macOS', ''),
    ('2026-03-16 09:16:00', 'shop-web', 'e1', 'u4', 'flag_evaluated', {'flag': 'new_checkout', 'variant': 'on'}, 'web', 'h1', 'US', 'desktop', 'Chrome', 'macOS', ''),
    ('2026-03-16 10:00:00', 'shop-web', 'e1', '',   'page_viewed', {'route': '/', 'entry': 'true', 'utm_source': 'Newsletter', 'utm_medium': 'email', 'utm_campaign': 'spring'}, 'web', 'h2', 'DE', 'mobile', 'Safari', 'iOS', ''),
    ('2026-03-16 10:00:30', 'shop-web', 'e1', '',   'flag_evaluated', {'flag': 'new_checkout', 'variant': 'off'}, 'web', 'h2', 'DE', 'mobile', 'Safari', 'iOS', ''),
    ('2026-03-16 11:00:00', 'shop-web', 'e1', '',   'page_viewed', {'route': '/pricing', 'entry': 'true'}, 'web', 'h2', 'DE', 'mobile', 'Safari', 'iOS', ''),
    ('2026-03-16 11:30:00', 'shop-web', 'e1', '',   'page_viewed', {'route': '/', 'entry': 'true', 'utm_source': 'google', 'utm_medium': 'cpc', 'click_id': 'google'}, 'web', 'h3', 'US', 'desktop', 'Firefox', 'Windows', ''),
    ('2026-03-16 11:31:00', 'shop-web', 'e1', '',   'page_viewed', {'route': '/signup'}, 'web', 'h3', 'US', 'desktop', 'Firefox', 'Windows', ''),
    ('2026-03-16 11:33:00', 'shop-web', 'e1', 'u5', 'auth_signed_up', {'method': 'social', 'provider': 'forge', 'social_provider': 'google', 'verified': 'false'}, 'web', 'h3', 'US', 'desktop', 'Firefox', 'Windows', ''),
    ('2026-03-16 10:30:00', 'shop-web', 'e1', '',   'page_viewed', {'route': '/'}, 'web', 'h4', 'US', 'bot', '', '', ''),
    ('2026-03-16 08:00:00', 'shop-web', 'e1', '',   'page_viewed', {'route': '/', 'entry': 'true', 'referrer_host': 'www.google.com'}, 'web', 'h5', 'FR', 'desktop', 'Chrome', 'Linux', 'gpc'),
    ('2026-03-16 11:58:00', 'shop-web', 'e1', '',   'page_viewed', {'route': '/', 'entry': 'true', 'referrer_host': 'chatgpt.com'}, 'web', 'h6', 'GB', 'desktop', 'Edge', 'Windows', ''),
    ('2026-03-13 14:00:00', 'shop-web', 'e1', '',   'page_viewed', {'route': '/', 'entry': 'true', 'referrer_host': 't.co'}, 'web', 'h7', 'US', 'desktop', 'Chrome', 'macOS', ''),
    ('2026-03-13 14:02:00', 'shop-web', 'e1', 'u1', 'page_viewed', {'route': '/orders'}, 'web', 'h7', 'US', 'desktop', 'Chrome', 'macOS', ''),
    ('2026-03-13 15:00:00', 'shop-web', 'e1', 'u2', 'page_viewed', {'route': '/orders', 'entry': 'true'}, 'web', 'h8', 'US', 'desktop', 'Chrome', 'Windows', ''),
    ('2026-03-13 15:05:00', 'shop-web', 'e1', 'u3', 'page_viewed', {'route': '/orders'}, 'web', 'h8', 'US', 'desktop', 'Chrome', 'Windows', ''),
    ('2026-03-01 10:00:00', 'shop-web', 'e1', '',   'page_viewed', {'route': '/', 'entry': 'true', 'referrer_host': 'partner.example', 'utm_source': 'partner', 'utm_medium': 'referral'}, 'web', 'h10', 'CA', 'tablet', 'Safari', 'iPadOS', ''),
    ('2026-03-14 23:59:30', 'shop-web', 'e1', '',   'page_viewed', {'route': '/', 'entry': 'true'}, 'web', 'h11', 'US', 'mobile', 'Chrome', 'Android', ''),
    ('2026-03-15 00:00:10', 'shop-web', 'e1', '',   'page_viewed', {'route': '/docs'}, 'web', 'h11', 'US', 'mobile', 'Chrome', 'Android', ''),
    ('2026-03-16 09:12:00', 'api', 'e1', 'u4', 'auth_signed_up', {'method': 'password', 'provider': 'forge', 'verified': 'true'}, 'backend', 'h1', '', '', '', '', ''),
    ('2026-03-16 09:12:01', 'api', 'e1', 'u4', 'auth_signed_in', {'kind': 'credential', 'method': 'password', 'provider': 'forge'}, 'backend', 'h1', '', '', '', '', ''),
    ('2026-03-16 09:20:00', 'api', 'e1', 'u4', 'order_created', {}, 'backend', '', '', '', '', '', ''),
    ('2026-03-16 09:25:00', 'api', 'e1', 'u4', 'purchase', {}, 'backend', '', '', '', '', '', ''),
    ('2026-03-16 11:33:01', 'api', 'e1', 'u5', 'auth_signed_up', {'method': 'social', 'provider': 'forge', 'social_provider': 'google', 'verified': 'false'}, 'backend', '', '', '', '', '', ''),
    ('2025-12-01 09:00:00', 'api', 'e1', 'u1', 'auth_signed_up', {'method': 'password', 'provider': 'forge', 'verified': 'true'}, 'backend', '', '', '', '', '', ''),
    ('2026-03-07 09:00:00', 'api', 'e1', 'u1', 'order_created', {}, 'backend', '', '', '', '', '', ''),
    ('2026-03-16 10:00:00', 'api', 'e1', 'u1', 'auth_signed_in', {'kind': 'resumed', 'provider': 'forge'}, 'backend', '', '', '', '', '', ''),
    ('2026-02-20 10:00:00', 'api', 'e1', 'u2', 'auth_signed_up', {'method': 'magic_link', 'provider': 'forge', 'verified': 'true'}, 'backend', '', '', '', '', '', ''),
    ('2026-02-20 10:00:05', 'api', 'e1', 'u2', 'auth_signed_in', {'kind': 'credential', 'method': 'magic_link', 'provider': 'forge'}, 'backend', '', '', '', '', '', ''),
    ('2026-02-20 10:30:00', 'api', 'e1', 'u2', 'order_created', {}, 'backend', '', '', '', '', '', ''),
    ('2026-03-02 09:00:00', 'api', 'e1', 'u2', 'order_updated', {}, 'backend', '', '', '', '', '', ''),
    ('2026-02-24 09:00:00', 'api', 'e1', 'u3', 'auth_signed_up', {'method': 'invite', 'provider': 'forge', 'verified': 'true'}, 'backend', '', '', '', '', '', ''),
    ('2026-03-01 10:10:00', 'api', 'e1', 'u6', 'auth_signed_up', {'method': 'password', 'provider': 'forge', 'verified': 'false'}, 'backend', 'h10', '', '', '', '', ''),
    ('2026-03-05 12:00:00', 'api', 'e1', 'u6', 'order_created', {}, 'backend', '', '', '', '', '', ''),
    ('2026-03-13 09:00:00', 'api', 'e1', 'u6', 'order_updated', {}, 'backend', '', '', '', '', '', ''),
    ('2026-03-15 08:00:00', 'api', 'e1', 'u7', 'auth_signed_out', {'provider': 'forge'}, 'backend', '', '', '', '', '', ''),
    ('2026-03-14 00:10:00', 'platform', 'e1', '', 'auth_accounts', {'total': '40'}, 'platform', '', '', '', '', '', ''),
    ('2026-03-15 00:10:00', 'platform', 'e1', '', 'auth_accounts', {'total': '42'}, 'platform', '', '', '', '', '', ''),
    ('2026-03-16 09:00:00', 'api', 'e2', 'u9', 'order_created', {}, 'backend', '', '', '', '', '', ''),
    ('2026-03-16 09:00:00', 'shop-web', 'e2', '', 'page_viewed', {'route': '/', 'entry': 'true'}, 'web', 'h1', 'US', 'desktop', 'Chrome', 'macOS', '');
INSERT INTO forge_obsbuiltin_population.error_events (Timestamp, ServiceName, EnvId, SessionId) VALUES
    ('2026-03-16 09:01:00', 'shop-web', 'e1', 's1'), ('2026-03-16 10:01:00', 'shop-web', 'e1', 's2'), ('2026-03-16 09:01:00', 'shop-web', 'e2', 's3');
`

// w4Cohort is e3: 23 sign-ups of 2026-01-10, each on a visit of its own
// day. w01-w20 came from a newsletter (Email), w21-w23 directly. w01-w08
// and w21 are active on 02-08, inside their fourth week (days 28 to 35);
// w09 is active on 02-20, after it.
func w4Cohort() string {
	var b strings.Builder
	b.WriteString("INSERT INTO forge_obsbuiltin_population.product_events (Timestamp, ServiceName, EnvId, UserId, EventName, Props, Client, DayVisitor, DeviceType) VALUES\n")
	var rows []string
	for i := 1; i <= 23; i++ {
		u, h := fmt.Sprintf("w%02d", i), fmt.Sprintf("hw%02d", i)
		props := "{'route': '/', 'entry': 'true'}"
		if i <= 20 {
			props = "{'route': '/', 'entry': 'true', 'utm_source': 'news', 'utm_medium': 'email'}"
		}
		rows = append(rows,
			fmt.Sprintf("('2026-01-10 10:00:00', 'shop-web', 'e3', '', 'page_viewed', %s, 'web', '%s', 'desktop')", props, h),
			fmt.Sprintf("('2026-01-10 10:05:00', 'api', 'e3', '%s', 'auth_signed_up', {'method': 'password'}, 'backend', '%s', '')", u, h))
		if i <= 8 || i == 21 {
			rows = append(rows, fmt.Sprintf("('2026-02-08 12:00:00', 'api', 'e3', '%s', 'order_created', {}, 'backend', '', '')", u))
		}
		if i == 9 {
			rows = append(rows, fmt.Sprintf("('2026-02-20 12:00:00', 'api', 'e3', '%s', 'order_created', {}, 'backend', '', '')", u))
		}
	}
	b.WriteString(strings.Join(rows, ",\n"))
	return b.String() + ";\n"
}

// TestBuiltinDashboardsOnAPopulation runs every built-in panel over the
// population and checks each number against the answer worked by hand from
// the definitions (product-analytics design §2).
func TestBuiltinDashboardsOnAPopulation(t *testing.T) {
	ch := newClickHouse(t, "forge_obsbuiltin_population", populationSeed+w4Cohort())
	end := time.Date(2026, 3, 16, 12, 0, 0, 0, time.UTC)
	start := end.Add(-30 * 24 * time.Hour)
	in := fullInput()
	byID := map[string]dashspec.Dashboard{}
	for _, d := range Dashboards(in) {
		byID[d.ID] = d
	}
	byID["overview"] = ExtendOverview(dashspec.Dashboard{ID: "overview", Title: "shop: overview"}, in)
	check := func(dash, id string, trend bool, env, want string) {
		t.Helper()
		p := panel(t, byID[dash], id)
		sql := p.SQL
		if trend {
			sql = p.TrendSQL
		}
		got, err := ch.try(sql, start, end, env)
		if err != nil {
			t.Errorf("%s/%s: %v", dash, id, err)
			return
		}
		if got != want {
			t.Errorf("%s/%s (trend %v, %s) =\n%s\nwant\n%s", dash, id, trend, env, got, want)
		}
	}
	rows := func(r ...string) string { return strings.Join(r, "\n") }

	// Overview: Traffic. Nine visitor-days (h4 is a bot, h11's two days'
	// rows are one hash); h6 alone in the last 5 minutes; 16 page views over
	// 10 visits (h2 came back after an hour), 5 of them bounces; 2 browser
	// errors per 16 page views.
	check("overview", "live-visitors", false, "e1", "1")
	check("overview", "visitors", false, "e1", "9")
	check("overview", "pageviews", false, "e1", "16\t1.6")
	check("overview", "bounce", false, "e1", "0.5")
	check("overview", "js-errors-per-1k", false, "e1", "125")
	check("overview", "top-pages-5", false, "e1", rows("/\t8", "/orders\t3", "/pricing\t2", "/docs\t1", "/signup\t1"))
	// h1 by the custom host rule, h10 by the custom source+medium rule; h8
	// and h11 came with no referrer and no campaign.
	check("overview", "top-channels-5", false, "e1", rows("Direct\t2", "AI assistants\t1", "Email\t1", "Hacker News\t1", "Organic search\t1"))
	// Overview: Users, the last 7 days against the 7 before. Active now: u1,
	// u2, u3, u6 (03-13), u4, u5; before: u6 (03-05), u1 (03-07) — u2's
	// 03-02 09:00 is before the window's 12:00. New now: u4, u5; before:
	// none (u6 signed up 03-01). Returning = active minus new.
	check("overview", "active-7d", false, "e1", "6\t2")
	check("overview", "new-7d", false, "e1", "2\t0")
	check("overview", "returning-7d", false, "e1", "4\t2")
	check("overview", "accounts", false, "e1", "42")
	check("overview", "dau-30d", false, "e1", rows("2026-02-20\t1", "2026-02-24\t1", "2026-03-01\t1", "2026-03-02\t1",
		"2026-03-05\t1", "2026-03-07\t1", "2026-03-13\t4", "2026-03-16\t3"))
	// e2 has no snapshot: its users seen in 90 days. Its visitor shares h1's
	// hash string and is still e2's alone.
	check("overview", "accounts", false, "e2", "1")
	check("overview", "visitors", false, "e2", "1")

	// Users. u7 only signed out (not active); u1 signed up outside the
	// 90-day window, so is returning; u2..u6 signed up in the range.
	check("users", "accounts", false, "e1", "42")
	check("users", "accounts", true, "e1", rows("2026-03-14\t40", "2026-03-15\t42"))
	check("users", "active", false, "e1", "6")
	check("users", "active", true, "e1", rows("2026-02-20\t1", "2026-02-24\t1", "2026-03-01\t1", "2026-03-02\t1",
		"2026-03-05\t1", "2026-03-07\t1", "2026-03-13\t4", "2026-03-16\t3"))
	check("users", "new", false, "e1", "5")
	check("users", "new", true, "e1", rows("2026-02-20\t1", "2026-02-24\t1", "2026-03-01\t1", "2026-03-16\t2"))
	check("users", "returning", false, "e1", "1")
	// e2 reports no sign-ups and its data begins with u9's one event, inside
	// the range: no part of the range can tell new from returning, so New and
	// Returning show no figure rather than counting u9 new.
	check("users", "new", false, "e2", "\\N")
	check("users", "returning", false, "e2", "\\N")
	// Stickiness is the days' summed DAU over their summed MAU: 13 user-days
	// over u1 10 + u2 25 + u3 21 + u4 1 + u5 1 + u6 16 = 74 user-windows.
	check("users", "stickiness", false, "e1", "0.1757")
	if got := ch.run(panel(t, byID["users"], "dau-wau-mau").SQL, start, end, "e1"); !strings.HasPrefix(got, "2026-02-20\t1\t1\t1\n") ||
		!strings.HasSuffix(got, "2026-03-13\t4\t4\t4\n2026-03-14\t0\t4\t4\n2026-03-15\t0\t4\t4\n2026-03-16\t3\t6\t6") || strings.Count(got, "\n") != 24 {
		t.Errorf("dau-wau-mau =\n%s\nwant 25 days from 2026-02-20 (1, 1, 1) to 2026-03-16 (3, 6, 6)", got)
	}
	check("users", "power-users-l28", false, "e1", rows("1\t2", "2\t1", "3\t3"))
	// Lifecycle by ISO week: u2 new 02-16 and gone 02-23; u3 and u6 (03-01
	// is a Sunday) new 02-23; u1 and u2 resurrected 03-02, u3 gone; u3 back
	// 03-09; u4, u5 new 03-16. The current week counts no churn yet.
	check("users", "lifecycle-weekly", false, "e1", rows(
		"2026-02-16\t1\t0\t0\t0", "2026-02-23\t2\t0\t0\t-1", "2026-03-02\t0\t1\t2\t-1", "2026-03-09\t0\t3\t1\t0", "2026-03-16\t2\t1\t0\t0"))
	nulls := func(n int) string { return strings.Repeat("\t\\N", n) }
	check("users", "retention-cohorts", false, "e1", rows(
		"2026-02-16 · 1 user\t1\t0\t1\t1\t0"+nulls(7),
		"2026-02-23 · 2 users\t1\t0.5\t1\t0"+nulls(8),
		"2026-03-16 · 2 users\t1"+nulls(11)))
	// A range ending on a Monday midnight (exclusive): the week starting at
	// the end has not begun, so its cells are NULL, never 0.
	if got, err := ch.try(panel(t, byID["users"], "retention-cohorts").SQL, start, time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC), "e1"); err != nil {
		t.Errorf("retention-cohorts ending 03-16 00:00: %v", err)
	} else if want := rows(
		"2026-02-16 · 1 user\t1\t0\t1\t1"+nulls(8),
		"2026-02-23 · 2 users\t1\t0.5\t1"+nulls(9)); got != want {
		t.Errorf("retention-cohorts ending on a Monday midnight =\n%s\nwant\n%s", got, want)
	}
	// order_created is the declared activation: u2 in 30 minutes, u4 in 8,
	// u6 after 4 days; u3 and u5 not yet.
	check("users", "time-to-activation", false, "e1", rows("< 1 hour\t2", "3-7 days\t1", "Not yet\t2"))
	check("users", "signups-by-method", false, "e1", rows("2026-02-20\tmagic_link\t1", "2026-02-24\tinvite\t1", "2026-03-01\tpassword\t1",
		"2026-03-16\tpassword\t1", "2026-03-16\tsocial\t1"))
	check("users", "signins-by-method", false, "e1", rows("2026-02-20\tmagic_link\t1", "2026-03-16\tpassword\t1"))
	check("users", "verified-share", false, "e1", "0.6")
	check("users", "signups-by-provider", false, "e1", rows("forge\t4\t80", "forge/google\t1\t20"))
	check("users", "auth-sources", false, "e1", rows("backend\t5\t2\t1\t1", "web\t1\t0\t0\t0"))
	check("users", "auth-sources", false, "e2", "sign-ups not reported: new users are the users first seen from 2026-03-23 09:00 UTC, "+
		"7 days after the earliest event retained; a user first seen before then may be returning and counts as returning\t0\t0\t0\t0")
	check("users", "most-active-users", false, "e1", rows(
		"u4\t6\t1\t6\t2026-03-16 09:25:00", "u2\t5\t3\t5\t2026-03-13 15:00:00", "u1\t3\t3\t3\t2026-03-16 10:00:00",
		"u6\t3\t3\t3\t2026-03-13 09:00:00", "u3\t2\t2\t2\t2026-03-13 15:05:00", "u5\t2\t1\t1\t2026-03-16 11:33:01"))

	// Product. h1's visit lasted 900 s, h3's 180, h7's 120, h8's 300, h11's
	// 40 across midnight, five bounces 0: 1540 s over 10 visits.
	check("product", "pageviews", false, "e1", "16")
	check("product", "pageviews", true, "e1", rows("2026-03-01\t1", "2026-03-13\t4", "2026-03-14\t2", "2026-03-16\t9"))
	check("product", "visits", false, "e1", "10")
	check("product", "visits", true, "e1", rows("2026-03-01\t1", "2026-03-13\t2", "2026-03-14\t1", "2026-03-16\t6"))
	check("product", "views-per-visit", false, "e1", "1.6")
	check("product", "bounce", false, "e1", "0.5")
	check("product", "visit-duration", false, "e1", "154")
	check("product", "events", false, "e1", "39")
	check("product", "top-pages", false, "e1", rows("/\t8\t8\t0\t8\t4", "/orders\t4\t3\t4\t1\t3", "/pricing\t2\t2\t0\t1\t1",
		"/docs\t1\t1\t0\t0\t1", "/signup\t1\t1\t0\t0\t1"))
	// Visits entering on /: h1, h3 and h10 belong to visitor-days that signed
	// up (h1 and h10 by the backend's stamped hash, h3 by the browser's).
	check("product", "entry-pages", false, "e1", rows("/\t8\t50\t37.5", "/orders\t1\t0\t0", "/pricing\t1\t100\t0"))
	check("product", "exit-pages", false, "e1", rows("/\t4\t50", "/orders\t3\t75", "/docs\t1\t100", "/pricing\t1\t50", "/signup\t1\t100"))
	// The /pricing goal: h1's anonymous view is u4's (the same-day link), and
	// h2's visitor-day; conversion is persons over 9 visitor-days.
	check("product", "goals", false, "e1", rows("Checkout\t1\t1\t11.11", "pricing\t2\t2\t22.22"))
	check("product", "events-by-name", false, "e1", rows(
		"auth_signed_up\t6\t5\t2026-03-16 11:33:01", "order_created\t4\t4\t2026-03-16 09:20:00", "auth_signed_in\t3\t3\t2026-03-16 10:00:00",
		"flag_evaluated\t2\t2\t2026-03-16 10:00:30", "order_updated\t2\t2\t2026-03-13 09:00:00", "add_to_cart\t1\t1\t2026-03-16 09:10:00",
		"auth_signed_out\t1\t1\t2026-03-15 08:00:00", "purchase\t1\t1\t2026-03-16 09:25:00"))
	check("product", "actions-per-entity", false, "e1", "order\t4\t2\t0")
	check("product", "funnel-activation", false, "e1", rows("Visited\t9", "Signed up\t5", "Activated\t3"))
	check("product", "signups-from-visit", false, "e1", "0.6")
	// buy, by person: ten persons viewed a page (h1's views are u4's, h3's
	// u5's, h7's u1's, h10's u6's; h8 is u2 and u3; h2, h5, h6, h11 stay
	// visitor-days); only u4 added to the cart (anonymously, linked) and
	// bought (on the server).
	check("product", "funnel-buy", false, "e1", rows("page_viewed\t10", "add_to_cart\t1", "purchase\t1"))
	check("product", "funnel-browse", false, "e1", rows("page_viewed\t9", "add_to_cart\t1"))
	check("product", "flag-exposure", false, "e1", rows("new_checkout\toff\t1\t0", "new_checkout\ton\t1\t100"))

	// Acquisition. u2 and u3 signed up with no visit: Unattributed.
	check("acquisition", "visitors", false, "e1", "9")
	check("acquisition", "visitors", true, "e1", rows("2026-03-01\t1", "2026-03-13\t2", "2026-03-15\t1", "2026-03-16\t5"))
	check("acquisition", "visits", false, "e1", "10")
	check("acquisition", "signups", false, "e1", "5")
	check("acquisition", "signups", true, "e1", rows("2026-02-20\t1", "2026-02-24\t1", "2026-03-01\t1", "2026-03-16\t2"))
	check("acquisition", "visit-to-signup", false, "e1", "0.3333")
	check("acquisition", "channels", false, "e1", rows(
		"Direct\t2\t2\t0\t0\t0\t\\N",
		"AI assistants\t1\t1\t100\t0\t0\t\\N",
		"Email\t1\t2\t100\t0\t0\t\\N",
		"Hacker News\t1\t1\t0\t1\t100\t100",
		"Organic search\t1\t1\t100\t0\t0\t\\N",
		"Organic social\t1\t1\t0\t0\t0\t\\N",
		"Paid search\t1\t1\t0\t1\t100\t0",
		"Partners\t1\t1\t100\t1\t100\t100",
		"Unattributed\t0\t0\t\\N\t2\t\\N\t50"))
	check("acquisition", "visitors-by-channel", false, "e1", rows(
		"2026-03-01\tPartners\t1", "2026-03-13\tDirect\t1", "2026-03-13\tOrganic social\t1", "2026-03-15\tDirect\t1",
		"2026-03-16\tAI assistants\t1", "2026-03-16\tEmail\t1", "2026-03-16\tHacker News\t1", "2026-03-16\tOrganic search\t1", "2026-03-16\tPaid search\t1"))
	check("acquisition", "signups-by-channel-weekly", false, "e1", rows(
		"2026-02-16\tUnattributed\t1", "2026-02-23\tPartners\t1", "2026-02-23\tUnattributed\t1", "2026-03-16\tHacker News\t1", "2026-03-16\tPaid search\t1"))
	check("acquisition", "sources", false, "e1", rows(
		"(direct)\t2\t0\t0", "chatgpt.com\t1\t0\t0", "google\t1\t1\t100", "news.ycombinator.com\t1\t1\t100", "newsletter\t1\t0\t0",
		"partner\t1\t1\t100", "t.co\t1\t0\t0", "www.google.com\t1\t0\t0", "Unattributed\t0\t2\t\\N"))
	check("acquisition", "referrers", false, "e1", rows(
		"chatgpt.com\t\t1\t1", "news.ycombinator.com\t/item\t1\t1", "partner.example\t\t1\t1", "t.co\t\t1\t1", "www.google.com\t\t1\t1"))
	check("acquisition", "campaigns", false, "e1", rows(
		"google\tcpc\t\t\t\t1\t1\t100\t0", "newsletter\temail\tspring\t\t\t1\t0\t0\t\\N", "partner\treferral\t\t\t\t1\t1\t100\t100"))
	check("acquisition", "landing-pages", false, "e1", rows(
		"/\tEmail\t2\t0", "/\tAI assistants\t1\t0", "/\tDirect\t1\t0", "/\tHacker News\t1\t1", "/\tOrganic search\t1\t0",
		"/\tOrganic social\t1\t0", "/\tPaid search\t1\t1", "/\tPartners\t1\t1", "/orders\tDirect\t1\t0", "Unattributed\t\t0\t2"))
	check("acquisition", "countries", false, "e1", rows("US\t5\t2\t40", "CA\t1\t1\t100", "DE\t1\t0\t0", "FR\t1\t0\t0", "GB\t1\t0\t0", "Unattributed\t0\t2\t\\N"))
	check("acquisition", "devices", false, "e1", rows("desktop\t6\t2\t33.33", "mobile\t2\t0\t0", "tablet\t1\t1\t100", "Unattributed\t0\t2\t\\N"))
	// The breakdowns join visitor-days to sign-ups on normalized keys, so a
	// session with join_use_nulls (NULL for an unmatched side) answers the
	// same: empty dimensions (Content, Term) still join, and Unattributed
	// sign-ups keep their own row.
	nullJoin := ch.with("--join_use_nulls=1")
	for _, id := range []string{"channels", "campaigns", "landing-pages", "countries", "sources"} {
		p := panel(t, byID["acquisition"], id)
		def, err1 := ch.try(p.SQL, start, end, "e1")
		nul, err2 := nullJoin.try(p.SQL, start, end, "e1")
		if err1 != nil || err2 != nil || def != nul {
			t.Errorf("acquisition/%s with join_use_nulls=1 =\n%s (%v)\nwant the default session's\n%s (%v)", id, nul, err2, def, err1)
		}
	}
	check("acquisition", "browsers", false, "e1", rows("Chrome\t5", "Safari\t2", "Edge\t1", "Firefox\t1"))
	check("acquisition", "os", false, "e1", rows("Windows\t3", "macOS\t2", "Android\t1", "Linux\t1", "iOS\t1", "iPadOS\t1"))
	// Week-4 retention reads the sign-ups of 35-90 days before: none in e1;
	// in e3, 8 of the 20 newsletter sign-ups were active in week 4, and the
	// 3 direct ones are too few to show a share.
	check("acquisition", "retention-by-channel", false, "e1", "")
	check("acquisition", "retention-by-channel", false, "e3", rows("Email\t20\t40", "Direct\t3\t\\N"))
}
