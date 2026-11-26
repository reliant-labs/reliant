// Copyright (c) 2026 Reliant Labs

//go:build clickhouse

package analytics

// The ClickHouse check: the sql templates run against a real ClickHouse,
// over the hosted product_events table's columns, bound the way the control
// plane binds a sql panel (forge obsquery.SQLPanel), and the answers go
// through decode. It follows forge's pkg/obsbuiltin/clickhouse_harness_test.go.
//
// It needs a running ClickHouse container with clickhouse-client:
//
//	docker run -d --name ch -e CLICKHOUSE_PASSWORD=pw clickhouse/clickhouse-server
//	GTM_ANALYTICS_CLICKHOUSE=ch go test -tags clickhouse ./internal/gtm/analytics/
//
// GTM_ANALYTICS_CLICKHOUSE_PASSWORD overrides the password "pw". Without the
// container variable every test here skips.

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/obsquery"

	obsv1 "github.com/reliant-labs/reliant/gen/controlplane/services/observability/v1"
	"github.com/reliant-labs/reliant/internal/gtm"
)

const chDatabase = "gtm_analytics_test"

// chSeed is the tenant's product_events (the columns of control-plane's
// tenant schema the templates read, and the identifiers they must not) and
// a known set of events in the golden queries' window [2026-11-01,
// 2026-11-15):
//
//	page_viewed       30 events, users u0..u9; even hours env-prod, odd env-dev;
//	                  route /a (u0..u4, 15 events), /b (u5..u7, 9), /c (u8, u9, 6)
//	signup_completed   8 events, users u0..u3
//	                  and route /m on 2026-11-06, env-prod: users ua, ub and
//	                  8 anonymous events (10; under k: anonymous counts no one)
//	signup_completed   8 events, users u0..u3
//	rare_event         5 events, users u0, u1 (under k)
//	anon_event         6 events, no user (under k)
//	mixed_event       24 events, env-prod: users ua, ub and 20 anonymous (under k)
//
// and one page_viewed after the window. Counting the empty UserId as a user
// would show route /m and mixed_event (3 "users" each); the tests say they
// stay hidden.
const chSeed = `
DROP DATABASE IF EXISTS ` + chDatabase + `;
CREATE DATABASE ` + chDatabase + `;
CREATE TABLE ` + chDatabase + `.product_events (
    Timestamp DateTime64(9), ServiceName LowCardinality(String), EnvId LowCardinality(String),
    SessionId String, UserId String, EventName LowCardinality(String), Props Map(LowCardinality(String), String),
    Client LowCardinality(String), VisitorId String,
    DayVisitor String DEFAULT '', Country LowCardinality(String) DEFAULT '', Region LowCardinality(String) DEFAULT '',
    DeviceType LowCardinality(String) DEFAULT '', Browser LowCardinality(String) DEFAULT '', OS LowCardinality(String) DEFAULT '',
    Privacy LowCardinality(String) DEFAULT '', SaltScope LowCardinality(String) DEFAULT '',
    OrgId String DEFAULT ''
) ENGINE = MergeTree ORDER BY (ServiceName, EventName, Timestamp);
INSERT INTO ` + chDatabase + `.product_events (Timestamp, ServiceName, EnvId, SessionId, UserId, EventName, Props, Client, VisitorId)
SELECT toDateTime64('2026-11-01 00:00:00', 9, 'UTC') + toIntervalHour(number), 'web', if(number % 2 = 0, 'env-prod', 'env-dev'),
  concat('s', toString(number)), concat('u', toString(number % 10)), 'page_viewed',
  map('route', multiIf(number % 10 < 5, '/a', number % 10 < 8, '/b', '/c')), 'web', 'v'
FROM numbers(30);
INSERT INTO ` + chDatabase + `.product_events (Timestamp, ServiceName, EnvId, SessionId, UserId, EventName, Props, Client, VisitorId)
SELECT toDateTime64('2026-11-03 00:00:00', 9, 'UTC') + toIntervalHour(number), 'web', if(number % 2 = 0, 'env-prod', 'env-dev'),
  's', concat('u', toString(number % 4)), 'signup_completed', map(), 'web', 'v'
FROM numbers(8);
INSERT INTO ` + chDatabase + `.product_events (Timestamp, ServiceName, EnvId, SessionId, UserId, EventName, Props, Client, VisitorId)
SELECT toDateTime64('2026-11-04 00:00:00', 9, 'UTC') + toIntervalHour(number), 'web', if(number % 2 = 0, 'env-prod', 'env-dev'),
  's', concat('u', toString(number % 2)), 'rare_event', map(), 'web', 'v'
FROM numbers(5);
INSERT INTO ` + chDatabase + `.product_events (Timestamp, ServiceName, EnvId, SessionId, UserId, EventName, Props, Client, VisitorId)
SELECT toDateTime64('2026-11-05 00:00:00', 9, 'UTC') + toIntervalHour(number), 'web', 'env-prod', 's', '', 'anon_event', map(), 'web', 'v'
FROM numbers(6);
INSERT INTO ` + chDatabase + `.product_events (Timestamp, ServiceName, EnvId, SessionId, UserId, EventName, Props, Client, VisitorId)
SELECT toDateTime64('2026-11-06 00:00:00', 9, 'UTC') + toIntervalHour(number), 'web', 'env-prod', 's',
  if(number < 2, ['ua', 'ub'][number + 1], ''), 'page_viewed', map('route', '/m'), 'web', 'v'
FROM numbers(10);
INSERT INTO ` + chDatabase + `.product_events (Timestamp, ServiceName, EnvId, SessionId, UserId, EventName, Props, Client, VisitorId)
SELECT toDateTime64('2026-11-07 00:00:00', 9, 'UTC') + toIntervalMinute(number), 'web', 'env-prod', 's',
  if(number < 4, ['ua', 'ub'][number % 2 + 1], ''), 'mixed_event', map(), 'web', 'v'
FROM numbers(24);
INSERT INTO ` + chDatabase + `.product_events (Timestamp, ServiceName, EnvId, SessionId, UserId, EventName, Props, Client, VisitorId)
VALUES (toDateTime64('2026-12-01 00:00:00', 9, 'UTC'), 'web', 'env-prod', 's', 'u0', 'page_viewed', map('route', '/a'), 'web', 'v');
`

