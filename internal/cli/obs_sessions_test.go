package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/obsbackend"
)

// The session verbs against both backends. One fixture session is written
// once, as ClickHouse rows for the local backend and as the control plane's
// protojson for the cloud one, so the parity test compares the same session.

const (
	sessID     = "91b955b313a1a86644ea53730a92deab"
	sessTrace  = "0af7651916cd43dd8448eb211c80319c"
	sessTrace2 = "b7ad6b7169203331b7ad6b7169203331"

	procListSessions   = "controlplane.v1.RumService/ListSessions"
	procGetSession     = "controlplane.v1.RumService/GetSession"
	procSessionTimline = "controlplane.v1.RumService/GetSessionTimeline"
)

var (
	sessStart = time.Date(2026, 10, 22, 11, 44, 6, 285_000_000, time.UTC)
	sessEnd   = time.Date(2026, 10, 22, 11, 47, 18, 100_000_000, time.UTC)
)

type sessEntry struct {
	at                                         time.Duration
	kind, name, path, method                   string
	status                                     int
	selector, element, errType, msg, fp, issue string
	trace, span                                string
	durMs                                      float64
	props                                      map[string]string
}

func sessFixture(issue string) []sessEntry {
	return []sessEntry{
		{at: 0, kind: "navigation", name: "documentLoad", path: "/"},
		{at: 4200 * time.Millisecond, kind: "navigation", name: "routeChange", path: "/jobs/new", props: map[string]string{"from": "/"}},
		{at: 12400 * time.Millisecond, kind: "click", name: "click", path: "/jobs/new", selector: `//*[@id="save"]`, element: "button"},
		{at: 12500 * time.Millisecond, kind: "request_failed", name: "POST", method: "POST", path: "/api.v1.JobService/CreateJob", status: 500, durMs: 230.5, trace: sessTrace, span: "00f067aa0ba902b7"},
		{at: 12600 * time.Millisecond, kind: "error", name: "onerror", path: "/jobs/new", errType: "TypeError", msg: "Cannot read properties of undefined", fp: "0123456789abcdef", issue: issue, trace: sessTrace, span: "b7ad6b7169203331"},
		{at: 30 * time.Second, kind: "event", name: "checkout_started", path: "/jobs/new", props: map[string]string{"plan": "pro", "seats": "3"}},
		{at: 41 * time.Second, kind: "request_failed", name: "GET", method: "GET", path: "/api.v1.JobService/ListJobs", status: 0, errType: "network", trace: sessTrace2, span: "1111111111111111"},
	}
}

func (e sessEntry) localRow() []any {
	props := map[string]any{}
	for k, v := range e.props {
		props[k] = v
	}
	return []any{sessStart.Add(e.at).Format("2006-01-02 15:04:05.000000000"), e.kind, e.name, e.path, e.method, jn(fmt.Sprint(e.status)),
		e.selector, e.element, e.errType, e.msg, e.fp, e.trace, e.span, jn(fmt.Sprint(e.durMs)), props, "reliant-web"}
}

func (e sessEntry) cloudJSON() map[string]any {
	m := map[string]any{
		"time": sessStart.Add(e.at).Format(time.RFC3339Nano), "kind": "SESSION_TIMELINE_KIND_" + strings.ToUpper(e.kind),
		"name": e.name, "path": e.path, "method": e.method, "status": e.status, "selector": e.selector, "element": e.element,
		"errorType": e.errType, "message": e.msg, "fingerprint": e.fp, "issueId": e.issue, "traceId": e.trace, "spanId": e.span,
		"durationMs": e.durMs, "service": "reliant-web",
	}
	if e.props != nil {
		m["props"] = e.props
	}
	return m
}

func localTimelineAnswer(entries []sessEntry) scriptedAnswer {
	var rows [][]any
	for _, e := range entries {
		rows = append(rows, e.localRow())
	}
	return scriptedAnswer{"UNION ALL", obsbackend.Result{Rows: rows}}
}

