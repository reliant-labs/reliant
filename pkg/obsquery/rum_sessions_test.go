package obsquery

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

const rumHostile = "x') OR 1=1 --"

func rumScope() Scope {
	return Scope{Database: "t_org_a", Start: t0, End: t0.Add(24 * time.Hour),
		EnvRestricted: true, EnvIDs: []string{"env-prod"}, Envs: []Env{{ID: "env-prod", Name: "prod"}}}
}

func TestSessionQueryCompile(t *testing.T) {
	sc := rumScope()
	sc.Services = []string{rumHostile}
	st, err := SessionQuery{Scope: sc, HasError: true, HasReplay: true, UserID: rumHostile, PathContains: rumHostile, Limit: 10}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	assertParameterized(t, st, rumHostile)
	for _, want := range []string{
		"FROM t_org_a.rum_sessions", "GROUP BY ServiceName, SessionId", "sum(Errors) > 0", "max(HasReplay) = 1",
		"max(UserId) = {", "positionCaseInsensitive(argMinMerge(EntryPath)", "max(EnvId) = {", "ServiceName IN (",
		"ORDER BY started DESC, ServiceName DESC, SessionId DESC", "LIMIT 11",
	} {
		if !strings.Contains(st.SQL, want) {
			t.Errorf("SQL lacks %q:\n%s", want, st.SQL)
		}
	}
	// the day before the range is read: a session that began there is not clipped
	if !strings.Contains(st.SQL, "subtractDays(") {
		t.Errorf("no day lower bound:\n%s", st.SQL)
	}
}

