// Copyright (c) 2025 Reliant Labs
package netguardtest

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/netguard"
)

// network answers with the network of the connection the request came in on.
var network = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	_, _ = io.WriteString(w, r.Context().Value(http.LocalAddrContextKey).(net.Addr).Network())
})

func get(t *testing.T, c *http.Client, url string) string {
	t.Helper()
	resp, err := c.Get(url)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(b)
}

// guardClient is how an integration client is built: the guard's transport,
// trusting the server's certificate.
func guardClient(srv *httptest.Server, g *netguard.Guard) *http.Client {
	tr := g.Transport()
	tr.TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig
	return &http.Client{Transport: tr}
}

func TestGuardReachesTheServerOverItsUnixSocket(t *testing.T) {
	for name, srv := range map[string]*httptest.Server{"tls": NewTLSServer(t, network), "plain": NewServer(t, network)} {
		t.Run(name, func(t *testing.T) {
			url := srv.URL
			assert.Equal(t, "unix", get(t, guardClient(srv, Guard()), url), "the guard's dialer")
			assert.Equal(t, "unix", get(t, srv.Client(), url), "srv.Client()")

			// srv.URL is still a real loopback listener, for a client that
			// dials it directly.
			direct := srv.Client().Transport.(*http.Transport).Clone()
			direct.DialContext = (&net.Dialer{Timeout: 10 * time.Second}).DialContext
			assert.Equal(t, "tcp", get(t, &http.Client{Transport: direct}, url))
		})
	}
}

// Routing the connection does not route around the policy: the address is
// vetted before Dial is called.
func TestGuardStillVetsTheAddress(t *testing.T) {
	srv := NewTLSServer(t, network)
	g := netguard.New()
	g.Dial = Dial
	_, err := guardClient(srv, g).Get(srv.URL)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a public address")
}

// The guard's Timeout bounds a Dial that never connects.
func TestGuardTimeoutBoundsDial(t *testing.T) {
	g := Guard()
	g.Timeout = 50 * time.Millisecond
	g.Dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	start := time.Now()
	_, err := g.DialContext(context.Background(), "tcp", "127.0.0.1:1")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 5*time.Second)
}

func TestCloseUnregistersTheServer(t *testing.T) {
	var addr string
	t.Run("server", func(t *testing.T) {
		srv := NewServer(t, network)
		addr = srv.Listener.Addr().String()
		_, ok := sockets.Load(addr)
		require.True(t, ok)
	})
	_, ok := sockets.Load(addr)
	assert.False(t, ok, "a closed server's address dials as usual")
}
