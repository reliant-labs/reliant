package observe_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/reliant-labs/forge/pkg/observe"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// leak_test.go — what an unauthenticated caller can read off a 500.
//
// Both of the paths tested here published server internals verbatim,
// because connect.Error.Message() is err.Error() and neither path ever
// substituted a safe string:
//
//   - a repository error handed to svcerr.Wrap returned a planted DSN,
//     password and all, in the response body;
//   - a panic returned "panic: assignment to entry in nil map".
//
// These tests drive a real Connect handler over HTTP and assert on the
// bytes that actually reach the socket, because that is where the defect
// was visible and where a message-construction change could reintroduce it.

const leakProcedure = "/observe.test.v1.LeakService/Do"

const (
	leakSecret    = "dsn=postgres://app:s3cr3t@db.internal:5432/prod"
	leakPanicText = "assignment to entry in nil map"
)

// leakServer mounts a Connect handler whose implementation fails the way the
// caller asks, behind the standard observe chain.
func leakServer(t *testing.T, logger *slog.Logger, impl func() error) *httptest.Server {
	t.Helper()
	handler := connect.NewUnaryHandler(
		leakProcedure,
		func(_ context.Context, _ *connect.Request[structpb.Struct]) (*connect.Response[structpb.Struct], error) {
			if err := impl(); err != nil {
				return nil, err
			}
			return connect.NewResponse(&structpb.Struct{}), nil
		},
		connect.WithInterceptors(observe.Chain(observe.Deps{Logger: logger})...),
	)
	mux := http.NewServeMux()
	mux.Handle(leakProcedure, handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func callLeak(t *testing.T, srv *httptest.Server) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+leakProcedure, strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req) //nolint:bodyclose // closed below
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, string(body)
}

// TestWire_RepositoryErrorDoesNotLeakInternals follows the documented handler
// idiom exactly — `return nil, svcerr.Wrap(err)` on a raw driver error — and
// asserts the response body an anonymous caller receives.
func TestWire_RepositoryErrorDoesNotLeakInternals(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	srv := leakServer(t, logger, func() error {
		return svcerr.Wrap(errors.New(`ERROR: relation "accounts" does not exist (SQLSTATE 42P01) ` + leakSecret))
	})

	status, body := callLeak(t, srv)

	if status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", status, body)
	}
	if strings.Contains(body, leakSecret) {
		t.Errorf("the response body handed the caller a database connection string:\n%s", body)
	}
	if strings.Contains(body, "SQLSTATE") || strings.Contains(body, "relation") {
		t.Errorf("the response body handed the caller schema/driver detail:\n%s", body)
	}
	if !strings.Contains(body, svcerr.InternalMessage) {
		t.Errorf("expected the fixed %q message, got:\n%s", svcerr.InternalMessage, body)
	}

	// Sanitize the wire, never the log.
	if !strings.Contains(logs.String(), leakSecret) {
		t.Errorf("the diagnostic reached neither the client nor the log — it exists nowhere:\n%s", logs.String())
	}
}

// TestWire_PanicDoesNotLeakInternals covers the other half: the recovery
// interceptor shipped the recovered value to the client.
func TestWire_PanicDoesNotLeakInternals(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	srv := leakServer(t, logger, func() error {
		var m map[string]string
		m["boom"] = leakPanicText // panics: assignment to entry in nil map
		return nil
	})

	status, body := callLeak(t, srv)

	if status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", status, body)
	}
	if strings.Contains(body, "panic") || strings.Contains(body, leakPanicText) {
		t.Errorf("the response body described the server's crash to the caller:\n%s", body)
	}
	if !strings.Contains(body, svcerr.InternalMessage) {
		t.Errorf("expected the fixed %q message, got:\n%s", svcerr.InternalMessage, body)
	}

	// The panic and its stack must still be fully recorded server-side.
	out := logs.String()
	if !strings.Contains(out, "panic recovered") || !strings.Contains(out, leakPanicText) {
		t.Errorf("panic detail missing from the log:\n%s", out)
	}
}

// TestWire_DeliberateMessagesStillReachTheClient guards against over-redaction:
// the codes an application chooses carry messages that are part of its API, and
// a client that can no longer tell "user: not found" from "internal server
// error" is worse off, not safer.
func TestWire_DeliberateMessagesStillReachTheClient(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))

	srv := leakServer(t, logger, func() error {
		return svcerr.Wrap(svcerr.NotFound("user"))
	})

	status, body := callLeak(t, srv)

	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", status, body)
	}
	if !strings.Contains(body, "not found") {
		t.Errorf("a deliberate NotFound message must reach the client, got:\n%s", body)
	}
}
