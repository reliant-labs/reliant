// Package netguard refuses outbound connections to destinations a caller
// picked but must not be able to reach: loopback, private, link-local,
// cloud-metadata, CGNAT, multicast, reserved and documentation ranges, the
// in-cluster and internal hostnames, and the IPv4 addresses hidden inside IPv6
// transition forms (mapped, compatible, translated, NAT64, 6to4, Teredo).
//
// Use it wherever a server dials an address a customer or end user supplied: a
// webhook URL, a model endpoint, a cluster API server. The refused set is one
// predicate, [Refused], so the hostname check, the URL check and the dialer
// cannot disagree about a range.
//
// # What is the boundary
//
// The dialer. [Policy.DialContext] (and [HTTPClient], built on it) checks the
// address the socket is about to connect to, after DNS resolution, so there is
// no window between "the name resolved to a public IP" and "the socket
// connected to a private one": DNS rebinding, wildcard-DNS names such as
// 10-0-0-1.nip.io, and anything else that changes what a name means lose.
// Behind it sits the network policy of the deployment (an egress NetworkPolicy
// matches the destination IP the same way) for the processes that dial through
// code that is not yours.
//
// [Policy.CheckAddress] is NOT a security boundary. It resolves the name once,
// at validation time, and the answer can differ by the time anything dials. It
// exists to reject a bad address early with an error that names the host and
// the range, so a user is told at submit time rather than by a failed
// converge. Never rely on it alone.
//
// # Usage
//
//	policy := netguard.Policy{Ports: []uint16{443, 6443}}
//	if err := policy.CheckAddress(ctx, req.Server, nil); err != nil {
//		return svcerr.Wrap(connect.NewError(connect.CodeInvalidArgument, err))
//	}
//	client := netguard.HTTPClient(policy, 10*time.Second) // follows no redirect, ignores proxy variables
//
// Policy.Allow is the exception list for destinations the platform itself
// must reach inside an otherwise refused range (an in-cluster endpoint, a
// proxy). It admits IP addresses only; the hostname rules are never relaxed.
//
//forge:lint-disable-next-line forge-exclude-contract-outbound-io: a dialer primitive (the guarded http.Transport's DialContext), not a boundary; the clients built on it are the outbound adapters
//forge:exclude-contract: network-policy primitives (address vetting, a guarded dialer and client) that outbound HTTP clients are built from
package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ErrRefused is returned (wrapped) whenever a destination is refused. The
// message names the host the caller supplied and the range it fell in, never
// anything else about the process's network.
var ErrRefused = errors.New("netguard: destination refused")

// ErrRedirectRefused is returned by the client from [HTTPClient] when a
// response is a redirect. It wraps [ErrRefused].
var ErrRedirectRefused = fmt.Errorf("%w: redirects are not followed", ErrRefused)

// resolveTimeout bounds the DNS lookup of [Policy.CheckAddress].
const resolveTimeout = 5 * time.Second

// defaultPorts is the port set of a Policy with none given.
var defaultPorts = []uint16{443}

// Resolver looks up the A and AAAA records of a host. *net.Resolver
// satisfies it; tests inject a fake.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Policy decides which destinations a caller-chosen address may reach. The
// zero value refuses the whole refused set and admits https on port 443.
type Policy struct {
	// Allow lists prefixes admitted before the refused set is consulted: the
	// platform exemption. An address inside one is admitted whatever range it
	// is in. It never relaxes the hostname rules, the scheme or the port.
	Allow []netip.Prefix

	// Ports is the set of destination ports; empty means {443}. It applies to
	// [Policy.CheckAddress] and to the dialer.
	Ports []uint16
}

func (p Policy) ports() []uint16 {
	if len(p.Ports) == 0 {
		return defaultPorts
	}
	return p.Ports
}

func (p Policy) portAllowed(port uint16) bool {
	for _, a := range p.ports() {
		if a == port {
			return true
		}
	}
	return false
}

func (p Policy) portsString() string {
	ps := p.ports()
	s := make([]string, len(ps))
	for i, n := range ps {
		s[i] = strconv.Itoa(int(n))
	}
	return strings.Join(s, ", ")
}

func refusal(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrRefused, fmt.Sprintf(format, args...))
}

