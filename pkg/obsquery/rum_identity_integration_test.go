//go:build integration

package obsquery_test

// Who and where, against a real ClickHouse holding the hosted browser tables:
// a session carries the location the platform stamped on its browser resource,
// and a backend trace joined to it by the web runtime's session baggage is
// found by session id and by the authenticated user id, with both columns set.
//
//	go test -tags integration -run 'RumIdentity' ./pkg/obsquery/

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/obsquery"
)

func TestRumIdentitySessionGeoAndTraceLinks(t *testing.T) {
	c := startClickHouse(t)
	ctx := context.Background()
	rumHostedDB(t, c, "ri")
	// The captured sessions, as the gateway stamps them: one located browser.
	located := strings.ReplaceAll(rumFixture(t, "traces.jsonl"), `"rum.sessionId":"`+rumReplaySession+`"`,
		`"rum.sessionId":"`+rumReplaySession+`","geo.country.iso_code":"DE","geo.region.name":"Land Berlin","geo.locality.name":"Berlin"`)
	rumInsert(t, c, "ri", "otel_traces", located)
	rumInsert(t, c, "ri", "otel_logs", rumFixture(t, "logs.jsonl"))

	// A backend trace the browser's baggage reached: the server span carries
	// session.id (from baggage) and user.id (from the authenticated caller).
	server, _ := json.Marshal(map[string]any{
		"Timestamp": "2026-10-19 11:44:09.000000000", "TraceId": "b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0", "SpanId": "b0b0b0b0b0b0b0b1",
		"ParentSpanId": "", "SpanName": "/svc.v1.Svc/Get", "SpanKind": "Server", "ServiceName": "api",
		"ResourceAttributes": map[string]string{"service.name": "api", "reliant.env.id": "env-prod"},
		"SpanAttributes":     map[string]string{"session.id": rumReplaySession, "user.id": "u-auth"},
		"Duration":           1000000, "StatusCode": "Unset",
	})
	rumInsert(t, c, "ri", "otel_traces", string(server))
	run := rumRunner(c)
	sc := rumScopeOf("ri", "env-prod")
	sc.SessionGeo = true

	s, err := obsquery.GetSession(ctx, run, obsquery.SessionLookup{Scope: sc, SessionID: rumReplaySession})
	if err != nil {
		t.Fatal(err)
	}
	if s.Country != "DE" || s.Region != "Land Berlin" || s.City != "Berlin" || s.ClientAddress != "" {
		t.Errorf("located session = %+v", s)
	}
	plain, err := obsquery.GetSession(ctx, run, obsquery.SessionLookup{Scope: sc, SessionID: rumPlainSession})
	if err != nil || plain.Country != "" || plain.City != "" {
		t.Errorf("unlocated session = %+v, %v", plain, err)
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
				svcs                                               []string
			)
			if err := rows.Scan(&id, &start, &dur, &rs, &rn, &rd, &spans, &errs, &tw, &svcs, &src, &user, &sess, &country, &region, &city); err != nil {
				t.Fatal(err)
			}
			out[id] = []string{user, sess, country, city}
		}
		return out
	}
	bySession := traces(obsquery.TraceQuery{SessionID: rumReplaySession})
	if got := bySession["b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0"]; len(got) == 0 || got[0] != "u-auth" || got[1] != rumReplaySession {
		t.Errorf("the backend trace by session = %v (all: %v)", got, bySession)
	}
	for id, v := range bySession {
		if v[1] != rumReplaySession {
			t.Errorf("trace %s of another session matched: %v", id, v)
		}
	}
	locatedTraces := 0
	for _, v := range bySession {
		if v[2] == "DE" && v[3] == "Berlin" {
			locatedTraces++
		}
	}
	if locatedTraces == 0 {
		t.Errorf("no browser trace of the session carries its location: %v", bySession)
	}
	byUser := traces(obsquery.TraceQuery{UserID: "u-auth"})
	if len(byUser) != 1 || byUser["b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0"] == nil {
		t.Errorf("traces of u-auth = %v, want the one backend trace", byUser)
	}
	if n := len(traces(obsquery.TraceQuery{UserID: "user-42"})); n == 0 {
		t.Error("the page-reported user id finds none of its browser traces")
	}
}

// An org whose tables are not yet migrated to the location columns (per-org schema
// v19) is still readable: without Scope.SessionGeo the session reads name none of
// them. With it, the same tables refuse the read, which is why a reader says
// which shape its org's tables have rather than assuming the newest.
func TestRumIdentitySessionReadsSurviveAnUnmigratedOrg(t *testing.T) {
	c := startClickHouse(t)
	ctx := context.Background()
	raw, err := os.ReadFile("testdata/rum/hosted_schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	legacy := string(raw)
	for _, cut := range []string{
		",\n    `Geo` SimpleAggregateFunction(max, Tuple(String, String, String)),\n    `ClientAddress` SimpleAggregateFunction(max, String)",
		"    max((ResourceAttributes['geo.country.iso_code'], ResourceAttributes['geo.region.name'], ResourceAttributes['geo.locality.name'])) AS Geo,\n",
		"    max(ResourceAttributes['client.address']) AS ClientAddress,\n",
	} {
		if !strings.Contains(legacy, cut) {
			t.Fatalf("hosted_schema.sql moved; update this test's cut: %q", cut)
		}
		legacy = strings.Replace(legacy, cut, "", 1)
	}
	for _, stmt := range strings.Split(strings.ReplaceAll(legacy, "{{DB}}", "rl"), ";\n\n") {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if err := c.Exec(ctx, stmt); err != nil {
			t.Fatalf("%v\n%s", err, stmt)
		}
	}
	loadCapture(t, c, "rl")
	run := rumRunner(c)
	sc := rumScopeOf("rl", "env-prod")

	s, err := obsquery.GetSession(ctx, run, obsquery.SessionLookup{Scope: sc, SessionID: rumReplaySession})
	if err != nil || s.UserID != "user-42" || s.Country != "" || s.ClientAddress != "" {
		t.Fatalf("unmigrated session = %+v, %v", s, err)
	}
	if page, err := obsquery.ListSessions(ctx, run, obsquery.SessionQuery{Scope: sc}); err != nil || len(page.Sessions) == 0 {
		t.Fatalf("unmigrated list = %+v, %v", page, err)
	}
	sc.SessionGeo = true
	if _, err := obsquery.GetSession(ctx, run, obsquery.SessionLookup{Scope: sc, SessionID: rumReplaySession}); err == nil {
		t.Error("an unmigrated org answered a read of its missing location columns")
	}
}
