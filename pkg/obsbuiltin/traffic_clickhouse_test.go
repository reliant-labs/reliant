package obsbuiltin

import (
	"strings"
	"testing"
	"time"
)

// trafficSeed is one day of three visitors in e1, and one the day before:
//
//	a: /pricing from google.com, then /signup five minutes later (one visit,
//	   not a bounce), engaged 30s; US, desktop, Chrome, Mac OS
//	b: / from the "launch" email campaign, nothing else (a bounce), engaged
//	   5s; DE, mobile, Safari, iOS
//	c: /pricing direct, then / an hour later (two visits, two bounces), the
//	   second engaged 10s; no country, desktop, Firefox, Linux
//	p: / on the day before (the previous period): one bouncing visit
//
// plus rows every card must ignore: a bot, another environment, an
// unhashed server event.
const trafficSeed = `
INSERT INTO forge_obsbuiltin_traffic.product_events
    (Timestamp, ServiceName, EnvId, SessionId, UserId, EventName, Props, Client, VisitorId, DayVisitor, Country, DeviceType, Browser, OS) VALUES
    ('2026-03-16 10:00:00', 'web', 'e1', '', '', 'page_viewed', {'route': '/pricing', 'referrer_host': 'www.google.com'}, 'web', '', 'a', 'US', 'desktop', 'Chrome', 'Mac OS'),
    ('2026-03-16 10:05:00', 'web', 'e1', '', '', 'page_viewed', {'route': '/signup'}, 'web', '', 'a', 'US', 'desktop', 'Chrome', 'Mac OS'),
    ('2026-03-16 10:06:00', 'web', 'e1', '', '', 'page_engaged', {'route': '/signup', 'engaged_ms': '30000'}, 'web', '', 'a', 'US', 'desktop', 'Chrome', 'Mac OS'),
    ('2026-03-16 11:00:00', 'web', 'e1', '', '', 'page_viewed', {'route': '/', 'utm_source': 'newsletter', 'utm_medium': 'email', 'utm_campaign': 'launch'}, 'web', '', 'b', 'DE', 'mobile', 'Safari', 'iOS'),
    ('2026-03-16 11:01:00', 'web', 'e1', '', '', 'page_engaged', {'route': '/', 'engaged_ms': '5000'}, 'web', '', 'b', 'DE', 'mobile', 'Safari', 'iOS'),
    ('2026-03-16 12:00:00', 'web', 'e1', '', '', 'page_viewed', {'route': '/pricing'}, 'web', '', 'c', '', 'desktop', 'Firefox', 'Linux'),
    ('2026-03-16 13:00:00', 'web', 'e1', '', '', 'page_viewed', {'route': '/'}, 'web', '', 'c', '', 'desktop', 'Firefox', 'Linux'),
    ('2026-03-16 13:01:00', 'web', 'e1', '', '', 'page_engaged', {'route': '/', 'engaged_ms': '10000'}, 'web', '', 'c', '', 'desktop', 'Firefox', 'Linux'),
    ('2026-03-15 09:00:00', 'web', 'e1', '', '', 'page_viewed', {'route': '/'}, 'web', '', 'p', 'US', 'desktop', 'Chrome', 'Mac OS'),
    ('2026-03-16 10:00:00', 'web', 'e1', '', '', 'page_viewed', {'route': '/pricing'}, 'web', '', 'bot', 'US', 'bot', 'Chrome', 'Linux'),
    ('2026-03-16 10:00:00', 'web', 'e2', '', '', 'page_viewed', {'route': '/pricing'}, 'web', '', 'x', 'US', 'desktop', 'Chrome', 'Mac OS'),
    ('2026-03-16 10:00:00', 'api', 'e1', '', 'u1', 'order_created', {}, 'backend', '', '', '', '', '', '');
`

