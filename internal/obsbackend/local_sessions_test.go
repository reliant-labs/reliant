package obsbackend

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/obsquery"
)

// sessionRow is a rum_sessions row in sessionCols' order: 16 summary columns,
// then the 12 client columns.
func sessionRow(id, svc string, started, ended time.Time) []any {
	row := []any{
		id, svc, "v1.4.0", "env-1", "user-7", "/checkout",
		started.UTC().Format("2006-01-02 15:04:05.000000000"), ended.UTC().Format("2006-01-02 15:04:05.000000000"),
		num64("3"), num64("9"), num64("2"), num64("1"),
		"", "", "", "",
	}
	for i := 0; i < 12; i++ {
		row = append(row, "")
	}
	return row
}

func localWith(fr *fakeRunner) Local { return Local{Env: "dev", Runner: fr} }

func TestLocalListSessionsParsesAPageAndMapsTheSummary(t *testing.T) {
	t1 := time.Date(2026, 10, 22, 10, 30, 0, 0, time.UTC)
	fr := &fakeRunner{answers: []fakeAnswer{{"rum_sessions", Result{Rows: [][]any{
		sessionRow("s-new", "web", t1, t1.Add(5*time.Minute)),
		sessionRow("s-old", "web", t1.Add(-time.Hour), t1.Add(-50*time.Minute)),
	}}}}}
	res, err := localWith(fr).ListSessions(context.Background(), ListSessionsRequest{Narrow: rng(), Limit: 1, HasError: true, UserID: "user-7", PathContains: "check"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sessions) != 1 || res.NextPageToken == "" {
		t.Fatalf("a page of one with a second row waiting must carry a cursor: %+v", res)
	}
	s := res.Sessions[0]
	if s.ID != "s-new" || s.Service != "web" || s.Version != "v1.4.0" || s.UserID != "user-7" || s.EntryPath != "/checkout" ||
		s.PageViews != 3 || s.Interactions != 9 || s.Errors != 2 || !s.HasReplay || !s.Started.Equal(t1) || !s.Ended.Equal(t1.Add(5*time.Minute)) {
		t.Errorf("summary = %+v", s)
	}
	if len(res.Statements) != 1 || res.Statements[0].Purpose != "sessions" || !strings.Contains(res.Statements[0].SQL, "sum(Errors) > 0") {
		t.Errorf("statements = %+v", res.Statements)
	}

	// The cursor resumes the same query; one for another query is refused.
	next, err := localWith(fr).ListSessions(context.Background(), ListSessionsRequest{Narrow: rng(), Limit: 1, HasError: true, UserID: "user-7", PathContains: "check", PageToken: res.NextPageToken})
	if err != nil || len(next.Statements) != 1 || !strings.Contains(next.Statements[0].SQL, "<") {
		t.Errorf("resumed page: %+v, %v", next, err)
	}
	_, err = localWith(fr).ListSessions(context.Background(), ListSessionsRequest{Narrow: rng(), Limit: 1, PageToken: res.NextPageToken})
	if err == nil || exitOf(err) != ExitWrong || !strings.Contains(err.Error(), "does not belong to this query") {
		t.Errorf("a cursor of another query: %v", err)
	}
}

func TestLocalListSessionsExplainAndRefusals(t *testing.T) {
	fr := &fakeRunner{}
	res, err := localWith(fr).ListSessions(context.Background(), ListSessionsRequest{Narrow: explainNarrow()})
	if err != nil || len(res.Statements) != 1 || len(fr.ran) != 0 {
		t.Errorf("explain = %+v, %v; ran %d statements", res, err, len(fr.ran))
	}
	if _, err := localWith(fr).ListSessions(context.Background(), ListSessionsRequest{Narrow: rng(), PageToken: "!!not-a-cursor"}); exitOf(err) != ExitWrong {
		t.Errorf("a malformed cursor: %v", err)
	}
	if _, err := localWith(fr).ListSessions(context.Background(), ListSessionsRequest{Narrow: rng(), Limit: 100000}); exitOf(err) != ExitWrong {
		t.Errorf("an oversized page: %v", err)
	}
}

// A ClickStack without the control plane's derived tables answers "could not
// look" with the remedy, not "the request is wrong".
func TestLocalSessionReadsMapFailuresOntoTheExitTable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		exit int
		msg  string
	}{
		{"unknown table", Undetermined("Code: 60. Table default.rum_sessions does not exist. (UNKNOWN_TABLE)"), ExitUndetermined, "no browser session tables"},
		{"unknown table wording", Undetermined("Unknown table expression"), ExitUndetermined, "no browser session tables"},
		{"another backend error passes through", Undetermined("the container is down"), ExitUndetermined, "the container is down"},
		{"an invalid request", obsquery.ErrInvalid, ExitWrong, ""},
		{"anything else is a read failure", errors.New("boom"), ExitUndetermined, "returned sessions forge cannot read"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := localWith(&fakeRunner{err: tc.err}).ListSessions(context.Background(), ListSessionsRequest{Narrow: rng()})
			if exitOf(err) != tc.exit || !strings.Contains(err.Error(), tc.msg) {
				t.Errorf("err = %v (exit %d), want exit %d containing %q", err, exitOf(err), tc.exit, tc.msg)
			}
		})
	}
	if sessionErr(nil) != nil {
		t.Error("sessionErr(nil) must be nil")
	}
}