type chContainer struct {
	t         *testing.T
	container string
	password  string
}

// newClickHouse seeds chSeed, or skips without a container.
func newClickHouse(t *testing.T) *chContainer {
	t.Helper()
	container := os.Getenv("GTM_ANALYTICS_CLICKHOUSE")
	if testing.Short() || container == "" {
		t.Skip("needs a ClickHouse container; set GTM_ANALYTICS_CLICKHOUSE=<container>")
	}
	pw := os.Getenv("GTM_ANALYTICS_CLICKHOUSE_PASSWORD")
	if pw == "" {
		pw = "pw"
	}
	c := &chContainer{t: t, container: container, password: pw}
	c.exec(chSeed, "--multiquery")
	t.Cleanup(func() { c.exec("DROP DATABASE IF EXISTS "+chDatabase, "--multiquery") })
	return c
}

func (c *chContainer) exec(stdin string, args ...string) string {
	c.t.Helper()
	cmd := exec.Command("docker", append([]string{"exec", "-i", c.container, "clickhouse-client", "--password", c.password}, args...)...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		c.t.Fatalf("clickhouse: %v\n%s", err, firstLines(string(out), 6))
	}
	return string(out)
}

// panel runs q's statement as the control plane runs a sql panel over envs
// ([] is every environment), folds the rows as a draw "table" response
// does (numeric columns to values, the rest to the group) and decodes it.
func (c *chContainer) panel(q gtm.AnalyticsQuery, envs ...string) gtm.AnalyticsResult {
	c.t.Helper()
	sql := buildSQL(q)
	sc := obsquery.Scope{Start: q.From, End: q.To, Database: chDatabase, EnvRestricted: len(envs) > 0, EnvIDs: envs}
	for _, e := range envs {
		sc.Envs = append(sc.Envs, obsquery.Env{ID: e, Name: e})
	}
	st, err := obsquery.SQLPanel{Scope: sc, SQL: sql, StepSeconds: 86400}.Compile()
	if err != nil {
		c.t.Fatalf("compile: %v\n%s", err, sql)
	}
	args := []string{"--database", chDatabase, "--format", "TSVWithNames"}
	for k, v := range st.Params {
		args = append(args, "--param_"+k+"="+v)
	}
	out := c.exec(st.SQL, args...)
	resp, err := foldTable(out)
	if err != nil {
		c.t.Fatalf("fold: %v\n%s", err, out)
	}
	res, err := decode(q, resp)
	if err != nil {
		c.t.Fatalf("decode: %v\n%s", err, out)
	}
	return res
}