func TestSessionQueryDefaultsAndValidation(t *testing.T) {
	st, err := SessionQuery{Scope: rumScope()}.Compile()
	if err != nil || !strings.Contains(st.SQL, "LIMIT 26") {
		t.Fatalf("default limit: %v\n%s", err, st.SQL)
	}
	for name, q := range map[string]SessionQuery{
		"limit too big":  {Scope: rumScope(), Limit: MaxSessionLimit + 1},
		"negative limit": {Scope: rumScope(), Limit: -1},
		"long user":      {Scope: rumScope(), UserID: strings.Repeat("u", maxRumValueLen+1)},
		"long path":      {Scope: rumScope(), PathContains: strings.Repeat("p", maxRumValueLen+1)},
		"range too long": {Scope: Scope{Database: "t_org_a", Start: t0, End: t0.Add(MaxRangeSessions + time.Hour)}},
		"no range":       {Scope: Scope{Database: "t_org_a"}},
		"bad database":   {Scope: Scope{Database: "t;drop", Start: t0, End: t0.Add(time.Hour)}},
		"local default":  {Scope: Scope{Database: LocalDatabase, Start: t0, End: t0.Add(time.Hour)}},
		"unknown filter": {Scope: Scope{Database: "t_org_a", Start: t0, End: t0.Add(time.Hour), Filters: []AttrFilter{{Key: "nope", Value: "x"}}}},
		"foreign cursor": {Scope: rumScope(), Cursor: &SessionCursor{TsMs: 1, Service: "web", SessionID: "s", Finger: "other"}},
	} {
		if _, err := q.Compile(); !isInvalid(err) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}

func TestSessionCursorIsBoundToTheFilters(t *testing.T) {
	q := SessionQuery{Scope: rumScope(), HasError: true, Limit: 2}
	tok := q.NextCursor(time.UnixMilli(1792410246461), "web", "sess-1")
	c, err := DecodeSessionCursor(tok)
	if err != nil || c == nil || c.SessionID != "sess-1" || c.Service != "web" || c.TsMs != 1792410246461 {
		t.Fatalf("cursor = %+v, %v", c, err)
	}
	q.Cursor = c
	st, err := q.Compile()
	if err != nil {
		t.Fatal(err)
	}
	assertParameterized(t, st)
	if !strings.Contains(st.SQL, "(started, ServiceName, SessionId) < (fromUnixTimestamp64Milli(") {
		t.Errorf("no keyset condition:\n%s", st.SQL)
	}
	// the time range is not part of the binding; every filter is
	moved := q
	moved.Scope.Start, moved.Scope.End = moved.Scope.Start.Add(time.Hour), moved.Scope.End.Add(time.Hour)
	if _, err := moved.Compile(); err != nil {
		t.Errorf("a moved range must keep the cursor: %v", err)
	}
	for name, mut := range map[string]func(*SessionQuery){
		"has_error":   func(q *SessionQuery) { q.HasError = false },
		"has_replay":  func(q *SessionQuery) { q.HasReplay = true },
		"user":        func(q *SessionQuery) { q.UserID = "u" },
		"path":        func(q *SessionQuery) { q.PathContains = "/pay" },
		"service":     func(q *SessionQuery) { q.Scope.Services = []string{"api"} },
		"environment": func(q *SessionQuery) { q.Scope.EnvIDs = []string{"env-stg"} },
		"database":    func(q *SessionQuery) { q.Scope.Database = "t_org_b" },
	} {
		other := q
		mut(&other)
		if _, err := other.Compile(); !isInvalid(err) {
			t.Errorf("%s changed but the cursor still applies: %v", name, err)
		}
	}
	for _, bad := range []string{"!!", "e30", ""} {
		if c, err := DecodeSessionCursor(bad); bad == "" && (c != nil || err != nil) || bad != "" && !isInvalid(err) {
			t.Errorf("DecodeSessionCursor(%q) = %v, %v", bad, c, err)
		}
	}
}

func TestCursorsOfDifferentReadsDoNotCross(t *testing.T) {
	tl := SessionTimeline{Scope: rumScope(), SessionID: "s1"}
	rp := ReplayQuery{Scope: rumScope(), SessionID: "s1"}
	sq := SessionQuery{Scope: rumScope()}
	tlTok := tl.NextCursor(time.Unix(1, 0), "click", "span")
	rpTok := rp.NextToken(time.Unix(1, 0), 7)
	sqTok := sq.NextCursor(time.Unix(1, 0), "web", "s1")

	c, err := DecodeSessionCursor(rpTok)
	if err == nil {
		sq.Cursor = c
		if _, err := sq.Compile(); !isInvalid(err) {
			t.Error("a replay token continued a session list")
		}
	}
	if c, err := DecodeTimelineCursor(sqTok); err == nil {
		tl.Cursor = c
		if _, err := tl.Compile(); !isInvalid(err) {
			t.Error("a session-list token continued a timeline")
		}
	}
	if c, err := DecodeReplayCursor(tlTok); err == nil {
		rp.Cursor = c
		if _, err := rp.Compile(); !isInvalid(err) {
			t.Error("a timeline token continued a replay")
		}
	}
}

func TestSessionLookupCompile(t *testing.T) {
	st, err := SessionLookup{Scope: rumScope(), SessionID: "91b955b313a1a86644ea53730a92deab"}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	assertParameterized(t, st)
	for _, want := range []string{"FROM t_org_a.rum_sessions", "SessionId = {", "max(EnvId) = {", "LIMIT 1"} {
		if !strings.Contains(st.SQL, want) {
			t.Errorf("SQL lacks %q:\n%s", want, st.SQL)
		}
	}
	// a lookup is not narrowed by when the session started
	if strings.Contains(st.SQL, "started >=") {
		t.Errorf("lookup filters on the start time:\n%s", st.SQL)
	}
	for _, id := range []string{"", rumHostile, strings.Repeat("a", 129), "a b"} {
		if _, err := (SessionLookup{Scope: rumScope(), SessionID: id}).Compile(); !isInvalid(err) {
			t.Errorf("session id %q: err = %v, want ErrInvalid", id, err)
		}
	}
}

func TestSessionTimelineCompile(t *testing.T) {
	sc := rumScope()
	sc.Services = []string{rumHostile}
	st, err := SessionTimeline{Scope: sc, SessionID: "91b955b313a1a86644ea53730a92deab", Limit: 50}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	assertParameterized(t, st, rumHostile)
	for _, want := range []string{
		"FROM t_org_a.otel_traces", "FROM t_org_a.error_events", "FROM t_org_a.error_stacks",
		"ResourceAttributes['rum.sessionId'] = {", "'navigation'", "'click'", "'request_failed'", "'error'", "'event'",
		"'documentLoad', 'routeChange'", "'mousedown'", "'custom-action'", "substring(path(",
		"ORDER BY ts, kind, span_id", "LIMIT 51",
	} {
		if !strings.Contains(st.SQL, want) {
			t.Errorf("SQL lacks %q:\n%s", want, st.SQL)
		}
	}
	// queries and fragments carry tokens: no branch may select a raw URL
	for _, raw := range []string{"SpanAttributes['location.href'] AS url", "SpanAttributes['http.url'] AS url"} {
		if strings.Contains(st.SQL, raw) {
			t.Errorf("raw URL selected (%s):\n%s", raw, st.SQL)
		}
	}
	for name, q := range map[string]SessionTimeline{
		"no session": {Scope: rumScope()},
		"big limit":  {Scope: rumScope(), SessionID: "s", Limit: MaxTimelineLimit + 1},
		"neg limit":  {Scope: rumScope(), SessionID: "s", Limit: -1},
	} {
		if _, err := q.Compile(); !isInvalid(err) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	// scope attribute filters narrow lists, not a read of one named session
	f := rumScope()
	f.Filters = []AttrFilter{{Key: "span.attr.userId", Value: "u"}}
	if _, err := (SessionTimeline{Scope: f, SessionID: "s"}).Compile(); err != nil {
		t.Errorf("filters must not break a timeline: %v", err)
	}
}

func TestSessionTimelineCursor(t *testing.T) {
	q := SessionTimeline{Scope: rumScope(), SessionID: "s1", Limit: 10}
	c, err := DecodeTimelineCursor(q.NextCursor(time.Unix(0, 1792410247791000000), "click", "cb6192c1989419b0"))
	if err != nil || c == nil {
		t.Fatal(c, err)
	}
	q.Cursor = c
	st, err := q.Compile()
	if err != nil {
		t.Fatal(err)
	}
	assertParameterized(t, st)
	if !strings.Contains(st.SQL, "(ts, kind, span_id) > (fromUnixTimestamp64Nano(") {
		t.Errorf("no keyset condition:\n%s", st.SQL)
	}
	q.SessionID = "s2"
	if _, err := q.Compile(); !isInvalid(err) {
		t.Errorf("a cursor of another session applied: %v", err)
	}
}

func sessionRow(id, svc string, startedMs int64, errs uint64, replay uint8) []any {
	return []any{id, svc, "3f9c0e", "env-prod", "user-42", "/pay",
		time.UnixMilli(startedMs).UTC(), time.UnixMilli(startedMs + 5000).UTC(), uint64(2), uint64(3), errs, replay}
}

func TestParseSessions(t *testing.T) {
	q := SessionQuery{Scope: rumScope(), Limit: 2}
	rows := &catalogTestRows{rows: [][]any{
		sessionRow("s3", "web", 3000, 0, 1),
		sessionRow("s2", "web", 2000, 2, 0),
		sessionRow("s1", "web", 1000, 0, 0),
	}}
	page, err := q.Parse(rows)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Sessions) != 2 || page.Sessions[0].SessionID != "s3" || page.Sessions[1].SessionID != "s2" {
		t.Fatalf("sessions = %+v", page.Sessions)
	}
	s := page.Sessions[0]
	if !s.HasReplay || s.EnvName != "prod" || s.UserID != "user-42" || s.EntryPath != "/pay" || s.Duration() != 5*time.Second ||
		s.PageViews != 2 || s.Interactions != 3 || s.Service != "web" || s.Version != "3f9c0e" {
		t.Errorf("summary = %+v", s)
	}
	if page.Sessions[1].Errors != 2 {
		t.Errorf("errors = %d", page.Sessions[1].Errors)
	}
	// the cursor continues after the last RETURNED session, not the probe row
	c, err := DecodeSessionCursor(page.NextCursor)
	if err != nil || c == nil || c.SessionID != "s2" || c.TsMs != 2000 {
		t.Fatalf("next cursor = %+v, %v", c, err)
	}

	rows = &catalogTestRows{rows: [][]any{sessionRow("s1", "web", 1000, 0, 0)}}
	page, err = q.Parse(rows)
	if err != nil || len(page.Sessions) != 1 || page.NextCursor != "" {
		t.Errorf("last page = %+v, %v", page, err)
	}
	if page, err := q.Parse(&catalogTestRows{}); err != nil || page.Sessions == nil || len(page.Sessions) != 0 {
		t.Errorf("empty page = %+v, %v", page, err)
	}
	if _, err := q.Parse(&catalogTestRows{rows: [][]any{{"only one"}}}); err == nil {
		t.Error("a short row parsed")
	}
	if _, err := q.Parse(&catalogTestRows{err: errors.New("boom")}); err == nil {
		t.Error("a rows error was swallowed")
	}
}

func TestParseSessionNotFound(t *testing.T) {
	if _, err := ParseSession(&catalogTestRows{}, rumScope().Envs); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("err = %v, want ErrSessionNotFound", err)
	}
	s, err := ParseSession(&catalogTestRows{rows: [][]any{sessionRow("s1", "web", 1000, 1, 1)}}, rumScope().Envs)
	if err != nil || s.SessionID != "s1" || s.EnvName != "prod" {
		t.Errorf("summary = %+v, %v", s, err)
	}
}

