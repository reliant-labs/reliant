package httpaction

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/reliant-labs/reliant/internal/integrations/manifest"
	"github.com/reliant-labs/reliant/internal/netguard/netguardtest"
	"github.com/reliant-labs/reliant/internal/telemetry"
)

// The integration.call span every authenticated call is made under. The tests
// swap the global tracer provider, so none of them runs in parallel.

// bodyMarker and secretMarker are what a provider's response and a credential
// hold: neither may reach a span.
const (
	bodyMarker   = "body-marker-7f3a"
	secretMarker = "secret-marker-k9"
)

func recordCallSpans(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	return rec
}

// callSpan is the one integration.call span rec ended, with its attributes.
func callSpan(t *testing.T, rec *tracetest.SpanRecorder) (sdktrace.ReadOnlySpan, map[string]string) {
	t.Helper()
	var found []sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		if strings.HasPrefix(s.Name(), telemetry.SpanNameIntegrationCall+" ") {
			found = append(found, s)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want one integration.call span, got %d", len(found))
	}
	s := found[0]
	attrs := map[string]string{}
	for _, kv := range s.Attributes() {
		v := kv.Value.Emit()
		attrs[string(kv.Key)] = v
		if strings.Contains(v, bodyMarker) || strings.Contains(v, secretMarker) {
			t.Errorf("attribute %s carries the response or the credential: %q", kv.Key, v)
		}
	}
	if strings.Contains(s.Status().Description, bodyMarker) || strings.Contains(s.Status().Description, secretMarker) {
		t.Errorf("status carries the response or the credential: %q", s.Status().Description)
	}
	if len(s.Events()) != 0 {
		t.Errorf("a call's span records no events (an error event's text can carry the response): %v", s.Events())
	}
	return s, attrs
}

