// Copyright (c) 2025 Reliant Labs
package streamabort

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

// The property every user relies on: over HTTP/2, with the client still
// connected and sending nothing, Abort wakes a read parked on the request
// body — which cancelling the request's context does not.
func TestAbortWakesAReadParkedOnTheRequestBody(t *testing.T) {
	type outcome struct {
		installed  bool
		cancelWoke bool
		readErr    error
	}
	got := make(chan outcome, 1)
	h := Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()

		readDone := make(chan error, 1)
		go func() {
			_, err := r.Body.Read(make([]byte, 1))
			readDone <- err
		}()

		// A cancelled context alone leaves the read parked.
		ctx, cancel := context.WithCancel(r.Context())
		cancel()
		<-ctx.Done()
		o := outcome{installed: Installed(r.Context())}
		select {
		case <-readDone:
			o.cancelWoke = true
		case <-time.After(100 * time.Millisecond):
		}

		require.True(t, Abort(r.Context()))
		require.True(t, Abort(r.Context()), "Abort is idempotent")
		select {
		case o.readErr = <-readDone:
		case <-time.After(3 * time.Second):
		}
		got <- o
	}))
	srv := httptest.NewUnstartedServer(h2c.NewHandler(h, &http2.Server{}))
	srv.Start()
	t.Cleanup(srv.Close)

	// The request body is a pipe the test never writes to: a client that is
	// connected and silent.
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	req, err := http.NewRequest(http.MethodPost, srv.URL, pr)
	require.NoError(t, err)
	cl := &http.Client{Transport: &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}}
	resp, err := cl.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	select {
	case o := <-got:
		require.True(t, o.installed)
		require.False(t, o.cancelWoke, "a cancelled context woke the read; Abort would not be needed")
		require.Error(t, o.readErr, "Abort did not wake the parked read")
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not finish")
	}
}

func TestAbortWithoutHandlerReportsFalse(t *testing.T) {
	require.False(t, Installed(context.Background()))
	require.False(t, Abort(context.Background()))
}