func tlRow(ts int64, kind, name, url, spanID string) []any {
	return []any{time.Unix(0, ts).UTC(), kind, name, url, "", uint16(0), "", "", "", "", "", "tr-" + spanID, spanID, float64(0), map[string]string{}, "web"}
}

func TestParseTimeline(t *testing.T) {
	q := SessionTimeline{Scope: rumScope(), SessionID: "s1", Limit: 2}
	rows := &catalogTestRows{rows: [][]any{
		tlRow(1_000, "navigation", "routeChange", "/pay", "a"),
		tlRow(2_000, "click", "BUTTON", "/pay", "b"),
		tlRow(3_000, "error", "onerror", "/pay", "c"),
	}}
	page, err := q.Parse(rows)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 2 || page.Entries[1].Kind != TimelineClick || page.Entries[0].Kind != TimelineNavigation {
		t.Fatalf("entries = %+v", page.Entries)
	}
	if e := page.Entries[0]; e.TraceID != "tr-a" || e.SpanID != "a" || e.Service != "web" || e.Time.UnixNano() != 1_000 {
		t.Errorf("entry = %+v", e)
	}
	c, err := DecodeTimelineCursor(page.NextCursor)
	if err != nil || c == nil || c.Kind != "click" || c.SpanID != "b" || c.TsNano != 2_000 {
		t.Errorf("cursor = %+v, %v", c, err)
	}
	if page, err := q.Parse(&catalogTestRows{rows: [][]any{tlRow(1, "event", "checkout_started", "/pay", "z")}}); err != nil || page.NextCursor != "" || len(page.Entries) != 1 {
		t.Errorf("last page = %+v, %v", page, err)
	}
}

