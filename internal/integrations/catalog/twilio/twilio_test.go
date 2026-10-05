// Copyright (c) 2025 Reliant Labs

// Package twilio_test drives every action in the embedded `twilio` manifest
// through the real declarative runner against an httptest fake of Twilio's
// REST API. The manifest is the one the binary ships (catalog.MustBuiltin),
// with only its base_url pointed at the fake, so these tests pin the request
// each action sends — form-encoded, under the connection's account — the
// output it selects, and how Twilio's error codes are classified.
//
// Request and response shapes come from Twilio's API reference
// (twilio.com/docs/messaging/api/message-resource,
// /docs/phone-numbers/api/incomingphonenumber-resource) and its error catalog
// (twilio.com/docs/api/errors).
package twilio_test

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
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

const (
	accountSid = "AC11111111111111111111111111111111"
	authToken  = "twilio-auth-token-canary-9f3c"
	msgSid     = "SM22222222222222222222222222222222"
)

type recorded struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
	Form   url.Values
	Raw    string
}

// fakeTwilio answers each "METHOD <path below the account>" with a canned
// handler and records every request.
type fakeTwilio struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	got    []recorded
	routes map[string]http.HandlerFunc
}

func newFake(t *testing.T) *fakeTwilio {
	t.Helper()
	f := &fakeTwilio{t: t, routes: map[string]http.HandlerFunc{}}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

const accountPath = "/2010-04-01/Accounts/" + accountSid

func (f *fakeTwilio) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	rec := recorded{Method: r.Method, Path: r.URL.EscapedPath(), Query: r.URL.Query(), Header: r.Header.Clone(), Raw: string(raw)}
	if len(raw) > 0 {
		form, err := url.ParseQuery(string(raw))
		require.NoError(f.t, err, "request body must be a form: %s", raw)
		rec.Form = form
	}
	f.mu.Lock()
	f.got = append(f.got, rec)
	h, ok := f.routes[r.Method+" "+strings.TrimPrefix(r.URL.Path, accountPath)]
	f.mu.Unlock()
	if !ok {
		twilioReply(404, `{"code": 20404, "message": "The requested resource was not found", "more_info": "https://www.twilio.com/docs/errors/20404", "status": 404}`)(w, r)
		return
	}
	h(w, r)
}

func (f *fakeTwilio) on(method, path string, h http.HandlerFunc) { f.routes[method+" "+path] = h }

func twilioReply(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

func (f *fakeTwilio) requests() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorded(nil), f.got...)
}

func (f *fakeTwilio) only() recorded {
	f.t.Helper()
	got := f.requests()
	require.Len(f.t, got, 1, "exactly one request")
	return got[0]
}

// basicCred is what a Twilio connection resolves to: HTTP Basic with the
// account sid (its account_sid param) and the Auth Token.
type basicCred struct{}

func (basicCred) ConnectionID() string { return "conn_twilio" }
func (basicCred) Params() map[string]string {
	return map[string]string{"account_sid": accountSid}
}
func (basicCred) Scrub(s string) string {
	s = strings.ReplaceAll(s, authToken, "[redacted]")
	return strings.ReplaceAll(s, base64.StdEncoding.EncodeToString([]byte(accountSid+":"+authToken)), "[redacted]")
}
func (basicCred) Apply(r *http.Request) error {
	r.SetBasicAuth(accountSid, authToken)
	return nil
}

type staticSource struct{}

func (staticSource) Credential(context.Context, httpaction.CredentialRequest) (httpaction.Credential, error) {
	return basicCred{}, nil
}

// action resolves twilio/<id>@1 from the embedded catalog and points a clone
// of its manifest at the fake: the shipped declaration — including its
// templated account path — on a different host.
func (f *fakeTwilio) action(id string) (*reliantv1.IntegrationManifest, *reliantv1.ActionSpec) {
	f.t.Helper()
	resolved, err := catalog.MustBuiltin().Resolve("twilio/" + id + "@1")
	require.NoError(f.t, err)
	m := proto.Clone(resolved.Manifest).(*reliantv1.IntegrationManifest)
	shipped := m.Connection.BaseUrl
	require.True(f.t, strings.HasPrefix(shipped, "https://api.twilio.com/"), "shipped base_url %q", shipped)
	m.Connection.BaseUrl = f.srv.URL + strings.TrimPrefix(shipped, "https://api.twilio.com")
	for _, a := range m.GetActions() {
		if a.GetId() == id {
			return m, a
		}
	}
	f.t.Fatalf("action %s vanished from the clone", id)
	return nil, nil
}

