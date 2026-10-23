package lease_test

import (
	"context"
	"net"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/lease"
)

// link is a TCP proxy between a Locker and postgres. It delays every reply
// from the server by a fixed latency, which is what a CPU-starved client sees
// (the answer arrives long after the query was sent), and it can go silent,
// which is what a dead network looks like: the connection stays open and
// nothing comes back.
type link struct {
	addr    string
	latency time.Duration
	silent  atomic.Bool
	closed  atomic.Bool
	// refuse drops every NEW connection on accept: a client that is cut off
	// from the database cannot open a fresh session. Existing connections
	// are untouched.
	refuse atomic.Bool

	mu    sync.Mutex
	conns []net.Conn
}

func newLink(t *testing.T, dsn string, latency time.Duration) (*link, string) {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	upstream := u.Host
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	k := &link{addr: ln.Addr().String(), latency: latency}
	t.Cleanup(func() {
		k.closed.Store(true)
		_ = ln.Close()
		k.mu.Lock()
		defer k.mu.Unlock()
		for _, c := range k.conns {
			_ = c.Close()
		}
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			if k.refuse.Load() {
				_ = c.Close()
				continue
			}
			s, err := net.Dial("tcp", upstream)
			if err != nil {
				_ = c.Close()
				continue
			}
			k.mu.Lock()
			k.conns = append(k.conns, c, s)
			k.mu.Unlock()
			go k.pipe(s, c, false) // client -> server
			go k.pipe(c, s, true)  // server -> client: late, and gated by silent
		}
	}()
	u.Host = k.addr
	return k, u.String()
}

// pipe copies src to dst. Replies are held for the latency, and held
// indefinitely while the link is silent; queries always go through, so a
// client that hangs up is seen by the server, which then drops the session.
// Either side closing closes both.
func (k *link) pipe(dst, src net.Conn, reply bool) {
	defer func() { _ = dst.Close(); _ = src.Close() }()
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 && reply {
			time.Sleep(k.latency)
			for k.silent.Load() && !k.closed.Load() {
				time.Sleep(5 * time.Millisecond)
			}
		}
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// A healthy session whose replies arrive late, here several heartbeat
// intervals late on every query, must keep its leases. Before the query
// timeout was separated from the heartbeat interval, each heartbeat's
// deadline was the interval itself: the reply missed it, the driver closed
// the connection, and every lease on the session was dropped. That is how a
// CPU-starved CI runner (or pod) lost all twenty leases of
// TestManyLeasesOnOneLockerSurviveConcurrentPollingAndHeartbeats.
func TestSlowRepliesDoNotLoseAHealthySession(t *testing.T) {
	dsn := testDSN(t)
	const latency = 150 * time.Millisecond
	_, slow := newLink(t, dsn, latency)
	opt := lease.Options{RetryInterval: 20 * time.Millisecond, HeartbeatInterval: 30 * time.Millisecond, Logger: quiet}
	if latency < 4*opt.HeartbeatInterval {
		t.Fatal("the reply latency must dwarf the heartbeat interval, or this proves nothing")
	}

	a := &replica{l: lease.New(pool(t, slow), opt), done: make(chan error, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); _ = a.l.Close() })
	go func() {
		a.done <- a.l.Run(ctx, "slow", func(ctx context.Context) error {
			a.starts.Add(1)
			a.leading.Store(true)
			defer a.leading.Store(false)
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	eventually(t, "a to lead over the slow link", a.leading.Load)

	// Each heartbeat now takes five intervals to answer; let several of them
	// complete.
	time.Sleep(8 * latency)
	if !a.leading.Load() || a.starts.Load() != 1 {
		t.Fatalf("leading=%v starts=%d: a slow reply was treated as a lost session", a.leading.Load(), a.starts.Load())
	}
	if h := a.l.Held(); len(h) != 1 {
		t.Fatalf("Held() = %v, want [slow]", h)
	}
}

// The query timeout still detects a network that has stopped answering: the
// leader's job is cancelled within one heartbeat plus one query timeout, and
// the server, seeing the connection close, frees the lease for a standby.
func TestSilentNetworkLosesTheLeaseWithinTheQueryTimeout(t *testing.T) {
	dsn := testDSN(t)
	k, via := newLink(t, dsn, 0)
	opt := lease.Options{RetryInterval: 20 * time.Millisecond, HeartbeatInterval: 30 * time.Millisecond, QueryTimeout: 300 * time.Millisecond, Logger: quiet}

	a := &replica{l: lease.New(pool(t, via), opt), done: make(chan error, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); _ = a.l.Close() })
	go func() {
		a.done <- a.l.Run(ctx, "partitioned", func(ctx context.Context) error {
			a.starts.Add(1)
			a.leading.Store(true)
			defer a.leading.Store(false)
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	eventually(t, "a to lead", a.leading.Load)

	k.silent.Store(true)
	silenced := time.Now()
	bound := opt.HeartbeatInterval + opt.QueryTimeout
	for a.leading.Load() {
		if time.Since(silenced) > bound+2*time.Second {
			t.Fatalf("a still led %v after the network went silent (bound %v)", time.Since(silenced), bound)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if took := time.Since(silenced); took < opt.QueryTimeout/2 {
		t.Fatalf("lease lost after %v, before the query timeout could have expired", took)
	}

	b := start(t, dsn, "partitioned", nil)
	eventually(t, "a standby to take the lease the dead session held", b.leading.Load)
}
