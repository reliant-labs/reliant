// Copyright (c) 2025 Reliant Labs

// Package google_calendar_test drives every action in the embedded
// `google_calendar` manifest through the real declarative runner against an
// httptest fake of the Calendar API v3. The manifest is the one the binary
// ships (catalog.MustBuiltin), with only its base_url pointed at the fake, so
// these tests pin the request each action sends, the output it selects and how
// it classifies failures. Shapes follow Google's Calendar API discovery
// document (Event, Events, CalendarListEntry) and its JSON error model.
package google_calendar_test

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/netguard/netguardtest"
)

const (
	token  = "ya29.fake-calendar-access-token-3a9e"
	prefix = "/calendar/v3"
)

type recorded struct {
	Method string
	Path   string // decoded
	Query  url.Values
	Header http.Header
	Body   map[string]any
}

// fakeCalendar answers with whatever reply is set and records every request.
type fakeCalendar struct {
	t     *testing.T
	srv   *httptest.Server
	mu    sync.Mutex
	got   []recorded
	reply func(w http.ResponseWriter, r recorded)
}

func newFake(t *testing.T) *fakeCalendar {
	t.Helper()
	f := &fakeCalendar{t: t}
	f.srv = netguardtest.NewTLSServer(t, http.HandlerFunc(f.serve))
	return f
}

func (f *fakeCalendar) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	rec := recorded{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Header: r.Header.Clone()}
	if len(raw) > 0 {
		require.NoError(f.t, json.Unmarshal(raw, &rec.Body), "request body must be JSON: %s", raw)
	}
	f.mu.Lock()
	f.got = append(f.got, rec)
	reply := f.reply
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	if reply == nil {
		googleErr(w, 404, "NOT_FOUND", "notFound", "no reply configured")
		return
	}
	reply(w, rec)
}

func (f *fakeCalendar) requests() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorded(nil), f.got...)
}

func (f *fakeCalendar) only() recorded {
	f.t.Helper()
	got := f.requests()
	require.Len(f.t, got, 1, "exactly one request")
	return got[0]
}

func writeJSON(w http.ResponseWriter, v any) { _ = json.NewEncoder(w).Encode(v) }

func googleErr(w http.ResponseWriter, code int, status, reason, message string) {
	w.WriteHeader(code)
	writeJSON(w, map[string]any{"error": map[string]any{
		"code": code, "message": message, "status": status,
		"errors": []any{map[string]any{"reason": reason, "domain": "global", "message": message}},
	}})
}

type staticCred struct{}

func (staticCred) ConnectionID() string      { return "conn_gcal" }
func (staticCred) Params() map[string]string { return nil }
func (staticCred) Scrub(s string) string     { return strings.ReplaceAll(s, token, "[redacted]") }
func (staticCred) Apply(r *http.Request) error {
	r.Header.Set("Authorization", "Bearer "+token)
	return nil
}

type staticSource struct{}

func (staticSource) Credential(context.Context, httpaction.CredentialRequest) (httpaction.Credential, error) {
	return staticCred{}, nil
}

func shipped(t *testing.T) *reliantv1.IntegrationManifest {
	t.Helper()
	m, err := catalog.MustBuiltin().Manifest("google_calendar", 1)
	require.NoError(t, err)
	return m
}

func (f *fakeCalendar) try(id string, params map[string]any) (*httpaction.Result, error) {
	f.t.Helper()
	m := proto.Clone(shipped(f.t)).(*reliantv1.IntegrationManifest)
	m.Connection.BaseUrl = f.srv.URL + prefix
	var action *reliantv1.ActionSpec
	for _, a := range m.GetActions() {
		if a.GetId() == id {
			action = a
		}
	}
	require.NotNil(f.t, action, "google_calendar has no action %s", id)
	pool := x509.NewCertPool()
	pool.AddCert(f.srv.Certificate())
	runner := httpaction.NewRunner(netguardtest.Guard()).WithRootCAs(pool)
	return runner.RunAuthenticated(context.Background(), m, action, params, staticSource{}, httpaction.CallSite{RunID: "run-1", NodeID: "n"})
}

func (f *fakeCalendar) run(id string, params map[string]any) *httpaction.Result {
	f.t.Helper()
	res, err := f.try(id, params)
	require.NoError(f.t, err)
	return res
}

