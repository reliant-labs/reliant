package oauth2

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
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

// fakeBroker is the broker half of a handoff: start records the request,
// /consent (the "page") redirects the browser to the loopback with a code,
// redeem checks the verifier against the RECORDED challenge itself.
type fakeBroker struct {
	mu                         sync.Mutex
	challenge, redirect, state string
	method                     string
	stateOverride              string
	verifier                   string
	traceparents               map[string]string
}

func (b *fakeBroker) server(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.traceparents != nil {
			b.traceparents[r.URL.Path] = r.Header.Get("traceparent")
		}
		switch r.URL.Path {
		case "/auth/handoff/start":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			b.challenge, b.redirect, b.state, b.method = body["code_challenge"], body["redirect_uri"], body["state"], body["code_challenge_method"]
			_ = json.NewEncoder(w).Encode(map[string]string{"authorize_url": srv.URL + "/consent?req=r1"})
		case "/consent":
			state := b.state
			if b.stateOverride != "" {
				state = b.stateOverride
			}
			http.Redirect(w, r, b.redirect+"?"+url.Values{"code": {"fhc_1"}, "state": {state}}.Encode(), http.StatusSeeOther)
		case "/auth/handoff/redeem":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			b.verifier = body["code_verifier"]
			sum := sha256.Sum256([]byte(body["code_verifier"]))
			if base64.RawURLEncoding.EncodeToString(sum[:]) != b.challenge || body["redirect_uri"] != b.redirect || body["code"] != "fhc_1" {
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"code":"handoff_invalid","error":"The code is not valid."}`))
				return
			}
			_, _ = fmt.Fprintf(w, `{"kind":"native","session":"fs_new","token_type":"Bearer","expires_at":%q}`,
				time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestHandoffLoginRoundTrip(t *testing.T) {
	b := &fakeBroker{}
	srv := b.server(t)
	var out strings.Builder
	got, err := HandoffLogin{Base: srv.URL + "/", OpenURL: browser, Out: &out, Timeout: 10 * time.Second}.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Session != "fs_new" || !got.ExpiresAt.Equal(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("session %#v %v", got.Session, got.ExpiresAt)
	}
	if b.method != "S256" || !strings.HasPrefix(b.redirect, "http://127.0.0.1:") || !strings.HasSuffix(b.redirect, "/callback") {
		t.Errorf("start sent method %q redirect %q", b.method, b.redirect)
	}
	if strings.Contains(fmt.Sprint(*got), "fs_new") || strings.Contains(fmt.Sprintf("%#v", *got), "fs_new") {
		t.Error("the session credential does not redact itself")
	}
	if !strings.Contains(out.String(), "/consent?req=r1") {
		t.Errorf("the notice does not name the URL: %q", out.String())
	}
}

// Keep this serial: the default client uses the process-wide OTel provider.
func TestHandoffLoginDefaultClientTracesWithoutCredentials(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	previousTP, previousProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTracerProvider(previousTP)
		otel.SetTextMapPropagator(previousProp)
		if err := tp.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})

	b := &fakeBroker{traceparents: make(map[string]string)}
	srv := b.server(t)
	ctx, parent := tp.Tracer("handoff-test").Start(context.Background(), "login")
	// No HTTPClient override: reverting HandoffLogin's default to an ordinary
	// http.Client must lose both client spans and trace propagation here.
	got, err := (HandoffLogin{Base: srv.URL, OpenURL: browser, Timeout: 10 * time.Second}).Run(ctx)
	parent.End()
	if err != nil {
		t.Fatal(err)
	}
	if got.Session != "fs_new" {
		t.Fatal("handoff did not return the broker session")
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	spans := exporter.GetSpans()
	clientSpans := make(map[string]bool)
	for _, span := range spans {
		if span.SpanKind != trace.SpanKindClient {
			continue
		}
		if span.Parent.SpanID() != parent.SpanContext().SpanID() || span.SpanContext.TraceID() != parent.SpanContext().TraceID() {
			t.Error("handoff client span is not a child of the caller")
		}
		carrier := propagation.MapCarrier{}
		propagation.TraceContext{}.Inject(trace.ContextWithSpanContext(context.Background(), span.SpanContext), carrier)
		clientSpans[carrier.Get("traceparent")] = true
	}
	if len(clientSpans) != 2 {
		t.Fatalf("got %d client spans, want start and redeem", len(clientSpans))
	}
	for _, path := range []string{"/auth/handoff/start", "/auth/handoff/redeem"} {
		if header := b.traceparents[path]; header == "" || !clientSpans[header] {
			t.Errorf("%s did not propagate its client span", path)
		}
	}

	// Serialize the complete exported span stubs (including attributes,
	// events, links, status and resource), not only selected URL attributes.
	telemetry, err := json.Marshal(spans)
	if err != nil {
		t.Fatal(err)
	}
	if b.verifier == "" {
		t.Fatal("broker did not receive a verifier")
	}
	for name, secret := range map[string]string{"verifier": b.verifier, "code": "fhc_1", "session": got.Session} {
		if strings.Contains(string(telemetry), secret) {
			t.Errorf("exported handoff telemetry contains the %s credential", name)
		}
	}
}

func TestHandoffLoginRefusesAForeignState(t *testing.T) {
	b := &fakeBroker{stateOverride: "someone-elses"}
	srv := b.server(t)
	_, err := HandoffLogin{Base: srv.URL, OpenURL: browser, Timeout: 10 * time.Second}.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "state") {
		t.Fatalf("a callback with another state was accepted: %v", err)
	}
}

func TestHandoffLoginReportsTheBrokersRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"code":"handoff_invalid","error":"redirect_uri is not allowed"}`))
	}))
	t.Cleanup(srv.Close)
	_, err := HandoffLogin{Base: srv.URL, Timeout: time.Second}.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "redirect_uri is not allowed") {
		t.Fatalf("err = %v", err)
	}
	if _, err := (HandoffLogin{}).Run(context.Background()); err == nil {
		t.Fatal("a handoff with no base ran")
	}
}
