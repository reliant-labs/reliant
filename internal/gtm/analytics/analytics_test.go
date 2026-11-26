// Copyright (c) 2026 Reliant Labs

package analytics

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	obsv1 "github.com/reliant-labs/reliant/gen/controlplane/services/observability/v1"
	"github.com/reliant-labs/reliant/internal/gtm"
)

// fakeCP is the control plane's QueryPanel: it records each request and
// answers with resp or err.
type fakeCP struct {
	reqs []*connect.Request[obsv1.QueryPanelRequest]
	resp *obsv1.QueryPanelResponse
	err  error
}

func (f *fakeCP) QueryPanel(_ context.Context, req *connect.Request[obsv1.QueryPanelRequest]) (*connect.Response[obsv1.QueryPanelResponse], error) {
	f.reqs = append(f.reqs, req)
	if f.err != nil {
		return nil, f.err
	}
	resp := f.resp
	if resp == nil {
		resp = &obsv1.QueryPanelResponse{}
	}
	return connect.NewResponse(resp), nil
}

func token(s string) func() string { return func() string { return s } }

// testToken is distinctive, so a test can tell it leaked.
const testToken = "rlat_TESTTOKEN_7f3a9c_do-not-log"

func newTestClient(cp ObservabilityClient, tok string, log *bytes.Buffer) *client {
	d := Deps{CP: cp, Token: token(tok)}
	if log != nil {
		d.Log = slog.New(slog.NewTextHandler(log, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	return newClient(d)
}

// kRows is a k-anonymous template's answer drawn as a table: one 'group' row
// per given group (counts 100, 99, ...), then the summary row with the
// groups shown before LIMIT and the groups hidden.
func kRows(shown, hidden int64, rows ...map[string]string) *obsv1.QueryPanelResponse {
	resp := &obsv1.QueryPanelResponse{Kind: "sql", Columns: []string{countColumn, groupsColumn, hiddenColumn}}
	for i, g := range rows {
		g[kindColumn] = kindGroup
		resp.Rows = append(resp.Rows, &obsv1.PanelTableRow{Group: g, Values: []float64{float64(100 - i), 0, 0}})
	}
	resp.Rows = append(resp.Rows, &obsv1.PanelTableRow{
		Group:  map[string]string{kindColumn: kindSummary, eventColumn: "", valueColumn: ""},
		Values: []float64{0, float64(shown), float64(hidden)},
	})
	return resp
}

// trendRows is event_trend's answer: counts 100, 99, ... and the groups
// before LIMIT on every row.
func trendRows(groups int64, rows ...map[string]string) *obsv1.QueryPanelResponse {
	resp := &obsv1.QueryPanelResponse{Kind: "sql", Columns: []string{countColumn, groupsColumn}}
	for i, g := range rows {
		resp.Rows = append(resp.Rows, &obsv1.PanelTableRow{Group: g, Values: []float64{float64(100 - i), float64(groups)}})
	}
	return resp
}

// emptyFor is an empty answer of q's shape.
func emptyFor(q gtm.AnalyticsQuery) *obsv1.QueryPanelResponse {
	if q.Template == gtm.AnalyticsEventTrend {
		return trendRows(0)
	}
	return kRows(0, 0)
}

func ev(e string) map[string]string { return map[string]string{eventColumn: e} }

func TestEmptyTokenFailsClosed(t *testing.T) {
	q := sqlQueries["event_counts"]
	for name, c := range map[string]*client{
		"empty token": newTestClient(&fakeCP{}, "", nil),
		"blank token": newTestClient(&fakeCP{}, "  \n", nil),
		"nil token":   newClient(Deps{CP: &fakeCP{}}),
		"nil cp":      newClient(Deps{Token: token(testToken)}),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := c.Query(context.Background(), tenantZero, q)
			if !errors.Is(err, gtm.ErrAnalyticsUnavailable) {
				t.Fatalf("err = %v, want ErrAnalyticsUnavailable", err)
			}
			if cp, ok := c.cp.(*fakeCP); ok && len(cp.reqs) != 0 {
				t.Fatalf("called the control plane %d times", len(cp.reqs))
			}
		})
	}
}

func TestQueryScopesToTheOrgWithTheToken(t *testing.T) {
	for name, q := range sqlQueries {
		t.Run(name, func(t *testing.T) {
			cp := &fakeCP{resp: emptyFor(q)}
			if _, err := newTestClient(cp, testToken, nil).Query(context.Background(), tenantZero, q); err != nil {
				t.Fatal(err)
			}
			if len(cp.reqs) != 1 {
				t.Fatalf("%d requests", len(cp.reqs))
			}
			req := cp.reqs[0]
			if got := req.Header().Get("Authorization"); got != "Bearer "+testToken {
				t.Errorf("Authorization = %q", got)
			}
			s := req.Msg.GetScope()
			if s.GetOrgId() != tenantZero {
				t.Errorf("scope org = %q, want %q", s.GetOrgId(), tenantZero)
			}
			if s.ProjectId != nil || len(s.GetEnvIds()) != 0 || s.GetPlatformScope() || len(s.GetServices()) != 0 || len(s.GetFilters()) != 0 {
				t.Errorf("scope narrowed beyond the org: %v", s)
			}
			if !s.GetTimeRange().GetStart().AsTime().Equal(q.From) || !s.GetTimeRange().GetEnd().AsTime().Equal(q.To) {
				t.Errorf("time range = %v, want [%v, %v)", s.GetTimeRange(), q.From, q.To)
			}
			p := req.Msg.GetPanel()
			if p.GetKind() != "sql" || p.GetDraw() != "table" || p.GetSql() != buildSQL(q) || p.GetFunnel() != nil {
				t.Errorf("panel = %v", p)
			}
			if req.Msg.GetPanelRef() != nil || req.Msg.GetExplain() || len(req.Msg.GetGroupBy()) != 0 || len(req.Msg.GetFilters()) != 0 {
				t.Errorf("request carries more than the panel: %v", req.Msg)
			}
		})
	}
}

func TestOrgIsThePassedOrg(t *testing.T) {
	cp := &fakeCP{resp: kRows(0, 0)}
	c := newTestClient(cp, testToken, nil)
	for _, org := range []string{tenantZero, "0b7f3c1e-0000-4000-8000-000000000001"} {
		if _, err := c.Query(context.Background(), org, sqlQueries["event_counts"]); err != nil {
			t.Fatal(err)
		}
		if got := cp.reqs[len(cp.reqs)-1].Msg.GetScope().GetOrgId(); got != org {
			t.Errorf("scope org = %q, want %q", got, org)
		}
	}
	if _, err := c.Query(context.Background(), "", sqlQueries["event_counts"]); !errors.Is(err, ErrNoOrg) {
		t.Errorf("no org: err = %v", err)
	}
}

func TestFunnelRequestAndResult(t *testing.T) {
	q := gtm.AnalyticsQuery{Template: gtm.AnalyticsFunnel, Events: []string{"signup_started", "signup_completed", "project_created"},
		From: to.Add(-90 * 24 * time.Hour), To: to, Limit: 50}
	cp := &fakeCP{resp: &obsv1.QueryPanelResponse{Kind: "funnel", FunnelSteps: []*obsv1.FunnelStepResult{
		{Event: "signup_started", Count: 200, RateFromFirst: 1, RateFromPrevious: 1},
		{Event: "signup_completed", Count: 120, RateFromFirst: 0.6, RateFromPrevious: 0.6},
		{Event: "project_created", Count: 30, RateFromFirst: 0.15, RateFromPrevious: 0.25},
	}}}
	res, err := newTestClient(cp, testToken, nil).Query(context.Background(), tenantZero, q)
	if err != nil {
		t.Fatal(err)
	}
	p := cp.reqs[0].Msg.GetPanel()
	if p.GetKind() != "funnel" || p.GetSql() != "" {
		t.Fatalf("panel = %v", p)
	}
	f := p.GetFunnel()
	if strings.Join(f.GetSteps(), ",") != strings.Join(q.Events, ",") || f.GetBy() != "user" || f.GetWindow() != "2160h" {
		t.Errorf("funnel = %v", f)
	}
	want := []gtm.AnalyticsRow{
		{Event: "signup_started", Step: 1, Count: 200, Rate: 1},
		{Event: "signup_completed", Step: 2, Count: 120, Rate: 0.6},
		{Event: "project_created", Step: 3, Count: 30, Rate: 0.15},
	}
	if len(res.Rows) != len(want) {
		t.Fatalf("rows = %v", res.Rows)
	}
	for i := range want {
		if res.Rows[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, res.Rows[i], want[i])
		}
	}
	if res.Template != gtm.AnalyticsFunnel || !res.From.Equal(q.From) || !res.To.Equal(q.To) || res.Truncated || res.HiddenGroups != 0 {
		t.Errorf("result = %+v", res)
	}
}