func timelineRow(ts time.Time, kind, name string) []any {
	return []any{ts.UTC().Format("2006-01-02 15:04:05.000000000"), kind, name, "/p", "GET", num64("500"), "div#x", "<button>", "TypeError", "boom", "fp1", "trace1", "span1", num64("12.5"), map[string]any{"k": "v"}, "web"}
}

func TestLocalGetSessionReadsTheSummaryThenItsTimeline(t *testing.T) {
	t1 := time.Date(2026, 10, 22, 10, 30, 0, 0, time.UTC)
	fr := &fakeRunner{answers: []fakeAnswer{
		{"rum_sessions", Result{Rows: [][]any{sessionRow("s-1", "web", t1, t1.Add(4*time.Minute))}}},
		{"otel_traces", Result{Rows: [][]any{timelineRow(t1, "click", "click"), timelineRow(t1.Add(time.Second), "error", "js error")}}},
	}}
	res, err := localWith(fr).GetSession(context.Background(), GetSessionRequest{ID: "s-1", Now: t1.Add(time.Hour), Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if res.Session.ID != "s-1" || res.Session.Errors != 2 {
		t.Errorf("session = %+v", res.Session)
	}
	if len(res.Timeline) != 1 || res.NextPageToken == "" {
		t.Fatalf("timeline page = %+v, token %q", res.Timeline, res.NextPageToken)
	}
	e := res.Timeline[0]
	if e.Kind != "click" || e.Path != "/p" || e.Method != "GET" || e.Status != 500 || e.Selector != "div#x" || e.ErrorType != "TypeError" ||
		e.Message != "boom" || e.Fingerprint != "fp1" || e.TraceID != "trace1" || e.SpanID != "span1" || e.DurationMs != 12.5 || e.Props["k"] != "v" || e.Service != "web" {
		t.Errorf("entry = %+v", e)
	}
	if len(res.Statements) != 2 || res.Statements[0].Purpose != "session summary" || res.Statements[1].Purpose != "session timeline" {
		t.Errorf("statements = %+v", res.Statements)
	}
	// With no range the lookup spans the session lookback ending at Now.
	if !strings.Contains(res.Statements[0].SQL, "SessionId") {
		t.Errorf("summary statement = %s", res.Statements[0].SQL)
	}

	// A session longer than a timeline can read is clipped to the window.
	long := sessionRow("s-2", "web", t1, t1.Add(10*24*time.Hour))
	fr2 := &fakeRunner{answers: []fakeAnswer{{"rum_sessions", Result{Rows: [][]any{long}}}}}
	if _, err := localWith(fr2).GetSession(context.Background(), GetSessionRequest{ID: "s-2", Narrow: Narrow{Range: Range{From: t1.Add(-time.Hour), To: t1.Add(11 * 24 * time.Hour)}}}); err != nil {
		t.Errorf("a long session must still be readable: %v", err)
	}
}

func TestLocalGetSessionNotFoundExplainAndFailures(t *testing.T) {
	_, err := localWith(&fakeRunner{}).GetSession(context.Background(), GetSessionRequest{ID: "ghost"})
	if exitOf(err) != ExitWrong || !strings.Contains(err.Error(), "session ghost not found") {
		t.Errorf("a session that is not there: %v", err)
	}

	fr := &fakeRunner{}
	res, err := localWith(fr).GetSession(context.Background(), GetSessionRequest{ID: "s-1", Narrow: explainNarrow()})
	if err != nil || len(res.Statements) != 2 || len(fr.ran) != 0 || !strings.Contains(res.Statements[1].Purpose, "over the longest window") {
		t.Errorf("explain = %+v, %v", res, err)
	}
	if _, err := localWith(fr).GetSession(context.Background(), GetSessionRequest{ID: "bad id with spaces!", Narrow: rng()}); exitOf(err) != ExitWrong {
		t.Errorf("an invalid session id: %v", err)
	}
	if _, err := localWith(fr).GetSession(context.Background(), GetSessionRequest{ID: "s-1", Narrow: rng(), PageToken: "!!"}); exitOf(err) != ExitWrong {
		t.Errorf("a malformed timeline cursor: %v", err)
	}
	if _, err := localWith(&fakeRunner{err: errors.New("down")}).GetSession(context.Background(), GetSessionRequest{ID: "s-1"}); exitOf(err) != ExitUndetermined {
		t.Errorf("a failing runner: %v", err)
	}

	// The timeline read failing after the summary succeeded still returns
	// the summary alongside the error.
	t1 := time.Date(2026, 10, 22, 10, 30, 0, 0, time.UTC)
	fail := &failingSecond{first: Result{Rows: [][]any{sessionRow("s-1", "web", t1, t1.Add(time.Minute))}}}
	res, err = Local{Env: "dev", Runner: fail}.GetSession(context.Background(), GetSessionRequest{ID: "s-1"})
	if err == nil || res.Session.ID != "s-1" {
		t.Errorf("partial result = %+v, %v", res, err)
	}
}

// failingSecond answers the first statement and fails every later one.
type failingSecond struct {
	first Result
	n     int
}

func (f *failingSecond) Run(context.Context, obsquery.Stmt, Limits) (Result, error) {
	f.n++
	if f.n == 1 {
		return f.first, nil
	}
	return Result{}, errors.New("the second read failed")
}

func explainNarrow() Narrow {
	n := rng()
	n.Explain = true
	return n
}
