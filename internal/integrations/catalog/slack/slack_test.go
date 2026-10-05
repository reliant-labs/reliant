// Copyright (c) 2025 Reliant Labs

// Package slack_test drives every action in the embedded `slack` manifest
// through the real declarative runner against an httptest fake of the Slack
// Web API. The manifest is the one the binary ships (catalog.MustBuiltin),
// with only its base_url pointed at the fake, so these tests pin the request
// each action sends, the output it selects, and how it classifies failures —
// including Slack's habit of reporting them as HTTP 200 with ok:false.
//
// Request and response shapes come from Slack's method reference
// (docs.slack.dev/reference/methods).
package slack_test

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/netguard"
)

const token = "xoxb-fake-bot-token-5be1"

type recorded struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
	Body   map[string]any
}

// fakeSlack answers each "METHOD /api/<method>" with a canned handler and
// records every request.
type fakeSlack struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	got    []recorded
	routes map[string]http.HandlerFunc
}

func newFake(t *testing.T) *fakeSlack {
	t.Helper()
	f := &fakeSlack{t: t, routes: map[string]http.HandlerFunc{}}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeSlack) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	rec := recorded{Method: r.Method, Path: r.URL.EscapedPath(), Query: r.URL.Query(), Header: r.Header.Clone()}
	if len(raw) > 0 {
		require.NoError(f.t, json.Unmarshal(raw, &rec.Body), "request body must be JSON: %s", raw)
	}
	f.mu.Lock()
	f.got = append(f.got, rec)
	h, ok := f.routes[r.Method+" "+r.URL.Path]
	f.mu.Unlock()
	if !ok {
		// Slack answers an unknown method with 200 and ok:false.
		slackReply(`{"ok":false,"error":"unknown_method"}`)(w, r)
		return
	}
	h(w, r)
}

func (f *fakeSlack) on(method, path string, h http.HandlerFunc) { f.routes[method+" /api"+path] = h }

func slackReply(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(body))
	}
}

func (f *fakeSlack) requests() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorded(nil), f.got...)
}

func (f *fakeSlack) only() recorded {
	f.t.Helper()
	got := f.requests()
	require.Len(f.t, got, 1, "exactly one request")
	return got[0]
}

type staticCred struct{}

func (staticCred) ConnectionID() string      { return "conn_slack" }
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

// action resolves slack/<id>@1 from the embedded catalog and points a clone
// of its manifest at the fake: the shipped declaration, a different host.
func (f *fakeSlack) action(id string) (*reliantv1.IntegrationManifest, *reliantv1.ActionSpec) {
	f.t.Helper()
	resolved, err := catalog.MustBuiltin().Resolve("slack/" + id + "@1")
	require.NoError(f.t, err)
	m := proto.Clone(resolved.Manifest).(*reliantv1.IntegrationManifest)
	m.Connection.BaseUrl = f.srv.URL + "/api"
	for _, a := range m.GetActions() {
		if a.GetId() == id {
			return m, a
		}
	}
	f.t.Fatalf("action %s vanished from the clone", id)
	return nil, nil
}

func (f *fakeSlack) runner() *httpaction.Runner {
	pool := x509.NewCertPool()
	pool.AddCert(f.srv.Certificate())
	g := netguard.New()
	g.AllowLoopback = true
	return httpaction.NewRunner(g).WithRootCAs(pool)
}

func (f *fakeSlack) run(id string, params map[string]any) *httpaction.Result {
	f.t.Helper()
	res, err := f.try(id, params)
	require.NoError(f.t, err)
	return res
}

func (f *fakeSlack) try(id string, params map[string]any) (*httpaction.Result, error) {
	m, a := f.action(id)
	return f.runner().RunAuthenticated(context.Background(), m, a, params, staticSource{}, httpaction.CallSite{RunID: "run-1", NodeID: "n"})
}

func assertAuth(t *testing.T, r recorded) {
	t.Helper()
	assert.Equal(t, "Bearer "+token, r.Header.Get("Authorization"))
	assert.NotEmpty(t, r.Header.Get("User-Agent"))
}