func (f *fakeCalendar) ok(id string, params map[string]any) *httpaction.Result {
	f.t.Helper()
	res := f.run(id, params)
	require.False(f.t, res.IsError, res.Content)
	return res
}

// ---------------------------------------------------------------------------
// The shipped declaration.
// ---------------------------------------------------------------------------

func TestManifestDeclaration(t *testing.T) {
	m := shipped(t)
	oauth := m.GetConnection().GetAuth()[0].GetOauth2()
	assert.Equal(t, []string{
		"https://www.googleapis.com/auth/calendar.events",
		"https://www.googleapis.com/auth/calendar.calendarlist.readonly",
	}, oauth.GetScopes())
	assert.Equal(t, "S256", oauth.GetPkce())
	assert.Equal(t, "offline", oauth.GetAuthorizeParams()["access_type"])
	assert.Equal(t, "consent", oauth.GetAuthorizeParams()["prompt"])
	assert.True(t, strings.HasPrefix(m.GetConnection().GetBaseUrl(), "https://www.googleapis.com/calendar/v3"))

	// The probe must be one calendar.calendarlist.readonly authorises:
	// calendarList.get, not calendars.get.
	assert.Equal(t, "/users/me/calendarList/primary", m.GetConnection().GetProbe().GetPath())
	assert.Equal(t, "response.id", m.GetConnection().GetProbe().GetExternalId())

	mutates := map[string]bool{
		"calendar.list": false, "event.list": false, "event.get": false,
		"event.create": true, "event.update": true, "event.delete": true,
	}
	require.Len(t, m.GetActions(), len(mutates))
	for _, a := range m.GetActions() {
		want, known := mutates[a.GetId()]
		require.True(t, known, "unexpected action %s", a.GetId())
		assert.Equal(t, want, a.GetMutates(), a.GetId())
		assert.Equal(t, "server", a.GetPlacement(), "%s holds a credential, so it runs on the server", a.GetId())
		assert.True(t, a.GetTool().GetExpose(), a.GetId())
		assert.NotEmpty(t, a.GetKeywords(), a.GetId())
		raw, err := json.Marshal(a.GetParams().AsMap())
		require.NoError(t, err)
		assert.Contains(t, string(raw), `"connection"`, "%s takes a connection", a.GetId())
	}
	assert.Empty(t, m.GetTriggers(), "no trigger in this version")
}

// A Calendar action never emails guests unless the caller says so.
func TestSendUpdatesDefaultsToNone(t *testing.T) {
	m := shipped(t)
	for _, a := range m.GetActions() {
		props, _ := a.GetParams().AsMap()["properties"].(map[string]any)
		su, ok := props["send_updates"].(map[string]any)
		if !ok {
			continue
		}
		assert.Equal(t, "none", su["default"], a.GetId())
		assert.ElementsMatch(t, []any{"none", "externalOnly", "all"}, su["enum"], a.GetId())
	}
}

// ---------------------------------------------------------------------------
// Reads.
// ---------------------------------------------------------------------------

func TestCalendarListPaginates(t *testing.T) {
	f := newFake(t)
	f.reply = func(w http.ResponseWriter, r recorded) {
		if r.Query.Get("pageToken") == "" {
			writeJSON(w, map[string]any{"nextPageToken": "p2", "items": []any{
				map[string]any{"id": "me@example.com", "summary": "me@example.com", "primary": true, "accessRole": "owner", "timeZone": "America/New_York"},
			}})
			return
		}
		writeJSON(w, map[string]any{"items": []any{
			map[string]any{"id": "team@group.calendar.google.com", "summary": "Team", "summaryOverride": "Our team", "accessRole": "writer", "description": "Shared"},
		}})
	}
	res := f.ok("calendar.list", map[string]any{"min_access_role": "writer", "page_size": 2.0})

	items, _ := res.Data["items"].([]any)
	require.Len(t, items, 2)
	assert.Equal(t, map[string]any{
		"id": "me@example.com", "summary": "me@example.com", "description": nil,
		"primary": true, "access_role": "owner", "time_zone": "America/New_York",
	}, items[0])
	second, _ := items[1].(map[string]any)
	assert.Equal(t, "Our team", second["summary"], "the account's own name for the calendar wins")
	assert.Equal(t, false, second["primary"])

	reqs := f.requests()
	require.Len(t, reqs, 2)
	assert.Equal(t, "GET", reqs[0].Method)
	assert.Equal(t, prefix+"/users/me/calendarList", reqs[0].Path)
	assert.Equal(t, "Bearer "+token, reqs[0].Header.Get("Authorization"))
	assert.Equal(t, "2", reqs[0].Query.Get("maxResults"))
	assert.Equal(t, "false", reqs[0].Query.Get("showHidden"))
	assert.Equal(t, "writer", reqs[0].Query.Get("minAccessRole"))
	assert.Equal(t, "p2", reqs[1].Query.Get("pageToken"))
	assert.Equal(t, "writer", reqs[1].Query.Get("minAccessRole"), "later pages keep the filter")
}

