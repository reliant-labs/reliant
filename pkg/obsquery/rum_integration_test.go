//go:build integration

package obsquery_test

// The session list, summary, timeline and replay pages against a real
// ClickHouse holding the hosted observability database's browser tables (testdata/rum/
// hosted_schema.sql, generated from control-plane's 010_tenant_create.sql) and
// payloads captured from a real Chrome running @hyperdx/browser 0.26 (traces.jsonl,
// logs.jsonl: control-plane's obstenant fixtures). The multi-chunk full snapshot
// is generated from the recorder's measured chunk size.
//
//	go test -tags integration -run 'Rum' ./pkg/obsquery/

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"

	"github.com/reliant-labs/forge/pkg/obsquery"
)

const (
	rumReplaySession = "91b955b313a1a86644ea53730a92deab" // replay on; 4 error spans, 2 custom events
	rumPlainSession  = "072af6846fd16a702ead77ec49f2408e" // replay off; 1 error span
	// chunkBytes is the recorder's MAX_CHUNK_SIZE (950 KiB), measured in the capture.
	chunkBytes = 972800
)

func rumRunner(c clickhouse.Conn) obsquery.Runner {
	return func(ctx context.Context, st obsquery.Stmt) (obsquery.Rows, error) {
		return c.Query(clickhouse.Context(ctx, clickhouse.WithParameters(clickhouse.Parameters(st.Params))), st.SQL)
	}
}

// rumHostedDB creates a hosted database holding the hosted browser tables.
func rumHostedDB(t *testing.T, c clickhouse.Conn, db string) {
	t.Helper()
	raw, err := os.ReadFile("testdata/rum/hosted_schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range strings.Split(strings.ReplaceAll(string(raw), "{{DB}}", db), ";\n\n") {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if err := c.Exec(context.Background(), stmt); err != nil {
			t.Fatalf("%v\n%s", err, stmt)
		}
	}
}

func rumInsert(t *testing.T, c clickhouse.Conn, db, table, jsonLines string) {
	t.Helper()
	if err := c.Exec(context.Background(), "INSERT INTO "+db+"."+table+" FORMAT JSONEachRow\n"+jsonLines); err != nil {
		t.Fatalf("insert %s.%s: %v", db, table, err)
	}
}

func rumFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/rum/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// rumLines are the fixture lines of one session.
func rumLines(t *testing.T, file, session string) []string {
	t.Helper()
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(rumFixture(t, file)), "\n") {
		if strings.Contains(l, `"rum.sessionId":"`+session+`"`) {
			out = append(out, l)
		}
	}
	if len(out) == 0 {
		t.Fatalf("no %s lines of session %s", file, session)
	}
	return out
}

func rumScopeOf(db string, envs ...string) obsquery.Scope {
	sc := obsquery.Scope{Database: db, Start: time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)}
	for _, e := range envs {
		sc.EnvRestricted = true
		sc.EnvIDs = append(sc.EnvIDs, e)
		sc.Envs = append(sc.Envs, obsquery.Env{ID: e, Name: strings.TrimPrefix(e, "env-")})
	}
	return sc
}

// loadCapture ingests the captured spans and replay of the two fixture sessions.
func loadCapture(t *testing.T, c clickhouse.Conn, db string) {
	t.Helper()
	rumInsert(t, c, db, "otel_traces", rumFixture(t, "traces.jsonl"))
	rumInsert(t, c, db, "otel_logs", rumFixture(t, "logs.jsonl"))
}

