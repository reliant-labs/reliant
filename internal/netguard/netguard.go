// Copyright (c) 2025 Reliant Labs
//
//forge:exclude-contract: a stateless network-policy helper with no I/O contract of its own
package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// ErrBlockedAddress is returned (wrapped) when a destination is refused by
// policy. Callers show it to the user as-is; it never mentions internals
// beyond the address the user themselves typed.
var ErrBlockedAddress = errors.New("address is not allowed")

// Policy decides which network destinations a user-supplied URL may reach.
//
// A hosted deployment runs one shared worker for every tenant, so a URL a user
// types must not be able to reach that worker's own network: loopback, the
// cluster's private ranges, or the cloud metadata service. A self-hosted
// deployment runs on the user's own machine or network, where those addresses
// are exactly what the user means (an Ollama on localhost, a vLLM on the LAN).
type Policy struct {
	// AllowPrivate permits loopback, private, link-local and metadata
	// addresses. False is the hosted setting.
	AllowPrivate bool

	// LookupIP resolves a hostname; nil uses the system resolver. Tests
	// inject it to simulate DNS answers (including rebinding).
	LookupIP func(ctx context.Context, host string) ([]net.IP, error)
}

// ForDeployment is the policy for user-supplied model endpoint URLs, chosen
// from the deployment kind. controlPlaneURL is tokenauthority.ControlPlaneURL():
// the same signal that makes the token authority choose its store. A
// configured control plane means a HOSTED, multi-tenant deployment, where the
// shared worker must not be pointed at its own network. None means a
// self-hosted deployment on the user's own machine or network, where localhost
// and LAN addresses are exactly what they mean. One signal, not a second flag,
// so a hosted deployment cannot ship open by forgetting to set it.
func ForDeployment(controlPlaneURL string) Policy {
	return Policy{AllowPrivate: controlPlaneURL == ""}
}

func (p Policy) lookup(ctx context.Context, host string) ([]net.IP, error) {
	if p.LookupIP != nil {
		return p.LookupIP(ctx, host)
	}
	return net.DefaultResolver.LookupIP(ctx, "ip", host)
}

var blockedPrefixes = mustPrefixes(
	"0.0.0.0/8",      // "this network"
	"10.0.0.0/8",     // RFC 1918
	"100.64.0.0/10",  // CGNAT / shared address space (Tailscale lives here)
	"127.0.0.0/8",    // loopback
	"169.254.0.0/16", // link-local, incl. the 169.254.169.254 metadata service
	"172.16.0.0/12",  // RFC 1918
	"192.0.0.0/24",   // IETF protocol assignments
	"192.168.0.0/16", // RFC 1918
	"198.18.0.0/15",  // benchmarking
	"224.0.0.0/4",    // multicast
	"240.0.0.0/4",    // reserved
	"::/128",         // unspecified
	"::1/128",        // loopback
	"fc00::/7",       // unique local, incl. fd00:ec2::254 (AWS IMDS over IPv6)
	"fe80::/10",      // link-local
	"ff00::/8",       // multicast
)

func mustPrefixes(cidrs ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(cidrs))
	for i, c := range cidrs {
		out[i] = netip.MustParsePrefix(c)
	}
	return out
}

// IPAllowed reports whether ip may be dialed under p.
func (p Policy) IPAllowed(ip net.IP) bool {
	if p.AllowPrivate {
		return true
	}
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	// ::ffff:10.0.0.1 is 10.0.0.1; checking the mapped form against v4
	// prefixes would miss it.
	addr = addr.Unmap()
	for _, prefix := range blockedPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

func blocked(host string, ip net.IP) error {
	if ip == nil {
		return fmt.Errorf("%w: %s is a private or internal host; Reliant's cloud can't reach it. Route this endpoint through one of your machines instead", ErrBlockedAddress, host)
	}
	return fmt.Errorf("%w: %s resolves to %s, a private or internal address; Reliant's cloud can't reach it. Route this endpoint through one of your machines instead", ErrBlockedAddress, host, ip)
}

// CheckHost resolves host and fails if ANY resolved address is refused. An
// unresolvable host is not an error here: it cannot be dialed either, and the
// dial-time guard checks whatever it eventually resolves to.
func (p Policy) CheckHost(ctx context.Context, host string) error {
	if p.AllowPrivate {
		return nil
	}
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" {
		return fmt.Errorf("%w: empty host", ErrBlockedAddress)
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return blocked(host, nil)
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		if !p.IPAllowed(ip) {
			return blocked(host, ip)
		}
		return nil
	}
	ips, err := p.lookup(ctx, host)
	if err != nil {
		return nil
	}
	for _, ip := range ips {
		if !p.IPAllowed(ip) {
			return blocked(host, ip)
		}
	}
	return nil
}

// CheckURL applies CheckHost to rawURL's host.
func (p Policy) CheckURL(ctx context.Context, rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	return p.CheckHost(ctx, u.Hostname())
}

// control runs after DNS resolution, on the exact address about to be
// connected to. Checking here rather than before dialing is what defeats DNS
// rebinding: there is no window between "the name resolved to a public IP" and
// "the socket connected to a private one".
func (p Policy) control(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || !p.IPAllowed(ip) {
		return blocked(host, ip)
	}
	return nil
}

// Transport returns an http.Transport that refuses to connect to a destination
// the policy forbids, whatever name the request used and however many
// redirects it followed. It ignores proxy environment variables: a proxy would
// be dialed instead of the destination, defeating the guard.
func (p Policy) Transport() *http.Transport {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	if !p.AllowPrivate {
		dialer.Control = p.control
	}
	return &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 0,
	}
}