const eventsJSON = `{"items": [
  {"id": "evt1", "status": "confirmed", "summary": "Quarterly review", "description": "Agenda", "location": "Room 4B",
   "htmlLink": "https://www.google.com/calendar/event?eid=abc", "hangoutLink": "https://meet.google.com/abc-defg-hij",
   "organizer": {"email": "me@example.com", "displayName": "Me", "self": true},
   "start": {"dateTime": "2026-12-01T10:00:00-05:00", "timeZone": "America/New_York"},
   "end": {"dateTime": "2026-12-01T11:00:00-05:00", "timeZone": "America/New_York"},
   "attendees": [{"email": "ann@example.com", "responseStatus": "accepted"}, {"email": "bo@example.com"}]},
  {"id": "evt2", "summary": "Company offsite", "start": {"date": "2026-12-03"}, "end": {"date": "2026-12-05"}}
]}`

func TestEventListRequestAndOutput(t *testing.T) {
	f := newFake(t)
	f.reply = func(w http.ResponseWriter, _ recorded) { _, _ = w.Write([]byte(eventsJSON)) }
	res := f.ok("event.list", map[string]any{
		"time_min": "2026-12-01T00:00:00Z", "time_max": "2026-12-08T00:00:00Z", "q": "review",
	})

	req := f.only()
	assert.Equal(t, "GET", req.Method)
	assert.Equal(t, prefix+"/calendars/primary/events", req.Path, "the calendar defaults to primary")
	assert.Equal(t, "true", req.Query.Get("singleEvents"))
	assert.Equal(t, "startTime", req.Query.Get("orderBy"))
	assert.Equal(t, "100", req.Query.Get("maxResults"))
	assert.Equal(t, "2026-12-01T00:00:00Z", req.Query.Get("timeMin"))
	assert.Equal(t, "2026-12-08T00:00:00Z", req.Query.Get("timeMax"))
	assert.Equal(t, "review", req.Query.Get("q"))

	items, _ := res.Data["items"].([]any)
	require.Len(t, items, 2)
	timed, _ := items[0].(map[string]any)
	assert.Equal(t, "evt1", timed["id"])
	assert.Equal(t, "Quarterly review", timed["summary"])
	assert.Equal(t, "confirmed", timed["status"])
	assert.Equal(t, "Room 4B", timed["location"])
	assert.Equal(t, "https://meet.google.com/abc-defg-hij", timed["hangout_link"])
	assert.Equal(t, map[string]any{"date": nil, "date_time": "2026-12-01T10:00:00-05:00", "time_zone": "America/New_York"}, timed["start"])
	assert.Equal(t, map[string]any{"email": "me@example.com", "display_name": "Me", "self": true}, timed["organizer"])
	assert.Equal(t, []any{
		map[string]any{"email": "ann@example.com", "response_status": "accepted"},
		map[string]any{"email": "bo@example.com", "response_status": "needsAction"},
	}, timed["attendees"], "a guest who has not answered is needsAction")

	allDay, _ := items[1].(map[string]any)
	assert.Equal(t, map[string]any{"date": "2026-12-03", "date_time": nil, "time_zone": nil}, allDay["start"])
	assert.Equal(t, []any{}, allDay["attendees"])
	assert.Nil(t, allDay["hangout_link"])
}

