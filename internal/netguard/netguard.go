// Copyright (c) 2025 Reliant Labs
//
// Package netguard refuses outbound connections to private, loopback,
// link-local and cloud-metadata addresses, in two forms: Policy (URL/host
// checks plus a dial-time Control hook, for user-supplied endpoint URLs) and
// Guard (a dialer that vets resolved IPs, for integration/MCP HTTP clients).
// Both decide with one predicate, IPBlocked, which delegates to forge's
// netguard.Refused: the refused ranges, the IPv4 decoding of IPv6 transition
// forms and the NAT64 refusal live there, not here. It must not import
// os/exec.
//
// Guard.DialContext dials, so forge's exclusion gate reads the package as an
// outbound boundary. It is a dialer primitive instead: the DialContext of the
// transports that outbound clients are built from. Those clients' packages
// are the boundaries, with their own contracts and stub-server tests; a
// contract here would mock and decorate every TCP connection they open.
//
//forge:lint-disable-next-line forge-exclude-contract-outbound-io: a dialer primitive (the guarded http.Transport's DialContext), not a boundary; the clients built on it are the outbound adapters
//forge:exclude-contract: network-policy primitives (address vetting, a guarded dialer and transports) that outbound HTTP clients are built from
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

	forgenetguard "github.com/reliant-labs/forge/pkg/netguard"
)

// ErrBlockedAddress is returned (wrapped) when a destination is refused by
// policy. Callers show it to the user as-is; it never mentions internals
// beyond the address the user themselves typed.
var ErrBlockedAddress = errors.New("address is not allowed")

// IPBlocked reports whether ip is not a routable public unicast address: the
// one predicate behind both Policy and Guard, so the two cannot disagree. It
// is forge's netguard.Refused, so it refuses the IPv4 addresses hidden inside
// IPv6 transition forms, the Private Google Access VIPs, and the NAT64
// well-known prefix whole. A nil or malformed ip is blocked.
func IPBlocked(ip net.IP) bool {
	_, refused := forgenetguard.Refused(addrOf(ip))
	return refused
}

// addrOf converts ip to a netip.Addr; a nil or malformed ip becomes the zero
// Addr, which forge's netguard refuses.
func addrOf(ip net.IP) netip.Addr {
	addr, _ := netip.AddrFromSlice(ip)
	return addr
}

// loopbackOnly admits loopback and nothing else of the refused set. The
// exception applies to the address itself, so a transition address that merely
// embeds loopback stays refused.
var loopbackOnly = forgenetguard.Policy{Allow: []netip.Prefix{
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("::1/128"),
}}

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

// ForDeployment is the policy for the user-steerable dials of a server role
// (`reliant server api|worker|gateway`): fetch, websearch, sourcegraph and
// user-supplied model endpoints. It fails CLOSED. A server role dials from a
// shared process that sits next to databases, brokers and a cloud metadata
// service, so private, loopback, link-local and metadata targets are refused
// unless the operator explicitly opted a self-hosted deployment in
// (ServerProcessConfig.network.allow_private_egress). Nothing else may imply
// it: in particular a missing control-plane URL, which is how a
// misconfigured hosted deployment looks, is not a reason to open the network.
func ForDeployment(allowPrivateEgress bool) Policy {
	return Policy{AllowPrivate: allowPrivateEgress}
}

func (p Policy) lookup(ctx context.Context, host string) ([]net.IP, error) {
	if p.LookupIP != nil {
		return p.LookupIP(ctx, host)
	}
	return net.DefaultResolver.LookupIP(ctx, "ip", host)
}

// IPAllowed reports whether ip may be dialed under p.
func (p Policy) IPAllowed(ip net.IP) bool {
	if p.AllowPrivate {
		return true
	}
	return !IPBlocked(ip)
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

// Guard dials only public addresses.
type Guard struct {
	// AllowLoopback permits 127.0.0.0/8 and ::1. For tests that talk to httptest servers only.
	AllowLoopback bool
	Timeout       time.Duration

	// Resolve overrides DNS lookup; tests use it to make a hostname resolve to a chosen address.
	Resolve func(ctx context.Context, host string) ([]net.IP, error)

	// Dial connects to one vetted address, an IP literal and port, within
	// Timeout. nil is a net.Dialer. Tests route it to an in-process server
	// (netguardtest), which a loopback TCP dial would reach only while the
	// host's conntrack table has room.
	Dial func(ctx context.Context, network, address string) (net.Conn, error)
}

// New returns a Guard with a 10s connect timeout.
func New() *Guard {
	return &Guard{Timeout: 10 * time.Second}
}

func (g *Guard) blocked(ip net.IP) bool {
	if g.AllowLoopback {
		_, refused := loopbackOnly.Refused(addrOf(ip))
		return refused
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
	var lastErr error
	for _, ip := range ips {
		conn, err := g.dialVetted(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// dialVetted connects to one address that already passed the policy, with
// Timeout per attempt.
func (g *Guard) dialVetted(ctx context.Context, network, address string) (net.Conn, error) {
	if g.Dial == nil {
		return (&net.Dialer{Timeout: g.Timeout}).DialContext(ctx, network, address)
	}
	if g.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, g.Timeout)
		defer cancel()
	}
	return g.Dial(ctx, network, address)
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