func wantAttrs(t *testing.T, got map[string]string, want map[string]string) {
	t.Helper()
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

// runCtx is a workflow node's context: a node span, and the run scope the
// worker's interceptor binds.
func runCtx(t *testing.T, org string) (context.Context, trace.Span) {
	t.Helper()
	ctx, node := otel.Tracer("test").Start(context.Background(), "workflow.node fetch")
	t.Cleanup(func() { node.End() })
	return telemetry.WithRunScope(ctx, telemetry.RunScope{
		UserID: "user-1", ChatID: "chat-1", WorkflowID: "run-1", OrgID: org,
		WorkflowName: "pr-review", RunID: "run-1", NodeID: "fetch", NodeType: "action",
	}), node
}

func tenantAction(t *testing.T) (*Runner, func(ctx context.Context, src CredentialSource, site CallSite) (*Result, error), *http.ServeMux) {
	t.Helper()
	mux := http.NewServeMux()
	srv := netguardtest.NewTLSServer(t, mux)
	m, err := manifest.Parse([]byte(tenantManifest), manifest.TrustCurated)
	if err != nil {
		t.Fatal(err)
	}
	r := tenantRunner(t, srv)
	return r, func(ctx context.Context, src CredentialSource, site CallSite) (*Result, error) {
		return r.RunAuthenticated(ctx, m, m.GetActions()[0], map[string]any{"id": "7"}, src, site)
	}, mux
}

var blueCred = fakeCred{id: "conn_1", secret: secretMarker, params: map[string]string{"tenant": "blue"}}

// A call the provider answers is a client span under the node's span, named
// by the integration and its action, carrying the run and the provider's rate
// limit from the response's headers, and nothing of the response itself.
func TestIntegrationCallSpan_ASuccessfulCallCarriesTheRunAndTheRateLimit(t *testing.T) {
	rec := recordCallSpans(t)
	_, call, mux := tenantAction(t)
	mux.HandleFunc("/api/things/7", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "4990")
		w.Header().Set("X-RateLimit-Used", "10")
		w.Header().Set("X-RateLimit-Reset", "1800003600")
		w.Header().Set("X-RateLimit-Resource", "core")
		_, _ = w.Write([]byte(`{"title":"` + bodyMarker + `"}`))
	})
	ctx, node := runCtx(t, "org-1")

	res, err := call(ctx, &fixedSource{cred: blueCred}, CallSite{RunID: "run-1", NodeID: "fetch"})
	if err != nil || res.IsError {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	s, got := callSpan(t, rec)
	if s.Name() != "integration.call acme.thing.get" {
		t.Errorf("name = %q", s.Name())
	}
	if s.SpanKind() != trace.SpanKindClient {
		t.Errorf("kind = %v", s.SpanKind())
	}
	if s.Parent().SpanID() != node.SpanContext().SpanID() {
		t.Error("the call's span is not a child of the node's")
	}
	if s.Status().Code != codes.Unset {
		t.Errorf("status = %v", s.Status())
	}
	wantAttrs(t, got, map[string]string{
		telemetry.AttrIntegrationID:                 "acme",
		telemetry.AttrIntegrationOperation:          "thing.get",
		telemetry.AttrIntegrationUses:               "acme/thing.get@1",
		telemetry.AttrIntegrationOutcome:            "ok",
		telemetry.AttrIntegrationConnectionID:       "conn_1",
		telemetry.AttrIntegrationPages:              "1",
		telemetry.AttrIntegrationTruncated:          "false",
		telemetry.AttrHTTPResponseStatusCode:        "200",
		telemetry.AttrServerAddress:                 "blue.acme.example.com",
		telemetry.AttrIntegrationRateLimitLimit:     "5000",
		telemetry.AttrIntegrationRateLimitRemaining: "4990",
		telemetry.AttrIntegrationRateLimitUsed:      "10",
		telemetry.AttrIntegrationRateLimitReset:     "1800003600",
		telemetry.AttrIntegrationRateLimitResource:  "core",
		telemetry.AttrOrgID:                         "org-1",
		telemetry.AttrUserID:                        "user-1",
		telemetry.AttrGenAIConversationID:           "chat-1",
		telemetry.AttrAIRunID:                       "run-1",
		telemetry.AttrAIWorkflowName:                "pr-review",
		telemetry.AttrAINodeID:                      "fetch",
	})
	for _, k := range []string{telemetry.AttrIntegrationErrorCode, telemetry.AttrIntegrationRetryable, telemetry.AttrIntegrationRateLimitRetryAfter} {
		if _, ok := got[k]; ok {
			t.Errorf("%s set on a success", k)
		}
	}
}

