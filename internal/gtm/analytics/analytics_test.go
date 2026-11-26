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

func newTestClient(cp ObservabilityClient, tok string, log *bytes.Buffer) *client {
	d := Deps{CP: cp, Token: token(tok)}
	if log != nil {
		d.Log = slog.New(slog.NewTextHandler(log, nil))
	}
	return newClient(d)
}

// sqlRows is a sql panel's answer drawn as a table: the numeric columns,
// and each row's other columns in its group.
func sqlRows(groups int64, rows ...map[string]string) *obsv1.QueryPanelResponse {
	resp := &obsv1.QueryPanelResponse{Kind: "sql", Columns: []string{countColumn, groupsColumn}}
	for i, g := range rows {
		resp.Rows = append(resp.Rows, &obsv1.PanelTableRow{Group: g, Values: []float64{float64(100 - i), float64(groups)}})
	}
	return resp
}

func TestEmptyTokenFailsClosed(t *testing.T) {
	q := sqlQueries["event_counts"]
	for name, c := range map[string]*client{
		"empty token": newTestClient(&fakeCP{}, "", nil),
		"blank token": newTestClient(&fakeCP{}, "  \n", nil),
		"nil token":   newClient(Deps{CP: &fakeCP{}}),
		"nil cp":      newClient(Deps{Token: token("tok")}),
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
			cp := &fakeCP{resp: sqlRows(0)}
			if _, err := newTestClient(cp, "obs-read-token", nil).Query(context.Background(), tenantZero, q); err != nil {
				t.Fatal(err)
			}
			if len(cp.reqs) != 1 {
				t.Fatalf("%d requests", len(cp.reqs))
			}
			req := cp.reqs[0]
			if got := req.Header().Get("Authorization"); got != "Bearer obs-read-token" {
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
	cp := &fakeCP{resp: sqlRows(0)}
	c := newTestClient(cp, "tok", nil)
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
	res, err := newTestClient(cp, "tok", nil).Query(context.Background(), tenantZero, q)
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
	if res.Template != gtm.AnalyticsFunnel || !res.From.Equal(q.From) || !res.To.Equal(q.To) || res.Truncated {
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
		30 * 24 * time.Hour:  "720h",
		12 * 24 * time.Hour:  "288h",
		2 * 24 * time.Hour:   "48h",
		100 * 24 * time.Hour: "2400h",
	} {
		if got := funnelWindow(d); got != want {
			t.Errorf("funnelWindow(%v) = %s, want %s", d, got, want)
		}
	}
}

func TestFunnelStepCountMismatchIsBadResponse(t *testing.T) {
	q := gtm.AnalyticsQuery{Template: gtm.AnalyticsFunnel, Events: []string{"a", "b"}, From: from, To: to, Limit: 10}
	cp := &fakeCP{resp: &obsv1.QueryPanelResponse{FunnelSteps: []*obsv1.FunnelStepResult{{Event: "a", Count: 1}}}}
	if _, err := newTestClient(cp, "tok", nil).Query(context.Background(), tenantZero, q); !errors.Is(err, ErrBadResponse) || !errors.Is(err, gtm.ErrAnalyticsFailed) {
		t.Fatalf("err = %v", err)
	}
}

func TestSQLResults(t *testing.T) {
	t.Run("event_counts", func(t *testing.T) {
		cp := &fakeCP{resp: sqlRows(2, map[string]string{eventColumn: "page_viewed"}, map[string]string{eventColumn: "chat.started"})}
		res, err := newTestClient(cp, "tok", nil).Query(context.Background(), tenantZero, sqlQueries["event_counts"])
		if err != nil {
			t.Fatal(err)
		}
		want := []gtm.AnalyticsRow{{Event: "page_viewed", Count: 100}, {Event: "chat.started", Count: 99}}
		if len(res.Rows) != 2 || res.Rows[0] != want[0] || res.Rows[1] != want[1] || res.Truncated {
			t.Fatalf("result = %+v", res)
		}
	})
	t.Run("event_trend", func(t *testing.T) {
		cp := &fakeCP{resp: sqlRows(1, map[string]string{dayColumn: "2026-11-03", eventColumn: "page_viewed"})}
		res, err := newTestClient(cp, "tok", nil).Query(context.Background(), tenantZero, sqlQueries["event_trend"])
		if err != nil {
			t.Fatal(err)
		}
		want := gtm.AnalyticsRow{Event: "page_viewed", Day: time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC), Count: 100}
		if len(res.Rows) != 1 || res.Rows[0] != want {
			t.Fatalf("rows = %+v", res.Rows)
		}
	})
	t.Run("event_trend bad day", func(t *testing.T) {
		cp := &fakeCP{resp: sqlRows(1, map[string]string{dayColumn: "yesterday", eventColumn: "page_viewed"})}
		if _, err := newTestClient(cp, "tok", nil).Query(context.Background(), tenantZero, sqlQueries["event_trend"]); !errors.Is(err, ErrBadResponse) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("property_breakdown", func(t *testing.T) {
		cp := &fakeCP{resp: sqlRows(2, map[string]string{valueColumn: "/pricing"}, map[string]string{valueColumn: ""})}
		res, err := newTestClient(cp, "tok", nil).Query(context.Background(), tenantZero, sqlQueries["property_breakdown"])
		if err != nil {
			t.Fatal(err)
		}
		want := []gtm.AnalyticsRow{{Event: "page_viewed", Value: "/pricing", Count: 100}, {Event: "page_viewed", Value: "", Count: 99}}
		if len(res.Rows) != 2 || res.Rows[0] != want[0] || res.Rows[1] != want[1] {
			t.Fatalf("rows = %+v", res.Rows)
		}
	})
	t.Run("missing value is zero", func(t *testing.T) {
		resp := sqlRows(1, map[string]string{eventColumn: "x"})
		resp.Rows[0].MissingValueIndexes = []uint32{0}
		res, err := newTestClient(&fakeCP{resp: resp}, "tok", nil).Query(context.Background(), tenantZero, sqlQueries["event_counts"])
		if err != nil || res.Rows[0].Count != 0 {
			t.Fatalf("res = %+v, err = %v", res, err)
		}
	})
	t.Run("no count column", func(t *testing.T) {
		resp := sqlRows(1, map[string]string{eventColumn: "x"})
		resp.Columns = []string{"other", groupsColumn}
		if _, err := newTestClient(&fakeCP{resp: resp}, "tok", nil).Query(context.Background(), tenantZero, sqlQueries["event_counts"]); !errors.Is(err, ErrBadResponse) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestRowCapAndTruncation(t *testing.T) {
	q := sqlQueries["event_counts"]
	q.Limit = 2
	t.Run("more groups than the limit", func(t *testing.T) {
		cp := &fakeCP{resp: sqlRows(9, map[string]string{eventColumn: "a"}, map[string]string{eventColumn: "b"})}
		res, err := newTestClient(cp, "tok", nil).Query(context.Background(), tenantZero, q)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Rows) != 2 || !res.Truncated {
			t.Fatalf("result = %+v", res)
		}
		if !strings.HasSuffix(cp.reqs[0].Msg.GetPanel().GetSql(), " LIMIT 2") {
			t.Errorf("sql = %s", cp.reqs[0].Msg.GetPanel().GetSql())
		}
	})
	t.Run("a control plane that returns too many rows", func(t *testing.T) {
		cp := &fakeCP{resp: sqlRows(0, map[string]string{eventColumn: "a"}, map[string]string{eventColumn: "b"}, map[string]string{eventColumn: "c"})}
		res, err := newTestClient(cp, "tok", nil).Query(context.Background(), tenantZero, q)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Rows) != 2 || !res.Truncated {
			t.Fatalf("result = %+v", res)
		}
	})
	t.Run("exactly the limit", func(t *testing.T) {
		cp := &fakeCP{resp: sqlRows(2, map[string]string{eventColumn: "a"}, map[string]string{eventColumn: "b"})}
		res, err := newTestClient(cp, "tok", nil).Query(context.Background(), tenantZero, q)
		if err != nil || len(res.Rows) != 2 || res.Truncated {
			t.Fatalf("res = %+v, err = %v", res, err)
		}
	})
	t.Run("limit over the cap is refused before the control plane", func(t *testing.T) {
		cp := &fakeCP{}
		q := q
		q.Limit = gtm.MaxAnalyticsRows + 1
		if _, err := newTestClient(cp, "tok", nil).Query(context.Background(), tenantZero, q); !errors.Is(err, gtm.ErrInvalidArgument) || len(cp.reqs) != 0 {
			t.Fatalf("err = %v, %d requests", err, len(cp.reqs))
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
			if _, err := newTestClient(cp, "tok", nil).Query(context.Background(), tenantZero, tc.q); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if len(cp.reqs) != 0 {
				t.Fatalf("called the control plane")
			}
		})
	}
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
			_, err := newTestClient(cp, "tok", &log).Query(context.Background(), tenantZero, sqlQueries["event_counts"])
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
			if strings.Contains(log.String(), "tok") && strings.Contains(log.String(), "Bearer") {
				t.Errorf("token logged: %s", log.String())
			}
		})
	}
	t.Run("a plain transport error", func(t *testing.T) {
		cp := &fakeCP{err: errors.New("dial tcp 10.0.0.7:443: connection refused")}
		_, err := newTestClient(cp, "tok", &bytes.Buffer{}).Query(context.Background(), tenantZero, sqlQueries["event_counts"])
		if err != gtm.ErrAnalyticsFailed {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a cancelled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		cp := &fakeCP{err: connect.NewError(connect.CodeCanceled, errors.New(secret))}
		_, err := newTestClient(cp, "tok", &bytes.Buffer{}).Query(ctx, tenantZero, sqlQueries["event_counts"])
		if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "clickhouse") {
			t.Fatalf("err = %v", err)
		}
	})
}

// TestResultsCarryNoIdentifiers: whatever the control plane puts in a row's
// group, only the template's own dimension reaches a result row.
func TestResultsCarryNoIdentifiers(t *testing.T) {
	resp := sqlRows(1, map[string]string{eventColumn: "page_viewed", "UserId": "u-123", "SessionId": "s-9"})
	res, err := newTestClient(&fakeCP{resp: resp}, "tok", nil).Query(context.Background(), tenantZero, sqlQueries["event_counts"])
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Rows[0]; got != (gtm.AnalyticsRow{Event: "page_viewed", Count: 100}) {
		t.Fatalf("row = %+v", got)
	}
}

func TestServiceContract(t *testing.T) {
	var _ gtm.Analytics = New(Deps{})
	if NewObservabilityClient("https://cp.example.test/") == nil {
		t.Fatal("nil client")
	}
}
