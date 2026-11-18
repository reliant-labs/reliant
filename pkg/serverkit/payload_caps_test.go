package serverkit_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/reliant-labs/forge/pkg/serverkit"
)

// payload_caps_test.go — the Connect payload cap is a SECURITY control, and
// its failure mode is silent.
//
// connect-go documents a zero ReadMaxBytes/SendMaxBytes as UNLIMITED. The
// generated composition root builds its handler options from a
// serverkit.Config BEFORE serverkit.Run is entered, so a cap read off an
// un-normalized Config reaches connect as 0 and the server buffers + decodes
// whatever an anonymous caller sends — a 300 MiB request costs ~1.4 GB of RSS
// against a 1 GiB pod limit. Nothing errors; the server just OOMKills.
//
// These tests pin the behaviour on both sides of Normalize: un-normalized is
// genuinely unlimited (the bug, kept as an executable negative control so the
// hazard is never mistaken for theoretical), normalized rejects oversized
// bodies before the handler runs.

const echoProcedure = "/serverkit.test.v1.EchoService/Echo"

// echoServer mounts a real Connect unary handler with the supplied options —
// the same construction the generated serve.go performs — and records whether
// the handler was ever entered.
func echoServer(t *testing.T, opts ...connect.HandlerOption) (*httptest.Server, *bool) {
	t.Helper()
	handlerRan := false
	handler := connect.NewUnaryHandler(
		echoProcedure,
		func(_ context.Context, req *connect.Request[structpb.Struct]) (*connect.Response[structpb.Struct], error) {
			handlerRan = true
			return connect.NewResponse(req.Msg), nil
		},
		opts...,
	)
	mux := http.NewServeMux()
	mux.Handle(echoProcedure, handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &handlerRan
}

// postJSON sends a Connect-protocol JSON unary request whose single string
// field is `size` bytes of filler.
func postJSON(t *testing.T, srv *httptest.Server, size int) (int, string) {
	t.Helper()
	body := `{"value":"` + strings.Repeat("A", size) + `"}`
	req, err := http.NewRequest(http.MethodPost, srv.URL+echoProcedure, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, string(out)
}

// TestPayloadCaps_UnnormalizedConfigIsUnlimited is the negative control: it
// asserts the exact hazard. A Config whose caps were never filled in hands
// connect a 0, and connect then accepts a body far larger than the 4 MiB the
// Config's documentation promises. If connect-go ever changes zero to mean
// "reject everything" this test fails loudly and the guard below can be
// simplified — until then, zero means unlimited and Normalize is load-bearing.
func TestPayloadCaps_UnnormalizedConfigIsUnlimited(t *testing.T) {
	var cfg serverkit.Config // never normalized — exactly what a pre-Run read sees
	if cfg.ReadMaxBytes != 0 {
		t.Fatalf("precondition: zero Config should have ReadMaxBytes == 0, got %d", cfg.ReadMaxBytes)
	}

	srv, handlerRan := echoServer(t, connect.WithReadMaxBytes(cfg.ReadMaxBytes))
	status, body := postJSON(t, srv, 8<<20) // 8 MiB — double the documented cap

	if status != http.StatusOK || !*handlerRan {
		t.Fatalf("expected the uncapped server to ACCEPT an 8 MiB body (status 200, handler entered); got status=%d handlerRan=%v body=%.200s",
			status, *handlerRan, body)
	}
}

// TestPayloadCaps_NormalizedConfigRejectsOversizedBody is the fix: the same
// Config, normalized, rejects the same body — and rejects it BEFORE the
// handler runs, which is the property that matters (the body never reaches
// application code, so no amount of handler-side validation is required).
func TestPayloadCaps_NormalizedConfigRejectsOversizedBody(t *testing.T) {
	var cfg serverkit.Config
	cfg.Normalize()

	if cfg.ReadMaxBytes != 4<<20 {
		t.Fatalf("Normalize should default ReadMaxBytes to 4 MiB, got %d", cfg.ReadMaxBytes)
	}
	if cfg.SendMaxBytes != 4<<20 {
		t.Fatalf("Normalize should default SendMaxBytes to 4 MiB, got %d", cfg.SendMaxBytes)
	}

	srv, handlerRan := echoServer(t, connect.WithReadMaxBytes(cfg.ReadMaxBytes))
	status, body := postJSON(t, srv, 8<<20)

	if status == http.StatusOK {
		t.Errorf("capped server accepted an 8 MiB body (status 200); body=%.200s", body)
	}
	if *handlerRan {
		t.Error("capped server let an oversized body reach the handler — the cap must reject before decode")
	}
	if !strings.Contains(body, "resource_exhausted") {
		t.Errorf("expected a resource_exhausted Connect error, got status=%d body=%.300s", status, body)
	}
}

// TestPayloadCaps_NormalizedConfigAllowsNormalBody guards the other direction:
// the cap must not be so eager that ordinary traffic breaks.
func TestPayloadCaps_NormalizedConfigAllowsNormalBody(t *testing.T) {
	var cfg serverkit.Config
	cfg.Normalize()

	srv, handlerRan := echoServer(t, connect.WithReadMaxBytes(cfg.ReadMaxBytes))
	status, body := postJSON(t, srv, 1<<10) // 1 KiB

	if status != http.StatusOK || !*handlerRan {
		t.Fatalf("a 1 KiB body must be accepted; got status=%d handlerRan=%v body=%.200s", status, *handlerRan, body)
	}
}

// TestConfigNormalize_IsIdempotentAndPreservesExplicitValues makes sure the
// composition root can normalize early without stomping operator-supplied
// values, and that Run's own second Normalize is a no-op.
func TestConfigNormalize_IsIdempotentAndPreservesExplicitValues(t *testing.T) {
	cfg := serverkit.Config{ReadMaxBytes: 1 << 20, SendMaxBytes: 32 << 20}
	cfg.Normalize()
	cfg.Normalize()

	if cfg.ReadMaxBytes != 1<<20 {
		t.Errorf("Normalize overwrote an explicit ReadMaxBytes: got %d, want %d", cfg.ReadMaxBytes, 1<<20)
	}
	if cfg.SendMaxBytes != 32<<20 {
		t.Errorf("Normalize overwrote an explicit SendMaxBytes: got %d, want %d", cfg.SendMaxBytes, 32<<20)
	}
	if cfg.DBDriver != "pgx" {
		t.Errorf("Normalize should default DBDriver to pgx, got %q", cfg.DBDriver)
	}
}