func localSessionRow(id string, started time.Time, user, entry string, pv, ia, errs int, replay int) []any {
	return []any{id, "reliant-web", "3f9c0e", "", user, entry, started.Format("2006-01-02 15:04:05.000000000"),
		started.Add(3*time.Minute + 12*time.Second).Format("2006-01-02 15:04:05.000000000"),
		jn(fmt.Sprint(pv)), jn(fmt.Sprint(ia)), jn(fmt.Sprint(errs)), jn(fmt.Sprint(replay))}
}

func localSessionAnswers(entries []sessEntry) []scriptedAnswer {
	return []scriptedAnswer{
		{"ORDER BY started DESC\nLIMIT 1", obsbackend.Result{Rows: [][]any{
			{sessID, "reliant-web", "3f9c0e", "", "user-42", "/", sessStart.Format("2006-01-02 15:04:05.000000000"), sessEnd.Format("2006-01-02 15:04:05.000000000"), jn("5"), jn("12"), jn("2"), jn("1")},
		}}},
		localTimelineAnswer(entries),
	}
}

func cloudSessionJSON() string {
	return `{"session":{"sessionId":"` + sessID + `","service":"reliant-web","serviceVersion":"3f9c0e","envId":"env-prod","envName":"prod","userId":"user-42",
"entryPath":"/","startedAt":"` + sessStart.Format(time.RFC3339Nano) + `","endedAt":"` + sessEnd.Format(time.RFC3339Nano) + `","pageViews":"5","interactions":"12","errors":"2","hasReplay":true}}`
}

func cloudTimelineJSON(entries []sessEntry, next string) string {
	var es []map[string]any
	for _, e := range entries {
		es = append(es, e.cloudJSON())
	}
	raw, _ := json.Marshal(map[string]any{"entries": es, "nextPageToken": next})
	return string(raw)
}

func sessFacts(env string) obsbackend.Facts {
	return obsbackend.Facts{Env: env, Declared: true, Enabled: true, HasLocal: true}
}

// ── sessions (the list) ─────────────────────────────────────────────────

func localListAnswer() scriptedAnswer {
	return scriptedAnswer{"HAVING", obsbackend.Result{Rows: [][]any{
		localSessionRow(sessID, sessStart, "user-42", "/", 5, 12, 2, 1),
		localSessionRow("072af6846fd16a702ead77ec49f2408e", sessStart.Add(-time.Hour), "", "/pricing", 1, 3, 0, 0),
	}}}
}

func TestObsSessionsLocalJSONAndTextGolden(t *testing.T) {
	sr := &scriptedRunner{answers: []scriptedAnswer{localListAnswer()}}
	out, _, err := obsRun(t, sessFacts("dev"), sr, "sessions", "dev", "--json")
	if err != nil {
		t.Fatal(err)
	}
	obsGolden(t, "sessions.json", out)
	doc := decode(t, out)
	if doc["schema"] != "forge.obs.sessions/v1" || doc["ok"] != true || doc["query_mode"] != "structured" {
		t.Errorf("head = %v", doc)
	}
	first := doc["sessions"].([]any)[0].(map[string]any)
	if first["id"] != sessID || first["errors"] != float64(2) || first["has_replay"] != true || first["user_id"] != "user-42" || first["entry_path"] != "/" {
		t.Errorf("first session = %v", first)
	}
	if sc := doc["scope"].(map[string]any); sc["backend"] != "local" {
		t.Errorf("scope = %v", sc)
	}
	sug := strings.Join(toStrings(doc["suggested"]), "|")
	if !strings.Contains(sug, "forge obs session "+sessID+" dev") {
		t.Errorf("suggested = %s", sug)
	}
	if len(sr.ran) != 1 || !strings.Contains(sr.ran[0], "rum_sessions") {
		t.Errorf("ran = %v", sr.ran)
	}

	sr = &scriptedRunner{answers: []scriptedAnswer{localListAnswer()}}
	text, _, err := obsRun(t, sessFacts("dev"), sr, "sessions", "dev")
	if err != nil {
		t.Fatal(err)
	}
	obsGolden(t, "sessions.txt", text)
	// A row leads with the app and the time, not the opaque id — which stays, whole, as a column.
	lines := strings.Split(text, "\n")
	if len(lines) < 2 || !strings.HasPrefix(lines[0], "APP ") || !strings.HasPrefix(lines[1], "reliant-web ") || !strings.HasSuffix(strings.TrimSpace(lines[1]), sessID) {
		t.Errorf("table should lead with app + time and end with the full id:\n%s", text)
	}
}