func TestEventListOnAnotherCalendarAndRecurringSeries(t *testing.T) {
	f := newFake(t)
	f.reply = func(w http.ResponseWriter, _ recorded) { writeJSON(w, map[string]any{}) }
	res := f.ok("event.list", map[string]any{"calendar_id": "team@group.calendar.google.com", "single_events": false})
	assert.Empty(t, res.Data["items"], "an empty calendar lists nothing")

	req := f.only()
	assert.Equal(t, prefix+"/calendars/team@group.calendar.google.com/events", req.Path)
	assert.Equal(t, "false", req.Query.Get("singleEvents"))
	assert.NotContains(t, req.Query, "orderBy", "startTime ordering is invalid unless recurring events are expanded")
}

func TestEventListRefusesAnUnzonedTimestampBeforeCallingGoogle(t *testing.T) {
	f := newFake(t)
	for name, params := range map[string]map[string]any{
		"no offset":      {"time_min": "2026-12-01T00:00:00"},
		"not a time":     {"time_max": "tomorrow"},
		"unknown param":  {"calendar": "primary"},
		"bad order":      {"order_by": "created"},
		"page size zero": {"page_size": 0.0},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := f.try("event.list", params)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "invalid params")
		})
	}
	assert.Empty(t, f.requests())
}

func TestEventGet(t *testing.T) {
	f := newFake(t)
	f.reply = func(w http.ResponseWriter, _ recorded) {
		var list struct{ Items []any }
		require.NoError(t, json.Unmarshal([]byte(eventsJSON), &list))
		writeJSON(w, list.Items[0])
	}
	res := f.ok("event.get", map[string]any{"event_id": "evt1", "calendar_id": "me@example.com"})
	assert.Equal(t, "evt1", res.Data["id"])
	assert.Equal(t, "Quarterly review", res.Data["summary"])

	req := f.only()
	assert.Equal(t, "GET", req.Method)
	assert.Equal(t, prefix+"/calendars/me@example.com/events/evt1", req.Path)

	_, err := f.try("event.get", map[string]any{"event_id": "../acl"})
	require.Error(t, err, "an event id cannot walk the path")
	assert.Contains(t, err.Error(), "invalid params")
}

// ---------------------------------------------------------------------------
// Writes.
// ---------------------------------------------------------------------------

func TestEventCreateTimedWithAttendees(t *testing.T) {
	f := newFake(t)
	f.reply = func(w http.ResponseWriter, r recorded) {
		writeJSON(w, map[string]any{
			"id": "new1", "status": "confirmed", "summary": r.Body["summary"], "htmlLink": "https://www.google.com/calendar/event?eid=new1",
			"start": r.Body["start"], "end": r.Body["end"], "attendees": r.Body["attendees"],
		})
	}
	res := f.ok("event.create", map[string]any{
		"summary":     "Quarterly review",
		"start":       map[string]any{"date_time": "2026-12-01T15:00:00", "time_zone": "America/New_York"},
		"end":         map[string]any{"date_time": "2026-12-01T16:00:00", "time_zone": "America/New_York"},
		"description": "Agenda", "location": "Room 4B",
		"attendees": []any{"ann@example.com", "bo@example.com"},
	})

	req := f.only()
	assert.Equal(t, "POST", req.Method)
	assert.Equal(t, prefix+"/calendars/primary/events", req.Path)
	assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
	assert.Equal(t, "none", req.Query.Get("sendUpdates"), "nobody is emailed unless asked")
	assert.NotContains(t, req.Query, "conferenceDataVersion")
	assert.Equal(t, map[string]any{
		"summary":     "Quarterly review",
		"start":       map[string]any{"dateTime": "2026-12-01T15:00:00", "timeZone": "America/New_York"},
		"end":         map[string]any{"dateTime": "2026-12-01T16:00:00", "timeZone": "America/New_York"},
		"description": "Agenda", "location": "Room 4B",
		"attendees": []any{map[string]any{"email": "ann@example.com"}, map[string]any{"email": "bo@example.com"}},
	}, req.Body)

	assert.Equal(t, "new1", res.Data["id"])
	assert.Equal(t, "https://www.google.com/calendar/event?eid=new1", res.Data["html_link"])
}

