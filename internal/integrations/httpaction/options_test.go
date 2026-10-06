package httpaction

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/manifest"
)

// The per-call request options (timeout, body/response format, redirects)
// are what the generic HTTP action hands its caller, so they are tested
// through the manifest the binary ships: http/request@1.

func builtinHTTPRequest(t *testing.T) (*reliantv1.IntegrationManifest, *reliantv1.ActionSpec) {
	t.Helper()
	a, err := catalog.MustBuiltin().Resolve("http/request@1")
	if err != nil {
		t.Fatal(err)
	}
	return a.Manifest, a.Spec
}

func runHTTP(t *testing.T, params map[string]any) (*Result, error) {
	t.Helper()
	m, a := builtinHTTPRequest(t)
	return newRunner().Run(context.Background(), m, a, params)
}

func TestHTTPRequestTimeoutParam(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)

	start := time.Now()
	res, err := runHTTP(t, map[string]any{"url": srv.URL, "timeout": 1.0})
	if err == nil && !res.IsError {
		t.Fatal("a hung server must time out")
	}
	// The default is 30s, so finishing near 1s proves the param took effect.
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("timeout 1 took %s", elapsed)
	}

	for _, bad := range []any{0.0, 121.0} {
		if _, err := runHTTP(t, map[string]any{"url": srv.URL, "timeout": bad}); err == nil {
			t.Errorf("timeout %v must be refused", bad)
		}
	}
}

func TestTimeoutExprMustYieldSecondsInRange(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) }))
	defer srv.Close()
	// No schema bounds here, so the runner is the backstop.
	m, a := mustParse(t, manifestFor(srv.URL, "    params: { type: object }\n    request: { method: GET, path: /x, timeout_expr: params.t }\n"))
	r := newRunner()
	r.client.Transport.(*http.Transport).TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig
	for _, bad := range []any{0.0, 500.0, "ten"} {
		if _, err := r.Run(context.Background(), m, a, map[string]any{"t": bad}); err == nil || !strings.Contains(err.Error(), "timeout_expr") {
			t.Errorf("t=%v: want a timeout_expr error, got %v", bad, err)
		}
	}
	if res, err := r.Run(context.Background(), m, a, map[string]any{"t": 5.0}); err != nil || res.IsError {
		t.Fatalf("t=5: %v %+v", err, res)
	}
}

func TestHTTPRequestResponseFormat(t *testing.T) {
	var body, contentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	for _, tc := range []struct {
		name, format, body, contentType string
		wantError                       bool
		wantJSON                        any
	}{
		{"auto parses JSON", "", `{"ok":true}`, "application/json", false, map[string]any{"ok": true}},
		{"auto keeps HTML as text only", "auto", "<html>hi</html>", "text/html", false, nil},
		{"json parses JSON", "json", `[1,2]`, "application/json", false, []any{1.0, 2.0}},
		{"json refuses HTML", "json", "<html>login</html>", "text/html", true, nil},
		{"json allows an empty body", "json", "", "", false, nil},
		{"text never parses", "text", `{"ok":true}`, "application/json", false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, contentType = tc.body, tc.contentType
			params := map[string]any{"url": srv.URL}
			if tc.format != "" {
				params["response_format"] = tc.format
			}
			res, err := runHTTP(t, params)
			if err != nil {
				t.Fatal(err)
			}
			if res.IsError != tc.wantError {
				t.Fatalf("IsError = %v, want %v: %s", res.IsError, tc.wantError, res.Content)
			}
			if tc.wantError {
				if !strings.Contains(res.Content, "not JSON") || !strings.Contains(res.Content, "text/html") || res.Retryable {
					t.Errorf("want a permanent not-JSON error naming the content type, got %+v", res)
				}
				return
			}
			if got := res.Data["json"]; !equalJSON(got, tc.wantJSON) {
				t.Errorf("json = %#v, want %#v", got, tc.wantJSON)
			}
			if res.Data["text"] != tc.body {
				t.Errorf("text = %#v, want the raw body %q", res.Data["text"], tc.body)
			}
		})
	}
}

func equalJSON(a, b any) bool {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k := range av {
			if !equalJSON(av[k], bv[k]) {
				return false
			}
		}
		return true
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !equalJSON(av[i], bv[i]) {
				return false
			}
		}
		return true
	default:
		return a == b
	}
}

