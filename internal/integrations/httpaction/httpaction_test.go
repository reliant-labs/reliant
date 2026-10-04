package httpaction

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/manifest"
	"github.com/reliant-labs/reliant/internal/netguard"
)

// newRunner returns a runner that may reach loopback (the httptest server) but
// is otherwise the production guard.
func newRunner() *Runner {
	g := netguard.New()
	g.AllowLoopback = true
	return NewRunner(g)
}

func mustParse(t *testing.T, doc string) (*reliantv1.IntegrationManifest, *reliantv1.ActionSpec) {
	t.Helper()
	m, err := manifest.Parse([]byte(doc), manifest.TrustCurated)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return m, m.GetActions()[0]
}

func manifestFor(base, action string) string {
	return "id: t\nversion: 1\ndisplay_name: T\nconnection:\n  type: none\n  base_url: " + base + "\nactions:\n  - id: a\n    placement: server\n" + action
}

func TestRendersRequestFromParams(t *testing.T) {
	var gotPath, gotQuery, gotBody, gotHeader, gotMethod string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotQuery, gotHeader = r.Method, r.URL.EscapedPath(), r.URL.RawQuery, r.Header.Get("X-Trace")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"number": 7, "html_url": "https://x/7"}`))
	}))
	defer srv.Close()

	m, a := mustParse(t, manifestFor(srv.URL+"/api", `    params: { type: object }
    request:
      method: POST
      path: /repos/{{ params.owner }}/issues
      query: { state: "{{ params.state }}" }
      headers: { X-Trace: "t-{{ params.id }}" }
      body:
        title: "{{ params.title }}"
        labels: "{{ params.labels }}"
        count: "{{ params.count }}"
    output: { select: "response.number" }
`))
	_ = a
	r := newRunner()
	r.client.Transport.(*http.Transport).TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig
	res, err := r.Run(context.Background(), m, a, map[string]any{
		"owner": "a b", "state": "open", "id": "42", "title": "Hello", "labels": []any{"x", "y"}, "count": 3.0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" || gotPath != "/api/repos/a%20b/issues" || gotQuery != "state=open" || gotHeader != "t-42" {
		t.Errorf("request wrong: %s %s ?%s hdr=%s", gotMethod, gotPath, gotQuery, gotHeader)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(gotBody), &body); err != nil {
		t.Fatal(err)
	}
	if body["title"] != "Hello" || body["count"] != 3.0 || len(body["labels"].([]any)) != 2 {
		t.Errorf("body templating lost types: %s", gotBody)
	}
	if res.StatusCode != 200 || res.IsError {
		t.Errorf("result: %+v", res)
	}
	if res.Data["value"] != 7.0 {
		t.Errorf("select of a scalar should land under data.value, got %#v", res.Data)
	}
}

func TestResponseSelection(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data": {"id": 9, "name": "n"}, "meta": 1}`))
	}))
	defer srv.Close()
	run := func(sel string) *Result {
		m, a := mustParse(t, manifestFor(srv.URL, "    params: { type: object }\n    request: { method: GET, path: /x }\n    output: { select: \""+sel+"\" }\n"))
		r := newRunner()
		r.client.Transport.(*http.Transport).TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig
		res, err := r.Run(context.Background(), m, a, nil)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	if d := run("$").Data; d["meta"] != 1.0 {
		t.Errorf("$ selects whole body: %#v", d)
	}
	if d := run("response.data").Data; d["id"] != 9.0 {
		t.Errorf("CEL select: %#v", d)
	}
	if d := run("$raw").Data; !strings.Contains(d["body"].(string), `"meta"`) {
		t.Errorf("$raw: %#v", d)
	}
}

