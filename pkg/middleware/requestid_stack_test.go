package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/reliant-labs/forge/pkg/observe"
)

// requestid_stack_test.go — the request ID must survive the WHOLE edge.
//
// The HTTP middleware and the Connect interceptor each looked correct in
// isolation, and each had passing unit tests. Composed the way serverkit
// composes them they disagreed: the HTTP layer minted an ID onto the
// response header and the context; the interceptor then read the INBOUND
// request header (which the HTTP layer never wrote), found it empty, and
// minted a second. The response carried two X-Request-Id values, every
// standard client read the first, and only the second reached the logs —
// so the single most common incident workflow ("customer quotes an ID, you
// grep for it") returned nothing.
//
// Nothing short of an assembled-stack test can catch that, which is what
// this file is.

const stackProcedure = "/middleware.test.v1.EchoService/Echo"

// assembledEdge builds the real edge: HTTP request-id middleware on the
// outside (as serverkit wires it) and a Connect handler carrying the full
// observe chain — request-id, logging and audit — on the inside.
func assembledEdge(t *testing.T, logger *slog.Logger) http.Handler {
	t.Helper()
	handler := connect.NewUnaryHandler(
		stackProcedure,
		func(_ context.Context, req *connect.Request[structpb.Struct]) (*connect.Response[structpb.Struct], error) {
			return connect.NewResponse(req.Msg), nil
		},
		connect.WithInterceptors(observe.Chain(observe.Deps{
			Logger: logger,
			Audit:  AuditInterceptor(logger, nil),
		})...),
	)
	mux := http.NewServeMux()
	mux.Handle(stackProcedure, handler)
	return RequestIDMiddleware()(mux)
}

func callEdge(t *testing.T, h http.Handler, inbound string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, stackProcedure, strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	if inbound != "" {
		req.Header.Set(RequestIDHeader, inbound)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("edge returned %d, body=%s", rec.Code, rec.Body.String())
	}
	return rec
}

// logRecords parses the JSON log lines the stack emitted, keyed by msg.
func logRecords(t *testing.T, buf *bytes.Buffer) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line is not JSON: %q (%v)", line, err)
		}
		msg, _ := rec["msg"].(string)
		out[msg] = rec
	}
	return out
}

// TestRequestID_SingleHeaderOnResponse pins the client-visible half: exactly
// one X-Request-Id comes back. Two values on one response is not a cosmetic
// problem — it makes "the ID we gave the client" ambiguous, and the two
// layers disagreed about which one that was.
func TestRequestID_SingleHeaderOnResponse(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))

	rec := callEdge(t, assembledEdge(t, logger), "")

	got := rec.Header().Values(RequestIDHeader)
	if len(got) != 1 {
		t.Fatalf("expected exactly one %s on the response, got %d: %v", RequestIDHeader, len(got), got)
	}
	if got[0] == "" {
		t.Fatalf("%s must not be empty", RequestIDHeader)
	}
}

// TestRequestID_ClientHeaderMatchesLogs is the incident workflow itself: take
// the ID off the response exactly as a client would, and require every log
// record for that call to carry it. This is what failed in production — the
// client's ID appeared in no log line at all.
func TestRequestID_ClientHeaderMatchesLogs(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	rec := callEdge(t, assembledEdge(t, logger), "")
	clientID := rec.Header().Get(RequestIDHeader)

	records := logRecords(t, &buf)
	for _, msg := range []string{"rpc completed", auditMessage} {
		record, ok := records[msg]
		if !ok {
			t.Fatalf("no %q log record emitted; got %v", msg, keys(records))
		}
		logged, _ := record["request_id"].(string)
		if logged == "" {
			t.Errorf("%q record carries no request_id — it cannot be joined to the id the client holds", msg)
			continue
		}
		if logged != clientID {
			t.Errorf("%q logged request_id=%q but the client was given %q — grepping the client's id finds nothing",
				msg, logged, clientID)
		}
	}
}

// TestRequestID_InboundHeaderIsPropagated covers the cross-hop case: an edge
// proxy or calling service supplies the ID and both layers must adopt it
// rather than mint over it.
func TestRequestID_InboundHeaderIsPropagated(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	const upstream = "upstream-correlation-id"
	rec := callEdge(t, assembledEdge(t, logger), upstream)

	got := rec.Header().Values(RequestIDHeader)
	if len(got) != 1 || got[0] != upstream {
		t.Fatalf("inbound %s must be adopted and echoed once, got %v", RequestIDHeader, got)
	}
	records := logRecords(t, &buf)
	for _, msg := range []string{"rpc completed", auditMessage} {
		if logged, _ := records[msg]["request_id"].(string); logged != upstream {
			t.Errorf("%q logged request_id=%q, want the upstream id %q", msg, logged, upstream)
		}
	}
}

// TestRequestID_InterceptorAloneStillEchoes guards the fix from over-reaching.
// The interceptor stops writing the response header only because an OUTER
// layer already owns it. Mounted without the HTTP middleware — a bare Connect
// mux — it must still mint and echo, or a whole deployment shape silently
// loses correlation IDs.
func TestRequestID_InterceptorAloneStillEchoes(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))

	handler := connect.NewUnaryHandler(
		stackProcedure,
		func(_ context.Context, req *connect.Request[structpb.Struct]) (*connect.Response[structpb.Struct], error) {
			return connect.NewResponse(req.Msg), nil
		},
		connect.WithInterceptors(observe.Chain(observe.Deps{Logger: logger})...),
	)
	mux := http.NewServeMux()
	mux.Handle(stackProcedure, handler)

	rec := callEdge(t, mux, "") // no RequestIDMiddleware in front

	got := rec.Header().Values(RequestIDHeader)
	if len(got) != 1 || got[0] == "" {
		t.Fatalf("a bare Connect mux must still echo exactly one %s, got %v", RequestIDHeader, got)
	}
}

func keys(m map[string]map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