// CheckAddress validates a user-chosen URL: https, a port in p.Ports, no
// userinfo, a host that passes [Policy.RefusedHost], and, for a name, a
// resolution (A and AAAA, 5s) in which EVERY answer passes [Policy.Refused].
// A name that does not resolve, resolves to nothing, or cannot be resolved
// is refused: failing closed here costs one retry, failing open costs the
// network. A nil resolver is [net.DefaultResolver].
//
// CheckAddress is for error messages. It is not the boundary: the answer it
// saw can change before anything dials. The dialer ([Policy.DialContext]) and
// the network policy are the boundary.
func (p Policy) CheckAddress(ctx context.Context, rawURL string, resolver Resolver) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return refusal("not a valid URL")
	}
	if u.Scheme != "https" {
		return refusal("the address must use https")
	}
	if u.User != nil {
		return refusal("the address must not carry userinfo (credentials in the URL)")
	}
	host := u.Hostname()
	if u.Opaque != "" || host == "" {
		return refusal("the address has no host")
	}
	port := uint64(443)
	if ps := u.Port(); ps != "" {
		if port, err = strconv.ParseUint(ps, 10, 16); err != nil {
			return refusal("%q is not a valid port", ps)
		}
	}
	if !p.portAllowed(uint16(port)) {
		return refusal("port %d is not allowed; use one of %s", port, p.portsString())
	}

	if reason, refused := p.RefusedHost(host); refused {
		return refusal("%s: %s", host, reason)
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if _, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		return nil // a literal, already judged by RefusedHost
	}

	if resolver == nil {
		resolver = net.DefaultResolver
	}
	ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	addrs, err := resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return refusal("%s does not resolve (%v)", host, err)
	}
	if len(addrs) == 0 {
		return refusal("%s resolves to no addresses", host)
	}
	for _, a := range addrs {
		if reason, refused := p.Refused(a); refused {
			return refusal("%s resolves to %s, which is %s", host, a.Unmap(), reason)
		}
	}
	return nil
}

// control runs on the exact ip:port a socket is about to connect to, after
// DNS resolution. It is the dial-time check that defeats rebinding.
func (p Policy) control(_, address string, _ syscall.RawConn) error {
	host, ps, err := net.SplitHostPort(address)
	if err != nil {
		return refusal("%q is not a host:port address", address)
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return refusal("%q is not an IP address at connect time", host)
	}
	if reason, refused := p.Refused(addr); refused {
		return refusal("connecting to %s: %s", addr.Unmap(), reason)
	}
	port, err := strconv.ParseUint(ps, 10, 16)
	if err != nil || !p.portAllowed(uint16(port)) {
		return refusal("port %s is not allowed; use one of %s", ps, p.portsString())
	}
	return nil
}

// DialContext returns a dial function that refuses to connect to a
// destination the policy forbids. The check runs through the dialer's Control
// hook, on the address the socket is actually about to connect to, so it
// holds whatever name was used and however it resolved. base supplies the
// dialer's other settings (timeouts, resolver, local address); nil is a
// dialer with a 10s timeout and 30s keep-alive. base is copied, never
// modified, and a Control or ControlContext it carries still runs after the
// guard admits the address.
func (p Policy) DialContext(base *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	d := net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	if base != nil {
		d = *base
	}
	prev, prevCtx := d.Control, d.ControlContext
	d.Control = nil
	d.ControlContext = func(ctx context.Context, network, address string, c syscall.RawConn) error {
		if err := p.control(network, address, c); err != nil {
			return err
		}
		if prevCtx != nil {
			if err := prevCtx(ctx, network, address, c); err != nil {
				return err
			}
		}
		if prev != nil {
			return prev(network, address, c)
		}
		return nil
	}
	return d.DialContext
}

// Transport returns an [http.Transport] that dials through [Policy.DialContext]
// and never uses an ambient proxy: a proxy would be dialed instead of the
// destination, defeating the guard. Each call returns a new Transport.
func (p Policy) Transport() *http.Transport {
	return &http.Transport{
		Proxy:                 nil,
		DialContext:           p.DialContext(nil),
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
}

// HTTPClient returns a client for user-chosen addresses: guarded dialer, no
// proxy, and no redirect followed. A redirect is a second destination the
// caller never vetted, so it fails with [ErrRedirectRefused] instead.
func HTTPClient(p Policy, timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: p.Transport(),
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return ErrRedirectRefused
		},
	}
}