// assertJSONPost checks a write method's transport: Slack reads a JSON body
// only with an explicit charset, and otherwise warns or misparses.
func assertJSONPost(t *testing.T, r recorded, path string) {
	t.Helper()
	assert.Equal(t, "POST", r.Method)
	assert.Equal(t, "/api"+path, r.Path)
	assert.Equal(t, "application/json; charset=utf-8", r.Header.Get("Content-Type"))
	assertAuth(t, r)
}

func data(t *testing.T, res *httpaction.Result) map[string]any {
	t.Helper()
	require.False(t, res.IsError, "unexpected error result: %s", res.Content)
	return res.Data
}

// ---------------------------------------------------------------------------
// Request shape and output mapping, one test per action.
// ---------------------------------------------------------------------------

const postedJSON = `{"ok": true, "channel": "C0GENERAL", "ts": "1700000000.000100",
  "message": {"type": "message", "subtype": "bot_message", "text": "Deploy finished", "ts": "1700000000.000100",
              "bot_id": "B0BOT", "user": "U0BOT"},
  "warning": "missing_charset", "response_metadata": {"warnings": ["missing_charset"]}}`

func TestMessagePost(t *testing.T) {
	f := newFake(t)
	f.on("POST", "/chat.postMessage", slackReply(postedJSON))
	blocks := []any{map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": "*Deploy* finished"}}}
	out := data(t, f.run("message.post", map[string]any{
		"channel": "C0GENERAL", "text": "Deploy finished", "blocks": blocks, "unfurl_links": false,
	}))

	req := f.only()
	assertJSONPost(t, req, "/chat.postMessage")
	assert.Equal(t, map[string]any{
		"channel": "C0GENERAL", "text": "Deploy finished", "blocks": blocks, "unfurl_links": false,
	}, req.Body)
	assert.Equal(t, map[string]any{
		"channel": "C0GENERAL", "ts": "1700000000.000100", "thread_ts": nil, "text": "Deploy finished",
	}, out)
}

// Only what was given is sent: no null thread_ts that Slack would reject.
func TestMessagePostSendsOnlyWhatWasGiven(t *testing.T) {
	f := newFake(t)
	f.on("POST", "/chat.postMessage", slackReply(postedJSON))
	f.run("message.post", map[string]any{"channel": "C0GENERAL", "text": "hi"})
	assert.Equal(t, map[string]any{"channel": "C0GENERAL", "text": "hi"}, f.only().Body)
}

func TestMessagePostIntoAThread(t *testing.T) {
	f := newFake(t)
	f.on("POST", "/chat.postMessage", slackReply(`{"ok": true, "channel": "C0GENERAL", "ts": "1700000001.000200",
	  "message": {"text": "on it", "ts": "1700000001.000200", "thread_ts": "1700000000.000100"}}`))
	out := data(t, f.run("message.post", map[string]any{"channel": "C0GENERAL", "text": "on it", "thread_ts": "1700000000.000100"}))
	assert.Equal(t, map[string]any{"channel": "C0GENERAL", "text": "on it", "thread_ts": "1700000000.000100"}, f.only().Body)
	assert.Equal(t, "1700000000.000100", out["thread_ts"])
}

// Channels are ids, never #names; a bad id is refused before any request.
func TestChannelMustBeAConversationID(t *testing.T) {
	f := newFake(t)
	for _, bad := range []string{"#general", "general", "C", "c0lower", "C0/../x"} {
		_, err := f.try("message.post", map[string]any{"channel": bad, "text": "x"})
		assert.Error(t, err, "channel %q", bad)
	}
	assert.Empty(t, f.requests())
}

func TestMessageReply(t *testing.T) {
	f := newFake(t)
	f.on("POST", "/chat.postMessage", slackReply(`{"ok": true, "channel": "C0GENERAL", "ts": "1700000002.000300",
	  "message": {"text": "Looking now", "ts": "1700000002.000300", "thread_ts": "1700000000.000100"}}`))
	out := data(t, f.run("message.reply", map[string]any{
		"channel": "C0GENERAL", "thread_ts": "1700000000.000100", "text": "Looking now", "reply_broadcast": true,
	}))
	req := f.only()
	assertJSONPost(t, req, "/chat.postMessage")
	assert.Equal(t, map[string]any{
		"channel": "C0GENERAL", "thread_ts": "1700000000.000100", "text": "Looking now", "reply_broadcast": true,
	}, req.Body)
	assert.Equal(t, map[string]any{
		"channel": "C0GENERAL", "ts": "1700000002.000300", "thread_ts": "1700000000.000100", "text": "Looking now",
	}, out)

	_, err := f.try("message.reply", map[string]any{"channel": "C0GENERAL", "text": "no thread"})
	assert.Error(t, err, "a reply needs the thread it replies in")
}

func TestMessageUpdate(t *testing.T) {
	f := newFake(t)
	f.on("POST", "/chat.update", slackReply(`{"ok": true, "channel": "C0GENERAL", "ts": "1700000000.000100",
	  "text": "Deploy finished (v1.2)", "message": {"text": "Deploy finished (v1.2)"}}`))
	out := data(t, f.run("message.update", map[string]any{
		"channel": "C0GENERAL", "ts": "1700000000.000100", "text": "Deploy finished (v1.2)",
	}))
	req := f.only()
	assertJSONPost(t, req, "/chat.update")
	assert.Equal(t, map[string]any{"channel": "C0GENERAL", "ts": "1700000000.000100", "text": "Deploy finished (v1.2)"}, req.Body)
	assert.Equal(t, map[string]any{"channel": "C0GENERAL", "ts": "1700000000.000100", "text": "Deploy finished (v1.2)"}, out)
}

func TestReactionAdd(t *testing.T) {
	f := newFake(t)
	f.on("POST", "/reactions.add", slackReply(`{"ok": true}`))
	out := data(t, f.run("reaction.add", map[string]any{
		"channel": "C0GENERAL", "timestamp": "1700000000.000100", "name": "white_check_mark",
	}))
	req := f.only()
	assertJSONPost(t, req, "/reactions.add")
	assert.Equal(t, map[string]any{"channel": "C0GENERAL", "timestamp": "1700000000.000100", "name": "white_check_mark"}, req.Body)
	assert.Equal(t, map[string]any{"ok": true}, out)

	_, err := f.try("reaction.add", map[string]any{"channel": "C0GENERAL", "timestamp": "1700000000.000100", "name": ":tada:"})
	assert.Error(t, err, "emoji names are given without colons")
}

func TestReactionAddAlreadyReactedIsAPermanentError(t *testing.T) {
	f := newFake(t)
	f.on("POST", "/reactions.add", slackReply(`{"ok": false, "error": "already_reacted"}`))
	res := f.run("reaction.add", map[string]any{"channel": "C0GENERAL", "timestamp": "1700000000.000100", "name": "eyes"})
	assert.True(t, res.IsError)
	assert.False(t, res.Retryable)
	assert.Contains(t, res.Content, "already_reacted")
}

func TestUserLookupByEmail(t *testing.T) {
	f := newFake(t)
	f.on("GET", "/users.lookupByEmail", slackReply(`{"ok": true, "user": {
	  "id": "U0ADA", "team_id": "T0ACME", "name": "ada", "deleted": false, "real_name": "Ada Lovelace",
	  "tz": "Europe/London", "is_bot": false,
	  "profile": {"real_name": "Ada Lovelace", "display_name": "ada", "email": "ada@acme.example"}}}`))
	out := data(t, f.run("user.lookup_by_email", map[string]any{"email": "ada@acme.example"}))
	req := f.only()
	assert.Equal(t, "GET", req.Method)
	assert.Equal(t, "/api/users.lookupByEmail", req.Path)
	assert.Equal(t, "ada@acme.example", req.Query.Get("email"))
	assertAuth(t, req)
	assert.Equal(t, map[string]any{
		"id": "U0ADA", "team_id": "T0ACME", "name": "ada", "real_name": "Ada Lovelace", "display_name": "ada",
		"email": "ada@acme.example", "is_bot": false, "deleted": false, "tz": "Europe/London",
	}, out)
}

func TestUserLookupByEmailNotFound(t *testing.T) {
	f := newFake(t)
	f.on("GET", "/users.lookupByEmail", slackReply(`{"ok": false, "error": "users_not_found"}`))
	res := f.run("user.lookup_by_email", map[string]any{"email": "nobody@acme.example"})
	assert.True(t, res.IsError)
	assert.False(t, res.Retryable)
	assert.Contains(t, res.Content, "users_not_found")
}

func channel(id, name string, member bool) string {
	b, _ := json.Marshal(map[string]any{
		"id": id, "name": name, "is_channel": true, "is_private": strings.HasPrefix(id, "G"),
		"is_archived": false, "is_member": member, "num_members": 4, "topic": map[string]any{"value": "about " + name},
	})
	return string(b)
}

func TestConversationsListPaginatesByCursor(t *testing.T) {
	f := newFake(t)
	f.on("GET", "/conversations.list", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("cursor") == "dGVhbTpDMDYxRkE1UEI=" {
			slackReply(`{"ok": true, "channels": [`+channel("G0SECRET", "secret", true)+`],
			  "response_metadata": {"next_cursor": ""}}`)(w, r)
			return
		}
		slackReply(`{"ok": true, "channels": [`+channel("C0GENERAL", "general", true)+`,`+channel("C0RANDOM", "random", false)+`],
		  "response_metadata": {"next_cursor": "dGVhbTpDMDYxRkE1UEI="}}`)(w, r)
	})
	out := data(t, f.run("conversations.list", map[string]any{}))

	reqs := f.requests()
	require.Len(t, reqs, 2, "follows next_cursor once, stops on an empty cursor")
	assert.Equal(t, "/api/conversations.list", reqs[0].Path)
	assert.Equal(t, url.Values{"limit": {"200"}, "types": {"public_channel,private_channel"}, "exclude_archived": {"true"}}, reqs[0].Query)
	assert.Equal(t, "dGVhbTpDMDYxRkE1UEI=", reqs[1].Query.Get("cursor"))
	assert.Equal(t, "public_channel,private_channel", reqs[1].Query.Get("types"), "the filter is kept on later pages")
	for _, r := range reqs {
		assertAuth(t, r)
	}

	items, _ := out["items"].([]any)
	require.Len(t, items, 3, "items from both pages, in order")
	assert.Equal(t, map[string]any{
		"id": "C0GENERAL", "name": "general", "is_private": false, "is_member": true, "is_archived": false,
		"num_members": 4.0, "topic": "about general",
	}, items[0])
	assert.Equal(t, false, items[1].(map[string]any)["is_member"])
	assert.Equal(t, map[string]any{
		"id": "G0SECRET", "name": "secret", "is_private": true, "is_member": true, "is_archived": false,
		"num_members": 4.0, "topic": "about secret",
	}, items[2])
}

