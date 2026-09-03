// Copyright (c) 2025 Reliant Labs
package servergateway

import (
	"fmt"
	"net"
	"net/http"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// freePort reserves a port, reads it, and releases it. startPprofServer takes
// an address and returns nothing, so there is no way to ask it which port it
// bound — ":0" would bind a port the test could never discover. Reserving and
// releasing is the available approximation; the poll in the serving test
// absorbs the gap between release and re-bind.
func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}

// An unset PPROF_ADDR is the default in every environment, so the no-op path is
// the one that runs in production. It must not listen, must not leak a
// goroutine, and must not panic.
func TestStartPprofServer_EmptyAddrIsNoOp(t *testing.T) {
	t.Parallel()
	before := runtime.NumGoroutine()

	require.NotPanics(t, func() {
		startPprofServer("")
		startPprofServer("   ")
		startPprofServer("\t\n")
	})

	// The early return happens before the `go func()`, so no goroutine is ever
	// spawned. Poll rather than sample once: an unrelated goroutine from
	// another test in this package could otherwise be misread as a leak.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if runtime.NumGoroutine() <= before {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutine count grew from %d to %d: an empty addr must not start a server",
				before, runtime.NumGoroutine())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The other half: when PPROF_ADDR does name an address, the profiler is
// actually reachable there. A debug surface that silently fails to serve is
// the same defect as one that never existed.
func TestStartPprofServer_ServesWhenAddrSet(t *testing.T) {
	t.Parallel()
	addr := freePort(t)
	startPprofServer(addr)

	client := &http.Client{Timeout: 2 * time.Second}
	url := fmt.Sprintf("http://%s/debug/pprof/", addr)

	// The listener comes up asynchronously inside the goroutine.
	var resp *http.Response
	var err error
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err = client.Get(url)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.NoError(t, err, "pprof server never became reachable on %s", addr)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
}

// The load-bearing property. A profiler that cannot bind — a port already
// taken, a permission problem — must be logged and forgotten, never allowed to
// take the gateway down. ListenAndServe runs in its own goroutine, so a panic
// there would kill the whole test binary rather than fail an assertion; the
// proof of survival is that the process goes on to serve a later profiler.
func TestStartPprofServer_BindFailureIsNotFatal(t *testing.T) {
	t.Parallel()
	// Hold the address ourselves so the pprof listener is guaranteed to lose.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()

	require.NotPanics(t, func() {
		startPprofServer(ln.Addr().String())
	})

	// Let the doomed ListenAndServe reach its error inside the goroutine.
	time.Sleep(250 * time.Millisecond)

	// Still standing: a fresh profiler on a free port still comes up. If the
	// failed bind had panicked or torn anything down, we would not get here.
	addr := freePort(t)
	startPprofServer(addr)

	client := &http.Client{Timeout: 2 * time.Second}
	url := fmt.Sprintf("http://%s/debug/pprof/", addr)

	var resp *http.Response
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err = client.Get(url)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.NoError(t, err, "gateway did not survive a pprof bind failure")
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}