// TestTrafficOnADay pins every Traffic card over trafficSeed, unfiltered and
// under each kind of filter, worked by hand: a row of a breakdown is what the
// Visitors card reads once the row is clicked.
func TestTrafficOnADay(t *testing.T) {
	ch := newClickHouse(t, "forge_obsbuiltin_traffic", trafficSeed)
	start := time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	d := Traffic(Input{Project: "shop", Web: true})
	sql := func(id string) string { return panel(t, d, id).SQL }
	run := func(id string, f map[string]string) string {
		return ch.filtered(sql(id), start, end, f, 86400, "e1")
	}
	none := map[string]string(nil)

	for _, c := range []struct {
		id   string
		f    map[string]string
		want string
	}{
		// Headline figures, each beside the day before.
		{TrafficVisitorsPanel, none, "3\t1"},
		{TrafficPageViewsPanel, none, "5\t1"},
		{TrafficBouncePanel, none, "0.75\t1\t4\t1"},
		{TrafficVisitDurationPanel, none, "10000\t\\N\t3\t0"},
		{TrafficLiveVisitorsPanel, none, "0"},

		// Breakdowns: value, visitors, its own count, share.
		{TrafficPagesPanel, none, "/\t2\t2\t66.7\n/pricing\t2\t2\t66.7\n/signup\t1\t1\t33.3"},
		{TrafficEntryPagesPanel, none, "/\t2\t2\t66.7\n/pricing\t2\t2\t66.7"},
		{TrafficExitPagesPanel, none, "/\t2\t2\t66.7\n/pricing\t1\t1\t33.3\n/signup\t1\t1\t33.3"},
		{TrafficChannelsPanel, none, "Direct\t1\t33.3\nEmail\t1\t33.3\nOrganic search\t1\t33.3"},
		{TrafficReferrersPanel, none, "www.google.com\t1\t33.3"},
		{TrafficCampaignsPanel, none, "launch\t1\t33.3"},
		{TrafficCountriesPanel, none, "(unknown)\t1\t33.3\nDE\t1\t33.3\nUS\t1\t33.3"},
		{TrafficDevicesPanel, none, "desktop\t2\t66.7\nmobile\t1\t33.3"},
		{TrafficBrowsersPanel, none, "Chrome\t1\t33.3\nFirefox\t1\t33.3\nSafari\t1\t33.3"},
		{TrafficOSPanel, none, "Linux\t1\t33.3\nMac OS\t1\t33.3\niOS\t1\t33.3"},

		// A page: a and c viewed /pricing, so all their page views count;
		// p did not, so the day before has none.
		{TrafficVisitorsPanel, map[string]string{FilterPage: "/pricing"}, "2\t0"},
		{TrafficPageViewsPanel, map[string]string{FilterPage: "/pricing"}, "4\t0"},
		{TrafficPagesPanel, map[string]string{FilterPage: "/pricing"}, "/pricing\t2\t2\t100\n/\t1\t1\t50\n/signup\t1\t1\t50"},
		// Entry and exit pages are per visit: c's second visit entered on /.
		{TrafficVisitorsPanel, map[string]string{FilterEntry: "/"}, "2\t1"},
		{TrafficVisitorsPanel, map[string]string{FilterExit: "/signup"}, "1\t0"},
		// First touch.
		{TrafficVisitorsPanel, map[string]string{FilterChannel: "Email"}, "1\t0"},
		{TrafficVisitorsPanel, map[string]string{FilterChannel: "Direct"}, "1\t1"},
		{TrafficVisitorsPanel, map[string]string{FilterReferrer: "www.google.com"}, "1\t0"},
		{TrafficVisitorsPanel, map[string]string{FilterCampaign: "launch"}, "1\t0"},
		// The edge's columns, the unknown row included.
		{TrafficVisitorsPanel, map[string]string{FilterCountry: "(unknown)"}, "1\t0"},
		{TrafficVisitorsPanel, map[string]string{FilterDevice: "desktop"}, "2\t1"},
		{TrafficVisitorsPanel, map[string]string{FilterBrowser: "Safari"}, "1\t0"},
		{TrafficVisitorsPanel, map[string]string{FilterOS: "Mac OS"}, "1\t1"},
		// Filters combine with AND.
		{TrafficVisitorsPanel, map[string]string{FilterPage: "/pricing", FilterDevice: "desktop"}, "2\t0"},
		{TrafficVisitorsPanel, map[string]string{FilterPage: "/pricing", FilterCountry: "US"}, "1\t0"},
		{TrafficBouncePanel, map[string]string{FilterPage: "/pricing"}, "0.6667\t\\N\t3\t0"},
		{TrafficVisitDurationPanel, map[string]string{FilterChannel: "Email"}, "5000\t\\N\t1\t0"},
		{TrafficChannelsPanel, map[string]string{FilterDevice: "desktop"}, "Direct\t1\t50\nOrganic search\t1\t50"},
		// A value is a typed parameter, never SQL: a quote matches nothing.
		{TrafficVisitorsPanel, map[string]string{FilterPage: "x' OR '1'='1"}, "0\t0"},
		{TrafficVisitorsPanel, map[string]string{FilterChannel: `Email\' OR 1=1 --`}, "0\t0"},
	} {
		if got := run(c.id, c.f); got != c.want {
			t.Errorf("%s %v =\n%s\nwant\n%s", c.id, c.f, got, c.want)
		}
	}

	// Each breakdown row is the Visitors card under its own filter.
	for _, id := range []string{TrafficPagesPanel, TrafficEntryPagesPanel, TrafficExitPagesPanel, TrafficChannelsPanel, TrafficReferrersPanel,
		TrafficCampaignsPanel, TrafficCountriesPanel, TrafficDevicesPanel, TrafficBrowsersPanel, TrafficOSPanel} {
		p := panel(t, d, id)
		for _, row := range strings.Split(run(id, none), "\n") {
			cols := strings.Split(row, "\t")
			got := run(TrafficVisitorsPanel, map[string]string{p.Select.Param: cols[0]})
			if first := strings.Split(got, "\t")[0]; first != cols[1] {
				t.Errorf("%s row %q has %s visitors; filtered by it, Visitors reads %s", id, cols[0], cols[1], first)
			}
		}
	}

	// The trends add up to the figures, per hour and per day.
	for _, step := range []int64{3600, 86400} {
		trend := ch.filtered(panel(t, d, TrafficVisitorsPanel).TrendSQL, start, end, nil, step, "e1")
		if sum := sumLast(t, trend); sum != 3 {
			t.Errorf("visitors trend by %ds sums to %d, want 3:\n%s", step, sum, trend)
		}
		trend = ch.filtered(panel(t, d, TrafficPageViewsPanel).TrendSQL, start, end, map[string]string{FilterPage: "/pricing"}, step, "e1")
		if sum := sumLast(t, trend); sum != 4 {
			t.Errorf("filtered page views trend by %ds sums to %d, want 4:\n%s", step, sum, trend)
		}
	}
	for _, id := range []string{TrafficBouncePanel, TrafficVisitDurationPanel} {
		ch.filtered(panel(t, d, id).TrendSQL, start, end, map[string]string{FilterDevice: "desktop"}, 3600, "e1")
	}

	// Visitors now: c's last event is within the 5 minutes before 13:03.
	if got := ch.filtered(sql(TrafficLiveVisitorsPanel), start, start.Add(13*time.Hour+3*time.Minute), nil, 60, "e1"); got != "1" {
		t.Errorf("visitors now at 13:03 = %s, want 1", got)
	}
}

// sumLast is the sum of the last column of TSV rows.
func sumLast(t *testing.T, tsv string) int {
	t.Helper()
	sum := 0
	for _, row := range strings.Split(tsv, "\n") {
		cols := strings.Split(row, "\t")
		var n int
		for _, ch := range cols[len(cols)-1] {
			if ch < '0' || ch > '9' {
				t.Fatalf("not a count: %q", row)
			}
			n = n*10 + int(ch-'0')
		}
		sum += n
	}
	return sum
}
