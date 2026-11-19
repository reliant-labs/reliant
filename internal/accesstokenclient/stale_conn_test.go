package accesstokenclient

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/observe"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// connFault is how a dead connection fails the request sent on it.
type connFault int32

const (
	faultNone connFault = iota
	// faultWrite: part of the request goes out, then the write fails with
	// EPIPE, and none of it reaches the server.
	faultWrite
	// faultRead: the whole request reaches the server, then the response
	// is lost to ECONNRESET.
	faultRead
)

// faultConn is a connection that fails as a pooled connection the peer closed
// or reset does (prod: "EOF" and "read: connection reset by peer" ~1ms into
// ResolveOrgMembership; CI: a write that failed after the headers went out).
// The fault is injected, not raced against a real close, so every run takes
// the same path.
type faultConn struct {
	net.Conn
	fault atomic.Int32
}

func (c *faultConn) Write(p []byte) (int, error) {
	if connFault(c.fault.Load()) == faultWrite {
		return len(p) / 2, &net.OpError{Op: "write", Net: "tcp", Err: os.NewSyscallError("write", syscall.EPIPE)}
	}
	return c.Conn.Write(p)
}

func (c *faultConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	// Checked after the read returns: the transport's reader sits in Read
	// while the connection idles in the pool, so the fault decides what the
	// server's answer to the next request turns into.
	if connFault(c.fault.Load()) == faultRead {
		return 0, &net.OpError{Op: "read", Net: "tcp", Err: os.NewSyscallError("read", syscall.ECONNRESET)}
	}
	return n, err
}

// faultDialer dials the control plane and keeps every connection, so a test
// can break the pooled one between calls.
type faultDialer struct {
	mu    sync.Mutex
	conns []*faultConn
	// onDial, when set, is the fault every new connection starts with.
	onDial connFault
}

func (d *faultDialer) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	fc := &faultConn{Conn: conn}
	fc.fault.Store(int32(d.onDial))
	d.mu.Lock()
	defer d.mu.Unlock()
	d.conns = append(d.conns, fc)
	return fc, nil
}

// breakPooled makes the one connection dialed so far, now idle in the pool,
// fail the next request sent on it.
func (d *faultDialer) breakPooled(t *testing.T, f connFault) {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.conns) != 1 {
		t.Fatalf("%d connections dialed, want the 1 pooled one", len(d.conns))
	}
	d.conns[0].fault.Store(int32(f))
}

func (d *faultDialer) dials() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.conns)
}