func (f *fakeTwilio) runner() *httpaction.Runner {
	pool := x509.NewCertPool()
	pool.AddCert(f.srv.Certificate())
	g := netguard.New()
	g.AllowLoopback = true
	return httpaction.NewRunner(g).WithRootCAs(pool)
}

func (f *fakeTwilio) run(id string, params map[string]any) *httpaction.Result {
	f.t.Helper()
	res, err := f.try(id, params)
	require.NoError(f.t, err)
	return res
}

func (f *fakeTwilio) try(id string, params map[string]any) (*httpaction.Result, error) {
	m, a := f.action(id)
	return f.runner().RunAuthenticated(context.Background(), m, a, params, staticSource{}, httpaction.CallSite{RunID: "run-1", NodeID: "n"})
}

func assertAuth(t *testing.T, r recorded) {
	t.Helper()
	user, pass, ok := (&http.Request{Header: r.Header}).BasicAuth()
	require.True(t, ok, "HTTP Basic auth")
	assert.Equal(t, accountSid, user, "the account sid is the username")
	assert.Equal(t, authToken, pass)
}

func data(t *testing.T, res *httpaction.Result) map[string]any {
	t.Helper()
	require.False(t, res.IsError, "unexpected error result: %s", res.Content)
	return res.Data
}

// ---------------------------------------------------------------------------
// message.send
// ---------------------------------------------------------------------------

func createdJSON(to, from, body, status string) string {
	return `{"account_sid": "` + accountSid + `", "api_version": "2010-04-01", "body": "` + body + `",
	  "date_created": "Thu, 24 Aug 2023 05:01:45 +0000", "date_sent": null, "date_updated": "Thu, 24 Aug 2023 05:01:45 +0000",
	  "direction": "outbound-api", "error_code": null, "error_message": null, "from": "` + from + `",
	  "messaging_service_sid": null, "num_media": "0", "num_segments": "1", "price": null, "price_unit": "USD",
	  "sid": "` + msgSid + `", "status": "` + status + `", "to": "` + to + `",
	  "uri": "/2010-04-01/Accounts/` + accountSid + `/Messages/` + msgSid + `.json"}`
}

func TestMessageSendSMSIsAFormPostUnderTheAccount(t *testing.T) {
	f := newFake(t)
	f.on("POST", "/Messages.json", twilioReply(201, createdJSON("+15551230000", "+15559870000", "Deploy finished", "queued")))
	out := data(t, f.run("message.send", map[string]any{
		"to": "+15551230000", "from": "+15559870000", "body": "Deploy finished",
	}))

	req := f.only()
	assert.Equal(t, "POST", req.Method)
	assert.Equal(t, accountPath+"/Messages.json", req.Path, "the account sid param fills the path")
	assert.Equal(t, "application/x-www-form-urlencoded", req.Header.Get("Content-Type"))
	assertAuth(t, req)
	assert.Equal(t, url.Values{"To": {"+15551230000"}, "From": {"+15559870000"}, "Body": {"Deploy finished"}}, req.Form,
		"only what was given is sent, with Twilio's parameter names")
	assert.Contains(t, req.Raw, "To=%2B15551230000", "a + in E.164 is form-escaped, never sent as a space")

	assert.Equal(t, msgSid, out["sid"])
	assert.Equal(t, "queued", out["status"])
	assert.Equal(t, "+15551230000", out["to"])
	assert.Equal(t, "outbound-api", out["direction"])
	assert.Nil(t, out["error_code"])
}

