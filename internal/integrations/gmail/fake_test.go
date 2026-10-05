// Copyright (c) 2025 Reliant Labs

package gmail_test

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/netguard"
)

// fakeGmail is an httptest TLS server standing in for
// gmail.googleapis.com/gmail/v1/users/me. Shapes follow the Gmail API
// discovery document: Message{id, threadId, labelIds, snippet, internalDate,
// payload{headers[], mimeType, body{data, attachmentId, size}, parts[]}},
// ListHistoryResponse{history[{id, messagesAdded[{message}]}], nextPageToken,
// historyId}, and Google's error model.
type fakeGmail struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	got      []recorded
	email    string
	history  uint64 // the mailbox's current history id
	messages map[string]map[string]any
	// records is the history log: each entry is one history record.
	records []historyRecord
	// expiredBefore makes history.list 404 for any startHistoryId below it.
	expiredBefore uint64
	// historyPageSize overrides how many records one history page carries.
	historyPageSize int
	unauthorized    bool
	sent            []map[string]any
	labels          []map[string]any
}

type historyRecord struct {
	id       uint64
	messages []map[string]any // {id, threadId, labelIds}
}

type recorded struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
	Body   map[string]any
}

const token = "ya29.fake-gmail-access-token-7c1d"

func newFakeGmail(t *testing.T) *fakeGmail {
	t.Helper()
	f := &fakeGmail{t: t, email: "me@example.com", history: 1000, messages: map[string]map[string]any{}}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

const prefix = "/gmail/v1/users/me"

func (f *fakeGmail) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	rec := recorded{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Header: r.Header.Clone()}
	if len(raw) > 0 {
		require.NoError(f.t, json.Unmarshal(raw, &rec.Body), "request body must be JSON: %s", raw)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, rec)
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	if f.unauthorized || r.Header.Get("Authorization") != "Bearer "+token {
		googleErr(w, 401, "UNAUTHENTICATED", "authError", "Request had invalid authentication credentials.")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, prefix)
	switch {
	case r.Method == "GET" && path == "/profile":
		writeJSON(w, map[string]any{"emailAddress": f.email, "messagesTotal": len(f.messages), "historyId": strconv.FormatUint(f.history, 10)})
	case r.Method == "GET" && path == "/history":
		f.historyList(w, r.URL.Query())
	case r.Method == "GET" && path == "/messages":
		f.list(w, r.URL.Query())
	case r.Method == "GET" && strings.HasPrefix(path, "/messages/"):
		id := strings.TrimPrefix(path, "/messages/")
		m, ok := f.messages[id]
		if !ok {
			googleErr(w, 404, "NOT_FOUND", "notFound", "Requested entity was not found.")
			return
		}
		writeJSON(w, f.render(m, r.URL.Query()))
	case r.Method == "POST" && path == "/messages/send":
		f.sent = append(f.sent, rec.Body)
		writeJSON(w, map[string]any{"id": fmt.Sprintf("sent%04d", len(f.sent)), "threadId": orString(rec.Body["threadId"], "thr-new"), "labelIds": []string{"SENT"}})
	case r.Method == "GET" && path == "/labels":
		writeJSON(w, map[string]any{"labels": f.labels})
	default:
		googleErr(w, 404, "NOT_FOUND", "notFound", "no route "+r.Method+" "+r.URL.Path)
	}
}

func (f *fakeGmail) historyList(w http.ResponseWriter, q url.Values) {
	start, err := strconv.ParseUint(q.Get("startHistoryId"), 10, 64)
	if err != nil {
		googleErr(w, 400, "INVALID_ARGUMENT", "invalidArgument", "Invalid startHistoryId")
		return
	}
	if start < f.expiredBefore {
		googleErr(w, 404, "NOT_FOUND", "notFound", "Requested entity was not found.")
		return
	}
	if q.Get("historyTypes") != "messageAdded" || q.Get("labelId") != "INBOX" {
		f.t.Errorf("history.list must ask for messageAdded in INBOX, got %v", q)
	}
	var after []historyRecord
	for _, rec := range f.records {
		if rec.id > start {
			after = append(after, rec)
		}
	}
	size := f.historyPageSize
	if size <= 0 {
		size = 100
	}
	offset := 0
	if tok := q.Get("pageToken"); tok != "" {
		offset, _ = strconv.Atoi(strings.TrimPrefix(tok, "page-"))
	}
	end := offset + size
	if end > len(after) {
		end = len(after)
	}
	resp := map[string]any{"historyId": strconv.FormatUint(f.history, 10)}
	if end > offset {
		var hist []any
		for _, rec := range after[offset:end] {
			var added []any
			for _, m := range rec.messages {
				added = append(added, map[string]any{"message": m})
			}
			hist = append(hist, map[string]any{"id": strconv.FormatUint(rec.id, 10), "messagesAdded": added})
		}
		resp["history"] = hist
	}
	if end < len(after) {
		resp["nextPageToken"] = "page-" + strconv.Itoa(end)
	}
	writeJSON(w, resp)
}