func TestObsSessionsLocalFlagsCompileToTheCompilersFilters(t *testing.T) {
	sr := &scriptedRunner{}
	out, _, err := obsRun(t, sessFacts("dev"), sr, "sessions", "dev", "--explain", "--json",
		"--has-error", "--has-replay", "--user", "user-42", "--path", "/Jobs", "--since", "6h", "--limit", "10")
	if err != nil {
		t.Fatal(err)
	}
	if len(sr.ran) != 0 {
		t.Fatal("--explain ran a statement")
	}
	st := decode(t, out)["statements"].([]any)[0].(map[string]any)
	sql, params := st["sql"].(string), fmt.Sprint(st["params"])
	for _, want := range []string{"sum(Errors) > 0", "max(HasReplay) = 1", "max(UserId)", "positionCaseInsensitive(argMinMerge(EntryPath)", "LIMIT 11"} {
		if !strings.Contains(sql, want) {
			t.Errorf("statement lacks %q:\n%s", want, sql)
		}
	}
	if !strings.Contains(params, "user-42") || !strings.Contains(params, "/Jobs") {
		t.Errorf("params = %s", params)
	}
}

func TestObsSessionsFlagValidationExits1(t *testing.T) {
	for _, args := range [][]string{
		{"sessions", "dev", "--limit", "0"},
		{"sessions", "dev", "--limit", "101"},
		{"sessions", "dev", "--since", "720h1m"},
		{"sessions", "dev", "--filter", "service.name=api"},
	} {
		sr := &scriptedRunner{}
		if _, _, err := obsRun(t, sessFacts("dev"), sr, args...); exitCodeForError(err) != 1 {
			t.Errorf("%v: exit %d, want 1 (%v)", args, exitCodeForError(err), err)
		}
		if len(sr.ran) != 0 {
			t.Errorf("%v: a refused request reached ClickHouse", args)
		}
	}
}

func TestObsSessionsCloudJSONGoldenAndRequestShape(t *testing.T) {
	fc := &fakeCloudCaller{replies: map[string]string{procListSessions: `{"sessions":[
{"sessionId":"` + sessID + `","service":"reliant-web","serviceVersion":"3f9c0e","envId":"env-prod","envName":"prod","userId":"user-42","entryPath":"/",
"startedAt":"2026-10-22T11:44:06.285Z","endedAt":"2026-10-22T11:47:18.1Z","pageViews":"5","interactions":"12","errors":"2","hasReplay":true},
{"sessionId":"072af6846fd16a702ead77ec49f2408e","service":"reliant-web","serviceVersion":"3f9c0e","envId":"env-prod","envName":"prod","entryPath":"/pricing",
"startedAt":"2026-10-22T10:44:06.285Z","endedAt":"2026-10-22T10:47:18.285Z","pageViews":"1","interactions":"3","errors":"0","hasReplay":false}],"nextPageToken":"tok2"}`}}
	out, _, err := obsRunCloud(t, fc, "sessions", "prod", "--json", "--has-error", "--has-replay", "--user", "user-42", "--path", "/jobs",
		"--service", "reliant-web", "--limit", "10", "--page-token", "tok1")
	if err != nil {
		t.Fatal(err)
	}
	obsGolden(t, "sessions.cloud.json", out)
	body := fc.calls[0].body
	for _, want := range []string{`"orgId":"org-1"`, `"envIds":["env-prod"]`, `"services":["reliant-web"]`, `"hasError":true`, `"hasReplay":true`,
		`"userId":"user-42"`, `"pathContains":"/jobs"`, `"pageSize":10`, `"pageToken":"tok1"`, `"timeRange":{"start":"2026-10-21T12:00:00Z","end":"2026-10-22T12:00:00Z"}`} {
		if !strings.Contains(body, want) {
			t.Errorf("request lacks %s:\n%s", want, body)
		}
	}
	doc := decode(t, out)
	if doc["truncated"] != true || doc["next_page_token"] != wrapPageToken(cloudTestRange, "tok2") {
		t.Errorf("page = %v %v", doc["truncated"], doc["next_page_token"])
	}
	if sc := doc["scope"].(map[string]any); sc["backend"] != "cloud" || sc["env_id"] != "env-prod" {
		t.Errorf("scope = %v", sc)
	}
}