func TestConversationsListFilters(t *testing.T) {
	f := newFake(t)
	f.on("GET", "/conversations.list", slackReply(`{"ok": true, "channels": [], "response_metadata": {"next_cursor": ""}}`))
	f.run("conversations.list", map[string]any{"types": "im,mpim", "exclude_archived": false})
	assert.Equal(t, url.Values{"limit": {"200"}, "types": {"im,mpim"}, "exclude_archived": {"false"}}, f.only().Query)

	_, err := f.try("conversations.list", map[string]any{"types": "public_channel,everything"})
	assert.Error(t, err, "only Slack's four conversation types are accepted")
}

func TestConversationsHistoryPaginatesByCursor(t *testing.T) {
	f := newFake(t)
	f.on("GET", "/conversations.history", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("cursor") == "bmV4dF90czoxNjk5" {
			slackReply(`{"ok": true, "messages": [
			  {"type": "message", "user": "U0ADA", "text": "first", "ts": "1699999990.000001"}],
			  "has_more": false, "response_metadata": {"next_cursor": ""}}`)(w, r)
			return
		}
		slackReply(`{"ok": true, "messages": [
		  {"type": "message", "user": "U0BOB", "text": "third", "ts": "1700000000.000100", "thread_ts": "1700000000.000100", "reply_count": 2},
		  {"type": "message", "subtype": "bot_message", "bot_id": "B0BOT", "text": "second", "ts": "1699999995.000001"}],
		  "has_more": true, "pin_count": 0, "response_metadata": {"next_cursor": "bmV4dF90czoxNjk5"}}`)(w, r)
	})
	out := data(t, f.run("conversations.history", map[string]any{"channel": "C0GENERAL", "oldest": "1699990000", "limit": 2}))

	reqs := f.requests()
	require.Len(t, reqs, 2)
	assert.Equal(t, url.Values{"channel": {"C0GENERAL"}, "limit": {"2"}, "oldest": {"1699990000"}}, reqs[0].Query)
	assert.Equal(t, "bmV4dF90czoxNjk5", reqs[1].Query.Get("cursor"))
	assert.Equal(t, "C0GENERAL", reqs[1].Query.Get("channel"))

	items, _ := out["items"].([]any)
	require.Len(t, items, 3)
	assert.Equal(t, map[string]any{
		"ts": "1700000000.000100", "user": "U0BOB", "bot_id": nil, "subtype": nil, "text": "third",
		"thread_ts": "1700000000.000100", "reply_count": 2.0,
	}, items[0])
	assert.Equal(t, map[string]any{
		"ts": "1699999995.000001", "user": nil, "bot_id": "B0BOT", "subtype": "bot_message", "text": "second",
		"thread_ts": nil, "reply_count": 0.0,
	}, items[1])
	assert.Equal(t, "first", items[2].(map[string]any)["text"])
}