func TestEventCreateAllDayNotifiesWhenAsked(t *testing.T) {
	f := newFake(t)
	f.reply = func(w http.ResponseWriter, r recorded) {
		writeJSON(w, map[string]any{"id": "new2", "summary": "Offsite"})
	}
	f.ok("event.create", map[string]any{
		"summary": "Offsite", "start": map[string]any{"date": "2026-12-03"}, "end": map[string]any{"date": "2026-12-04"},
		"send_updates": "externalOnly", "calendar_id": "team@group.calendar.google.com",
	})
	req := f.only()
	assert.Equal(t, prefix+"/calendars/team@group.calendar.google.com/events", req.Path)
	assert.Equal(t, "externalOnly", req.Query.Get("sendUpdates"))
	assert.Equal(t, map[string]any{
		"summary": "Offsite", "start": map[string]any{"date": "2026-12-03"}, "end": map[string]any{"date": "2026-12-04"},
	}, req.Body, "an all-day event sends dates only: no dateTime, no empty fields")
}

func TestEventCreateWithGoogleMeet(t *testing.T) {
	f := newFake(t)
	f.reply = func(w http.ResponseWriter, _ recorded) {
		writeJSON(w, map[string]any{"id": "new3", "summary": "Sync", "hangoutLink": "https://meet.google.com/xyz-abcd-efg"})
	}
	res := f.ok("event.create", map[string]any{
		"summary": "Sync", "add_google_meet": true,
		"start": map[string]any{"date_time": "2026-12-02T09:00:00Z"}, "end": map[string]any{"date_time": "2026-12-02T09:30:00Z"},
	})
	req := f.only()
	assert.Equal(t, "1", req.Query.Get("conferenceDataVersion"), "Google ignores conferenceData without version 1")
	conf, _ := req.Body["conferenceData"].(map[string]any)
	create, _ := conf["createRequest"].(map[string]any)
	require.NotNil(t, create, "body: %v", req.Body)
	assert.Equal(t, map[string]any{"type": "hangoutsMeet"}, create["conferenceSolutionKey"])
	assert.NotEmpty(t, create["requestId"])
	assert.Equal(t, "https://meet.google.com/xyz-abcd-efg", res.Data["hangout_link"])
}

func TestEventCreateRefusesBadParamsBeforeCallingGoogle(t *testing.T) {
	f := newFake(t)
	when := map[string]any{"date_time": "2026-12-01T15:00:00Z"}
	for name, params := range map[string]map[string]any{
		"no title":            {"start": when, "end": when},
		"no end":              {"summary": "x", "start": when},
		"bad notify mode":     {"summary": "x", "start": when, "end": when, "send_updates": "everyone"},
		"attendee as object":  {"summary": "x", "start": when, "end": when, "attendees": []any{map[string]any{"email": "a@x.com"}}},
		"bad all-day date":    {"summary": "x", "start": map[string]any{"date": "12/01/2026"}, "end": when},
		"unknown start field": {"summary": "x", "start": map[string]any{"when": "now"}, "end": when},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := f.try("event.create", params)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "invalid params")
		})
	}
	assert.Empty(t, f.requests(), "nothing was created")
}

// An update is a patch: only what the caller names is sent, so an unnamed
// field is never blanked.
func TestEventUpdateIsAPatch(t *testing.T) {
	f := newFake(t)
	f.reply = func(w http.ResponseWriter, _ recorded) {
		writeJSON(w, map[string]any{"id": "evt1", "summary": "Renamed"})
	}
	res := f.ok("event.update", map[string]any{"event_id": "evt1", "summary": "Renamed"})

	req := f.only()
	assert.Equal(t, "PATCH", req.Method)
	assert.Equal(t, prefix+"/calendars/primary/events/evt1", req.Path)
	assert.Equal(t, "none", req.Query.Get("sendUpdates"))
	assert.Equal(t, map[string]any{"summary": "Renamed"}, req.Body)
	assert.Equal(t, "Renamed", res.Data["summary"])
}

func TestEventUpdateMovesAndReplacesGuests(t *testing.T) {
	f := newFake(t)
	f.reply = func(w http.ResponseWriter, _ recorded) { writeJSON(w, map[string]any{"id": "evt1"}) }
	f.ok("event.update", map[string]any{
		"event_id": "evt1", "send_updates": "all", "description": "", "attendees": []any{"ann@example.com"},
		"start": map[string]any{"date_time": "2026-12-02T10:00:00Z"}, "end": map[string]any{"date_time": "2026-12-02T11:00:00Z"},
	})
	req := f.only()
	assert.Equal(t, "all", req.Query.Get("sendUpdates"))
	assert.Equal(t, map[string]any{
		"description": "",
		"start":       map[string]any{"dateTime": "2026-12-02T10:00:00Z"},
		"end":         map[string]any{"dateTime": "2026-12-02T11:00:00Z"},
		"attendees":   []any{map[string]any{"email": "ann@example.com"}},
	}, req.Body, "an empty description clears it; an unnamed summary is left out")
}