func TestRumSessionSummaryAndTimelineFromCapturedPayloads(t *testing.T) {
	c := startClickHouse(t)
	ctx := context.Background()
	rumHostedDB(t, c, "rt")
	loadCapture(t, c, "rt")
	run := rumRunner(c)
	sc := rumScopeOf("rt", "env-prod")

	s, err := obsquery.GetSession(ctx, run, obsquery.SessionLookup{Scope: sc, SessionID: rumReplaySession})
	if err != nil {
		t.Fatal(err)
	}
	if s.Service != "reliant-web" || s.Version != "3f9c0e" || s.EnvName != "prod" || s.UserID != "user-42" || s.EntryPath != "/" ||
		s.PageViews != 2 || s.Interactions != 1 || s.Errors != 4 || !s.HasReplay || s.Duration() < time.Second {
		t.Errorf("summary = %+v", s)
	}
	plain, err := obsquery.GetSession(ctx, run, obsquery.SessionLookup{Scope: sc, SessionID: rumPlainSession})
	if err != nil || plain.HasReplay || plain.Errors != 1 {
		t.Errorf("plain session = %+v, %v", plain, err)
	}

	// a failed fetch (500), an XHR 404 whose span status is unset, a network failure
	// (error status, no response), a successful fetch (not an entry) and a request
	// whose URL carries a token
	span := func(ts, name, component, spanID string, attrs map[string]string, status string) string {
		a := map[string]string{"component": component, "location.href": "http://127.0.0.1:43185/pay?token=abc#frag", "userId": "user-42"}
		for k, v := range attrs {
			a[k] = v
		}
		row := map[string]any{
			"Timestamp": ts, "TraceId": "f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f" + spanID[:1], "SpanId": spanID, "SpanName": name, "SpanKind": "Client",
			"ServiceName": "reliant-web", "ScopeName": "@opentelemetry/instrumentation-fetch", "StatusCode": status, "Duration": 42_000_000,
			"ResourceAttributes": map[string]string{"rum.sessionId": rumReplaySession, "reliant.env.id": "env-prod", "service.name": "reliant-web", "service.version": "3f9c0e"},
			"SpanAttributes":     a,
		}
		b, _ := json.Marshal(row)
		return string(b)
	}
	rumInsert(t, c, "rt", "otel_traces", strings.Join([]string{
		span("2026-10-19 11:44:07.900000000", "HTTP POST", "fetch", "aaaaaaaaaaaaaaa1", map[string]string{
			"http.method": "POST", "http.url": "https://api.example.com/api.v1.FooService/Bar?token=secret#x", "http.status_code": "500", "http.status_text": "Internal Server Error"}, "Error"),
		span("2026-10-19 11:44:07.950000000", "HTTP GET", "xml-http-request", "aaaaaaaaaaaaaaa2", map[string]string{
			"http.method": "GET", "http.url": "https://api.example.com/missing", "http.status_code": "404"}, "Unset"),
		span("2026-10-19 11:44:08.000000000", "HTTP GET", "fetch", "aaaaaaaaaaaaaaa3", map[string]string{
			"http.method": "GET", "http.url": "https://api.example.com/offline", "error.type": "TypeError"}, "Error"),
		span("2026-10-19 11:44:08.050000000", "HTTP GET", "fetch", "aaaaaaaaaaaaaaa4", map[string]string{
			"http.method": "GET", "http.url": "https://api.example.com/ok", "http.status_code": "200"}, "Unset"),
	}, "\n"))

	// the enrichment worker's row for the first error's stack; a newer row wins
	rows, err := c.Query(ctx, `SELECT ServiceName, ServiceVersion, RawStackHash FROM rt.error_events WHERE SessionId = ? AND SpanName = 'onerror'`, rumReplaySession)
	if err != nil {
		t.Fatal(err)
	}
	var svc, ver string
	var hash uint64
	if !rows.Next() || rows.Scan(&svc, &ver, &hash) != nil {
		t.Fatal("no onerror row in error_events")
	}
	rows.Close()
	for _, r := range []struct{ fp, at string }{{"0000000000000000", "2026-10-19 12:00:00.000"}, {"ab12cd34ef56ab12", "2026-10-19 12:05:00.000"}} {
		if err := c.Exec(ctx, `INSERT INTO rt.error_stacks (ServiceName, ServiceVersion, RawStackHash, Fingerprint, FpVersion, Title, Culprit, FramesJson, Symbolicated, NoStack, UpdatedAt)
VALUES (?, ?, ?, ?, 1, 't', 'c', '[]', 1, 0, ?)`, svc, ver, hash, r.fp, r.at); err != nil {
			t.Fatal(err)
		}
	}

	tlq := obsquery.SessionTimeline{Scope: sc, SessionID: rumReplaySession}
	page, err := obsquery.ReadTimeline(ctx, run, tlq)
	if err != nil {
		t.Fatal(err)
	}
	type line struct {
		kind obsquery.TimelineKind
		name string
		path string
	}
	var got []line
	for _, e := range page.Entries {
		got = append(got, line{e.Kind, e.Name, e.Path})
	}
	want := []line{
		{obsquery.TimelineNavigation, "documentLoad", "/"},
		{obsquery.TimelineClick, "click", "/"},
		{obsquery.TimelineNavigation, "routeChange", "/pay"},
		{obsquery.TimelineEvent, "checkout_started", "/pay"},
		{obsquery.TimelineError, "onerror", "/pay"},
		{obsquery.TimelineRequestFailed, "POST", "/api.v1.FooService/Bar"},
		{obsquery.TimelineRequestFailed, "GET", "/missing"},
		{obsquery.TimelineRequestFailed, "GET", "/offline"},
		{obsquery.TimelineError, "console.error", "/pay"},
		{obsquery.TimelineError, "unhandledrejection", "/pay"},
		{obsquery.TimelineError, "Error: manually recorded", "/pay"},
		{obsquery.TimelineEvent, "checkout_paid", "/pay"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("timeline\n got %v\nwant %v", got, want)
	}
	if page.NextCursor != "" {
		t.Errorf("a short timeline has a cursor %q", page.NextCursor)
	}
	by := func(kind obsquery.TimelineKind, name string) obsquery.TimelineEntry {
		for _, e := range page.Entries {
			if e.Kind == kind && e.Name == name {
				return e
			}
		}
		t.Fatalf("no %s %q", kind, name)
		return obsquery.TimelineEntry{}
	}
	if e := by(obsquery.TimelineClick, "click"); e.Selector != `//*[@id="go"]` || e.Element != "BUTTON" || e.TraceID == "" || e.SpanID == "" {
		t.Errorf("click = %+v", e)
	}
	if e := by(obsquery.TimelineNavigation, "routeChange"); e.Props["from"] != "/" {
		t.Errorf("route change = %+v", e)
	}
	if e := by(obsquery.TimelineEvent, "checkout_started"); e.Props["plan"] != "pro" || e.Props["step"] != "cart" || len(e.Props) != 2 {
		t.Errorf("custom event props = %v", e.Props)
	}
	if e := by(obsquery.TimelineError, "onerror"); e.ErrorType != "TypeError" || e.Fingerprint != "ab12cd34ef56ab12" ||
		e.Message != "Cannot read properties of undefined (reading <str>)" || e.TraceID == "" {
		t.Errorf("error = %+v", e)
	}
	if e := by(obsquery.TimelineError, "unhandledrejection"); e.Fingerprint != "" || e.Message != "payment declined for order <n>" {
		t.Errorf("an unenriched error must have no fingerprint, and a masked message: %+v", e)
	}
	failedReq := page.Entries[5]
	if failedReq.Status != 500 || failedReq.Method != "POST" || failedReq.Message != "Internal Server Error" || failedReq.TraceID == "" {
		t.Errorf("failed request = %+v", failedReq)
	}
	if page.Entries[6].Status != 404 || page.Entries[7].Status != 0 || page.Entries[7].ErrorType != "TypeError" {
		t.Errorf("404 and network failure = %+v / %+v", page.Entries[6], page.Entries[7])
	}
	// no raw URL, query, fragment or token anywhere in what a timeline returns
	blob, _ := json.Marshal(page.Entries)
	for _, leak := range []string{"token", "secret", "#frag", "#x", "127.0.0.1", "api.example.com", "?"} {
		if bytes.Contains(blob, []byte(leak)) {
			t.Errorf("timeline leaks %q: %s", leak, blob)
		}
	}

	// paging: two entries at a time reproduces the single page, in order, once each
	var paged []obsquery.TimelineEntry
	q := tlq
	q.Limit = 2
	for i := 0; ; i++ {
		if i > 20 {
			t.Fatal("timeline paging does not end")
		}
		p, err := obsquery.ReadTimeline(ctx, run, q)
		if err != nil {
			t.Fatal(err)
		}
		paged = append(paged, p.Entries...)
		if p.NextCursor == "" {
			break
		}
		if q.Cursor, err = obsquery.DecodeTimelineCursor(p.NextCursor); err != nil {
			t.Fatal(err)
		}
	}
	if len(paged) != len(page.Entries) {
		t.Fatalf("paged %d entries, want %d", len(paged), len(page.Entries))
	}
	for i := range paged {
		if paged[i].SpanID != page.Entries[i].SpanID || paged[i].Kind != page.Entries[i].Kind {
			t.Errorf("entry %d differs: %+v vs %+v", i, paged[i], page.Entries[i])
		}
	}

	// the other session's timeline: its one error, no replay, other trace ids
	pl, err := obsquery.ReadTimeline(ctx, run, obsquery.SessionTimeline{Scope: sc, SessionID: rumPlainSession})
	if err != nil || len(pl.Entries) == 0 {
		t.Fatalf("plain timeline = %+v, %v", pl, err)
	}
	for _, e := range pl.Entries {
		if e.TraceID != "" && strings.HasPrefix(e.TraceID, "f0f0f0f0") {
			t.Errorf("a span of another session leaked: %+v", e)
		}
	}
}

// cloneSession rewrites one captured session into another: new id, service,
// environment, user, entry path, hour and optionally without its error spans.
func cloneSession(t *testing.T, id, service, env, user, path, hour string, keepErrors bool) string {
	t.Helper()
	var out []string
	for _, l := range rumLines(t, "traces.jsonl", rumPlainSession) {
		if !keepErrors && strings.Contains(l, `"SpanName":"onerror"`) {
			continue
		}
		l = strings.ReplaceAll(l, rumPlainSession, id)
		l = strings.ReplaceAll(l, "reliant-web", service)
		l = strings.ReplaceAll(l, "env-prod", env)
		l = strings.ReplaceAll(l, "user-42", user)
		// the capture is compact JSON with escaped slashes; only the root URLs
		// (closing quote right after the slash) are the entry page
		l = strings.ReplaceAll(l, `127.0.0.1:43381\/"`, `127.0.0.1:43381`+strings.ReplaceAll(path, "/", `\/`)+`"`)
		l = strings.ReplaceAll(l, "2026-10-19 11:44:", "2026-10-19 "+hour+":44:")
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

func TestRumSessionListFiltersAndCursor(t *testing.T) {
	c := startClickHouse(t)
	ctx := context.Background()
	rumHostedDB(t, c, "rt")
	loadCapture(t, c, "rt")
	// five more sessions, one per hour after the capture: three in env-prod,
	// two of them user-77 on /docs/..., one other-web in env-stg
	rumInsert(t, c, "rt", "otel_traces", strings.Join([]string{
		cloneSession(t, "c1000000000000000000000000000001", "reliant-web", "env-prod", "user-77", "/docs/intro", "12", false),
		cloneSession(t, "c1000000000000000000000000000002", "reliant-web", "env-prod", "user-77", "/docs/api", "13", false),
		cloneSession(t, "c1000000000000000000000000000003", "reliant-web", "env-prod", "user-9", "/", "14", true),
		cloneSession(t, "c1000000000000000000000000000004", "other-web", "env-prod", "user-5", "/", "15", false),
		cloneSession(t, "c1000000000000000000000000000005", "reliant-web", "env-stg", "user-5", "/", "16", true),
	}, "\n"))
	run := rumRunner(c)
	prod := rumScopeOf("rt", "env-prod")

	ids := func(q obsquery.SessionQuery) []string {
		t.Helper()
		var all []string
		for i := 0; ; i++ {
			if i > 30 {
				t.Fatal("paging does not end")
			}
			p, err := obsquery.ListSessions(ctx, run, q)
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range p.Sessions {
				all = append(all, s.SessionID[:6])
			}
			if p.NextCursor == "" {
				return all
			}
			if q.Cursor, err = obsquery.DecodeSessionCursor(p.NextCursor); err != nil {
				t.Fatal(err)
			}
		}
	}
	// newest first; env-stg is outside the scope. 072af6 (11:44:17) started
	// after 91b955 (11:44:06).
	full := ids(obsquery.SessionQuery{Scope: prod})
	if fmt.Sprint(full) != "[c10000 c10000 c10000 c10000 072af6 91b955]" {
		t.Errorf("order: %v", full)
	}
	// every page size yields the same sequence, once each
	for _, n := range []int{1, 2, 3, 5} {
		got := ids(obsquery.SessionQuery{Scope: prod, Limit: n})
		if fmt.Sprint(got) != fmt.Sprint(full) {
			t.Errorf("limit %d: %v, want %v", n, got, full)
		}
	}
	count := func(q obsquery.SessionQuery) int { return len(ids(q)) }
	if n := count(obsquery.SessionQuery{Scope: prod, HasError: true}); n != 3 { // 91b955, 072af6, the user-9 clone keeps its error
		t.Errorf("has_error = %d, want 3", n)
	}
	if n := count(obsquery.SessionQuery{Scope: prod, HasReplay: true}); n != 1 {
		t.Errorf("has_replay = %d, want 1", n)
	}
	if n := count(obsquery.SessionQuery{Scope: prod, UserID: "user-77"}); n != 2 {
		t.Errorf("user_id = %d, want 2", n)
	}
	if n := count(obsquery.SessionQuery{Scope: prod, PathContains: "DOCS"}); n != 2 {
		t.Errorf("path_contains (case-insensitive) = %d, want 2", n)
	}
	if n := count(obsquery.SessionQuery{Scope: prod, PathContains: "docs", HasError: true}); n != 0 {
		t.Errorf("path and error = %d, want 0", n)
	}
	if n := count(obsquery.SessionQuery{Scope: prod, UserID: "user-42"}); n != 2 {
		t.Errorf("user-42 = %d, want 2 (both captured sessions)", n)
	}
	both := rumScopeOf("rt", "env-prod", "env-stg")
	if n := count(obsquery.SessionQuery{Scope: both}); n != 7 {
		t.Errorf("both environments = %d, want 7", n)
	}
	// a service filter and an attribute filter ride the scope
	svc := prod
	svc.Services = []string{"other-web"}
	if got := ids(obsquery.SessionQuery{Scope: svc}); len(got) != 1 {
		t.Errorf("service scope = %v", got)
	}
	// the time range bounds the START: a range after the capture sees no capture
	late := prod
	late.Start = time.Date(2026, 10, 19, 13, 0, 0, 0, time.UTC)
	if got := ids(obsquery.SessionQuery{Scope: late}); len(got) != 3 {
		t.Errorf("late range = %v, want the three sessions of 13:44 and after", got)
	}
	// a session that began before the range start is not clipped into it
	if n := count(obsquery.SessionQuery{Scope: obsquery.Scope{Database: "rt", Start: time.Date(2026, 10, 19, 11, 44, 7, 0, time.UTC), End: time.Date(2026, 10, 19, 11, 44, 30, 0, time.UTC)}}); n != 1 {
		t.Errorf("range inside the capture = %d, want only 072af6 (91b955 started at :06)", n)
	}
}

func TestRumDatabaseIsolation(t *testing.T) {
	c := startClickHouse(t)
	ctx := context.Background()
	rumHostedDB(t, c, "rt_a")
	rumHostedDB(t, c, "rt_b")
	loadCapture(t, c, "rt_a")
	run := rumRunner(c)

	probe := func(sc obsquery.Scope) (sessions int, found bool, entries, events int) {
		t.Helper()
		l, err := obsquery.ListSessions(ctx, run, obsquery.SessionQuery{Scope: sc})
		if err != nil {
			t.Fatal(err)
		}
		_, getErr := obsquery.GetSession(ctx, run, obsquery.SessionLookup{Scope: sc, SessionID: rumReplaySession})
		if getErr != nil && !errors.Is(getErr, obsquery.ErrSessionNotFound) {
			t.Fatal(getErr)
		}
		tl, err := obsquery.ReadTimeline(ctx, run, obsquery.SessionTimeline{Scope: sc, SessionID: rumReplaySession})
		if err != nil {
			t.Fatal(err)
		}
		rp, err := obsquery.ReadReplay(ctx, run, obsquery.ReplayQuery{Scope: sc, SessionID: rumReplaySession})
		if err != nil {
			t.Fatal(err)
		}
		return len(l.Sessions), getErr == nil, len(tl.Entries), len(rp.Events)
	}
	if n, ok, e, ev := probe(rumScopeOf("rt_a", "env-prod")); n != 2 || !ok || e == 0 || ev != 10 {
		t.Errorf("owner: %d sessions, found %v, %d timeline entries, %d replay events", n, ok, e, ev)
	}
	for name, sc := range map[string]obsquery.Scope{
		"another hosted database": rumScopeOf("rt_b", "env-prod"),
		"another environment":     rumScopeOf("rt_a", "env-stg"),
		"no environment":          func() obsquery.Scope { s := rumScopeOf("rt_a"); s.EnvRestricted = true; return s }(),
		"another service": func() obsquery.Scope {
			s := rumScopeOf("rt_a", "env-prod")
			s.Services = []string{"other-web"}
			return s
		}(),
	} {
		if n, ok, e, ev := probe(sc); n != 0 || ok || e != 0 || ev != 0 {
			t.Errorf("%s sees the session: %d sessions, found %v, %d timeline entries, %d replay events", name, n, ok, e, ev)
		}
	}
}

// ── replay ──────────────────────────────────────────────────────────────

type chunkRow struct {
	ts           time.Time
	idx          int
	chunk, total int
	body         string
}

func chunkLine(session, env string, r chunkRow) string {
	row := map[string]any{
		"Timestamp":   r.ts.UTC().Format("2006-01-02 15:04:05.000000000"),
		"ServiceName": "reliant-web", "ScopeName": "rum.rr-web", "ScopeVersion": "4.0.0", "Body": r.body,
		"ResourceAttributes": map[string]string{"rum.sessionId": session, "reliant.env.id": env, "service.name": "reliant-web", "service.version": "3f9c0e"},
		"LogAttributes": map[string]string{
			"rr-web.offset": fmt.Sprint(r.idx), "rr-web.event": fmt.Sprint(r.idx),
			"rr-web.chunk": fmt.Sprint(r.chunk), "rr-web.total-chunks": fmt.Sprint(r.total),
		},
	}
	b, _ := json.Marshal(row)
	return string(b)
}

// splitEvent cuts an event's JSON text into the recorder's byte chunks.
func splitEvent(text string, size int) []string {
	var out []string
	for i := 0; i < len(text); i += size {
		out = append(out, text[i:min(i+size, len(text))])
	}
	return out
}

// snapshotText is a masked full snapshot of a heavy page: a type-2 event over two
// recorder chunks plus a third nearly empty one, with escapes and non-ASCII
// before the first cut so no rune straddles a chunk boundary.
func snapshotText(ts int64) string {
	text := `{"type":2,"data":{"node":{"type":0,"childNodes":[{"type":3,"textContent":"` +
		`\u00e9 \"quoted\" \\n 名前 ✓ ` + strings.Repeat("*", 2*chunkBytes+1000) + `","id":7}],"id":1},"initialOffset":{"left":0,"top":0}},"timestamp":` + fmt.Sprint(ts) + `}`
	return text
}

func TestRumReplayChunksPagesAndWithheldEvents(t *testing.T) {
	c := startClickHouse(t)
	ctx := context.Background()
	rumHostedDB(t, c, "rt")
	run := rumRunner(c)
	const sess = "d1000000000000000000000000000001"
	base := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	at := func(ms int) time.Time { return base.Add(time.Duration(ms) * time.Millisecond) }
	inc := func(i int) string {
		return fmt.Sprintf(`{"type":3,"data":{"source":1,"positions":[{"x":%d,"y":%d,"id":9,"timeOffset":0}]},"timestamp":%d}`, i, i*2, at(i).UnixMilli())
	}
	snap := snapshotText(at(10).UnixMilli())
	snapChunks := splitEvent(snap, chunkBytes)
	if len(snapChunks) != 3 {
		t.Fatalf("the snapshot is %d chunks, want 3", len(snapChunks))
	}

	type ev struct {
		idx  int
		ts   time.Time
		text string
	}
	var want []ev // the events a complete read must return, in order
	var lines []string
	put := func(idx int, ts time.Time, text string) {
		want = append(want, ev{idx, ts, text})
		parts := splitEvent(text, chunkBytes)
		for i, p := range parts {
			lines = append(lines, chunkLine(sess, "env-prod", chunkRow{ts, idx, i + 1, len(parts), p}))
		}
	}
	put(1, at(0), `{"type":4,"data":{"href":"http://127.0.0.1:43185/","width":1280,"height":720},"timestamp":`+fmt.Sprint(at(0).UnixMilli())+`}`)
	put(2, at(10), snap)
	put(3, at(10), inc(10)) // same timestamp as the snapshot, later index
	for i := 4; i <= 13; i++ {
		put(i, at(1000+i), inc(1000+i))
	}
	// idx 14: its second chunk never arrives, and the session recorded 2 minutes
	// after it: lost for good, skipped
	lines = append(lines, chunkLine(sess, "env-prod", chunkRow{at(2000), 14, 1, 2, `{"type":3,"data":{"source":0,"texts":[`}))
	for i := 15; i <= 24; i++ {
		put(i, at(3000+i), inc(3000+i))
	}
	// a second page load in the same session: the counter restarts at 1
	put(1, at(120_000), `{"type":4,"data":{"href":"http://127.0.0.1:43185/pay","width":1280,"height":720},"timestamp":`+fmt.Sprint(at(120_000).UnixMilli())+`}`)
	put(2, at(120_050), inc(120_050))
	// the live edge: idx 3's second chunk is in flight, idx 4 is whole behind it
	lines = append(lines, chunkLine(sess, "env-prod", chunkRow{at(120_100), 3, 1, 2, `{"type":3,"data":{"source":0,"adds":[`}))

	// chunks arrive out of order and in several exports: the snapshot's last
	// chunk first, then everything else in reverse
	for i := len(lines) - 1; i >= 0; i-- {
		rumInsert(t, c, "rt", "otel_logs", lines[i])
	}
	rumInsert(t, c, "rt", "otel_logs", chunkLine(sess, "env-prod", chunkRow{at(120_200), 4, 1, 1, inc(120_200)}))

	sc := rumScopeOf("rt", "env-prod")
	s, err := obsquery.GetSession(ctx, run, obsquery.SessionLookup{Scope: sc, SessionID: sess})
	if err != nil || !s.HasReplay {
		t.Fatalf("session summary = %+v, %v (replay must mark the session through the view)", s, err)
	}

	read := func(q obsquery.ReplayQuery) obsquery.ReplayPage {
		t.Helper()
		p, err := obsquery.ReadReplay(ctx, run, q)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	q := obsquery.ReplayQuery{Scope: sc, SessionID: sess, MaxBytes: 1 << 20}

	// page 1: the tiny first event and nothing else: the 2.9 MB snapshot does not
	// fit the 1 MiB budget beside it
	p1 := read(q)
	if len(p1.Events) != 1 || p1.Events[0].Index != 1 || !p1.More || p1.NextToken == "" || p1.Bytes > 1<<20 {
		t.Fatalf("page 1 = %d events, more %v, %d bytes", len(p1.Events), p1.More, p1.Bytes)
	}
	var err2 error
	q.Cursor, err2 = obsquery.DecodeReplayCursor(p1.NextToken)
	if err2 != nil {
		t.Fatal(err2)
	}
	// page 2: the full snapshot alone, over budget, reassembled from three chunks
	p2 := read(q)
	if len(p2.Events) != 1 || p2.Events[0].Chunks != 3 || p2.Events[0].Index != 2 || !p2.More {
		t.Fatalf("page 2 = %d events (%+v)", len(p2.Events), p2.Events)
	}
	if string(p2.Events[0].Data) != snap {
		t.Fatalf("the snapshot changed: %d bytes back, %d sent", len(p2.Events[0].Data), len(snap))
	}
	if p2.Bytes <= 1<<20 || p2.Bytes > obsquery.MaxReplayEventBytes {
		t.Errorf("snapshot page bytes = %d", p2.Bytes)
	}
	q.Cursor, _ = obsquery.DecodeReplayCursor(p2.NextToken)
	// page 3: everything up to the live edge fits one page. idx 14 (lost) is
	// skipped; the second page load's idx 3 is incomplete and recent: withheld with
	// the whole event behind it (idx 4)
	p3 := read(q)
	if p3.More || p3.NextToken != "" || p3.Resume == "" || p3.Withheld != 0 {
		t.Fatalf("page 3: more %v, token %q, resume %q, withheld %d", p3.More, p3.NextToken, p3.Resume, p3.Withheld)
	}
	var got []ev
	for _, p := range []obsquery.ReplayPage{p1, p2, p3} {
		for _, e := range p.Events {
			got = append(got, ev{int(e.Index), e.Time, string(e.Data)})
		}
	}
	expected := want
	if len(got) != len(expected) {
		t.Fatalf("read %d events, want %d", len(got), len(expected))
	}
	for i := range got {
		if got[i].idx != expected[i].idx || !got[i].ts.Equal(expected[i].ts) || got[i].text != expected[i].text {
			t.Fatalf("event %d: idx %d ts %s, want idx %d ts %s (bytes equal %v)", i, got[i].idx, got[i].ts, expected[i].idx, expected[i].ts, got[i].text == expected[i].text)
		}
	}
	for _, e := range got {
		if !json.Valid([]byte(e.text)) {
			t.Errorf("half an event returned: idx %d", e.idx)
		}
		if strings.Contains(e.text, `"texts":[`) || strings.Contains(e.text, `"adds":[`) {
			t.Errorf("a partial event leaked: idx %d", e.idx)
		}
	}

	// the live edge completes: the second chunk of idx 3 arrives, and a poll from
	// the resume token returns it and the event behind it, in order
	rumInsert(t, c, "rt", "otel_logs", chunkLine(sess, "env-prod", chunkRow{at(120_100), 3, 2, 2, `]},"timestamp":` + fmt.Sprint(at(120_100).UnixMilli()) + `}`}))
	q.Cursor, _ = obsquery.DecodeReplayCursor(p3.Resume)
	p4 := read(q)
	if len(p4.Events) != 2 || p4.Events[0].Index != 3 || p4.Events[1].Index != 4 || string(p4.Events[0].Data) != `{"type":3,"data":{"source":0,"adds":[]},"timestamp":`+fmt.Sprint(at(120_100).UnixMilli())+`}` {
		t.Fatalf("poll after the last chunk = %+v", p4.Events)
	}
	if p4.More || p4.Resume == "" {
		t.Errorf("poll page: more %v resume %q", p4.More, p4.Resume)
	}
	q.Cursor, _ = obsquery.DecodeReplayCursor(p4.Resume)
	if p5 := read(q); len(p5.Events) != 0 || p5.More || p5.Resume != "" {
		t.Errorf("a poll with nothing new = %+v", p5)
	}

	// every budget walks the same sequence exactly once, each page inside its bound
	for _, budget := range []int{1, 2000, 50_000, 3 << 20, obsquery.MaxReplayPageBytes} {
		wq := obsquery.ReplayQuery{Scope: sc, SessionID: sess, MaxBytes: budget}
		var seq []string
		for i := 0; ; i++ {
			if i > 200 {
				t.Fatalf("budget %d: paging does not end", budget)
			}
			p := read(wq)
			if len(p.Events) > 1 && p.Bytes > budget {
				t.Errorf("budget %d: a page of %d events holds %d bytes", budget, len(p.Events), p.Bytes)
			}
			for _, e := range p.Events {
				seq = append(seq, fmt.Sprintf("%d@%d", e.Index, e.Time.UnixMilli()))
			}
			if !p.More {
				break
			}
			wq.Cursor, _ = obsquery.DecodeReplayCursor(p.NextToken)
		}
		if len(seq) != len(want)+2 { // the whole events, and idx 3 and 4 of the second load, now complete
			t.Errorf("budget %d: %d events (%v)", budget, len(seq), seq)
		}
		seen := map[string]bool{}
		for _, s := range seq {
			if seen[s] {
				t.Errorf("budget %d: %s returned twice", budget, s)
			}
			seen[s] = true
		}
	}
}

func TestRumReplayCapturedSessionIsPlayableInOrder(t *testing.T) {
	c := startClickHouse(t)
	ctx := context.Background()
	rumHostedDB(t, c, "rt")
	loadCapture(t, c, "rt")
	run := rumRunner(c)
	sc := rumScopeOf("rt", "env-prod")

	var sent []string
	for _, l := range rumLines(t, "logs.jsonl", rumReplaySession) {
		var row struct{ Body string }
		if err := json.Unmarshal([]byte(l), &row); err != nil {
			t.Fatal(err)
		}
		sent = append(sent, row.Body)
	}
	p, err := obsquery.ReadReplay(ctx, run, obsquery.ReplayQuery{Scope: sc, SessionID: rumReplaySession})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Events) != len(sent) || p.More {
		t.Fatalf("%d events (more %v), want %d", len(p.Events), p.More, len(sent))
	}
	types := ""
	for i, e := range p.Events {
		if string(e.Data) != sent[i] {
			t.Errorf("event %d changed:\n got %s\nwant %s", i, e.Data, sent[i])
		}
		var o struct{ Type int }
		_ = json.Unmarshal(e.Data, &o)
		types += fmt.Sprint(o.Type)
	}
	// rrweb's order of a page load: DomContentLoaded, Load, Meta, FullSnapshot, then increments
	if types != "0142333333" {
		t.Errorf("event types = %s", types)
	}
	// the capture is masked at record time and the server leaves it so
	if !bytes.Contains(p.Events[3].Data, []byte(`"textContent":"********"`)) {
		t.Errorf("the masked snapshot changed: %.300s", p.Events[3].Data)
	}
	// the recorded session has no unmasked text: whatever the page showed is stars
	if bytes.Contains(p.Events[3].Data, []byte("Checkout")) {
		t.Errorf("unmasked text in a masked recording")
	}

	// the other session has no replay: an empty page, not an error
	e, err := obsquery.ReadReplay(ctx, run, obsquery.ReplayQuery{Scope: sc, SessionID: rumPlainSession})
	if err != nil || len(e.Events) != 0 || e.More || e.Resume != "" {
		t.Errorf("a session without replay = %+v, %v", e, err)
	}
}