func TestConversationsHistoryDefaultsAndOmissions(t *testing.T) {
	f := newFake(t)
	f.on("GET", "/conversations.history", slackReply(`{"ok": true, "messages": [], "response_metadata": {"next_cursor": ""}}`))
	f.run("conversations.history", map[string]any{"channel": "C0GENERAL"})
	assert.Equal(t, url.Values{"channel": {"C0GENERAL"}, "limit": {"100"}}, f.only().Query, "no oldest/latest unless given")
}

// A later page that fails stops the walk as an error, not a short list.
func TestPaginationStopsOnAFailedPage(t *testing.T) {
	f := newFake(t)
	f.on("GET", "/conversations.list", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("cursor") != "" {
			slackReply(`{"ok": false, "error": "invalid_cursor"}`)(w, r)
			return
		}
		slackReply(`{"ok": true, "channels": [`+channel("C0GENERAL", "general", true)+`], "response_metadata": {"next_cursor": "c2"}}`)(w, r)
	})
	res := f.run("conversations.list", map[string]any{})
	assert.True(t, res.IsError)
	assert.Contains(t, res.Content, "invalid_cursor")
}

// ---------------------------------------------------------------------------
// Error mapping, for every action.
// ---------------------------------------------------------------------------

// minimal is the smallest valid params for each action, used to drive every
// error response through all of them.
var minimal = map[string]map[string]any{
	"message.post":          {"channel": "C0GENERAL", "text": "t"},
	"message.reply":         {"channel": "C0GENERAL", "thread_ts": "1700000000.000100", "text": "t"},
	"message.update":        {"channel": "C0GENERAL", "ts": "1700000000.000100", "text": "t"},
	"reaction.add":          {"channel": "C0GENERAL", "timestamp": "1700000000.000100", "name": "eyes"},
	"user.lookup_by_email":  {"email": "ada@acme.example"},
	"conversations.list":    {},
	"conversations.history": {"channel": "C0GENERAL"},
}