// numeric are the columns a draw "table" response carries as values.
var numeric = map[string]bool{countColumn: true, groupsColumn: true, hiddenColumn: true}

func foldTable(tsv string) (*obsv1.QueryPanelResponse, error) {
	lines := strings.Split(strings.TrimRight(tsv, "\n"), "\n")
	header := strings.Split(lines[0], "\t")
	resp := &obsv1.QueryPanelResponse{Kind: "sql", Draw: "table"}
	for _, h := range header {
		if numeric[h] {
			resp.Columns = append(resp.Columns, h)
		}
	}
	for _, line := range lines[1:] {
		cells := strings.Split(line, "\t")
		if len(cells) != len(header) {
			return nil, fmt.Errorf("row %q has %d cells", line, len(cells))
		}
		row := &obsv1.PanelTableRow{Group: map[string]string{}}
		for i, h := range header {
			if !numeric[h] {
				row.Group[h] = cells[i]
				continue
			}
			v, err := strconv.ParseFloat(cells[i], 64)
			if err != nil {
				return nil, fmt.Errorf("%s = %q: %w", h, cells[i], err)
			}
			row.Values = append(row.Values, v)
		}
		resp.Rows = append(resp.Rows, row)
	}
	return resp, nil
}

func firstLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

// rowsText is rows as "event|value|day|count" lines, for comparing.
func rowsText(rows []gtm.AnalyticsRow) string {
	var b strings.Builder
	for _, r := range rows {
		day := ""
		if !r.Day.IsZero() {
			day = r.Day.Format("2006-01-02")
		}
		fmt.Fprintf(&b, "%s|%s|%s|%d\n", r.Event, r.Value, day, r.Count)
	}
	return b.String()
}

func TestClickHouseTemplates(t *testing.T) {
	c := newClickHouse(t)
	for _, tc := range []struct {
		name      string
		q         gtm.AnalyticsQuery
		envs      []string
		rows      string
		hidden    int
		truncated bool
	}{
		{name: "event_counts", q: sqlQueries["event_counts"],
			rows: "page_viewed|||40\nsignup_completed|||8\n", hidden: 3},
		{name: "event_counts env-prod", q: sqlQueries["event_counts"], envs: []string{"env-prod"},
			rows: "page_viewed|||25\n", hidden: 4},
		{name: "event_counts limit 1", q: with(sqlQueries["event_counts"], func(q *gtm.AnalyticsQuery) { q.Limit = 1 }),
			rows: "page_viewed|||40\n", hidden: 3, truncated: true},
		{name: "event_counts_filtered", q: sqlQueries["event_counts_filtered"],
			rows: "signup_completed|||8\n"},
		{name: "event_counts all hidden", q: with(sqlQueries["event_counts"], func(q *gtm.AnalyticsQuery) { q.Events = []string{"rare_event", "anon_event", "mixed_event"} }),
			rows: "", hidden: 3},
		{name: "two users and anonymous traffic stay hidden", q: with(sqlQueries["event_counts"], func(q *gtm.AnalyticsQuery) { q.Events = []string{"mixed_event"} }),
			rows: "", hidden: 1},
		{name: "event_trend", q: sqlQueries["event_trend"],
			rows: "page_viewed||2026-11-01|24\npage_viewed||2026-11-02|6\npage_viewed||2026-11-06|10\n"},
		{name: "event_trend limit 1 keeps the newest day", q: with(sqlQueries["event_trend"], func(q *gtm.AnalyticsQuery) { q.Limit = 1 }),
			rows: "page_viewed||2026-11-06|10\n", truncated: true},
		{name: "property_breakdown", q: sqlQueries["property_breakdown"],
			rows: "page_viewed|/a||15\npage_viewed|/b||9\n", hidden: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validate(tenantZero, tc.q); err != nil {
				t.Fatal(err)
			}
			res := c.panel(tc.q, tc.envs...)
			if got := rowsText(res.Rows); got != tc.rows {
				t.Errorf("rows:\n%s\nwant:\n%s", got, tc.rows)
			}
			if res.HiddenGroups != tc.hidden || res.Truncated != tc.truncated {
				t.Errorf("hidden = %d, truncated = %v; want %d, %v", res.HiddenGroups, res.Truncated, tc.hidden, tc.truncated)
			}
		})
	}
}

func with(q gtm.AnalyticsQuery, f func(*gtm.AnalyticsQuery)) gtm.AnalyticsQuery {
	q.Events = append([]string(nil), q.Events...)
	f(&q)
	return q
}