// ── session (one timeline) ──────────────────────────────────────────────

func TestObsSessionLocalJSONAndTextGolden(t *testing.T) {
	sr := &scriptedRunner{answers: localSessionAnswers(sessFixture(""))}
	out, _, err := obsRun(t, sessFacts("prod"), sr, "session", sessID, "prod", "--json")
	if err != nil {
		t.Fatal(err)
	}
	obsGolden(t, "session.json", out)
	doc := decode(t, out)
	if doc["schema"] != "forge.obs.session/v1" || doc["ok"] != true {
		t.Errorf("head = %v", doc)
	}
	tl := doc["timeline"].([]any)
	if len(tl) != 7 {
		t.Fatalf("timeline has %d entries, want 7", len(tl))
	}
	failed := tl[3].(map[string]any)
	if failed["kind"] != "request_failed" || failed["status"] != float64(500) || failed["path"] != "/api.v1.JobService/CreateJob" || failed["offset_ms"] != float64(12500) {
		t.Errorf("failed request = %v", failed)
	}
	if bytesLeak := strings.Contains(out, "rrweb") || strings.Contains(out, "\"data\""); bytesLeak {
		t.Error("a session document carries no replay bytes")
	}
	if len(sr.ran) != 2 || !strings.Contains(sr.ran[0], "rum_sessions") || !strings.Contains(sr.ran[1], "UNION ALL") {
		t.Errorf("ran = %v", sr.ran)
	}

	sr = &scriptedRunner{answers: localSessionAnswers(sessFixture(""))}
	text, _, err := obsRun(t, sessFacts("prod"), sr, "session", sessID, "prod")
	if err != nil {
		t.Fatal(err)
	}
	obsGolden(t, "session.txt", text)
	for _, want := range []string{"POST /api.v1.JobService/CreateJob → 500", "forge obs trace " + sessTrace + " prod", "suggested:", "(fingerprint 0123456789abcdef)"} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
	if tail := text[strings.Index(text, "suggested:"):]; !strings.HasSuffix(strings.TrimRight(tail, "\n"), "forge obs trace "+sessTrace2+" prod") {
		t.Errorf("the suggested block comes last:\n%s", text)
	}
}

func TestObsSessionCloudJSONAndTextGolden(t *testing.T) {
	fc := &fakeCloudCaller{replies: map[string]string{
		procGetSession:     cloudSessionJSON(),
		procSessionTimline: cloudTimelineJSON(sessFixture("ISS-ab12"), ""),
	}}
	out, _, err := obsRunCloud(t, fc, "session", sessID, "prod", "--json")
	if err != nil {
		t.Fatal(err)
	}
	obsGolden(t, "session.cloud.json", out)
	for _, c := range fc.calls {
		for _, want := range []string{`"orgId":"org-1"`, `"envIds":["env-prod"]`, `"sessionId":"` + sessID + `"`} {
			if !strings.Contains(c.body, want) {
				t.Errorf("%s request lacks %s:\n%s", c.procedure, want, c.body)
			}
		}
		if strings.Contains(c.body, "timeRange") {
			t.Errorf("%s: a keyed read leaves the range to the control plane:\n%s", c.procedure, c.body)
		}
	}
	if fc.calls[1].procedure != procSessionTimline || !strings.Contains(fc.calls[1].body, `"pageSize":200`) {
		t.Errorf("timeline call = %+v", fc.calls[1])
	}
	doc := decode(t, out)
	errEntry := doc["timeline"].([]any)[4].(map[string]any)
	if errEntry["issue_id"] != "ISS-ab12" || errEntry["fingerprint"] != "0123456789abcdef" {
		t.Errorf("error entry = %v", errEntry)
	}
	sug := strings.Join(toStrings(doc["suggested"]), "|")
	if !strings.HasPrefix(sug, "forge obs issue ISS-ab12 prod|forge obs trace "+sessTrace+" prod") {
		t.Errorf("suggested = %s", sug)
	}

	fc = &fakeCloudCaller{replies: map[string]string{procGetSession: cloudSessionJSON(), procSessionTimline: cloudTimelineJSON(sessFixture("ISS-ab12"), "")}}
	text, _, err := obsRunCloud(t, fc, "session", sessID, "prod")
	if err != nil {
		t.Fatal(err)
	}
	obsGolden(t, "session.cloud.txt", text)
	if !strings.Contains(text, "→ issue ISS-ab12") {
		t.Errorf("text lacks the issue link:\n%s", text)
	}
}