// Adding an action without extending `minimal` (and so the error tests)
// fails here.
func TestEveryActionIsCovered(t *testing.T) {
	resolved, err := catalog.MustBuiltin().Resolve("slack/message.post@1")
	require.NoError(t, err)
	var ids, want []string
	for _, a := range resolved.Manifest.GetActions() {
		ids = append(ids, a.GetId())
	}
	for id := range minimal {
		want = append(want, id)
	}
	sort.Strings(ids)
	sort.Strings(want)
	assert.Equal(t, want, ids)
}

type errorCase struct {
	name      string
	status    int
	headers   map[string]string
	body      string
	retryable bool
	contains  []string
}

var errorCases = []errorCase{
	{
		name: "ok:false ratelimited", status: 200, headers: map[string]string{"Retry-After": "12"},
		body: `{"ok": false, "error": "ratelimited"}`, retryable: true, contains: []string{"rate limit", "12 seconds"},
	},
	{
		name: "429 with Retry-After", status: 429, headers: map[string]string{"Retry-After": "30"},
		body: `{"ok": false, "error": "ratelimited"}`, retryable: true, contains: []string{"rate limit", "30 seconds"},
	},
	{
		name: "not_in_channel", status: 200, body: `{"ok": false, "error": "not_in_channel"}`,
		contains: []string{"not_in_channel", "/invite"},
	},
	{
		name: "channel_not_found", status: 200, body: `{"ok": false, "error": "channel_not_found"}`,
		contains: []string{"channel_not_found", "conversation ID"},
	},
	{
		name: "invalid_auth", status: 200, body: `{"ok": false, "error": "invalid_auth"}`,
		contains: []string{"invalid_auth", "reconnect Slack"},
	},
	{
		name: "token_revoked", status: 200, body: `{"ok": false, "error": "token_revoked"}`,
		contains: []string{"token_revoked", "reconnect Slack"},
	},
	{
		name: "missing_scope", status: 200,
		body:     `{"ok": false, "error": "missing_scope", "needed": "chat:write", "provided": "users:read"}`,
		contains: []string{"chat:write", "reconnect Slack"},
	},
	{
		name: "internal_error", status: 200, body: `{"ok": false, "error": "internal_error"}`,
		retryable: true, contains: []string{"internal_error"},
	},
	{
		name: "unclassified ok:false", status: 200, body: `{"ok": false, "error": "is_archived"}`,
		contains: []string{"is_archived"},
	},
	{
		name: "503 upstream", status: 503, body: `<html>unavailable</html>`,
		retryable: true, contains: []string{"503"},
	},
}