func TestHTTPRequestBodyType(t *testing.T) {
	var gotType, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotType = r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	for _, tc := range []struct {
		name     string
		params   map[string]any
		wantType string
		wantBody string
	}{
		{"json by default", map[string]any{"body": map[string]any{"a": "b c"}}, "application/json", `{"a":"b c"}`},
		{"form", map[string]any{"body_type": "form", "body": map[string]any{"a": "b c", "n": 2.0}}, "application/x-www-form-urlencoded", "a=b+c&n=2"},
		{"text sends a string as-is", map[string]any{"body_type": "text", "body": "<order id=\"7\"/>"}, "text/plain; charset=utf-8", `<order id="7"/>`},
		{"a Content-Type header wins", map[string]any{"body_type": "text", "body": "<order/>", "headers": map[string]any{"Content-Type": "application/xml"}}, "application/xml", "<order/>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]any{"url": srv.URL, "method": "POST"}
			for k, v := range tc.params {
				params[k] = v
			}
			res, err := runHTTP(t, params)
			if err != nil {
				t.Fatal(err)
			}
			if res.IsError {
				t.Fatalf("unexpected error: %s", res.Content)
			}
			if gotType != tc.wantType || gotBody != tc.wantBody {
				t.Errorf("sent %q %q, want %q %q", gotType, gotBody, tc.wantType, tc.wantBody)
			}
		})
	}

	if _, err := runHTTP(t, map[string]any{"url": srv.URL, "method": "POST", "body_type": "text", "body": map[string]any{"a": 1.0}}); err == nil || !strings.Contains(err.Error(), "text body") {
		t.Errorf("an object as a text body must be refused, got %v", err)
	}
	if _, err := runHTTP(t, map[string]any{"url": srv.URL, "body_type": "xml"}); err == nil {
		t.Error("an unknown body_type must be refused by the param schema")
	}
}

func TestHTTPRequestFollowRedirects(t *testing.T) {
	var landed atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/old", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/new", http.StatusFound) })
	mux.HandleFunc("/new", func(w http.ResponseWriter, _ *http.Request) {
		landed.Add(1)
		_, _ = w.Write([]byte(`{"page":"new"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	res, err := runHTTP(t, map[string]any{"url": srv.URL + "/old"})
	if err != nil || res.IsError {
		t.Fatalf("followed redirect: %v %+v", err, res)
	}
	if landed.Load() != 1 || res.StatusCode != http.StatusOK {
		t.Fatalf("default must follow: landed=%d status=%d", landed.Load(), res.StatusCode)
	}

	res, err = runHTTP(t, map[string]any{"url": srv.URL + "/old", "follow_redirects": false})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("a returned redirect is the result, not a failure: %s", res.Content)
	}
	if landed.Load() != 1 {
		t.Error("follow_redirects false must not request the target")
	}
	headers, _ := res.Data["headers"].(map[string]any)
	if res.StatusCode != http.StatusFound || headers["location"] != "/new" {
		t.Errorf("want the 302 and its Location, got status %d headers %v", res.StatusCode, headers)
	}
}

// redirects: return never requests the Location, so a redirect to a host the
// rules would refuse is reported as the answer instead of an error.
func TestReturnedRedirectIsNotFollowedOffHost(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://169.254.169.254/latest/meta-data", http.StatusFound)
	}))
	defer srv.Close()
	m, a := mustParse(t, manifestFor(srv.URL, "    params: { type: object }\n    request: { method: GET, path: /r, redirects: return }\n"))
	r := newRunner()
	r.client.Transport.(*http.Transport).TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig
	res, err := r.Run(context.Background(), m, a, nil)
	if err != nil || res.IsError || res.StatusCode != http.StatusFound {
		t.Fatalf("redirects: return yields the 302 itself: %v %+v", err, res)
	}
}

func TestLoaderValidatesRequestOptions(t *testing.T) {
	for _, tc := range []struct{ field, value string }{
		{"response_format", "xml"},
		{"redirects", "maybe"},
		{"body_format", "yaml"},
		{"response_format", "{{ params. }}"},
	} {
		doc := manifestFor("https://api.example.com", "    params: { type: object }\n    request: { method: GET, path: /x, "+tc.field+": \""+tc.value+"\" }\n")
		if _, err := manifest.Parse([]byte(doc), manifest.TrustCurated); err == nil || !strings.Contains(err.Error(), tc.field) {
			t.Errorf("%s %q must be a load error naming the field, got %v", tc.field, tc.value, err)
		}
	}
	doc := manifestFor("https://api.example.com", "    params: { type: object }\n    request: { method: GET, path: /x, timeout_expr: \"params.\" }\n")
	if _, err := manifest.Parse([]byte(doc), manifest.TrustCurated); err == nil || !strings.Contains(err.Error(), "timeout_expr") {
		t.Errorf("a bad timeout_expr must be a load error, got %v", err)
	}
	ok := manifestFor("https://api.example.com", "    params: { type: object }\n    request: { method: GET, path: /x, response_format: \"{{ params.f }}\", redirects: return, body_format: text }\n")
	if _, err := manifest.Parse([]byte(ok), manifest.TrustCurated); err != nil {
		t.Errorf("valid options must load: %v", err)
	}
}

// A template that renders to a value outside the set is refused per call.
func TestTemplatedOptionOutsideTheSetIsRefused(t *testing.T) {
	m, a := mustParse(t, manifestFor("https://api.example.com", "    params: { type: object }\n    request: { method: GET, path: /x, response_format: \"{{ params.f }}\" }\n"))
	if _, err := newRunner().Run(context.Background(), m, a, map[string]any{"f": "xml"}); err == nil || !strings.Contains(err.Error(), "response_format") {
		t.Errorf("want a response_format error, got %v", err)
	}
}
