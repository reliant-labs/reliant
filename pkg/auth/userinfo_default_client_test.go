package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// These tests pass NewUserInfo a nil client, the path that copies
// http.DefaultClient and traces its transport. They swap process globals
// (http.DefaultClient, http.DefaultTransport, the otel tracer provider and
// propagator), so none of them is parallel.

const nilClientToken = "userinfo-secret-token-7f3a"

// withDefaultClient makes c http.DefaultClient for the test.
func withDefaultClient(t *testing.T, c *http.Client) {
	t.Helper()
	old := http.DefaultClient
	http.DefaultClient = c
	t.Cleanup(func() { http.DefaultClient = old })
}

// withTracing installs a recording tracer provider and the W3C propagator as
// the otel globals for the test, and returns the recorder.
func withTracing(t *testing.T) (*tracetest.SpanRecorder, trace.Tracer) {
	t.Helper()
	oldTP, oldProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTracerProvider(oldTP)
		otel.SetTextMapPropagator(oldProp)
		_ = tp.Shutdown(context.Background())
	})
	return rec, tp.Tracer("userinfo-test")
}

// lockedRecorder is recordingTransport made safe for the http.Client's
// goroutines, recording the traceparent header too.
type lockedRecorder struct {
	next http.RoundTripper

	mu          sync.Mutex
	urls        []string
	auth        []string
	traceparent []string
}

func (r *lockedRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.urls = append(r.urls, req.URL.String())
	r.auth = append(r.auth, req.Header.Get("Authorization"))
	r.traceparent = append(r.traceparent, req.Header.Get("traceparent"))
	r.mu.Unlock()
	if r.next == nil {
		return nil, errors.New("lockedRecorder: no network")
	}
	return r.next.RoundTrip(req)
}

func nilClientServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+nilClientToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"sub": "u1", "email": "ann@example.com", "email_verified": true})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A nil client looks the subject up through the application's
// http.DefaultClient transport (or http.DefaultTransport when that is unset),
// as a client span of the request's trace carrying its traceparent, and the
// bearer token appears in no span.
func TestUserInfo_NilClientTracesThroughTheDefaultClient(t *testing.T) {
	for _, via := range []string{"DefaultClient.Transport", "DefaultTransport"} {
		t.Run(via, func(t *testing.T) {
			srv := nilClientServer(t)
			rt := &lockedRecorder{next: srv.Client().Transport}
			if via == "DefaultClient.Transport" {
				withDefaultClient(t, &http.Client{Transport: rt})
			} else {
				withDefaultClient(t, &http.Client{})
				old := http.DefaultTransport
				http.DefaultTransport = rt
				t.Cleanup(func() { http.DefaultTransport = old })
			}
			rec, tracer := withTracing(t)

			a := tokenAuthenticator("u1")
			a.UseUserInfo(NewUserInfo(testIssuer, fixedEndpoint(srv.URL), nil))
			ctx, parent := tracer.Start(context.Background(), "request")
			c, err := a.Authenticate(ctx, nilClientToken)
			parent.End()
			if err != nil {
				t.Fatal(err)
			}
			if c.Email != "ann@example.com" || !c.EmailVerified {
				t.Fatalf("Email = %q (verified %v), want the userinfo address, verified", c.Email, c.EmailVerified)
			}

			traceID := parent.SpanContext().TraceID().String()
			if len(rt.urls) != 1 || rt.auth[0] != "Bearer "+nilClientToken {
				t.Fatalf("requests %v with Authorization %v, want one carrying the bearer token", rt.urls, rt.auth)
			}
			if !strings.Contains(rt.traceparent[0], traceID) {
				t.Errorf("traceparent = %q, want the request's trace %s", rt.traceparent[0], traceID)
			}

			var client sdktrace.ReadOnlySpan
			for _, s := range rec.Ended() {
				if s.SpanKind() == trace.SpanKindClient {
					client = s
				}
				for _, kv := range s.Attributes() {
					if strings.Contains(kv.Value.Emit(), nilClientToken) {
						t.Errorf("span %q attribute %s carries the bearer token", s.Name(), kv.Key)
					}
				}
				for _, ev := range s.Events() {
					for _, kv := range ev.Attributes {
						if strings.Contains(kv.Value.Emit(), nilClientToken) {
							t.Errorf("span %q event %q carries the bearer token", s.Name(), ev.Name)
						}
					}
				}
			}
			if client == nil {
				t.Fatal("no client span recorded for the userinfo lookup")
			}
			if client.SpanContext().TraceID().String() != traceID || client.Parent().SpanID() != parent.SpanContext().SpanID() {
				t.Errorf("client span is not a child of the request's span")
			}
			if want := "GET " + strings.TrimPrefix(srv.URL, "http://"); client.Name() != want {
				t.Errorf("client span name = %q, want %q", client.Name(), want)
			}
		})
	}
}

