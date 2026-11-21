package obsbuiltin

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/obsquery"
)

// uptimeSeed is an hour of probes in e1, one a minute, from 00:00:
//
//	site: fails at :10-:13 (an incident, closed by :14's pass), at :30 alone
//	      (noise), and at :57-:59 (an incident still open at 01:00); 0.1s a
//	      probe; a certificate with 30 days left
//	api:  passes from :00 to :19, then stops reporting (unknown at 01:00)
//	old:  fails at 23:50-23:52 the day before and passes at :05 (an incident
//	      that began before the range)
//
// and a probe of e2 the scope never reads.
func uptimeSeed() string {
	var rows []string
	put := func(env, check, metric string, at time.Time, v float64, attrs string) {
		rows = append(rows, fmt.Sprintf("({'reliant.env.id': '%s', 'deployment.environment.name': '%s'}, 'uptime-prober', '%s', {'check': '%s'%s}, '%s', %g)",
			env, env, metric, check, attrs, at.Format("2006-01-02 15:04:05"), v))
	}
	base := time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC)
	up := func(env, check string, at time.Time, pass bool) {
		if pass {
			put(env, check, "uptime.check.up", at, 1, ", 'http.response.status_code': '200'")
		} else {
			put(env, check, "uptime.check.up", at, 0, ", 'error.type': 'timeout'")
		}
	}
	for m := 0; m < 60; m++ {
		at := base.Add(time.Duration(m) * time.Minute)
		up("e1", "site", at, !(m >= 10 && m <= 13 || m == 30 || m >= 57))
		put("e1", "site", "uptime.check.duration", at, 0.1, "")
		put("e1", "site", "tls.cert.expiry_seconds", at, 30*86400, "")
		if m < 20 {
			up("e1", "api", at, true)
		}
	}
	for _, m := range []int{-10, -9, -8} {
		up("e1", "old", base.Add(time.Duration(m)*time.Minute), false)
	}
	up("e1", "old", base.Add(5*time.Minute), true)
	up("e2", "site", base.Add(30*time.Minute), false)
	return "INSERT INTO forge_obsbuiltin_uptime.otel_metrics_gauge (ResourceAttributes, ServiceName, MetricName, Attributes, TimeUnix, Value) VALUES\n" +
		strings.Join(rows, ",\n") + ";\n"
}

// TestUptimeOnAnHour pins every Uptime statement over uptimeSeed, worked by
// hand: incidents need three failures in a row, close at the next pass, may
// start before the range, and an open one counts its downtime to the end of
// the range.
func TestUptimeOnAnHour(t *testing.T) {
	ch := newClickHouse(t, "forge_obsbuiltin_uptime", uptimeSeed())
	start := time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	for _, c := range []struct {
		name, sql, want string
	}{
		{"checks", obsquery.UptimeChecksSQL(), strings.Join([]string{
			"site\tdown\t86.667\t60\t8\t2\t420\t2026-03-16 00:57:00\t100\t100\t30\t2026-03-16 00:59:00",
			"api\tunknown\t100\t20\t0\t0\t0\t\\N\t\\N\t\\N\t\\N\t2026-03-16 00:19:00",
			"old\tunknown\t100\t1\t0\t1\t300\t\\N\t\\N\t\\N\t\\N\t2026-03-16 00:05:00",
		}, "\n")},
		{"summary", obsquery.UptimeSummarySQL(), "0.90123\t3\t1\t2\t720"},
		{"incidents", obsquery.UptimeIncidentsSQL(), strings.Join([]string{
			"site\t2026-03-16 00:57:00\t\\N\t180\t3\ttimeout",
			"site\t2026-03-16 00:10:00\t2026-03-16 00:14:00\t240\t4\ttimeout",
			"old\t2026-03-15 23:50:00\t2026-03-16 00:05:00\t900\t3\ttimeout",
		}, "\n")},
	} {
		if got := ch.run(c.sql, start, end, "e1"); got != c.want {
			t.Errorf("%s =\n%s\nwant\n%s", c.name, got, c.want)
		}
	}
	// The timeline by 15 minutes: site lost 4 of 15 probes in the first
	// quarter, none in the second, 1 in the third, 3 in the fourth; api
	// reported in the first two only; old once.
	got := ch.filtered(obsquery.UptimeTimelineSQL(), start, end, nil, 900, "e1")
	want := strings.Join([]string{
		"2026-03-16 00:00:00\tapi\t1", "2026-03-16 00:00:00\told\t1", "2026-03-16 00:00:00\tsite\t0.7333",
		"2026-03-16 00:15:00\tapi\t1", "2026-03-16 00:15:00\tsite\t1",
		"2026-03-16 00:30:00\tsite\t0.9333",
		"2026-03-16 00:45:00\tsite\t0.8",
	}, "\n")
	if got != want {
		t.Errorf("timeline =\n%s\nwant\n%s", got, want)
	}
	// Over both environments a check is named with its environment's.
	if got := ch.run(obsquery.UptimeIncidentsSQL(), start, end, "e1", "e2"); !strings.Contains(got, "site (e1)") {
		t.Errorf("incidents over two environments do not name them:\n%s", got)
	}
	// The dashboard runs the same statements.
	for _, p := range Uptime(Input{Project: "shop", Uptime: true}).Panels {
		ch.filtered(p.SQL, start, end, nil, 900, "e1")
	}
}