// A failure the provider answers is outcome error, code http_<status>, with
// the retry verdict; the status is described by the code, never the body.
func TestIntegrationCallSpan_AnAnsweredFailure(t *testing.T) {
	for _, tc := range []struct {
		status        int
		header        map[string]string
		wantRetryable string
		want          map[string]string
	}{
		{status: 404, wantRetryable: "false"},
		{status: 429, header: map[string]string{"Retry-After": "30", "RateLimit-Remaining": "0"}, wantRetryable: "true",
			want: map[string]string{telemetry.AttrIntegrationRateLimitRetryAfter: "30", telemetry.AttrIntegrationRateLimitRemaining: "0"}},
	} {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) {
			rec := recordCallSpans(t)
			_, call, mux := tenantAction(t)
			mux.HandleFunc("/api/things/7", func(w http.ResponseWriter, _ *http.Request) {
				for k, v := range tc.header {
					w.Header().Set(k, v)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"message":"` + bodyMarker + `"}`))
			})
			ctx, _ := runCtx(t, "org-1")
			res, err := call(ctx, &fixedSource{cred: blueCred}, CallSite{RunID: "run-1"})
			if err != nil || !res.IsError {
				t.Fatalf("res=%+v err=%v", res, err)
			}
			s, got := callSpan(t, rec)
			code := "http_" + strconv.Itoa(tc.status)
			if s.Status().Code != codes.Error || s.Status().Description != code {
				t.Errorf("status = %v", s.Status())
			}
			wantAttrs(t, got, map[string]string{
				telemetry.AttrIntegrationOutcome:     "error",
				telemetry.AttrIntegrationErrorCode:   code,
				telemetry.AttrErrorType:              code,
				telemetry.AttrIntegrationRetryable:   tc.wantRetryable,
				telemetry.AttrHTTPResponseStatusCode: strconv.Itoa(tc.status),
			})
			wantAttrs(t, got, tc.want)
		})
	}
}

// A call that never reached the provider says why by its code alone: no
// credential, the org's policy refused it, or the caller hung up.
func TestIntegrationCallSpan_CallsThatSentNothing(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for name, tc := range map[string]struct {
		ctx        context.Context
		src        CredentialSource
		want       map[string]string
		wantStatus codes.Code
	}{
		"no credential": {
			src:        &fixedSource{},
			want:       map[string]string{telemetry.AttrIntegrationOutcome: "credential_error", telemetry.AttrIntegrationErrorCode: CodeFailedPrecondition},
			wantStatus: codes.Error,
		},
		"refused by the org's policy": {
			src:        &gatedSource{refuse: map[string]bool{"acme": true}},
			want:       map[string]string{telemetry.AttrIntegrationOutcome: "refused", telemetry.AttrIntegrationErrorCode: "integration_not_allowed"},
			wantStatus: codes.Error,
		},
		"canceled": {
			ctx:        canceled,
			src:        &fixedSource{cred: blueCred},
			want:       map[string]string{telemetry.AttrIntegrationOutcome: "canceled", telemetry.AttrErrorType: telemetry.ErrorTypeCanceled},
			wantStatus: codes.Unset,
		},
	} {
		t.Run(name, func(t *testing.T) {
			rec := recordCallSpans(t)
			_, call, mux := tenantAction(t)
			mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
				t.Error("the call reached the provider")
			})
			ctx := tc.ctx
			if ctx == nil {
				ctx = context.Background()
			}
			if _, err := call(ctx, tc.src, CallSite{RunID: "run-1"}); err == nil {
				t.Fatal("want an error")
			}
			s, got := callSpan(t, rec)
			if s.Status().Code != tc.wantStatus {
				t.Errorf("status = %v", s.Status())
			}
			if strings.Contains(s.Status().Description, " ") {
				t.Errorf("status description is text, not a code: %q", s.Status().Description)
			}
			wantAttrs(t, got, tc.want)
		})
	}
}

// orgSource is a credential source that can say which org a run belongs to.
type orgSource struct {
	fixedSource
	org   string
	err   error
	asked []string
}

func (s *orgSource) RunOrgOf(_ context.Context, runID string) (string, error) {
	s.asked = append(s.asked, runID)
	return s.org, s.err
}

// A run whose scope names no org (its chat has none) is attributed to the org
// recorded on the run, which the source is asked for. A scope that names one
// is never second-guessed, and a call with no run is not looked up.
func TestIntegrationCallSpan_OrgOfTheRun(t *testing.T) {
	for name, tc := range map[string]struct {
		scopeOrg  string
		runID     string
		org       string
		err       error
		wantOrg   string
		wantAsked int
	}{
		"the scope's org":              {scopeOrg: "org-chat", runID: "run-1", org: "org-run", wantOrg: "org-chat"},
		"the run's org, when unscoped": {runID: "run-1", org: "org-run", wantOrg: "org-run", wantAsked: 1},
		"a run of no org":              {runID: "run-1", wantAsked: 1},
		"a failed lookup":              {runID: "run-1", org: "org-run", err: errors.New("db down"), wantAsked: 1},
		"no run":                       {org: "org-run"},
	} {
		t.Run(name, func(t *testing.T) {
			rec := recordCallSpans(t)
			_, call, mux := tenantAction(t)
			mux.HandleFunc("/api/things/7", func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{}`))
			})
			ctx, _ := runCtx(t, tc.scopeOrg)
			src := &orgSource{fixedSource: fixedSource{cred: blueCred}, org: tc.org, err: tc.err}
			if _, err := call(ctx, src, CallSite{RunID: tc.runID}); err != nil {
				t.Fatal(err)
			}
			_, got := callSpan(t, rec)
			if got[telemetry.AttrOrgID] != tc.wantOrg {
				t.Errorf("org.id = %q, want %q", got[telemetry.AttrOrgID], tc.wantOrg)
			}
			if len(src.asked) != tc.wantAsked {
				t.Errorf("source asked %v", src.asked)
			}
		})
	}
}