func TestListSessionsRunsCompileThenParse(t *testing.T) {
	var got Stmt
	run := func(_ context.Context, st Stmt) (Rows, error) {
		got = st
		return &catalogTestRows{rows: [][]any{sessionRow("s1", "web", 1000, 0, 1)}}, nil
	}
	page, err := ListSessions(context.Background(), run, SessionQuery{Scope: rumScope(), HasError: true})
	if err != nil || len(page.Sessions) != 1 || !strings.Contains(got.SQL, "rum_sessions") {
		t.Fatalf("page = %+v, %v\n%s", page, err, got.SQL)
	}
	if _, err := ListSessions(context.Background(), run, SessionQuery{Scope: Scope{}}); !isInvalid(err) {
		t.Errorf("an invalid query ran: %v", err)
	}
	failing := func(context.Context, Stmt) (Rows, error) { return nil, errors.New("down") }
	if _, err := ListSessions(context.Background(), failing, SessionQuery{Scope: rumScope()}); err == nil {
		t.Error("a runner error was swallowed")
	}
	_, err = GetSession(context.Background(), func(context.Context, Stmt) (Rows, error) { return &catalogTestRows{}, nil },
		SessionLookup{Scope: rumScope(), SessionID: "nope"})
	if !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("GetSession err = %v", err)
	}
}
