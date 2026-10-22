// Copyright (c) 2025 Reliant Labs

// Package netguardtest starts httptest servers that a netguard.Guard reaches
// without a TCP connection, for tests of the integration clients built on it.
//
// A loopback TCP connection needs a slot in the host's conntrack table, which
// every process and container on the machine shares. While that table is full
// the kernel drops new SYNs ("nf_conntrack: table full, dropping packet"), and
// the dial times out however idle the test is: a failure that tracks what else
// the machine is doing, not the code under test. A server started here still
// listens on 127.0.0.1 — so srv.URL, its certificate and the guard's address
// vetting are what they always were — and also on a Unix socket, which netfilter
// never sees. Guard's dialer, and srv.Client(), take the Unix socket.
//
// forge:exclude-contract: test support
package netguardtest

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/reliant-labs/reliant/internal/netguard"
)

// sockets maps the TCP address of each live server here to its Unix socket.
var sockets sync.Map // "127.0.0.1:port" -> socket path

// NewTLSServer is httptest.NewTLSServer, reachable over a Unix socket by Guard
// and by srv.Client(). It closes when t's test ends.
func NewTLSServer(t testing.TB, h http.Handler) *httptest.Server {
	t.Helper()
	srv := newUnstarted(t, h)
	srv.StartTLS()
	routeClient(srv)
	return srv
}

// NewServer is httptest.NewServer, reachable over a Unix socket by Guard and
// by srv.Client(). It closes when t's test ends.
func NewServer(t testing.TB, h http.Handler) *httptest.Server {
	t.Helper()
	srv := newUnstarted(t, h)
	srv.Start()
	routeClient(srv)
	return srv
}

// Guard is netguard.New() for a test that talks to servers started here:
// loopback allowed, and each server reached over its Unix socket.
func Guard() *netguard.Guard {
	g := netguard.New()
	g.AllowLoopback = true
	g.Dial = Dial
	return g
}

// Dial is a netguard.Guard Dial: the address of a server started here is
// reached over its Unix socket, any other is dialed as usual.
func Dial(ctx context.Context, network, address string) (net.Conn, error) {
	var d net.Dialer
	if path, ok := sockets.Load(address); ok {
		return d.DialContext(ctx, "unix", path.(string))
	}
	return d.DialContext(ctx, network, address)
}

func newUnstarted(t testing.TB, h http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(h)
	// os.MkdirTemp, not t.TempDir: a socket path is capped at ~104 bytes, and
	// t.TempDir embeds the test's name.
	dir, err := os.MkdirTemp("", "ngt")
	if err != nil {
		t.Fatalf("netguardtest: %v", err)
	}
	path := filepath.Join(dir, "s")
	unix, err := net.Listen("unix", path)
	if err != nil {
		_ = os.RemoveAll(dir)
		t.Fatalf("netguardtest: %v", err)
	}
	l := newBoth(srv.Listener, unix)
	srv.Listener = l
	addr := l.Addr().String()
	sockets.Store(addr, path)
	t.Cleanup(func() {
		srv.Close()
		sockets.Delete(addr)
		_ = os.RemoveAll(dir)
	})
	return srv
}

// routeClient sends srv.Client() over the Unix socket too.
func routeClient(srv *httptest.Server) {
	srv.Client().Transport.(*http.Transport).DialContext = Dial
}

// both accepts from a TCP and a Unix listener and reports the TCP address,
// so a client that dials srv.URL directly is still served.
type both struct {
	tcp, unix net.Listener
	conns     chan net.Conn
	done      chan struct{}
	close     sync.Once
}

func newBoth(tcp, unix net.Listener) *both {
	b := &both{tcp: tcp, unix: unix, conns: make(chan net.Conn), done: make(chan struct{})}
	go b.feed(tcp)
	go b.feed(unix)
	return b
}

func (b *both) feed(l net.Listener) {
	for {
		c, err := l.Accept()
		if err != nil {
			return // closed; a transient Accept error is not worth a retry loop in a test
		}
		select {
		case b.conns <- c:
		case <-b.done:
			_ = c.Close()
			return
		}
	}
}

func (b *both) Accept() (net.Conn, error) {
	select {
	case c := <-b.conns:
		return c, nil
	case <-b.done:
		return nil, net.ErrClosed
	}
}

func (b *both) Close() error {
	var err error
	b.close.Do(func() {
		close(b.done)
		err = errors.Join(b.tcp.Close(), b.unix.Close())
	})
	return err
}

func (b *both) Addr() net.Addr { return b.tcp.Addr() }