func (f *fakeGmail) list(w http.ResponseWriter, q url.Values) {
	ids := make([]string, 0, len(f.messages))
	for id := range f.messages {
		ids = append(ids, id)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	size, _ := strconv.Atoi(q.Get("maxResults"))
	if size <= 0 {
		size = 100
	}
	offset := 0
	if tok := q.Get("pageToken"); tok != "" {
		offset, _ = strconv.Atoi(tok)
	}
	end := offset + size
	if end > len(ids) {
		end = len(ids)
	}
	var out []any
	for _, id := range ids[offset:end] {
		out = append(out, map[string]any{"id": id, "threadId": f.messages[id]["threadId"]})
	}
	resp := map[string]any{"resultSizeEstimate": len(ids)}
	if len(out) > 0 {
		resp["messages"] = out
	}
	if end < len(ids) {
		resp["nextPageToken"] = strconv.Itoa(end)
	}
	writeJSON(w, resp)
}

// render returns the message as format asks: metadata keeps only the
// requested headers and drops bodies.
func (f *fakeGmail) render(m map[string]any, q url.Values) map[string]any {
	if q.Get("format") != "metadata" {
		return m
	}
	want := map[string]bool{}
	for _, h := range q["metadataHeaders"] {
		want[strings.ToLower(h)] = true
	}
	out := map[string]any{}
	for k, v := range m {
		out[k] = v
	}
	payload, _ := m["payload"].(map[string]any)
	var headers []any
	if payload != nil {
		hs, _ := payload["headers"].([]any)
		for _, h := range hs {
			hm := h.(map[string]any)
			if want[strings.ToLower(hm["name"].(string))] {
				headers = append(headers, hm)
			}
		}
	}
	out["payload"] = map[string]any{"mimeType": payload["mimeType"], "headers": headers}
	return out
}

// addMessage stores a message and, when inbox is set, logs a messageAdded
// history record for it at the next history id.
func (f *fakeGmail) addMessage(id, thread string, labels []string, headers map[string]string, payload map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var hs []any
	for _, name := range sortedKeys(headers) {
		hs = append(hs, map[string]any{"name": name, "value": headers[name]})
	}
	if payload == nil {
		payload = map[string]any{"mimeType": "text/plain", "body": map[string]any{"size": 0}}
	}
	payload["headers"] = hs
	f.messages[id] = map[string]any{
		"id": id, "threadId": thread, "labelIds": labels, "snippet": "snippet of " + id,
		"internalDate": "1791000000000", "payload": payload, "historyId": strconv.FormatUint(f.history+1, 10),
	}
	f.history += 3 // history ids are increasing but not contiguous
	f.records = append(f.records, historyRecord{id: f.history, messages: []map[string]any{{"id": id, "threadId": thread, "labelIds": labels}}})
}

func (f *fakeGmail) requests() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorded(nil), f.got...)
}

func (f *fakeGmail) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = nil
}

func writeJSON(w http.ResponseWriter, v any) { _ = json.NewEncoder(w).Encode(v) }

func googleErr(w http.ResponseWriter, code int, status, reason, message string) {
	w.WriteHeader(code)
	writeJSON(w, map[string]any{"error": map[string]any{
		"code": code, "message": message, "status": status,
		"errors": []any{map[string]any{"reason": reason, "domain": "global", "message": message}},
	}})
}

func orString(v any, d string) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return d
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// staticCred stands in for a resolved Gmail connection: a bearer token,
// applied only by Apply and scrubbed from results.
type staticCred struct{}

func (staticCred) ConnectionID() string      { return "conn_gmail" }
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

// manifest is the embedded gmail manifest with base_url pointed at the fake:
// the shipped declaration, a different host.
func (f *fakeGmail) manifest() *reliantv1.IntegrationManifest {
	f.t.Helper()
	m, err := catalog.MustBuiltin().Manifest("gmail", 1)
	require.NoError(f.t, err)
	m = proto.Clone(m).(*reliantv1.IntegrationManifest)
	m.Connection.BaseUrl = f.srv.URL + prefix
	return m
}

func (f *fakeGmail) action(id string) (*reliantv1.IntegrationManifest, *reliantv1.ActionSpec) {
	f.t.Helper()
	m := f.manifest()
	for _, a := range m.GetActions() {
		if a.GetId() == id {
			return m, a
		}
	}
	f.t.Fatalf("gmail has no action %s", id)
	return nil, nil
}

func (f *fakeGmail) runner() *httpaction.Runner {
	pool := x509.NewCertPool()
	pool.AddCert(f.srv.Certificate())
	g := netguard.New()
	g.AllowLoopback = true
	return httpaction.NewRunner(g).WithRootCAs(pool)
}

// run executes gmail/<id>@1 with params, authenticated like a real call.
func (f *fakeGmail) run(id string, params map[string]any) *httpaction.Result {
	f.t.Helper()
	res, err := f.try(id, params)
	require.NoError(f.t, err)
	return res
}

func (f *fakeGmail) try(id string, params map[string]any) (*httpaction.Result, error) {
	m, a := f.action(id)
	return f.runner().RunAuthenticated(context.Background(), m, a, params, staticSource{}, httpaction.CallSite{RunID: "run-1", NodeID: "n"})
}

// rejectableCred is a credential whose token Google may refuse: Rejected
// hands out the next token (a refresh), or needs_reauth when dead.
type rejectableCred struct {
	tok   string
	next  *string
	dead  bool
	calls *int
}

func (c rejectableCred) ConnectionID() string      { return "conn_gmail" }
func (c rejectableCred) Params() map[string]string { return nil }
func (c rejectableCred) Scrub(s string) string     { return strings.ReplaceAll(s, c.tok, "[redacted]") }
func (c rejectableCred) Apply(r *http.Request) error {
	r.Header.Set("Authorization", "Bearer "+c.tok)
	return nil
}
func (c rejectableCred) Rejected(context.Context) (httpaction.Credential, error) {
	*c.calls++
	if c.dead || c.next == nil {
		return nil, &httpaction.CredentialError{Code: httpaction.CodeNeedsReauth, Message: `connection "work" needs to be reconnected`}
	}
	return rejectableCred{tok: *c.next, calls: c.calls, dead: true}, nil
}