func TestMessageSendWhatsAppWithMediaAndAService(t *testing.T) {
	f := newFake(t)
	f.on("POST", "/Messages.json", twilioReply(201, createdJSON("whatsapp:+15551230000", "whatsapp:+14155238886", "", "accepted")))
	data(t, f.run("message.send", map[string]any{
		"to": "whatsapp:+15551230000", "messaging_service_sid": "MG44444444444444444444444444444444",
		"media_url":       []any{"https://cdn.example.com/a.png", "https://cdn.example.com/b.pdf"},
		"status_callback": "https://hooks.example.com/status",
	}))
	req := f.only()
	assert.Equal(t, []string{"whatsapp:+15551230000"}, req.Form["To"])
	assert.Equal(t, []string{"MG44444444444444444444444444444444"}, req.Form["MessagingServiceSid"])
	assert.Equal(t, []string{"https://cdn.example.com/a.png", "https://cdn.example.com/b.pdf"}, req.Form["MediaUrl"],
		"each media URL is its own MediaUrl field, in order")
	assert.Equal(t, []string{"https://hooks.example.com/status"}, req.Form["StatusCallback"])
	assert.NotContains(t, req.Form, "From")
	assert.NotContains(t, req.Form, "Body")
}

func TestMessageSendWhatsAppTemplate(t *testing.T) {
	f := newFake(t)
	f.on("POST", "/Messages.json", twilioReply(201, createdJSON("whatsapp:+15551230000", "whatsapp:+14155238886", "", "queued")))
	data(t, f.run("message.send", map[string]any{
		"to": "whatsapp:+15551230000", "from": "whatsapp:+14155238886",
		"content_sid": "HX33333333333333333333333333333333", "content_variables": `{"1":"Ada"}`,
	}))
	req := f.only()
	assert.Equal(t, []string{"HX33333333333333333333333333333333"}, req.Form["ContentSid"])
	assert.Equal(t, []string{`{"1":"Ada"}`}, req.Form["ContentVariables"])
}

func TestMessageSendRefusesMalformedAddresses(t *testing.T) {
	for name, params := range map[string]map[string]any{
		"local number":        {"to": "5551230000", "body": "x"},
		"whatsapp wrong case": {"to": "WhatsApp:+15551230000", "body": "x"},
		"missing to":          {"body": "x"},
		"unknown param":       {"to": "+15551230000", "body": "x", "Body": "y"},
		"non-https callback":  {"to": "+15551230000", "body": "x", "status_callback": "http://x.example/s"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t)
			_, err := f.try("message.send", params)
			require.Error(t, err)
			assert.Empty(t, f.requests(), "no request for invalid params")
		})
	}
}

// ---------------------------------------------------------------------------
// message.get / message.list / phone_number.list
// ---------------------------------------------------------------------------

func TestMessageGetReportsALateFailure(t *testing.T) {
	f := newFake(t)
	failed := strings.Replace(strings.Replace(createdJSON("whatsapp:+15551230000", "whatsapp:+14155238886", "hi", "failed"),
		`"error_code": null`, `"error_code": 63016`, 1), `"error_message": null`, `"error_message": "Outside the allowed window"`, 1)
	f.on("GET", "/Messages/"+msgSid+".json", twilioReply(200, failed))
	out := data(t, f.run("message.get", map[string]any{"sid": msgSid}))
	req := f.only()
	assert.Equal(t, "GET", req.Method)
	assert.Equal(t, accountPath+"/Messages/"+msgSid+".json", req.Path)
	assertAuth(t, req)
	assert.Equal(t, "failed", out["status"], "a lookup reports the status; failing it is the caller's call")
	assert.EqualValues(t, 63016, out["error_code"])
	assert.Equal(t, "Outside the allowed window", out["error_message"])
}

func TestMessageGetRefusesAPathTraversal(t *testing.T) {
	f := newFake(t)
	_, err := f.try("message.get", map[string]any{"sid": "../../Calls"})
	require.Error(t, err)
	assert.Empty(t, f.requests())
}

func listPage(sids []string, next string) string {
	var msgs []string
	for _, s := range sids {
		msgs = append(msgs, `{"sid": "`+s+`", "status": "received", "direction": "inbound", "to": "+15559870000",
		  "from": "+15551230000", "body": "msg `+s+`", "num_media": "0", "error_code": null, "date_sent": "Thu, 24 Aug 2023 05:01:45 +0000"}`)
	}
	nextJSON := "null"
	if next != "" {
		nextJSON = `"` + next + `"`
	}
	return `{"messages": [` + strings.Join(msgs, ",") + `], "first_page_uri": "/x", "page": 0, "page_size": 2,
	  "previous_page_uri": null, "start": 0, "end": 1, "uri": "/x", "next_page_uri": ` + nextJSON + `}`
}

