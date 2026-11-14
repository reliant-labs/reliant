package hostinfra

import (
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The postgres probe must be BOUNDED when the declared port is held by
// something that is not postgres.
//
// This is a regression test for a real hang, found while building the
// host-infra observer and reproduced below. It is not an observation-only
// concern: identifyHolder is on the DEPLOY path too — startPostgres calls
// it whenever it cannot find an instance of its own — so the same silent
// hang takes `forge env up` with it.
//
// # The mechanism, because it is not the obvious one
//
// identifyHolder already wraps its query in a 5-second context. That
// bounds the QUERY, and the query never begins: lib/pq blocks in the
// connection HANDSHAKE, and pq documents that a zero or unspecified
// connect_timeout means wait indefinitely. A context on QueryRowContext
// cannot rescue a connection stuck before it.
//
// So the fix is in the DSN (connect_timeout), and a test that only
// asserted "identifyHolder has a context" would have passed against the
// broken code. This one holds the port open with a listener that accepts
// and then says nothing — exactly what a non-postgres service looks like
// to pq — and asserts the probe RETURNS.

// silentListener accepts connections and never writes a byte, holding
// each one open until the test finishes. This is the shape that hangs
// pq: the TCP connect succeeds, so portListening reports the port held,
// and then the postgres startup handshake waits forever for a reply.
func silentListener(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	done := make(chan struct{})
	var conns []net.Conn
	go func() {
		defer close(done)
		for {
			c, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			conns = append(conns, c)
			// Deliberately no read, no write, no close.
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		<-done
		for _, c := range conns {
			_ = c.Close()
		}
	})
	return ln.Addr().(*net.TCPAddr).Port
}

// TestIdentifyHolder_IsBoundedAgainstANonPostgresListener is the control.
//
// SABOTAGE CHECK: remove `connect_timeout` from Spec.baseDSN and this
// test does not fail with a wrong answer — it HANGS until the package
// timeout, which is what the bug did to `forge env up`. The explicit
// deadline below is what turns that hang into a named failure.
func TestIdentifyHolder_IsBoundedAgainstANonPostgresListener(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out real process/probe timeouts; runs in task test")
	}
	port := silentListener(t)
	spec := Spec{
		Name: "postgres", Engine: EnginePostgres, Port: port,
		Database: "app", User: "postgres", Password: "postgres",
	}

	// A budget generously above the 5s connect_timeout and far below any
	// package timeout, so a regression reports as this test failing
	// rather than as the whole package timing out.
	const budget = 20 * time.Second
	result := make(chan holder, 1)
	go func() {
		result <- identifyHolder(context.Background(), spec, t.TempDir())
	}()

	start := time.Now()
	select {
	case got := <-result:
		if got != holderForeign {
			t.Errorf("identifyHolder = %v, want holderForeign — something IS listening on the "+
				"declared port and it is not this instance", got)
		}
		if elapsed := time.Since(start); elapsed > budget {
			t.Errorf("probe took %v, over the %v budget", elapsed, budget)
		}
	case <-time.After(budget):
		t.Fatalf("identifyHolder did not return within %v against a listener that accepts and "+
			"never replies.\nThis is the hang connect_timeout exists to prevent: lib/pq treats "+
			"an unset connect_timeout as 'wait indefinitely', and the context around the QUERY "+
			"cannot bound a handshake that never completes. `forge env up` hangs with no output.",
			budget)
	}
}

// TestBaseDSN_CarriesConnectTimeout pins the fix at its source.
//
// The behavioural test above is the real control; this one exists because
// the behavioural test's failure mode is a 20-second wait, and a
// developer who removes the parameter deserves to be told which line did
// it in milliseconds.
func TestBaseDSN_CarriesConnectTimeout(t *testing.T) {
	spec := Spec{Port: 5432, User: "postgres", Password: "postgres", IDPDatabasePort: 5432}
	want := "connect_timeout=" + strconv.Itoa(probeConnectTimeoutSeconds)
	for name, dsn := range map[string]string{
		"baseDSN":        spec.baseDSN(),
		"zitadelDSNBase": spec.zitadelDSNBase(),
	} {
		if !strings.Contains(dsn, want) {
			t.Errorf("%s = %q, missing %q — without it lib/pq waits indefinitely on a handshake "+
				"that never completes", name, dsn, want)
		}
	}
	if probeConnectTimeoutSeconds <= 0 {
		t.Error("probeConnectTimeoutSeconds must be positive; lib/pq reads zero as 'wait forever'")
	}
}
