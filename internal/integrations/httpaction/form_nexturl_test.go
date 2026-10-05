package httpaction

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/reliant/internal/integrations/manifest"
)

// A form body is application/x-www-form-urlencoded: scalars become one
// key=value, a list repeats its key in order, and a null leaves the key out
// (an optional param the caller did not give is never sent as "null").
func TestFormBodyEncodesScalarsListsAndOmitsNulls(t *testing.T) {
	var gotType string
	var gotForm url.Values
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotType = r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		gotForm, _ = url.ParseQuery(string(raw))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sid":"SM1"}`))
	}))
	defer srv.Close()
	m, a := mustParse(t, manifestFor(srv.URL, `    params: { type: object }
    request:
      method: POST
      path: /Messages.json
      body_format: form
      body_expr: >-
        {"To": params.to, "Body": params.body, "MediaUrl": params.media,
         "Count": params.count, "Flag": params.flag, "Skip": null, ?"Opt": params.?opt}
`))
	r := newRunner()
	r.client.Transport.(*http.Transport).TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig
	res, err := r.Run(context.Background(), m, a, map[string]any{
		"to": "whatsapp:+15551230000", "body": "héllo & bye=1", "media": []any{"https://a/1.png", "https://a/2.png"},
		"count": 3.0, "flag": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %+v", res)
	}
	if gotType != "application/x-www-form-urlencoded" {
		t.Errorf("Content-Type = %q, want form", gotType)
	}
	want := url.Values{
		"To": {"whatsapp:+15551230000"}, "Body": {"héllo & bye=1"},
		"MediaUrl": {"https://a/1.png", "https://a/2.png"}, "Count": {"3"}, "Flag": {"true"},
	}
	if gotForm.Encode() != want.Encode() {
		t.Errorf("form = %v, want %v", gotForm, want)
	}
}

// A form body is flat: a nested object has no encoding Twilio or Stripe
// would read the same way, so it is a render error, not a guess.
func TestFormBodyRefusesNestedObjects(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("no request should be sent")
	}))
	defer srv.Close()
	m, a := mustParse(t, manifestFor(srv.URL, `    params: { type: object }
    request:
      method: POST
      path: /x
      body_format: form
      body_expr: '{"A": {"nested": 1}}'
`))
	r := newRunner()
	r.client.Transport.(*http.Transport).TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig
	if _, err := r.Run(context.Background(), m, a, nil); err == nil || !strings.Contains(err.Error(), "form") {
		t.Fatalf("a nested form value must be refused, got %v", err)
	}
}

// The static `body` template encodes as a form too, and an explicit
// Content-Type in the manifest is kept.
func TestFormBodyFromStaticTemplate(t *testing.T) {
	var gotType, gotRaw string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotType = r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		gotRaw = string(raw)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	m, a := mustParse(t, manifestFor(srv.URL, `    params: { type: object }
    request:
      method: POST
      path: /x
      body_format: form
      body: { Name: "{{ params.name }}" }
`))
	r := newRunner()
	r.client.Transport.(*http.Transport).TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig
	if _, err := r.Run(context.Background(), m, a, map[string]any{"name": "a b"}); err != nil {
		t.Fatal(err)
	}
	if gotType != "application/x-www-form-urlencoded" || gotRaw != "Name=a+b" {
		t.Errorf("got %q %q", gotType, gotRaw)
	}
}

func TestLoaderValidatesBodyFormat(t *testing.T) {
	doc := manifestFor("https://api.example.com", `    params: { type: object }
    request: { method: POST, path: /x, body_format: xml }
`)
	if _, err := manifest.Parse([]byte(doc), manifest.TrustCurated); err == nil || !strings.Contains(err.Error(), "body_format") {
		t.Fatalf("an unknown body_format must be a load error, got %v", err)
	}
}

// next_url pagination follows a URL the response names: Twilio's
// next_page_uri is a path relative to the API host, and it carries the
// paging state itself, so the first page's query is not re-applied.
func TestPaginationNextURLRelativeAndAbsolute(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	var srvURL string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.RequestURI())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("Page") {
		case "":
			_, _ = w.Write([]byte(`{"items":[1,2],"next_page_uri":"/v1/list.json?PageSize=2&Page=1&PageToken=PAx"}`))
		case "1":
			_, _ = w.Write([]byte(`{"items":[3],"next_page_uri":"` + srvURL + `/v1/list.json?PageSize=2&Page=2&PageToken=PAy"}`))
		default:
			_, _ = w.Write([]byte(`{"items":[4],"next_page_uri":null}`))
		}
	}))
	defer srv.Close()
	srvURL = srv.URL
	m, a := mustParse(t, manifestFor(srv.URL+"/v1", `    params: { type: object }
    request:
      method: GET
      path: /list.json
      query: { PageSize: "2" }
      pagination: { style: next_url, next_url: "response.?next_page_uri.orValue('')", max_pages: 5 }
    output: { select: "response.items" }
`))
	r := newRunner()
	r.client.Transport.(*http.Transport).TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig
	res, err := r.Run(context.Background(), m, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	items, _ := res.Data["items"].([]any)
	if len(items) != 4 {
		t.Fatalf("items = %v", items)
	}
	want := []string{
		"/v1/list.json?PageSize=2",
		"/v1/list.json?PageSize=2&Page=1&PageToken=PAx",
		"/v1/list.json?PageSize=2&Page=2&PageToken=PAy",
	}
	if strings.Join(seen, " ") != strings.Join(want, " ") {
		t.Errorf("requests = %v, want %v", seen, want)
	}
}

// A next_url may not leave the call's host, any more than a Link header may.
func TestPaginationNextURLCannotLeaveTheHost(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"items":[1],"next":"https://169.254.169.254/latest"}`))
	}))
	defer srv.Close()
	m, a := mustParse(t, manifestFor(srv.URL, `    params: { type: object }
    request:
      method: GET
      path: /list
      pagination: { style: next_url, next_url: "response.next" }
    output: { select: "response.items" }
`))
	r := newRunner()
	r.client.Transport.(*http.Transport).TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig
	res, err := r.Run(context.Background(), m, a, nil)
	if err == nil && !res.IsError {
		t.Fatal("a next_url to another host must be refused")
	}
}

func TestLoaderValidatesNextURLPagination(t *testing.T) {
	doc := manifestFor("https://api.example.com", `    params: { type: object }
    request: { method: GET, path: /x, pagination: { style: next_url } }
    output: { select: "response.items" }
`)
	if _, err := manifest.Parse([]byte(doc), manifest.TrustCurated); err == nil || !strings.Contains(err.Error(), "next_url") {
		t.Fatalf("next_url style without an expression must be a load error, got %v", err)
	}
}