func TestFunnelWindow(t *testing.T) {
	for d, want := range map[time.Duration]string{
		time.Minute:          "1h",
		time.Hour:            "1h",
		90 * time.Minute:     "2h",
		7 * 24 * time.Hour:   "168h",
		90 * 24 * time.Hour:  "2160h",
		0:                    "1h",
		-5 * time.Hour:       "1h",
		36*time.Hour + 1e9:   "37h",
		24*time.Hour - 1e9:   "24h",
		100 * 24 * time.Hour: "2400h",
	} {
		if got := funnelWindow(d); got != want {
			t.Errorf("funnelWindow(%v) = %s, want %s", d, got, want)
		}
	}
}

func TestFunnelBadResponses(t *testing.T) {
	q := gtm.AnalyticsQuery{Template: gtm.AnalyticsFunnel, Events: []string{"a", "b"}, From: from, To: to, Limit: 10}
	for name, steps := range map[string][]*obsv1.FunnelStepResult{
		"too few steps":  {{Event: "a", Count: 1}},
		"too many steps": {{Event: "a", Count: 2}, {Event: "b", Count: 1}, {Event: "c", Count: 1}},
		"steps swapped":  {{Event: "b", Count: 2}, {Event: "a", Count: 1}},
		"another event":  {{Event: "a", Count: 2}, {Event: "user@example.com", Count: 1}},
		"empty event":    {{Event: "a", Count: 2}, {Event: "", Count: 1}},
	} {
		t.Run(name, func(t *testing.T) {
			cp := &fakeCP{resp: &obsv1.QueryPanelResponse{FunnelSteps: steps}}
			_, err := newTestClient(cp, testToken, &bytes.Buffer{}).Query(context.Background(), tenantZero, q)
			if !errors.Is(err, ErrBadResponse) || !errors.Is(err, gtm.ErrAnalyticsFailed) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestSQLResults(t *testing.T) {
	t.Run("event_counts", func(t *testing.T) {
		cp := &fakeCP{resp: kRows(2, 0, ev("page_viewed"), ev("chat.started"))}
		res, err := newTestClient(cp, testToken, nil).Query(context.Background(), tenantZero, sqlQueries["event_counts"])
		if err != nil {
			t.Fatal(err)
		}
		want := []gtm.AnalyticsRow{{Event: "page_viewed", Count: 100}, {Event: "chat.started", Count: 99}}
		if len(res.Rows) != 2 || res.Rows[0] != want[0] || res.Rows[1] != want[1] || res.Truncated || res.HiddenGroups != 0 {
			t.Fatalf("result = %+v", res)
		}
	})
	t.Run("event_counts in any row order", func(t *testing.T) {
		resp := kRows(3, 0, ev("b"), ev("a"), ev("c"))
		// The summary first and the groups shuffled, as a union may return
		// them.
		resp.Rows = []*obsv1.PanelTableRow{resp.Rows[3], resp.Rows[2], resp.Rows[0], resp.Rows[1]}
		res, err := newTestClient(&fakeCP{resp: resp}, testToken, nil).Query(context.Background(), tenantZero, sqlQueries["event_counts"])
		if err != nil {
			t.Fatal(err)
		}
		if got := []string{res.Rows[0].Event, res.Rows[1].Event, res.Rows[2].Event}; strings.Join(got, ",") != "b,a,c" {
			t.Fatalf("order = %v", got)
		}
	})
	t.Run("event_trend", func(t *testing.T) {
		cp := &fakeCP{resp: trendRows(1, map[string]string{dayColumn: "2026-11-03", eventColumn: "page_viewed"})}
		res, err := newTestClient(cp, testToken, nil).Query(context.Background(), tenantZero, sqlQueries["event_trend"])
		if err != nil {
			t.Fatal(err)
		}
		want := gtm.AnalyticsRow{Event: "page_viewed", Day: time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC), Count: 100}
		if len(res.Rows) != 1 || res.Rows[0] != want {
			t.Fatalf("rows = %+v", res.Rows)
		}
	})
	t.Run("event_trend bad day", func(t *testing.T) {
		cp := &fakeCP{resp: trendRows(1, map[string]string{dayColumn: "yesterday", eventColumn: "page_viewed"})}
		if _, err := newTestClient(cp, testToken, &bytes.Buffer{}).Query(context.Background(), tenantZero, sqlQueries["event_trend"]); !errors.Is(err, ErrBadResponse) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("property_breakdown", func(t *testing.T) {
		cp := &fakeCP{resp: kRows(2, 0, map[string]string{valueColumn: "/pricing"}, map[string]string{valueColumn: ""})}
		res, err := newTestClient(cp, testToken, nil).Query(context.Background(), tenantZero, sqlQueries["property_breakdown"])
		if err != nil {
			t.Fatal(err)
		}
		want := []gtm.AnalyticsRow{{Event: "page_viewed", Value: "/pricing", Count: 100}, {Event: "page_viewed", Value: "", Count: 99}}
		if len(res.Rows) != 2 || res.Rows[0] != want[0] || res.Rows[1] != want[1] {
			t.Fatalf("rows = %+v", res.Rows)
		}
	})
	t.Run("missing value is zero", func(t *testing.T) {
		resp := kRows(1, 0, ev("x"))
		resp.Rows[0].MissingValueIndexes = []uint32{0}
		res, err := newTestClient(&fakeCP{resp: resp}, testToken, nil).Query(context.Background(), tenantZero, sqlQueries["event_counts"])
		if err != nil || res.Rows[0].Count != 0 {
			t.Fatalf("res = %+v, err = %v", res, err)
		}
	})
	for name, mangle := range map[string]func(*obsv1.QueryPanelResponse){
		"no count column":  func(r *obsv1.QueryPanelResponse) { r.Columns = []string{"other", groupsColumn, hiddenColumn} },
		"no hidden column": func(r *obsv1.QueryPanelResponse) { r.Columns = []string{countColumn, groupsColumn} },
		"no summary row":   func(r *obsv1.QueryPanelResponse) { r.Rows = r.Rows[:len(r.Rows)-1] },
		"two summary rows": func(r *obsv1.QueryPanelResponse) { r.Rows = append(r.Rows, r.Rows[len(r.Rows)-1]) },
		"unknown row kind": func(r *obsv1.QueryPanelResponse) { r.Rows[0].Group[kindColumn] = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			resp := kRows(1, 0, ev("x"))
			mangle(resp)
			if _, err := newTestClient(&fakeCP{resp: resp}, testToken, &bytes.Buffer{}).Query(context.Background(), tenantZero, sqlQueries["event_counts"]); !errors.Is(err, ErrBadResponse) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestHiddenGroups(t *testing.T) {
	for _, name := range []string{"event_counts", "property_breakdown"} {
		q := sqlQueries[name]
		t.Run(name+" some hidden", func(t *testing.T) {
			res, err := newTestClient(&fakeCP{resp: kRows(1, 4, map[string]string{eventColumn: "a", valueColumn: "a"})}, testToken, nil).
				Query(context.Background(), tenantZero, q)
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Rows) != 1 || res.HiddenGroups != 4 || res.Truncated {
				t.Fatalf("result = %+v", res)
			}
		})
		t.Run(name+" all hidden", func(t *testing.T) {
			res, err := newTestClient(&fakeCP{resp: kRows(0, 7)}, testToken, nil).Query(context.Background(), tenantZero, q)
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Rows) != 0 || res.HiddenGroups != 7 {
				t.Fatalf("result = %+v", res)
			}
		})
	}
	t.Run("trend hides nothing", func(t *testing.T) {
		res, err := newTestClient(&fakeCP{resp: trendRows(1, map[string]string{dayColumn: "2026-11-03", eventColumn: "a"})}, testToken, nil).
			Query(context.Background(), tenantZero, sqlQueries["event_trend"])
		if err != nil || res.HiddenGroups != 0 {
			t.Fatalf("res = %+v, err = %v", res, err)
		}
	})
}

func TestValuesShapedLikeIdentifiersAreRedacted(t *testing.T) {
	resp := kRows(3, 0,
		map[string]string{valueColumn: "/pricing"},
		map[string]string{valueColumn: "jane.doe@example.com"},
		map[string]string{valueColumn: "10.1.2.3"},
	)
	res, err := newTestClient(&fakeCP{resp: resp}, testToken, nil).Query(context.Background(), tenantZero, sqlQueries["property_breakdown"])
	if err != nil {
		t.Fatal(err)
	}
	got := []string{res.Rows[0].Value, res.Rows[1].Value, res.Rows[2].Value}
	if strings.Join(got, ",") != "/pricing,"+Redacted+","+Redacted {
		t.Fatalf("values = %v", got)
	}
	// Event names event_counts reads from the data go through it too.
	resp = kRows(2, 0, ev("page_viewed"), ev("123e4567-e89b-12d3-a456-426614174000"))
	res, err = newTestClient(&fakeCP{resp: resp}, testToken, nil).Query(context.Background(), tenantZero, sqlQueries["event_counts"])
	if err != nil {
		t.Fatal(err)
	}
	if res.Rows[0].Event != "page_viewed" || res.Rows[1].Event != Redacted {
		t.Fatalf("rows = %+v", res.Rows)
	}
}

func TestRowCapAndTruncation(t *testing.T) {
	q := sqlQueries["event_counts"]
	q.Limit = 2
	t.Run("more groups than the limit", func(t *testing.T) {
		cp := &fakeCP{resp: kRows(9, 0, ev("a"), ev("b"))}
		res, err := newTestClient(cp, testToken, nil).Query(context.Background(), tenantZero, q)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Rows) != 2 || !res.Truncated {
			t.Fatalf("result = %+v", res)
		}
		if !strings.Contains(cp.reqs[0].Msg.GetPanel().GetSql(), " LIMIT 2)") {
			t.Errorf("sql = %s", cp.reqs[0].Msg.GetPanel().GetSql())
		}
	})
	t.Run("a control plane that returns too many rows", func(t *testing.T) {
		cp := &fakeCP{resp: kRows(0, 0, ev("a"), ev("b"), ev("c"))}
		res, err := newTestClient(cp, testToken, nil).Query(context.Background(), tenantZero, q)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Rows) != 2 || !res.Truncated || res.Rows[0].Event != "a" || res.Rows[1].Event != "b" {
			t.Fatalf("result = %+v", res)
		}
	})
	t.Run("exactly the limit", func(t *testing.T) {
		cp := &fakeCP{resp: kRows(2, 5, ev("a"), ev("b"))}
		res, err := newTestClient(cp, testToken, nil).Query(context.Background(), tenantZero, q)
		if err != nil || len(res.Rows) != 2 || res.Truncated {
			t.Fatalf("res = %+v, err = %v", res, err)
		}
	})
	t.Run("limit over the cap is refused before the control plane", func(t *testing.T) {
		cp := &fakeCP{}
		q := q
		q.Limit = gtm.MaxAnalyticsRows + 1
		if _, err := newTestClient(cp, testToken, nil).Query(context.Background(), tenantZero, q); !errors.Is(err, gtm.ErrInvalidArgument) || len(cp.reqs) != 0 {
			t.Fatalf("err = %v, %d requests", err, len(cp.reqs))
		}
	})
}

// TestTrendKeepsTheNewestDays: the statement takes the newest days under its
// LIMIT (ORDER BY day DESC); the answer reads oldest first, and a control
// plane that returns too many rows loses the oldest.
func TestTrendKeepsTheNewestDays(t *testing.T) {
	q := sqlQueries["event_trend"]
	q.Limit = 2
	if sql := buildSQL(q); !strings.Contains(sql, "ORDER BY day DESC, event\nLIMIT 2") {
		t.Fatalf("sql = %s", sql)
	}
	day := func(d string) map[string]string { return map[string]string{dayColumn: d, eventColumn: "page_viewed"} }
	t.Run("newest first in, oldest first out", func(t *testing.T) {
		cp := &fakeCP{resp: trendRows(5, day("2026-11-14"), day("2026-11-13"))}
		res, err := newTestClient(cp, testToken, nil).Query(context.Background(), tenantZero, q)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Rows) != 2 || res.Rows[0].Day.Day() != 13 || res.Rows[1].Day.Day() != 14 || !res.Truncated {
			t.Fatalf("result = %+v", res)
		}
	})
	t.Run("too many rows drops the oldest", func(t *testing.T) {
		cp := &fakeCP{resp: trendRows(0, day("2026-11-12"), day("2026-11-14"), day("2026-11-13"))}
		res, err := newTestClient(cp, testToken, nil).Query(context.Background(), tenantZero, q)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Rows) != 2 || res.Rows[0].Day.Day() != 13 || res.Rows[1].Day.Day() != 14 || !res.Truncated {
			t.Fatalf("result = %+v", res)
		}
	})
}

func TestWindowCapsBeforeTheControlPlane(t *testing.T) {
	day := 24 * time.Hour
	for name, tc := range map[string]struct {
		q    gtm.AnalyticsQuery
		want error
	}{
		"sql over 30 days":    {gtm.AnalyticsQuery{Template: gtm.AnalyticsEventCounts, From: to.Add(-30*day - time.Second), To: to, Limit: 5}, ErrWindowTooLong},
		"funnel over 90 days": {gtm.AnalyticsQuery{Template: gtm.AnalyticsFunnel, Events: []string{"a", "b"}, From: to.Add(-90*day - time.Second), To: to, Limit: 5}, gtm.ErrInvalidArgument},
		"backwards":           {gtm.AnalyticsQuery{Template: gtm.AnalyticsEventCounts, From: to, To: from, Limit: 5}, gtm.ErrInvalidArgument},
	} {
		t.Run(name, func(t *testing.T) {
			cp := &fakeCP{}
			if _, err := newTestClient(cp, testToken, nil).Query(context.Background(), tenantZero, tc.q); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if len(cp.reqs) != 0 {
				t.Fatalf("called the control plane")
			}
		})
	}
}

// TestSentinelsWrapTheDomain: each fixed error is the domain sentinel the
// Service passes through, so a caller can classify it.
func TestSentinelsWrapTheDomain(t *testing.T) {
	for _, err := range []error{ErrNoOrg, ErrInvalidName, ErrIdentifierProperty, ErrDuplicateStep, ErrWindowTooLong} {
		if !errors.Is(err, gtm.ErrInvalidArgument) {
			t.Errorf("%v is not gtm.ErrInvalidArgument", err)
		}
	}
	for _, err := range []error{ErrCredentialRefused, ErrNotSetUp, ErrQuotaExhausted, ErrTimedOut, ErrQueryRefused, ErrBadResponse} {
		if !errors.Is(err, gtm.ErrAnalyticsFailed) {
			t.Errorf("%v is not gtm.ErrAnalyticsFailed", err)
		}
	}
}

// tokenLeak says where testToken leaked, the log or the error; "" when
// neither carries it.
func tokenLeak(log string, err error) string {
	switch {
	case strings.Contains(log, "TESTTOKEN"):
		return "log: " + log
	case err != nil && strings.Contains(err.Error(), "TESTTOKEN"):
		return "error: " + err.Error()
	}
	return ""
}

// assertNoToken fails when testToken is in the log or the error.
func assertNoToken(t *testing.T, log string, err error) {
	t.Helper()
	if leak := tokenLeak(log, err); leak != "" {
		t.Errorf("token leaked to the %s", leak)
	}
}

func TestTokenIsNeverLoggedOrReturned(t *testing.T) {
	q := sqlQueries["event_counts"]
	t.Run("success", func(t *testing.T) {
		var log bytes.Buffer
		_, err := newTestClient(&fakeCP{resp: kRows(1, 0, ev("a"))}, testToken, &log).Query(context.Background(), tenantZero, q)
		if err != nil {
			t.Fatal(err)
		}
		assertNoToken(t, log.String(), err)
	})
	t.Run("control-plane error", func(t *testing.T) {
		var log bytes.Buffer
		cp := &fakeCP{err: connect.NewError(connect.CodeUnauthenticated, errors.New("bad credential"))}
		_, err := newTestClient(cp, testToken, &log).Query(context.Background(), tenantZero, q)
		if err == nil {
			t.Fatal("no error")
		}
		if !strings.Contains(log.String(), "bad credential") {
			t.Errorf("cause not logged: %s", log.String())
		}
		assertNoToken(t, log.String(), err)
	})
	t.Run("bad response", func(t *testing.T) {
		var log bytes.Buffer
		resp := &obsv1.QueryPanelResponse{Columns: []string{countColumn, groupsColumn, hiddenColumn}}
		_, err := newTestClient(&fakeCP{resp: resp}, testToken, &log).Query(context.Background(), tenantZero, q)
		if !errors.Is(err, ErrBadResponse) {
			t.Fatalf("err = %v", err)
		}
		assertNoToken(t, log.String(), err)
	})
	// The check can fail: a log or an error carrying the token is caught.
	t.Run("the check bites", func(t *testing.T) {
		if tokenLeak("Authorization=Bearer "+testToken, nil) == "" || tokenLeak("", errors.New("bad "+testToken)) == "" {
			t.Fatal("tokenLeak missed the token")
		}
	})
}

func TestControlPlaneErrorsAreFixedMessages(t *testing.T) {
	const secret = "clickhouse r_55949942 at ch-0.internal:9440: SELECT UserId FROM product_events"
	for code, want := range map[connect.Code]error{
		connect.CodeUnauthenticated:    ErrCredentialRefused,
		connect.CodePermissionDenied:   ErrCredentialRefused,
		connect.CodeFailedPrecondition: ErrNotSetUp,
		connect.CodeResourceExhausted:  ErrQuotaExhausted,
		connect.CodeDeadlineExceeded:   ErrTimedOut,
		connect.CodeInvalidArgument:    ErrQueryRefused,
		connect.CodeNotFound:           ErrQueryRefused,
		connect.CodeInternal:           gtm.ErrAnalyticsFailed,
		connect.CodeUnavailable:        gtm.ErrAnalyticsFailed,
		connect.CodeUnknown:            gtm.ErrAnalyticsFailed,
	} {
		t.Run(code.String(), func(t *testing.T) {
			var log bytes.Buffer
			cp := &fakeCP{err: connect.NewError(code, errors.New(secret))}
			_, err := newTestClient(cp, testToken, &log).Query(context.Background(), tenantZero, sqlQueries["event_counts"])
			if err != want {
				t.Fatalf("err = %v, want %v", err, want)
			}
			if !errors.Is(err, gtm.ErrAnalyticsFailed) {
				t.Errorf("%v is not ErrAnalyticsFailed", err)
			}
			if strings.Contains(err.Error(), "clickhouse") || strings.Contains(err.Error(), "UserId") {
				t.Errorf("error leaks the cause: %v", err)
			}
			if !strings.Contains(log.String(), "ch-0.internal") {
				t.Errorf("cause not logged: %s", log.String())
			}
			assertNoToken(t, log.String(), err)
		})
	}
	t.Run("a plain transport error", func(t *testing.T) {
		cp := &fakeCP{err: errors.New("dial tcp 10.0.0.7:443: connection refused")}
		_, err := newTestClient(cp, testToken, &bytes.Buffer{}).Query(context.Background(), tenantZero, sqlQueries["event_counts"])
		if err != gtm.ErrAnalyticsFailed {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a cancelled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		cp := &fakeCP{err: connect.NewError(connect.CodeCanceled, errors.New(secret))}
		_, err := newTestClient(cp, testToken, &bytes.Buffer{}).Query(ctx, tenantZero, sqlQueries["event_counts"])
		if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "clickhouse") {
			t.Fatalf("err = %v", err)
		}
	})
}

// TestResultsCarryNoIdentifiers: whatever the control plane puts in a row's
// group, only the template's own dimension reaches a result row.
func TestResultsCarryNoIdentifiers(t *testing.T) {
	resp := kRows(1, 0, map[string]string{eventColumn: "page_viewed", "UserId": "u-123", "SessionId": "s-9"})
	res, err := newTestClient(&fakeCP{resp: resp}, testToken, nil).Query(context.Background(), tenantZero, sqlQueries["event_counts"])
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Rows[0]; got != (gtm.AnalyticsRow{Event: "page_viewed", Count: 100}) {
		t.Fatalf("row = %+v", got)
	}
}

func TestObservabilityClientURL(t *testing.T) {
	for _, u := range []string{
		"https://cp.example.test", "https://cp.example.test/", " https://cp.example.test:8443/base/ ",
		"http://localhost:8080", "http://127.0.0.1:9000", "http://[::1]:9000", "http://127.0.0.2",
	} {
		if c, err := NewObservabilityClient(u); err != nil || c == nil {
			t.Errorf("%q: refused: %v", u, err)
		}
	}
	for _, u := range []string{
		"", "cp.example.test", "http://cp.example.test", "http://10.0.0.1", "http://localhost.example.test",
		"ftp://cp.example.test", "ws://localhost", "https://", "https://user:pw@cp.example.test", "//cp.example.test",
		"http://0.0.0.0:8080",
	} {
		if _, err := NewObservabilityClient(u); !errors.Is(err, ErrInsecureControlPlaneURL) {
			t.Errorf("%q: err = %v, want ErrInsecureControlPlaneURL", u, err)
		}
	}
}

func TestServiceContract(t *testing.T) {
	var _ gtm.Analytics = New(Deps{})
}