// TestObsSessionParityLocalVsCloud: the same session read through either
// backend prints the same session and the same timeline, as JSON and as text.
func TestObsSessionParityLocalVsCloud(t *testing.T) {
	entries := sessFixture("")
	sr := &scriptedRunner{answers: localSessionAnswers(entries)}
	lOut, _, err := obsRun(t, sessFacts("prod"), sr, "session", sessID, "prod", "--json")
	if err != nil {
		t.Fatal(err)
	}
	fc := &fakeCloudCaller{replies: map[string]string{procGetSession: cloudSessionJSON(), procSessionTimline: cloudTimelineJSON(entries, "")}}
	cOut, _, err := obsRunCloud(t, fc, "session", sessID, "prod", "--json")
	if err != nil {
		t.Fatal(err)
	}
	l, c := decode(t, lOut), decode(t, cOut)
	for _, k := range []string{"schema", "ok", "exit_code", "query_mode", "session", "timeline", "suggested", "truncated"} {
		lj, _ := json.Marshal(l[k])
		cj, _ := json.Marshal(c[k])
		if string(lj) != string(cj) {
			t.Errorf("%s differs\nlocal: %s\ncloud: %s", k, lj, cj)
		}
	}
	if l["scope"].(map[string]any)["backend"] != "local" || c["scope"].(map[string]any)["backend"] != "cloud" {
		t.Error("the scope names the backend that answered")
	}

	sr = &scriptedRunner{answers: localSessionAnswers(entries)}
	lText, _, _ := obsRun(t, sessFacts("prod"), sr, "session", sessID, "prod")
	fc = &fakeCloudCaller{replies: map[string]string{procGetSession: cloudSessionJSON(), procSessionTimline: cloudTimelineJSON(entries, "")}}
	cText, _, _ := obsRunCloud(t, fc, "session", sessID, "prod")
	if lText != cText {
		t.Errorf("text differs\n--- local\n%s\n--- cloud\n%s", lText, cText)
	}
}

func TestObsSessionPagesTheTimelineOnBothBackends(t *testing.T) {
	entries := sessFixture("")[:3]
	sr := &scriptedRunner{answers: localSessionAnswers(entries)}
	out, _, err := obsRun(t, sessFacts("prod"), sr, "session", sessID, "prod", "--json", "--limit", "2")
	if err != nil {
		t.Fatal(err)
	}
	doc := decode(t, out)
	tok, _ := doc["next_page_token"].(string)
	if doc["truncated"] != true || tok == "" || len(doc["timeline"].([]any)) != 2 {
		t.Fatalf("page = truncated %v token %q entries %d", doc["truncated"], tok, len(doc["timeline"].([]any)))
	}
	if sug := strings.Join(toStrings(doc["suggested"]), "|"); !strings.Contains(sug, "--page-token "+tok) {
		t.Errorf("suggested lacks the next page: %s", sug)
	}
	sr = &scriptedRunner{answers: localSessionAnswers(entries[2:])}
	if _, _, err := obsRun(t, sessFacts("prod"), sr, "session", sessID, "prod", "--json", "--limit", "2", "--page-token", tok); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sr.ran[1], "(ts, kind, span_id) >") {
		t.Errorf("page 2 does not resume after the cursor:\n%s", sr.ran[1])
	}

	fc := &fakeCloudCaller{replies: map[string]string{procGetSession: cloudSessionJSON(), procSessionTimline: cloudTimelineJSON(entries[:2], "next-2")}}
	out, _, err = obsRunCloud(t, fc, "session", sessID, "prod", "--json", "--limit", "2", "--page-token", "prev")
	if err != nil {
		t.Fatal(err)
	}
	if body := fc.calls[1].body; !strings.Contains(body, `"pageSize":2`) || !strings.Contains(body, `"pageToken":"prev"`) {
		t.Errorf("timeline request = %s", body)
	}
	if doc := decode(t, out); doc["truncated"] != true || doc["next_page_token"] != "next-2" {
		t.Errorf("page = %v %v", doc["truncated"], doc["next_page_token"])
	}
}

