// Copyright (c) 2026 Reliant Labs
//go:build unix

package daemonruntime

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"testing"
)

// These tests need a loopback port that nothing else on the machine can
// listen on for their duration. A port that was free a moment ago is not
// one: while TestPreviewForwarder_IPv6OnlyUpstream listened on [::1]:<p>
// only, a parallel package's NATS server took 127.0.0.1:<p>, the
// forwarder's first dial reached it, and the test read NATS's INFO line as
// a malformed HTTP response (reliant #2479). So they hold the port with a
// socket that is bound and never listens: no other socket can bind it, and
// a dial to it is refused exactly as when nothing listens.

// holdUnlistened binds ip:port (port 0: one the kernel picks) with a socket
// that never listens, held until the test ends, and returns the port. The
// error wraps syscall.EADDRINUSE when the port is taken.
func holdUnlistened(t *testing.T, ip net.IP, port int) (int, error) {
	t.Helper()
	family := syscall.AF_INET6
	var sa syscall.Sockaddr
	if v4 := ip.To4(); v4 != nil {
		family = syscall.AF_INET
		a := &syscall.SockaddrInet4{Port: port}
		copy(a.Addr[:], v4)
		sa = a
	} else {
		a := &syscall.SockaddrInet6{Port: port}
		copy(a.Addr[:], ip.To16())
		sa = a
	}
	fd, err := syscall.Socket(family, syscall.SOCK_STREAM, 0)
	if err != nil {
		return 0, fmt.Errorf("socket: %w", err)
	}
	syscall.CloseOnExec(fd)
	// No SO_REUSEADDR: with it unset on this socket, no other socket can
	// bind the same address and port, listening or not.
	if err := syscall.Bind(fd, sa); err != nil {
		_ = syscall.Close(fd)
		return 0, fmt.Errorf("bind %s:%d: %w", ip, port, err)
	}
	t.Cleanup(func() { _ = syscall.Close(fd) })
	bound, err := syscall.Getsockname(fd)
	if err != nil {
		return 0, fmt.Errorf("getsockname: %w", err)
	}
	switch a := bound.(type) {
	case *syscall.SockaddrInet4:
		return a.Port, nil
	case *syscall.SockaddrInet6:
		return a.Port, nil
	}
	return 0, fmt.Errorf("getsockname: unexpected address %T", bound)
}

func TestPreviewForwarder_IPv6OnlyUpstream(t *testing.T) {
	// Bind ::1 ONLY — the original production failure mode (vite on ::1:5174).
	// The same port on 127.0.0.1 is held unlistened, so the forwarder's first
	// dial is refused and it must fall back to ::1.
	var (
		ln   net.Listener
		port int
	)
	for range 20 {
		l, err := net.Listen("tcp6", "[::1]:0")
		if err != nil {
			t.Skipf("IPv6 loopback unavailable: %v", err)
		}
		p := l.Addr().(*net.TCPAddr).Port
		if _, err := holdUnlistened(t, net.IPv4(127, 0, 0, 1), p); err == nil {
			ln, port = l, p
			break
		} else if !errors.Is(err, syscall.EADDRINUSE) {
			_ = l.Close()
			t.Fatal(err)
		}
		_ = l.Close()
	}
	if ln == nil {
		t.Fatal("no port was free on both ::1 and 127.0.0.1 in 20 tries")
	}
	if other, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
		_ = other.Close()
		t.Fatalf("another listener could take the held 127.0.0.1:%d", port)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "v6 says hi")
	})}
	go srv.Serve(ln)
	defer srv.Close()

	base := startForwarder(t)
	resp, err := http.Get(fmt.Sprintf("%s/%d/", base, port))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "v6 says hi" {
		t.Errorf("status = %d, body = %q; want 200 %q", resp.StatusCode, body, "v6 says hi")
	}
}

func TestPreviewForwarder_NothingListening502(t *testing.T) {
	// A port held unlistened on 127.0.0.1, and on ::1 where there is IPv6:
	// the forwarder dials both, and nothing can listen on either meanwhile.
	deadPort := 0
	for range 20 {
		p, err := holdUnlistened(t, net.IPv4(127, 0, 0, 1), 0)
		if err != nil {
			t.Fatal(err)
		}
		_, err = holdUnlistened(t, net.IPv6loopback, p)
		if err == nil || !errors.Is(err, syscall.EADDRINUSE) {
			// Bound, or no IPv6 loopback here, so no dial to [::1] succeeds.
			deadPort = p
			break
		}
	}
	if deadPort == 0 {
		t.Fatal("no port was free on both 127.0.0.1 and ::1 in 20 tries")
	}

	base := startForwarder(t)
	resp, err := http.Get(fmt.Sprintf("%s/%d/", base, deadPort))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
	if !strings.Contains(string(body), fmt.Sprintf("nothing is listening on port %d", deadPort)) {
		t.Errorf("502 body = %q, want actionable nothing-listening message", body)
	}
}
