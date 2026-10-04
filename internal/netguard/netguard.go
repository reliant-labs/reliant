// Package netguard refuses outbound connections to private, loopback,
// link-local and cloud-metadata addresses. The check runs in the dialer, on
// the IP actually being connected to, so a hostname that resolves to a private
// address is refused too (a URL-string check cannot see that), as is a DNS
// rebind between lookup and connect. It must not import os/exec.
package netguard

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

var blockedNets = mustCIDRs(
	"0.0.0.0/8",     // "this" network
	"100.64.0.0/10", // carrier-grade NAT
	"192.0.0.0/24",  // IETF protocol assignments
	"198.18.0.0/15", // benchmarking
	"240.0.0.0/4",   // reserved
	"64:ff9b::/96",  // NAT64
	"fc00::/7",      // unique local
)

func mustCIDRs(cidrs ...string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic(err)
		}
		out = append(out, n)
	}
	return out
}

// IPBlocked reports whether ip is not a routable public address.
func IPBlocked(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return true
	}
	for _, n := range blockedNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// Guard dials only public addresses.
type Guard struct {
	// AllowLoopback permits 127.0.0.0/8 and ::1. For tests that talk to httptest servers only.
	AllowLoopback bool
	Timeout       time.Duration

	// Resolve overrides DNS lookup; tests use it to make a hostname resolve to a chosen address.
	Resolve func(ctx context.Context, host string) ([]net.IP, error)
}

// New returns a Guard with a 10s connect timeout.
func New() *Guard {
	return &Guard{Timeout: 10 * time.Second}
}

func (g *Guard) blocked(ip net.IP) bool {
	if g.AllowLoopback && ip.IsLoopback() {
		return false
	}
	return IPBlocked(ip)
}

// DialContext resolves the host, refuses if ANY answer is blocked, and
// connects to the vetted IP directly.
func (g *Guard) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	var ips []net.IP
	if literal := net.ParseIP(host); literal != nil {
		ips = []net.IP{literal}
	} else {
		resolve := g.Resolve
		if resolve == nil {
			resolve = func(ctx context.Context, h string) ([]net.IP, error) {
				return net.DefaultResolver.LookupIP(ctx, "ip", h)
			}
		}
		if ips, err = resolve(ctx, host); err != nil {
			return nil, err
		}
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("netguard: %q resolved to no addresses", host)
	}
	for _, ip := range ips {
		if g.blocked(ip) {
			return nil, fmt.Errorf("netguard: connection to %s (%s) blocked: not a public address", host, ip)
		}
	}
	dialer := &net.Dialer{Timeout: g.Timeout}
	var lastErr error
	for _, ip := range ips {
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// Transport returns an http.Transport that dials through the guard and never
// uses an ambient proxy (a proxy would dial on our behalf, unguarded).
func (g *Guard) Transport() *http.Transport {
	return &http.Transport{
		DialContext:           g.DialContext,
		Proxy:                 nil,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		MaxIdleConns:          10,
		IdleConnTimeout:       30 * time.Second,
	}
}