// Twilio pages with next_page_uri: a path relative to api.twilio.com that
// carries its own PageToken. The runner follows it verbatim, on the same host
// and with the same credential, and stops at null.
func TestMessageListFollowsNextPageURI(t *testing.T) {
	f := newFake(t)
	f.on("GET", "/Messages.json", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("PageToken") {
		case "":
			twilioReply(200, listPage([]string{"SM01", "SM02"},
				accountPath+"/Messages.json?To=%2B15559870000&PageSize=2&Page=1&PageToken=PASM02"))(w, r)
		case "PASM02":
			twilioReply(200, listPage([]string{"SM03"}, ""))(w, r)
		}
	})
	out := data(t, f.run("message.list", map[string]any{"to": "+15559870000", "page_size": 2, "date_sent_after": "2023-08-01"}))
	items, _ := out["items"].([]any)
	require.Len(t, items, 3)
	assert.Equal(t, "SM03", items[2].(map[string]any)["sid"])
	assert.Equal(t, "msg SM01", items[0].(map[string]any)["body"])

	got := f.requests()
	require.Len(t, got, 2)
	assert.Equal(t, accountPath+"/Messages.json", got[0].Path)
	assert.Equal(t, "+15559870000", got[0].Query.Get("To"))
	assert.Equal(t, "2", got[0].Query.Get("PageSize"))
	assert.Equal(t, "2023-08-01", got[0].Query.Get("DateSent>"), "Twilio's inequality filter keeps its literal name")
	assert.Equal(t, "PASM02", got[1].Query.Get("PageToken"), "the second page is next_page_uri itself")
	assert.Empty(t, got[1].Query.Get("DateSent>"), "the next page carries Twilio's own state, not ours re-applied")
	for _, r := range got {
		assertAuth(t, r)
	}
}

func TestMessageListDefaults(t *testing.T) {
	f := newFake(t)
	f.on("GET", "/Messages.json", twilioReply(200, listPage(nil, "")))
	out := data(t, f.run("message.list", map[string]any{}))
	assert.Empty(t, out["items"])
	q := f.only().Query
	assert.Equal(t, "50", q.Get("PageSize"))
	assert.Equal(t, url.Values{"PageSize": {"50"}}, q, "no filter is sent that was not given")
}

func TestPhoneNumberList(t *testing.T) {
	f := newFake(t)
	f.on("GET", "/IncomingPhoneNumbers.json", twilioReply(200, `{"incoming_phone_numbers": [
	  {"sid": "PN01", "phone_number": "+15559870000", "friendly_name": "(555) 987-0000",
	   "capabilities": {"sms": true, "mms": true, "voice": true, "fax": false},
	   "sms_url": "https://reliant.example/integrations/twilio/events", "sms_method": "POST"},
	  {"sid": "PN02", "phone_number": "+447700900000", "friendly_name": null,
	   "capabilities": {"sms": false, "mms": false, "voice": true}, "sms_url": "", "sms_method": "POST"}],
	  "next_page_uri": null, "page": 0, "page_size": 100}`))
	out := data(t, f.run("phone_number.list", map[string]any{}))
	items, _ := out["items"].([]any)
	require.Len(t, items, 2)
	first := items[0].(map[string]any)
	assert.Equal(t, "+15559870000", first["phone_number"])
	assert.Equal(t, true, first["sms"])
	assert.Equal(t, "https://reliant.example/integrations/twilio/events", first["sms_url"])
	assert.Equal(t, false, items[1].(map[string]any)["sms"])
	req := f.only()
	assert.Equal(t, accountPath+"/IncomingPhoneNumbers.json", req.Path)
	assert.Equal(t, "100", req.Query.Get("PageSize"))
	assertAuth(t, req)
}

// ---------------------------------------------------------------------------
// Coverage and error classification across every action.
// ---------------------------------------------------------------------------

var minimal = map[string]map[string]any{
	"message.send":      {"to": "+15551230000", "from": "+15559870000", "body": "x"},
	"message.get":       {"sid": msgSid},
	"message.list":      {},
	"phone_number.list": {},
}