func TestErrorMapping(t *testing.T) {
	status := int32(404)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(int(atomic.LoadInt32(&status)))
		_, _ = w.Write([]byte(`{"message": "nope"}`))
	}))
	defer srv.Close()
	m, a := mustParse(t, manifestFor(srv.URL, `    params: { type: object }
    request:
      method: GET
      path: /x
      errors:
        - { status: 404, retryable: false, message: "not found: {{ response.message }}" }
        - { status_min: 500, status_max: 599, retryable: true, message: "upstream {{ status }}" }
`))
	run := func() *Result {
		r := newRunner()
		r.client.Transport.(*http.Transport).TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig
		res, err := r.Run(context.Background(), m, a, nil)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	res := run()
	if !res.IsError || res.Retryable || res.Content != "not found: nope" || res.StatusCode != 404 {
		t.Errorf("404: %+v", res)
	}
	atomic.StoreInt32(&status, 503)
	res = run()
	if !res.IsError || !res.Retryable || res.Content != "upstream 503" {
		t.Errorf("503: %+v", res)
	}
	atomic.StoreInt32(&status, 429)
	if res = run(); !res.Retryable {
		t.Errorf("429 should default to retryable: %+v", res)
	}
	atomic.StoreInt32(&status, 400)
	if res = run(); res.Retryable || !res.IsError {
		t.Errorf("400 should default to permanent: %+v", res)
	}
}

func TestPaginationCursor(t *testing.T) {
	var calls int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		switch r.URL.Query().Get("cursor") {
		case "":
			_, _ = w.Write([]byte(`{"items":[1,2],"next":"c2"}`))
		case "c2":
			_, _ = w.Write([]byte(`{"items":[3],"next":null}`))
		}
	}))
	defer srv.Close()
	m, a := mustParse(t, manifestFor(srv.URL, `    params: { type: object }
    request:
      method: GET
      path: /list
      pagination: { style: cursor, cursor_param: cursor, next_cursor: "response.next", max_pages: 5 }
    output: { select: "response.items" }
`))
	r := newRunner()
	r.client.Transport.(*http.Transport).TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig
	res, err := r.Run(context.Background(), m, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	items, _ := res.Data["items"].([]any)
	if len(items) != 3 || atomic.LoadInt32(&calls) != 2 {
		t.Errorf("items=%v calls=%d", items, calls)
	}
}

func TestPaginationPageAndMaxPages(t *testing.T) {
	var calls int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		_, _ = w.Write([]byte(`[1]`))
	}))
	defer srv.Close()
	m, a := mustParse(t, manifestFor(srv.URL, `    params: { type: object }
    request:
      method: GET
      path: /list
      pagination: { style: page, page_param: page, max_pages: 3 }
    output: { select: "response" }
`))
	r := newRunner()
	r.client.Transport.(*http.Transport).TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig
	res, err := r.Run(context.Background(), m, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(res.Data["items"].([]any)); got != 3 || atomic.LoadInt32(&calls) != 3 {
		t.Errorf("max_pages not honoured: items=%d calls=%d", got, calls)
	}
}

func TestPaginationLinkHeader(t *testing.T) {
	var srvURL string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("p") == "" {
			w.Header().Set("Link", `<`+srvURL+`/list?p=2>; rel="next"`)
			_, _ = w.Write([]byte(`[1,2]`))
			return
		}
		_, _ = w.Write([]byte(`[3]`))
	}))
	defer srv.Close()
	srvURL = srv.URL
	m, a := mustParse(t, manifestFor(srv.URL, `    params: { type: object }
    request:
      method: GET
      path: /list
      pagination: { style: link_header }
    output: { select: "response" }
`))
	r := newRunner()
	r.client.Transport.(*http.Transport).TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig
	res, err := r.Run(context.Background(), m, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(res.Data["items"].([]any)); got != 3 {
		t.Errorf("link_header items=%d", got)
	}
}

func TestLinkHeaderCannotLeaveAllowedHosts(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Link", `<https://169.254.169.254/latest>; rel="next"`)
		_, _ = w.Write([]byte(`[1]`))
	}))
	defer srv.Close()
	m, a := mustParse(t, manifestFor(srv.URL, `    params: { type: object }
    request: { method: GET, path: /list, pagination: { style: link_header } }
    output: { select: "response" }
`))
	r := newRunner()
	r.client.Transport.(*http.Transport).TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig
	res, err := r.Run(context.Background(), m, a, nil)
	if err == nil && !res.IsError {
		t.Fatal("a next link to another host must be refused")
	}
}

