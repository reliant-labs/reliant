// Copyright (c) 2025 Reliant Labs
package grpc

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
)

// serveCleartext starts an http.Server on a loopback port using the same
// protocol configuration NewServer/NewDaemonServer use for their no-TLS path,
// and returns its base URL.
func serveCleartext(t *testing.T, handler http.Handler) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	srv := &http.Server{
		Handler:           handler,
		Protocols:         cleartextHTTP2Protocols(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		_ = srv.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = srv.Close()
		<-serveDone
	})

	return fmt.Sprintf("http://%s", listener.Addr().String())
}

// priorKnowledgeClient dials cleartext HTTP/2 directly, without an
// "Upgrade: h2c" handshake — the same shape every Reliant HTTP/2 client uses
// (see internal/toolexec/transport.NewDaemonHTTPClient and
// internal/servergateway.h2cClient).
func priorKnowledgeClient() *http.Client {
	return &http.Client{
		Transport: &http2.Transport{
			AllowHTTP: true,
			DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, network, addr)
			},
		},
	}
}

// TestCleartextHTTP2ServesPriorKnowledgeClients pins the h2c replacement: a
// server configured with cleartextHTTP2Protocols must answer a prior-knowledge
// HTTP/2 client over HTTP/2, not downgrade it to HTTP/1.1.
func TestCleartextHTTP2ServesPriorKnowledgeClients(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/echo-proto", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Proto)
	})
	baseURL := serveCleartext(t, securityHeaders(mux))

	client := priorKnowledgeClient()
	t.Cleanup(client.CloseIdleConnections)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/echo-proto", nil)
	require.NoError(t, err)

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "HTTP/2.0", resp.Proto, "server must serve cleartext HTTP/2, not downgrade to HTTP/1.1")
	require.Equal(t, "HTTP/2.0", string(body), "the handler must see the request as HTTP/2")

	// h2c.NewHandler hijacked the connection and dispatched to its *inner*
	// handler, so middleware wrapping it from the outside never ran. net/http
	// routes cleartext HTTP/2 through srv.Handler, so it does.
	require.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"),
		"securityHeaders must apply to HTTP/2 requests")
	require.Equal(t, "DENY", resp.Header.Get("X-Frame-Options"))
}

// TestCleartextHTTP2StillServesHTTP1 confirms HTTP/1.1 clients (browsers, the
// /health probe) keep working on the same port.
func TestCleartextHTTP2StillServesHTTP1(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/echo-proto", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Proto)
	})
	baseURL := serveCleartext(t, securityHeaders(mux))

	client := &http.Client{Timeout: 10 * time.Second}
	t.Cleanup(client.CloseIdleConnections)

	resp, err := client.Get(baseURL + "/echo-proto")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "HTTP/1.1", string(body))
	require.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"))
}