func TestErrorMappingForEveryAction(t *testing.T) {
	for id, params := range minimal {
		for _, tc := range errorCases {
			t.Run(id+"/"+tc.name, func(t *testing.T) {
				f := newFake(t)
				f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					for k, v := range tc.headers {
						w.Header().Set(k, v)
					}
					w.Header().Set("Content-Type", "application/json; charset=utf-8")
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(tc.body))
				})
				res := f.run(id, params)
				assert.True(t, res.IsError, "%s is an error result: %+v", tc.name, res)
				assert.Equal(t, tc.status, res.StatusCode)
				assert.Equal(t, tc.retryable, res.Retryable, "retryable for %s: %s", tc.name, res.Content)
				for _, want := range tc.contains {
					assert.Contains(t, res.Content, want)
				}
				assert.NotContains(t, res.Content, token)
			})
		}
	}
}

// Every action's success body carries ok:true; none of them is mistaken for
// an error, and a warning alongside ok:true is still a success.
func TestOkTrueWithWarningIsASuccess(t *testing.T) {
	f := newFake(t)
	f.on("POST", "/chat.postMessage", slackReply(postedJSON))
	res := f.run("message.post", map[string]any{"channel": "C0GENERAL", "text": "x"})
	assert.False(t, res.IsError)
}