func TestResponseSizeLimit(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("a", 5000)))
	}))
	defer srv.Close()
	m, a := mustParse(t, manifestFor(srv.URL, "    params: { type: object }\n    request: { method: GET, path: /big, max_response_bytes: 1000 }\n"))
	r := newRunner()
	r.client.Transport.(*http.Transport).TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig
	res, err := r.Run(context.Background(), m, a, nil)
	if err == nil && !(res.IsError && strings.Contains(res.Content, "exceeds")) {
		t.Fatalf("oversized response must be an error: %+v", res)
	}
}

func TestTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	m, a := mustParse(t, manifestFor(srv.URL, "    params: { type: object }\n    request: { method: GET, path: /slow, timeout_seconds: 1 }\n"))
	r := newRunner()
	r.client.Transport.(*http.Transport).TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig
	res, err := r.Run(context.Background(), m, a, nil)
	if err == nil && !res.IsError {
		t.Fatal("a hung server must time out")
	}
}

func TestRedirectToOtherHostRefused(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://169.254.169.254/latest/meta-data", http.StatusFound)
	}))
	defer srv.Close()
	m, a := mustParse(t, manifestFor(srv.URL, "    params: { type: object }\n    request: { method: GET, path: /r }\n"))
	r := newRunner()
	r.client.Transport.(*http.Transport).TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig
	res, err := r.Run(context.Background(), m, a, nil)
	if err == nil && !res.IsError {
		t.Fatal("redirect off the allowed hosts must be refused")
	}
}

func TestSSRFGuardRefusesPrivateTargets(t *testing.T) {
	// The production guard (loopback NOT allowed) with a generic-http manifest.
	doc := `
id: web
version: 1
display_name: Web
connection: { type: none, allow_any_public_host: true }
actions:
  - id: request
    placement: server
    mutates: true
    params: { type: object }
    request: { method: GET, url: "{{ params.url }}", timeout_seconds: 5 }
`
	m, a := mustParse(t, doc)
	r := NewRunner(netguard.New())
	for _, target := range []string{
		"https://127.0.0.1/x", "https://169.254.169.254/latest/meta-data/", "https://10.0.0.1/x", "https://[::1]/x",
		"http://127.0.0.1:1/x", "https://localhost/x",
	} {
		res, err := r.Run(context.Background(), m, a, map[string]any{"url": target})
		if err == nil && !res.IsError {
			t.Errorf("%s must be refused", target)
			continue
		}
		msg := ""
		if err != nil {
			msg = err.Error()
		} else {
			msg = res.Content
		}
		if !strings.Contains(msg, "blocked") {
			t.Errorf("%s: want a blocked error, got %q", target, msg)
		}
	}
}

func TestOffHostURLRefusedAtRuntime(t *testing.T) {
	doc := `
id: t
version: 1
display_name: T
connection: { type: none, base_url: "https://api.example.com" }
actions:
  - id: a
    placement: server
    params: { type: object }
    request: { method: GET, path: "/{{ params.p }}" }
`
	m, a := mustParse(t, doc)
	r := NewRunner(netguard.New())
	// Path-escaping keeps a smuggled host inside the path, so the URL host stays api.example.com.
	u, err := r.buildURL(m, a.GetRequest(), map[string]any{"p": "@evil.example.org/x"})
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "api.example.com" {
		t.Fatalf("host escaped base_url: %s", u)
	}
}

func TestParamValidationAgainstSchema(t *testing.T) {
	m, a := mustParse(t, manifestFor("https://api.example.com", `    params:
      type: object
      required: [id]
      additionalProperties: false
      properties: { id: { type: string } }
    request: { method: GET, path: "/x/{{ params.id }}" }
`))
	r := NewRunner(netguard.New())
	if _, err := r.Run(context.Background(), m, a, map[string]any{}); err == nil || !strings.Contains(err.Error(), "id") {
		t.Errorf("missing required param must fail: %v", err)
	}
	if _, err := r.Run(context.Background(), m, a, map[string]any{"id": "x", "extra": 1}); err == nil {
		t.Error("unknown param must fail")
	}
}