func TestEventDelete(t *testing.T) {
	f := newFake(t)
	f.reply = func(w http.ResponseWriter, _ recorded) { w.WriteHeader(http.StatusNoContent) }
	res := f.ok("event.delete", map[string]any{"event_id": "evt1"})

	req := f.only()
	assert.Equal(t, "DELETE", req.Method)
	assert.Equal(t, prefix+"/calendars/primary/events/evt1", req.Path)
	assert.Equal(t, "none", req.Query.Get("sendUpdates"), "guests are not told unless asked")
	assert.Equal(t, map[string]any{"deleted": true, "calendar_id": "primary", "event_id": "evt1"}, res.Data)
}

func TestEventDeleteAlreadyGoneIsNotRetried(t *testing.T) {
	f := newFake(t)
	f.reply = func(w http.ResponseWriter, _ recorded) {
		googleErr(w, 410, "FAILED_PRECONDITION", "deleted", "Resource has been deleted")
	}
	res := f.run("event.delete", map[string]any{"event_id": "evt1"})
	assert.True(t, res.IsError)
	assert.Equal(t, 410, res.StatusCode)
	assert.False(t, res.Retryable)
	assert.Contains(t, res.Content, "gone")
	assert.Len(t, f.requests(), 1)
}

// ---------------------------------------------------------------------------
// Errors: Google's error model, classified once for every action.
// ---------------------------------------------------------------------------

func TestErrorsAreClassifiedForEveryAction(t *testing.T) {
	when := map[string]any{"date_time": "2026-12-01T15:00:00Z"}
	calls := map[string]map[string]any{
		"calendar.list": {},
		"event.list":    {},
		"event.get":     {"event_id": "evt1"},
		"event.create":  {"summary": "x", "start": when, "end": when},
		"event.update":  {"event_id": "evt1", "summary": "y"},
		"event.delete":  {"event_id": "evt1"},
	}
	cases := []struct {
		name      string
		status    int
		reason    string
		retryable bool
		contains  string
	}{
		{"rate limited", 403, "userRateLimitExceeded", true, "rate limit"},
		{"scope missing", 403, "insufficientPermissions", false, "reconnect Google Calendar and allow every permission"},
		{"forbidden", 403, "forbiddenForNonOrganizer", false, "refused"},
		{"too many", 429, "rateLimitExceeded", true, "rate limit"},
		{"bad request", 400, "invalid", false, "invalid"},
		{"not found", 404, "notFound", false, "not found"},
		{"backend", 503, "backendError", true, ""},
	}
	for _, tc := range cases {
		for action, params := range calls {
			t.Run(tc.name+"/"+action, func(t *testing.T) {
				f := newFake(t)
				f.reply = func(w http.ResponseWriter, _ recorded) {
					googleErr(w, tc.status, "X", tc.reason, "upstream said no ("+token+")")
				}
				res := f.run(action, params)
				assert.True(t, res.IsError)
				assert.Equal(t, tc.status, res.StatusCode)
				assert.Equal(t, tc.retryable, res.Retryable, res.Content)
				if tc.contains != "" {
					assert.Contains(t, res.Content, tc.contains)
				}
				assert.NotContains(t, res.Content, token, "the credential is scrubbed from an echoing error")
			})
		}
	}
}

func TestUnauthorizedSaysReconnect(t *testing.T) {
	f := newFake(t)
	f.reply = func(w http.ResponseWriter, _ recorded) {
		googleErr(w, 401, "UNAUTHENTICATED", "authError", "Invalid Credentials")
	}
	res := f.run("event.list", map[string]any{})
	assert.True(t, res.IsError)
	assert.False(t, res.Retryable)
	assert.Equal(t, 401, res.StatusCode)
	assert.Contains(t, res.Content, "reconnect Google Calendar")
}