func TestObsSessionExplainCompilesBothStatements(t *testing.T) {
	sr := &scriptedRunner{}
	out, _, err := obsRun(t, sessFacts("prod"), sr, "session", sessID, "prod", "--explain", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if len(sr.ran) != 0 {
		t.Fatal("--explain ran a statement")
	}
	if st := decode(t, out)["statements"].([]any); len(st) != 2 {
		t.Errorf("statements = %v", st)
	}
}

// ── not found, bad id, could not look ───────────────────────────────────

func TestObsSessionNotFoundAndBadIDExit1(t *testing.T) {
	sr := &scriptedRunner{}
	out, _, err := obsRun(t, sessFacts("prod"), sr, "session", sessID, "prod", "--json")
	if exitCodeForError(err) != 1 {
		t.Fatalf("local not found: exit %d, want 1 (%v)", exitCodeForError(err), err)
	}
	if doc := decode(t, out); doc["ok"] != false || doc["exit_code"] != float64(1) || !strings.Contains(doc["error"].(string), "session "+sessID+" not found") {
		t.Errorf("local not-found document = %v", doc)
	}
	if len(sr.ran) != 1 {
		t.Errorf("a session that is not there stops after the lookup: %v", sr.ran)
	}

	fc := &fakeCloudCaller{errs: map[string]error{procGetSession: &cloud.Error{Code: cloud.CodeNotFound, Message: "no such session"}}}
	out, _, err = obsRunCloud(t, fc, "session", sessID, "prod", "--json")
	if exitCodeForError(err) != 1 {
		t.Fatalf("cloud not found: exit %d, want 1 (%v)", exitCodeForError(err), err)
	}
	if doc := decode(t, out); doc["ok"] != false || !strings.Contains(doc["error"].(string), "session "+sessID+" not found") {
		t.Errorf("cloud not-found document = %v", doc)
	}
	if len(fc.calls) != 1 {
		t.Errorf("a missing session stops before the timeline: %v", fc.calls)
	}

	for _, id := range []string{"not a session!", strings.Repeat("a", 129)} {
		sr = &scriptedRunner{}
		if _, _, err := obsRun(t, sessFacts("prod"), sr, "session", id, "prod"); exitCodeForError(err) != 1 || len(sr.ran) != 0 {
			t.Errorf("bad id %q: exit %d, ran %v (%v)", id, exitCodeForError(err), sr.ran, err)
		}
	}
}

func TestObsSessionsOlderControlPlaneExits2WithTheRPCName(t *testing.T) {
	unimpl := &cloud.Error{Code: cloud.CodeUnimplemented, Message: "no such method"}
	for _, c := range []struct {
		name string
		args []string
		errs map[string]error
		rpc  string
	}{
		{"list", []string{"sessions", "prod", "--json"}, map[string]error{procListSessions: unimpl}, "ListSessions"},
		{"summary", []string{"session", sessID, "prod", "--json"}, map[string]error{procGetSession: unimpl}, "GetSession"},
		{"timeline", []string{"session", sessID, "prod", "--json"}, map[string]error{procSessionTimline: unimpl}, "GetSessionTimeline"},
	} {
		t.Run(c.name, func(t *testing.T) {
			fc := &fakeCloudCaller{errs: c.errs, replies: map[string]string{procGetSession: cloudSessionJSON()}}
			out, _, err := obsRunCloud(t, fc, c.args...)
			if exitCodeForError(err) != 2 {
				t.Fatalf("exit %d, want 2: %v", exitCodeForError(err), err)
			}
			doc := decode(t, out)
			if want := "this control plane does not serve " + c.rpc + " yet"; !strings.Contains(doc["error"].(string), want) {
				t.Errorf("error = %v, want it to say %q", doc["error"], want)
			}
			if doc["ok"] != false || doc["exit_code"] != float64(2) {
				t.Errorf("document = %v", doc)
			}
		})
	}
}

func TestObsSessionsCouldNotLookExits2(t *testing.T) {
	for _, args := range [][]string{{"sessions", "dev"}, {"session", sessID, "dev"}} {
		sr := &scriptedRunner{err: obsbackend.Undetermined("the local ClickStack is not running")}
		if _, _, err := obsRun(t, sessFacts("dev"), sr, args...); exitCodeForError(err) != 2 {
			t.Errorf("%v: exit %d, want 2 (%v)", args, exitCodeForError(err), err)
		}
	}
	fc := &fakeCloudCaller{errs: map[string]error{procListSessions: &cloud.Error{Code: cloud.CodePermissionDenied}}}
	if _, _, err := obsRunCloud(t, fc, "sessions", "prod"); exitCodeForError(err) != 2 {
		t.Errorf("permission denied: exit %d, want 2 (%v)", exitCodeForError(err), err)
	}
}

// A local ClickStack has no browser tables: the sessions are derived by the
// control plane. That is "could not look" with the remedy, never "the request
// is wrong".
func TestObsSessionsLocalWithoutBrowserTablesSaysSoAndExits2(t *testing.T) {
	missing := obsbackend.Wrong("Table default.rum_sessions does not exist. (UNKNOWN_TABLE)")
	for _, args := range [][]string{{"sessions", "dev", "--json"}, {"session", sessID, "dev", "--json"}} {
		sr := &scriptedRunner{err: missing}
		out, _, err := obsRun(t, sessFacts("dev"), sr, args...)
		if exitCodeForError(err) != 2 {
			t.Fatalf("%v: exit %d, want 2 (%v)", args, exitCodeForError(err), err)
		}
		if msg := decode(t, out)["error"].(string); !strings.Contains(msg, "no browser session tables") || !strings.Contains(msg, "hosted env") {
			t.Errorf("%v: error = %q", args, msg)
		}
	}
}

// Session text is written by whoever holds the public ingest key. Whatever a
// forged session carries, the terminal form must contain no escape sequence.
func TestObsSessionTextStripsTerminalEscapesFromForgedFields(t *testing.T) {
	evilLink := "\x1b]8;;https://evil.example\x07trusted\x1b]8;;\x07"
	entries := []sessEntry{
		{at: 0, kind: "navigation", name: "documentLoad", path: "/" + evilLink},
		{at: time.Second, kind: "click", name: "click", path: "/x", selector: "\x1b[2J\x1b[1;1H//*[@id=\"a\"]", element: "button\x1b]0;pwned\x07"},
		{at: 2 * time.Second, kind: "error", name: "onerror", path: "/x", errType: "TypeError", msg: "boom \x1b[31mred\u009b31m \u202egpj"},
		{at: 3 * time.Second, kind: "event", name: "e", path: "/x", props: map[string]string{"k": "v\x1b]8;;https://evil.example\x1b\\"}},
	}
	sr := &scriptedRunner{answers: localSessionAnswers(entries)}
	text, _, err := obsRun(t, sessFacts("prod"), sr, "session", sessID, "prod")
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"\x1b", "\x07", "\u009b", "\u202e", "evil.example"} {
		if strings.Contains(text, bad) {
			t.Errorf("terminal output carries %q:\n%q", bad, text)
		}
	}
	for _, want := range []string{"trusted", "TypeError: boom", "button"} {
		if !strings.Contains(text, want) {
			t.Errorf("sanitizing must keep the readable text; lacks %q:\n%s", want, text)
		}
	}
}
