package obsbackend

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
)

func TestLocalOverviewMergesRequestFiguresWithErrorLogsByService(t *testing.T) {
	fr := &fakeRunner{answers: []fakeAnswer{
		// The RED statement reads server spans: service, requests, errors, [p50, p95, p99].
		{"otel_traces", Result{Rows: [][]any{
			{"api", num64("1000"), num64("50"), []any{num64("12.5"), num64("80"), num64("250")}},
			{"worker", num64("10"), num64("0"), []any{num64("1"), num64("2"), "NaN"}},
			{"quiet", num64("0"), num64("0"), []any{}},
			{"", num64("5"), num64("0"), []any{num64("1"), num64("1"), num64("1")}},
		}}},
		// The log statement reads error-level records: service, ?, count.
		{"otel_logs", Result{Rows: [][]any{
			{"api", "ERROR", num64("7")},
			{"log-only", "ERROR", num64("30")},
		}}},
	}}
	// One hour, so rate per second is requests / 3600.
	res, err := localWith(fr).Overview(context.Background(), OverviewRequest{Narrow: rng()})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	by := map[string]ServiceOverview{}
	for _, s := range res.Services {
		names = append(names, s.Service)
		by[s.Service] = s
	}
	// Busiest first, then error logs, then name; the nameless service is dropped.
	if strings.Join(names, ",") != "api,worker,log-only,quiet" {
		t.Fatalf("order = %v", names)
	}
	api := by["api"]
	if api.Requests != 1000 || api.Errors != 50 || api.ErrorRate != 0.05 || api.P50Ms != 12.5 || api.P95Ms != 80 || api.P99Ms != 250 || api.ErrorLogs != 7 {
		t.Errorf("api = %+v", api)
	}
	if want := 1000.0 / 3600; math.Abs(api.RatePerSecond-want) > 1e-9 {
		t.Errorf("api rate = %v, want %v", api.RatePerSecond, want)
	}
	if w := by["worker"]; w.P99Ms != 0 {
		t.Errorf("a NaN percentile must read as zero, got %v", w.P99Ms)
	}
	// A service that only logged has no request figures, as on the control plane.
	if l := by["log-only"]; l.Requests != 0 || l.ErrorRate != 0 || l.RatePerSecond != 0 || l.ErrorLogs != 30 {
		t.Errorf("log-only = %+v", l)
	}
	if q := by["quiet"]; q.ErrorRate != 0 || q.P95Ms != 0 {
		t.Errorf("quiet = %+v", q)
	}
	if len(res.Statements) != 2 || !strings.Contains(res.Statements[0].Purpose, "rate, errors, duration") || !strings.Contains(res.Statements[1].Purpose, "error logs") {
		t.Errorf("statements = %+v", res.Statements)
	}
}

func TestLocalOverviewExplainAndFailures(t *testing.T) {
	fr := &fakeRunner{}
	res, err := localWith(fr).Overview(context.Background(), OverviewRequest{Narrow: explainNarrow()})
	if err != nil || len(res.Statements) != 2 || len(fr.ran) != 0 {
		t.Errorf("explain = %+v, %v; ran %d", res, err, len(fr.ran))
	}
	if _, err := localWith(&fakeRunner{err: errors.New("down")}).Overview(context.Background(), OverviewRequest{Narrow: rng()}); err == nil {
		t.Error("a failing runner must be an error")
	}

	short := &fakeRunner{answers: []fakeAnswer{{"otel_traces", Result{Rows: [][]any{{"api", num64("1")}}}}}}
	if _, err := localWith(short).Overview(context.Background(), OverviewRequest{Narrow: rng()}); exitOf(err) != ExitUndetermined || !strings.Contains(err.Error(), "returned 2 columns for the overview, want 4") {
		t.Errorf("a short RED row: %v", err)
	}
	shortLogs := &fakeRunner{answers: []fakeAnswer{{"otel_logs", Result{Rows: [][]any{{"api"}}}}}}
	if _, err := localWith(shortLogs).Overview(context.Background(), OverviewRequest{Narrow: rng()}); exitOf(err) != ExitUndetermined || !strings.Contains(err.Error(), "want 3") {
		t.Errorf("a short log row: %v", err)
	}

}
