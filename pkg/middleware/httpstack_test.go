package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/auth"
	"github.com/reliant-labs/forge/pkg/observe"
)

func TestHTTPStack_RecoversAndLogs(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	h := HTTPStack(logger, rlClaimsFromContext)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/hook", http.NoBody)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("panic must surface as 500, got %d", rec.Code)
	}
	out := buf.String()
	if !strings.Contains(out, "panic recovered") {
		t.Fatalf("panic must be logged, got: %s", out)
	}
}

//go:noinline
func handlerThatPanics(http.ResponseWriter, *http.Request) { panic("boom") }

func TestHTTPStack_PanicNamesThePanickingFrame(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(observe.NewLogHandler(slog.NewJSONHandler(&buf, nil), observe.Config{}))

	h := HTTPStack(logger, rlClaimsFromContext)(http.HandlerFunc(handlerThatPanics))
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/hook", http.NoBody)
	h.ServeHTTP(httptest.NewRecorder(), req)

	var line map[string]any
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("not JSON: %q", l)
		}
		if m["msg"] == "panic recovered" {
			line = m
		}
	}
	if line == nil {
		t.Fatalf("no panic record in %s", buf.String())
	}
	if got := line["code.function.name"]; got != "github.com/reliant-labs/forge/pkg/middleware.handlerThatPanics" {
		t.Errorf("code.function.name = %v, want the panicking handler, not httpRecovery", got)
	}
	if stack, _ := line["exception.stacktrace"].(string); !strings.Contains(stack, "handlerThatPanics") {
		t.Errorf("exception.stacktrace = %q", stack)
	}
	if line["path"] != "/hook" || line["method"] != "GET" || line["exception.type"] != "string" {
		t.Errorf("attrs: %v", line)
	}
}

func TestHTTPStack_AuditIdentity(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	h := HTTPStack(logger, rlClaimsFromContext)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Anonymous request.
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/hook", http.NoBody)
	h.ServeHTTP(httptest.NewRecorder(), req)
	if !strings.Contains(buf.String(), `"user_id":"anonymous"`) {
		t.Fatalf("anonymous request must audit as anonymous, got: %s", buf.String())
	}

	// Authenticated request — claims read through the project lookup.
	buf.Reset()
	ctx := rlContextWithClaims(context.Background(), &auth.Claims{UserID: "u-9", Email: "u@x.io"})
	req = httptest.NewRequestWithContext(ctx, http.MethodGet, "/hook", http.NoBody)
	h.ServeHTTP(httptest.NewRecorder(), req)
	if !strings.Contains(buf.String(), `"user_id":"u-9"`) {
		t.Fatalf("authenticated request must audit the claim subject, got: %s", buf.String())
	}
	// Identified by id only: an email in a per-request log line is personal
	// data in every line.
	if strings.Contains(buf.String(), "u@x.io") {
		t.Fatalf("the audit log line must not carry the email, got: %s", buf.String())
	}
}

func TestHTTPAuth(t *testing.T) {
	t.Parallel()

	validate := func(token string) (*auth.Claims, error) {
		if token == "good" {
			return &auth.Claims{UserID: "u-1"}, nil
		}
		return nil, errors.New("bad token")
	}

	var seen *auth.Claims
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = rlClaimsFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	h := HTTPAuth(validate, rlContextWithClaims)(next)

	// Missing header → 401.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing Authorization must 401, got %d", rec.Code)
	}

	// Invalid token → 401.
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
	req.Header.Set("Authorization", "Bearer bad")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("invalid token must 401, got %d", rec.Code)
	}

	// Valid token → claims on context.
	req = httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
	req.Header.Set("Authorization", "Bearer good")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid token must pass, got %d", rec.Code)
	}
	if seen == nil || seen.UserID != "u-1" {
		t.Fatalf("claims must reach the handler, got %+v", seen)
	}

	// nil authenticate (dev mode) → passthrough, no header required.
	devH := HTTPAuth(nil, rlContextWithClaims)(next)
	rec = httptest.NewRecorder()
	devH.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("nil authenticate must pass everything through, got %d", rec.Code)
	}
}

// AuditInterceptor: one audit.event record per RPC, WARN + code on
// error, anonymous identity when no claims lookup hits.
func TestAuditInterceptor(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	ic := AuditInterceptor(logger, rlClaimsFromContext)

	// Success path with claims.
	ctx := rlContextWithClaims(context.Background(), &auth.Claims{UserID: "u-7"})
	wrapped := ic.WrapUnary(func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		time.Sleep(time.Millisecond)
		return nil, nil
	})
	if _, err := wrapped(ctx, connect.NewRequest(&struct{}{})); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `"msg":"audit.event"`) || !strings.Contains(out, `"log_type":"audit"`) {
		t.Fatalf("audit record shape changed: %s", out)
	}
	if !strings.Contains(out, `"user_id":"u-7"`) || !strings.Contains(out, `"status":"ok"`) {
		t.Fatalf("audit record must carry identity + status: %s", out)
	}

	// Error path: a rejected request is a user error — INFO, with the
	// connect code.
	buf.Reset()
	wrapped = ic.WrapUnary(func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("nope"))
	})
	_, _ = wrapped(context.Background(), connect.NewRequest(&struct{}{}))
	out = buf.String()
	if !strings.Contains(out, `"level":"INFO"`) || !strings.Contains(out, `"code":"permission_denied"`) {
		t.Fatalf("error audit must be INFO with code: %s", out)
	}
	if !strings.Contains(out, `"user_id":"anonymous"`) {
		t.Fatalf("claims-less audit must log anonymous: %s", out)
	}
}