// Adding an action without extending `minimal` (and so the error tests)
// fails here.
func TestEveryActionIsCovered(t *testing.T) {
	resolved, err := catalog.MustBuiltin().Resolve("twilio/message.send@1")
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

func twilioError(code, status int, msg string) string {
	c := strconv.Itoa(code)
	return `{"code": ` + c + `, "message": "` + msg + `", "more_info": "https://www.twilio.com/docs/errors/` + c + `", "status": ` + strconv.Itoa(status) + `}`
}

type errorCase struct {
	name      string
	status    int
	body      string
	retryable bool
	contains  []string
}

var errorCases = []errorCase{
	{name: "20003 bad credentials", status: 401, body: twilioError(20003, 401, "Authenticate"),
		contains: []string{"20003", "Reconnect Twilio"}},
	{name: "21211 invalid To", status: 400, body: twilioError(21211, 400, "Invalid 'To' Phone Number: +1555"),
		contains: []string{"21211", "E.164"}},
	{name: "21614 not a mobile number", status: 400, body: twilioError(21614, 400, "'To' number is not a valid mobile number"),
		contains: []string{"21614", "E.164"}},
	{name: "21608 unverified trial recipient", status: 400, body: twilioError(21608, 400, "The number is unverified"),
		contains: []string{"21608", "verified", "trial"}},
	{name: "63016 outside the WhatsApp window", status: 400, body: twilioError(63016, 400, "Outside the allowed window"),
		contains: []string{"63016", "24 hours", "content_sid"}},
	{name: "63015 sandbox recipient not joined", status: 400, body: twilioError(63015, 400, "Sandbox"),
		contains: []string{"63015", "join"}},
	{name: "21610 opted out", status: 400, body: twilioError(21610, 400, "unsubscribed"),
		contains: []string{"21610", "STOP"}},
	{name: "21606 not a usable sender", status: 400, body: twilioError(21606, 400, "The From phone number is not a valid, SMS-capable inbound phone number"),
		contains: []string{"21606", "sender"}},
	{name: "429 too many requests", status: 429, body: twilioError(20429, 429, "Too Many Requests"),
		retryable: true, contains: []string{"rate limit", "20429"}},
	{name: "unclassified 400", status: 400, body: twilioError(21602, 400, "Message body is required."),
		contains: []string{"21602", "Message body is required"}},
	{name: "503 upstream", status: 503, body: `<html>unavailable</html>`,
		retryable: true, contains: []string{"503"}},
}

func TestErrorMappingForEveryAction(t *testing.T) {
	for id, params := range minimal {
		for _, tc := range errorCases {
			t.Run(id+"/"+tc.name, func(t *testing.T) {
				f := newFake(t)
				f.srv.Config.Handler = twilioReply(tc.status, tc.body)
				res := f.run(id, params)
				assert.True(t, res.IsError, "%s is an error result: %+v", tc.name, res)
				assert.Equal(t, tc.status, res.StatusCode)
				assert.Equal(t, tc.retryable, res.Retryable, "retryable for %s: %s", tc.name, res.Content)
				for _, want := range tc.contains {
					assert.Contains(t, res.Content, want)
				}
				assert.NotContains(t, res.Content, authToken)
			})
		}
	}
}

// Twilio accepts some sends (201) and fails them before answering: the
// created message already says failed. 63016 is reported that way for
// WhatsApp, and the send is an error with the template advice.
func TestMessageSendAcceptedButAlreadyFailed(t *testing.T) {
	for name, tc := range map[string]struct {
		code     string
		contains []string
	}{
		"63016":   {code: "63016", contains: []string{"63016", "24 hours", "content_sid"}},
		"30008":   {code: "30008", contains: []string{"30008", "failed"}},
		"no code": {code: "null", contains: []string{"failed"}},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t)
			body := strings.Replace(createdJSON("whatsapp:+15551230000", "whatsapp:+14155238886", "hi", "failed"),
				`"error_code": null`, `"error_code": `+tc.code, 1)
			f.on("POST", "/Messages.json", twilioReply(201, body))
			res := f.run("message.send", minimal["message.send"])
			require.True(t, res.IsError, "%+v", res)
			assert.False(t, res.Retryable)
			for _, want := range tc.contains {
				assert.Contains(t, res.Content, want)
			}
		})
	}
	f := newFake(t)
	f.on("POST", "/Messages.json", twilioReply(201, createdJSON("+15551230000", "+15559870000", "x", "accepted")))
	assert.False(t, f.run("message.send", minimal["message.send"]).IsError, "an accepted message is a success")
}