const subRequestManifest = `
id: acme
version: 1
display_name: Acme
connection:
  base_url: https://api.acme.example.com
  auth:
    - api_key: { in: header, name: X-Key }
actions:
  - id: merge
    placement: server
    executor: go:acme.test_merge
    params: { type: object }
  - id: probe
    placement: server
    params: { type: object, properties: { n: { type: string } } }
    request: { method: GET, path: "/probe/{{ params.n }}" }
`

// A go: executor that makes its requests through Do (GitHub's pr.merge, over
// GraphQL) reports them as its call's: how many, and the last one's host and
// rate limit.
func TestIntegrationCallSpan_AnExecutorsSubRequests(t *testing.T) {
	rec := recordCallSpans(t)
	mux := http.NewServeMux()
	srv := netguardtest.NewTLSServer(t, mux)
	mux.HandleFunc("/probe/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", strings.TrimPrefix(r.URL.Path, "/probe/"))
		w.Header().Set("X-RateLimit-Resource", "graphql")
		_, _ = w.Write([]byte(`{}`))
	})
	m, err := manifest.Parse([]byte(subRequestManifest), manifest.TrustCurated)
	if err != nil {
		t.Fatal(err)
	}
	reg := NewExecutorRegistry()
	if err := reg.Register("acme.test_merge", func(ctx context.Context, c ExecutorCall) (*Result, error) {
		for _, n := range []string{"99", "98"} {
			if _, err := c.Do(ctx, m.GetActions()[1], map[string]any{"n": n}); err != nil {
				return nil, err
			}
		}
		return &Result{Data: map[string]any{"merged": true}, StatusCode: 200}, nil
	}); err != nil {
		t.Fatal(err)
	}
	r := tenantRunner(t, srv).WithExecutors(reg)
	if _, err := r.RunAuthenticated(context.Background(), m, m.GetActions()[0], map[string]any{}, &fixedSource{cred: blueCred}, CallSite{RunID: "run-1"}); err != nil {
		t.Fatal(err)
	}
	s, got := callSpan(t, rec)
	if s.Name() != "integration.call acme.merge" {
		t.Errorf("name = %q", s.Name())
	}
	wantAttrs(t, got, map[string]string{
		telemetry.AttrIntegrationOutcome:            "ok",
		telemetry.AttrIntegrationPages:              "2",
		telemetry.AttrServerAddress:                 "api.acme.example.com",
		telemetry.AttrIntegrationRateLimitRemaining: "98",
		telemetry.AttrIntegrationRateLimitResource:  "graphql",
	})
}

// The unauthenticated door (Run) is spanned too.
func TestIntegrationCallSpan_Run(t *testing.T) {
	rec := recordCallSpans(t)
	srv := netguardtest.NewTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Limit", "60")
		_, _ = w.Write([]byte(`{}`))
	}))
	m, a := mustParse(t, manifestFor(srv.URL, "    params: { type: object }\n    request:\n      method: GET\n      path: /x\n"))
	r := newRunner()
	r.client.Transport.(*http.Transport).TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig
	if _, err := r.Run(context.Background(), m, a, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	s, got := callSpan(t, rec)
	if s.Name() != "integration.call t.a" {
		t.Errorf("name = %q", s.Name())
	}
	wantAttrs(t, got, map[string]string{
		telemetry.AttrIntegrationOutcome:        "ok",
		telemetry.AttrIntegrationUses:           "t/a@1",
		telemetry.AttrIntegrationRateLimitLimit: "60",
	})
}
