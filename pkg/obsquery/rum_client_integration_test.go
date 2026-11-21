//go:build integration

package obsquery_test

// Which client and which builds, against a real ClickHouse holding the hosted
// browser tables (control-plane tenant schema v21): a session carries the client
// the platform stamped on its browser resource and the viewport its page
// reported, filters find it by browser, OS, device type and build, and a trace
// names the frontend build that made the call and the backend build that served
// it, and is found by either.
//
//	go test -tags integration -run 'RumClient' ./pkg/obsquery/

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/obsquery"
)

func TestRumClientSessionAndTraceBuilds(t *testing.T) {
	c := startClickHouse(t)
	ctx := context.Background()
	rumHostedDB(t, c, "rc")
	// The captured sessions, as the gateway stamps a desktop app's renderer:
	// the client from its User-Agent, the desktop versions from the page.
	body := strings.ReplaceAll(rumFixture(t, "traces.jsonl"), `"rum.sessionId":"`+rumReplaySession+`"`,
		`"rum.sessionId":"`+rumReplaySession+`","user_agent.name":"Electron","user_agent.version":"41.10.7","os.name":"macOS",`+
			`"os.version":"15.1.0","reliant.device.type":"desktop","reliant.chromium.version":"146.0.7680.216",`+
			`"app.desktop.version":"1.8.16","app.desktop.electron.version":"41.0.0"`)
	body = strings.ReplaceAll(body, `"SpanAttributes":{`,
		`"SpanAttributes":{"app.viewport.width":"1440","app.viewport.height":"900","app.screen.width":"2560","app.screen.height":"1440",`)
	rumInsert(t, c, "rc", "otel_traces", body)

	// One browser trace of the session, and the server span that served it.
	const trace = "c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0"
	for _, span := range []map[string]any{
		{
			"Timestamp": "2026-10-19 11:44:08.000000000", "TraceId": trace, "SpanId": "c0c0c0c0c0c0c0c1", "ParentSpanId": "",
			"SpanName": "POST /svc.v1.Svc/Get", "SpanKind": "Client", "ServiceName": "reliant-web",
			"ResourceAttributes": map[string]string{"service.name": "reliant-web", "service.version": "1.8.16-dev+7d787d80",
				"reliant.env.id": "env-prod", "rum.sessionId": rumReplaySession, "user_agent.name": "Electron",
				"user_agent.version": "41.10.7", "os.name": "macOS", "os.version": "15.1.0", "reliant.device.type": "desktop",
				"app.desktop.version": "1.8.16", "reliant.ingest.class": "browser"},
			"SpanAttributes": map[string]string{}, "Duration": 3000000, "StatusCode": "Unset",
		},
		{
			"Timestamp": "2026-10-19 11:44:08.001000000", "TraceId": trace, "SpanId": "c0c0c0c0c0c0c0c2", "ParentSpanId": "c0c0c0c0c0c0c0c1",
			"SpanName": "/svc.v1.Svc/Get", "SpanKind": "Server", "ServiceName": "api",
			"ResourceAttributes": map[string]string{"service.name": "api", "service.version": "v1.7.91", "reliant.env.id": "env-prod"},
			"SpanAttributes":     map[string]string{"session.id": rumReplaySession}, "Duration": 1000000, "StatusCode": "Unset",
		},
	} {
		raw, _ := json.Marshal(span)
		rumInsert(t, c, "rc", "otel_traces", string(raw))
	}
	run := rumRunner(c)
	sc := rumScopeOf("rc", "env-prod")
	sc.SessionClient = true

	s, err := obsquery.GetSession(ctx, run, obsquery.SessionLookup{Scope: sc, SessionID: rumReplaySession})
	if err != nil {
		t.Fatal(err)
	}
	want := obsquery.ClientInfo{BrowserName: "Electron", BrowserVersion: "41.10.7", OSName: "macOS", OSVersion: "15.1.0",
		DeviceType: "desktop", ChromiumVersion: "146.0.7680.216", DesktopVersion: "1.8.16", ElectronVersion: "41.10.7",
		ViewportWidth: 1440, ViewportHeight: 900, ScreenWidth: 2560, ScreenHeight: 1440}
	if s.Client != want {
		t.Errorf("session client = %+v\nwant %+v", s.Client, want)
	}
	plain, err := obsquery.GetSession(ctx, run, obsquery.SessionLookup{Scope: sc, SessionID: rumPlainSession})
	if err != nil || plain.Client.BrowserName != "" {
		t.Errorf("an undescribed session = %+v, %v", plain.Client, err)
	}

	list := func(q obsquery.SessionQuery) []string {
		t.Helper()
		q.Scope = sc
		page, err := obsquery.ListSessions(ctx, run, q)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, s := range page.Sessions {
			ids = append(ids, s.SessionID)
		}
		return ids
	}
	for name, q := range map[string]obsquery.SessionQuery{
		"browser": {Browser: "Electron"}, "os": {OS: "macOS"}, "device": {DeviceType: "desktop"},
	} {
		if got := list(q); len(got) != 1 || got[0] != rumReplaySession {
			t.Errorf("sessions by %s = %v, want only %s", name, got, rumReplaySession)
		}
	}
	if got := list(obsquery.SessionQuery{Browser: "Firefox"}); len(got) != 0 {
		t.Errorf("sessions of Firefox = %v, want none", got)
	}

	traces := func(q obsquery.TraceQuery) map[string][]string {
		t.Helper()
		q.Scope, q.Limit = sc, 100
		st, err := q.Compile()
		if err != nil {
			t.Fatal(err)
		}
		rows, err := run(ctx, st)
		if err != nil {
			t.Fatalf("%v\n%s", err, st.SQL)
		}
		defer rows.Close()
		out := map[string][]string{}
		for rows.Next() {
			var (
				id, rs, rn, src, user, sess, country, region, city string
				start                                              time.Time
				dur, rd, tw                                        float64
				spans, errs                                        uint64
				svcs, bsvcs, bvers                                 []string
				cn, cv, on, ov, dt, dv, fv                         string
			)
			if err := rows.Scan(&id, &start, &dur, &rs, &rn, &rd, &spans, &errs, &tw, &svcs, &src, &user, &sess, &country, &region, &city,
				&cn, &cv, &on, &ov, &dt, &dv, &fv, &bsvcs, &bvers); err != nil {
				t.Fatal(err)
			}
			out[id] = []string{cn, cv, on, dt, dv, fv, strings.Join(bsvcs, ","), strings.Join(bvers, ",")}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	all := traces(obsquery.TraceQuery{})
	if got := strings.Join(all[trace], "|"); got != "Electron|41.10.7|macOS|desktop|1.8.16|1.8.16-dev+7d787d80|api|v1.7.91" {
		t.Errorf("trace client|version|os|device|desktop|frontend|backends|versions = %q", got)
	}
	for name, q := range map[string]obsquery.TraceQuery{
		"frontend": {FrontendVersion: "1.8.16-dev+7d787d80"}, "backend": {BackendVersion: "v1.7.91"},
	} {
		if got := traces(q); len(got) != 1 || got[trace] == nil {
			t.Errorf("traces by %s build = %v, want only %s", name, got, trace)
		}
	}
	if got := traces(obsquery.TraceQuery{Browser: "Electron", OS: "macOS"}); got[trace] == nil {
		t.Errorf("traces of Electron on macOS do not include %s: %v", trace, got)
	}
	if got := traces(obsquery.TraceQuery{BackendVersion: "1.8.16-dev+7d787d80"}); len(got) != 0 {
		t.Errorf("a frontend build matched as a backend one: %v", got)
	}
}