// staleCP is a healthy control plane behind a client whose connections the
// test can break. The client is built the way New builds it (observe's
// otelhttp transport) over the fault-injecting dialer.
func staleCP(t *testing.T, onDial connFault) (Service, *faultDialer, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"orgId":"org-home","member":true}`))
	}))
	t.Cleanup(srv.Close)
	d := &faultDialer{onDial: onDial}
	transport := &http.Transport{DialContext: d.dial}
	t.Cleanup(transport.CloseIdleConnections)
	c := New(Deps{
		BaseURL:    srv.URL,
		Sign:       sign,
		HTTPClient: observe.NewHTTPClient(observe.HTTPClientOptions{Base: transport, Timeout: 10 * time.Second}),
	})
	return c, d, &n
}

// A read is replayable, so a request a dead pooled connection fails is retried
// once on a fresh connection instead of failing the caller's request, whether
// the connection died on the write or on the response.
func TestCall_ReadRetriesADeadKeepAliveConnection(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fault connFault
		// served counts the second request when it reached the server
		// before the connection failed.
		served int32
	}{
		{"write", faultWrite, 2},
		{"read", faultRead, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, d, n := staleCP(t, faultNone)
			ctx := context.Background()

			if _, err := c.ResolveOrgMembership(ctx, "user-1", ""); err != nil {
				t.Fatalf("first call: %v", err)
			}
			d.breakPooled(t, tc.fault)
			got, err := c.ResolveOrgMembership(ctx, "user-1", "")
			if err != nil {
				t.Fatalf("second call on the dead connection was not retried: %v", err)
			}
			if got.OrgID != "org-home" || n.Load() != tc.served || d.dials() != 2 {
				t.Fatalf("got %+v after %d requests on %d connections, want org-home after %d on 2",
					got, n.Load(), d.dials(), tc.served)
			}
		})
	}
}

// A write is never replayed: a revoke whose connection died may have been
// revoked, so the caller sees the failure.
func TestCall_WriteIsNotRetried(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fault  connFault
		served int32
	}{
		{"write", faultWrite, 1},
		{"read", faultRead, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, d, n := staleCP(t, faultNone)
			ctx := context.Background()

			if _, err := c.ResolveOrgMembership(ctx, "user-1", ""); err != nil {
				t.Fatalf("first call: %v", err)
			}
			d.breakPooled(t, tc.fault)
			if err := c.RevokeForUser(ctx, "user-1", "tok-1"); err == nil {
				t.Fatal("a revoke on a dead connection succeeded; it must not be replayed")
			}
			if n.Load() != tc.served || d.dials() != 1 {
				t.Fatalf("server saw %d requests on %d connections, want %d on 1 (no replay)",
					n.Load(), d.dials(), tc.served)
			}
		})
	}
}

// A fresh connection that fails is an outage, not a stale connection: even a
// read is not retried, so the caller learns of it at once.
func TestCall_ReadOnAFreshConnectionIsNotRetried(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fault  connFault
		served int32
	}{
		{"write", faultWrite, 0},
		{"read", faultRead, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, d, n := staleCP(t, tc.fault)
			if _, err := c.ResolveOrgMembership(context.Background(), "user-1", ""); err == nil {
				t.Fatal("a call on a failing fresh connection succeeded")
			}
			if n.Load() != tc.served || d.dials() != 1 {
				t.Fatalf("server saw %d requests on %d connections, want %d on 1 (no retry)",
					n.Load(), d.dials(), tc.served)
			}
		})
	}
}

// The replay allowlist is exactly the methods whose server-side effects were
// shown read-only or idempotent (see replayable). Growing it means showing the
// same for the new method, so a change here must be deliberate.
func TestReplayable_IsExactlyTheVerifiedReads(t *testing.T) {
	want := map[string]bool{
		"Introspect":           true,
		"ListForUser":          true,
		"ResolveOrgMembership": true,
		"ResolveGoneOrgs":      true,
		"ResolveOrgPolicy":     true,
	}
	if len(replayable) != len(want) {
		t.Fatalf("replayable has %d methods, want %d: %v", len(replayable), len(want), replayable)
	}
	for method, ok := range replayable {
		if !ok || !want[method] {
			t.Errorf("replayable[%q] = %v: not one of the verified reads", method, ok)
		}
	}
}

func TestRPCError_ClassFollowsWhoMustAct(t *testing.T) {
	for _, tc := range []struct {
		err  *RPCError
		want svcerr.Class
	}{
		{&RPCError{Code: "failed_precondition"}, svcerr.ClassUser},
		{&RPCError{Code: "not_found"}, svcerr.ClassUser},
		{&RPCError{Code: "resource_exhausted"}, svcerr.ClassUser},
		// The internal-service door refuses OUR bearer as permission_denied
		// with no reason: ours to fix.
		{&RPCError{Code: "permission_denied"}, svcerr.ClassServer},
		{&RPCError{Code: "permission_denied", Reason: "scope_missing"}, svcerr.ClassUser},
		{&RPCError{Code: "unauthenticated"}, svcerr.ClassServer},
		{&RPCError{Code: "unavailable"}, svcerr.ClassServer},
		{&RPCError{Code: "internal"}, svcerr.ClassServer},
		{&RPCError{Code: "canceled"}, svcerr.ClassCanceled},
	} {
		if got := svcerr.Classify(tc.err); got != tc.want {
			t.Errorf("%+v: class %v, want %v", tc.err, got, tc.want)
		}
	}
}