// uptimeRunsSeed holds the incidents a run of consecutive failures makes, in
// e1, for the range 00:00 to 01:00:
//
//	flap: a probe a minute; fails at :10-:12 and :14-:16 (two incidents a
//	      pass apart, not one), and at :30-:31 then :33-:35 (one incident,
//	      from :33: the two failures before the pass are noise)
//	long: a probe every 5 minutes from 20:00 the day before; passes until
//	      20:55, then fails from 21:00 through 00:55: an incident three hours
//	      older than the range, still open at its end
//	gone: fails at 23:00, 23:05 and 23:10 the day before, then stops
//	      reporting: open, but unknown at 01:00
func uptimeRunsSeed() string {
	var rows []string
	up := func(check string, at time.Time, pass bool) {
		v, attrs := 1, "'http.response.status_code': '200'"
		if !pass {
			v, attrs = 0, "'error.type': 'timeout'"
		}
		rows = append(rows, fmt.Sprintf("({'reliant.env.id': 'e1', 'deployment.environment.name': 'e1'}, 'uptime-prober', 'uptime.check.up', {'check': '%s', %s}, '%s', %d)",
			check, attrs, at.Format("2006-01-02 15:04:05"), v))
	}
	base := time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC)
	for m := 0; m < 60; m++ {
		up("flap", base.Add(time.Duration(m)*time.Minute), !(m >= 10 && m <= 12 || m >= 14 && m <= 16 || m == 30 || m == 31 || m >= 33 && m <= 35))
	}
	for at := base.Add(-4 * time.Hour); at.Before(base.Add(time.Hour)); at = at.Add(5 * time.Minute) {
		up("long", at, at.Before(base.Add(-3*time.Hour)))
	}
	for _, m := range []int{-60, -55, -50} {
		up("gone", base.Add(time.Duration(m)*time.Minute), false)
	}
	return "INSERT INTO forge_obsbuiltin_uptime_runs.otel_metrics_gauge (ResourceAttributes, ServiceName, MetricName, Attributes, TimeUnix, Value) VALUES\n" +
		strings.Join(rows, ",\n") + ";\n"
}

// TestUptimeIncidentsAreRunsOfFailures pins that an incident is one run of
// consecutive failures: a passing probe between failures splits them (two
// outages are two incidents, each from its own first failure, and failures
// before a pass never join the run after it), and a run that began long
// before the range, with no passing probe since, is one open incident from
// where it began, so the check is down at the end of the range.
func TestUptimeIncidentsAreRunsOfFailures(t *testing.T) {
	ch := newClickHouse(t, "forge_obsbuiltin_uptime_runs", uptimeRunsSeed())
	start := time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	for _, c := range []struct {
		name, sql, want string
	}{
		{"incidents", obsquery.UptimeIncidentsSQL(), strings.Join([]string{
			"flap\t2026-03-16 00:33:00\t2026-03-16 00:36:00\t180\t3\ttimeout",
			"flap\t2026-03-16 00:14:00\t2026-03-16 00:17:00\t180\t3\ttimeout",
			"flap\t2026-03-16 00:10:00\t2026-03-16 00:13:00\t180\t3\ttimeout",
			"gone\t2026-03-15 23:00:00\t\\N\t7200\t3\ttimeout",
			"long\t2026-03-15 21:00:00\t\\N\t14400\t48\ttimeout",
		}, "\n")},
		{"checks", obsquery.UptimeChecksSQL(), strings.Join([]string{
			"long\tdown\t0\t12\t12\t1\t3600\t2026-03-15 21:00:00\t\\N\t\\N\t\\N\t2026-03-16 00:55:00",
			"gone\tunknown\t\\N\t0\t0\t1\t3600\t2026-03-15 23:00:00\t\\N\t\\N\t\\N\t2026-03-15 23:10:00",
			"flap\tup\t81.667\t60\t11\t3\t540\t\\N\t\\N\t\\N\t\\N\t2026-03-16 00:59:00",
		}, "\n")},
		{"summary", obsquery.UptimeSummarySQL(), "0.68056\t3\t1\t1\t7740"},
	} {
		if got := ch.run(c.sql, start, end, "e1"); got != c.want {
			t.Errorf("%s =\n%s\nwant\n%s", c.name, got, c.want)
		}
	}
}