// The copy keeps the application's DefaultClient settings: its jar, timeout
// and redirect policy.
func TestUserInfo_NilClientKeepsTheDefaultClientsSettings(t *testing.T) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	var redirects int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/moved", http.StatusFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"sub": "u1", "email": "ann@example.com", "email_verified": true})
	}))
	t.Cleanup(srv.Close)
	rt := &lockedRecorder{next: srv.Client().Transport}
	withDefaultClient(t, &http.Client{
		Transport: rt,
		Jar:       jar,
		Timeout:   42 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			redirects++
			return errors.New("the application forbids redirects")
		},
	})

	u := NewUserInfo(testIssuer, fixedEndpoint(srv.URL), nil)
	if u.client.Jar != jar || u.client.Timeout != 42*time.Second {
		t.Errorf("client Jar %v Timeout %v, want the DefaultClient's", u.client.Jar, u.client.Timeout)
	}
	a := tokenAuthenticator("u1")
	a.UseUserInfo(u)
	c, err := a.Authenticate(context.Background(), nilClientToken)
	if err != nil {
		t.Fatal(err)
	}
	if c.Email != "" || redirects != 1 || len(rt.urls) != 1 {
		t.Errorf("Email %q, DefaultClient.CheckRedirect calls %d, requests %v; want the redirect refused by the application's policy", c.Email, redirects, rt.urls)
	}
}

// The secure-endpoint guard holds on the nil-client path: no bearer token
// to an insecure endpoint, and no redirect downgrade to plain HTTP.
func TestUserInfo_NilClientRefusesInsecureEndpointsAndRedirects(t *testing.T) {
	for _, endpoint := range []string{"http://issuer.example.test/userinfo", "http://10.0.0.1/userinfo", "ftp://issuer.example.test/userinfo"} {
		rt := &lockedRecorder{}
		withDefaultClient(t, &http.Client{Transport: rt})
		a := tokenAuthenticator("u1")
		a.UseUserInfo(NewUserInfo(testIssuer, fixedEndpoint(endpoint), nil))
		c, err := a.Authenticate(context.Background(), nilClientToken)
		if err != nil {
			t.Fatal(err)
		}
		if c.Email != "" || len(rt.urls) != 0 {
			t.Errorf("%s: Email %q, requests %v; want no request sent", endpoint, c.Email, rt.urls)
		}
	}

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://issuer.example.test/userinfo", http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	rt := &lockedRecorder{next: srv.Client().Transport}
	withDefaultClient(t, &http.Client{Transport: rt})
	a := tokenAuthenticator("u1")
	a.UseUserInfo(NewUserInfo(testIssuer, fixedEndpoint(srv.URL), nil))
	c, err := a.Authenticate(context.Background(), nilClientToken)
	if err != nil {
		t.Fatal(err)
	}
	if c.Email != "" {
		t.Errorf("Email = %q after a refused redirect", c.Email)
	}
	if len(rt.urls) != 1 || rt.urls[0] != srv.URL {
		t.Errorf("requests = %v, want only the HTTPS endpoint (the redirect to HTTP refused)", rt.urls)
	}
}
