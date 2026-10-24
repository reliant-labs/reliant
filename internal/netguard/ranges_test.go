// Copyright (c) 2025 Reliant Labs
package netguard

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// errDialReached marks a Guard dial that passed vetting and reached the
// (stubbed) socket layer.
var errDialReached = errors.New("dial reached the socket layer")

// rangeCases names every non-routable range the package refuses, with one
// address inside it, and the public neighbours that must stay reachable. The
// same table drives both primitives, so the two cannot drift apart again.
var rangeCases = []struct {
	name    string
	addr    string
	blocked bool
}{
	// IPv4 special-purpose ranges.
	{"this-network 0.0.0.0/8", "0.1.2.3", true},
	{"unspecified 0.0.0.0", "0.0.0.0", true},
	{"RFC1918 10/8", "10.1.2.3", true},
	{"CGNAT 100.64/10", "100.64.0.1", true},
	{"CGNAT upper edge", "100.127.255.254", true},
	{"loopback 127/8", "127.0.0.1", true},
	{"loopback 127/8 non-.1", "127.8.8.8", true},
	{"link-local 169.254/16", "169.254.1.1", true},
	{"metadata 169.254.169.254", "169.254.169.254", true},
	{"RFC1918 172.16/12", "172.16.0.1", true},
	{"RFC1918 172.16/12 upper edge", "172.31.255.254", true},
	{"IETF protocol 192.0.0.0/24", "192.0.0.8", true},
	{"TEST-NET-1 192.0.2.0/24", "192.0.2.1", true},
	{"RFC1918 192.168/16", "192.168.1.1", true},
	{"benchmarking 198.18/15", "198.18.0.1", true},
	{"benchmarking 198.18/15 upper edge", "198.19.255.254", true},
	{"TEST-NET-2 198.51.100.0/24", "198.51.100.7", true},
	{"TEST-NET-3 203.0.113.0/24", "203.0.113.9", true},
	{"multicast 224/4", "224.0.0.1", true},
	{"multicast 239.255.255.250", "239.255.255.250", true},
	{"reserved 240/4", "240.0.0.1", true},
	{"limited broadcast", "255.255.255.255", true},

	// IPv6 special-purpose ranges.
	{"unspecified ::/128", "::", true},
	{"loopback ::1", "::1", true},
	{"unique local fc00::/7", "fc00::1", true},
	{"unique local fd00:ec2::254 (AWS IMDS)", "fd00:ec2::254", true},
	{"link-local fe80::/10", "fe80::1", true},
	{"link-local fe80::/10 upper edge", "febf::1", true},
	{"multicast ff00::/8", "ff02::1", true},
	{"discard-only 100::/64", "100::1", true},
	{"documentation 2001:db8::/32", "2001:db8::1", true},
	{"documentation 3fff::/20", "3fff::1", true},
	{"benchmarking 2001:2::/48", "2001:2::1", true},
	{"site-local fec0::/10", "fec0::1", true},
	{"SRv6 SIDs 5f00::/16", "5f00::1", true},

	// An IPv4 address inside an IPv6 one is judged by the IPv4 inside.
	{"IPv4-mapped private", "::ffff:10.0.0.1", true},
	{"IPv4-mapped metadata", "::ffff:169.254.169.254", true},
	{"IPv4-mapped loopback", "::ffff:127.0.0.1", true},
	{"IPv4-mapped public", "::ffff:8.8.8.8", false},
	{"IPv4-compatible private ::a.b.c.d", "::10.0.0.1", true},
	{"IPv4-compatible loopback", "::127.0.0.1", true},
	{"IPv4-compatible metadata", "::169.254.169.254", true},
	{"IPv4-translated private ::ffff:0:a.b.c.d", "::ffff:0:10.0.0.1", true},
	{"IPv4-translated public", "::ffff:0:8.8.8.8", false},
	{"NAT64 64:ff9b::/96 private", "64:ff9b::10.0.0.1", true},
	{"NAT64 64:ff9b::/96 metadata", "64:ff9b::169.254.169.254", true},
	{"NAT64 64:ff9b::/96 loopback", "64:ff9b::127.0.0.1", true},
	{"NAT64 64:ff9b::/96 public", "64:ff9b::8.8.8.8", false},
	// RFC 8215 local-use NAT64: the embedding layout depends on the prefix
	// length the translator uses, which the address does not carry, so the
	// whole /48 is refused (it is not globally routable either).
	{"local-use NAT64 64:ff9b:1::/48 base", "64:ff9b:1::", true},
	{"local-use NAT64 64:ff9b:1::/48 private", "64:ff9b:1:a00:1:200::", true},
	{"local-use NAT64 64:ff9b:1::/48 public-looking 8.8.8.8", "64:ff9b:1:808:8:800::", true},
	{"local-use NAT64 64:ff9b:1::/48 96-bit layout", "64:ff9b:1::8.8.8.8", true},
	{"just above 64:ff9b:1::/48", "64:ff9b:2::1", false},
	{"6to4 2002::/16 private", "2002:0a00:0001::1", true},
	{"6to4 2002::/16 metadata", "2002:a9fe:a9fe::1", true},
	{"6to4 2002::/16 loopback", "2002:7f00:0001::1", true},
	{"6to4 2002::/16 public", "2002:0808:0808::1", false},
	// Teredo (RFC 4380): server v4 in bytes 4-7, client v4 in bytes 12-15, bit-inverted.
	{"Teredo 2001::/32 private client 10.0.0.1", "2001:0:4136:e378:8000:63bf:f5ff:fffe", true},
	{"Teredo 2001::/32 metadata client 169.254.169.254", "2001:0:4136:e378:8000:63bf:5601:5601", true},
	{"Teredo 2001::/32 private server 10.0.0.1", "2001:0:0a00:0001:8000:63bf:f7f7:f7f7", true},
	{"Teredo 2001::/32 documentation client 192.0.2.8", "2001:0:4136:e378:8000:63bf:3f57:fef7", true},
	{"Teredo 2001::/32 public server 65.54.227.120 and client 8.8.8.8", "2001:0:4136:e378:8000:63bf:f7f7:f7f7", false},

	// Public neighbours of the ranges above stay reachable.
	{"public 8.8.8.8", "8.8.8.8", false},
	{"public 1.1.1.1", "1.1.1.1", false},
	{"below 100.64/10", "100.63.255.255", false},
	{"above 100.64/10", "100.128.0.1", false},
	{"below 172.16/12", "172.15.255.255", false},
	{"above 172.16/12", "172.32.0.1", false},
	{"below 198.18/15", "198.17.255.255", false},
	{"above 198.18/15", "198.20.0.1", false},
	{"public v6 Cloudflare", "2606:4700:4700::1111", false},
	{"public v6 Google", "2001:4860:4860::8888", false},
}

func TestRangeTable_IPBlocked(t *testing.T) {
	for _, tc := range rangeCases {
		t.Run(tc.name, func(t *testing.T) {
			ip := net.ParseIP(tc.addr)
			require.NotNil(t, ip, tc.addr)
			assert.Equal(t, tc.blocked, IPBlocked(ip), "IPBlocked(%s)", tc.addr)
		})
	}
}

func TestRangeTable_PolicyIPAllowed(t *testing.T) {
	hosted := Policy{}
	open := Policy{AllowPrivate: true}
	for _, tc := range rangeCases {
		t.Run(tc.name, func(t *testing.T) {
			ip := net.ParseIP(tc.addr)
			require.NotNil(t, ip, tc.addr)
			assert.Equal(t, !tc.blocked, hosted.IPAllowed(ip), "Policy.IPAllowed(%s)", tc.addr)
			assert.True(t, open.IPAllowed(ip), "AllowPrivate permits %s", tc.addr)
		})
	}
}

func TestRangeTable_PolicyCheckHostAndTransport(t *testing.T) {
	hosted := Policy{}
	for _, tc := range rangeCases {
		t.Run(tc.name, func(t *testing.T) {
			err := hosted.CheckHost(context.Background(), "["+tc.addr+"]")
			if tc.blocked {
				assert.ErrorIs(t, err, ErrBlockedAddress, "CheckHost(%s)", tc.addr)
			} else {
				assert.NoError(t, err, "CheckHost(%s)", tc.addr)
			}

			// The dial-time hook sees the exact ip:port being connected to.
			err = hosted.control("tcp", net.JoinHostPort(tc.addr, "443"), nil)
			if tc.blocked {
				assert.ErrorIs(t, err, ErrBlockedAddress, "control(%s)", tc.addr)
			} else {
				assert.NoError(t, err, "control(%s)", tc.addr)
			}
		})
	}
}

func TestRangeTable_GuardDial(t *testing.T) {
	for _, tc := range rangeCases {
		t.Run(tc.name, func(t *testing.T) {
			g := New()
			g.Dial = func(context.Context, string, string) (net.Conn, error) { return nil, errDialReached }
			_, err := g.DialContext(context.Background(), "tcp", net.JoinHostPort(tc.addr, "443"))
			require.Error(t, err)
			if tc.blocked {
				assert.Contains(t, err.Error(), "blocked", "Guard.DialContext(%s)", tc.addr)
				assert.NotErrorIs(t, err, errDialReached)
			} else {
				assert.ErrorIs(t, err, errDialReached, "Guard.DialContext(%s) must reach the socket layer", tc.addr)
			}
		})
	}
}

// A hostname is judged by every address it resolves to, in both primitives.
func TestRangeTable_ResolvedTransitionAddresses(t *testing.T) {
	lookup := func(_ context.Context, host string) ([]net.IP, error) {
		switch host {
		case "sixtofour.example.com":
			return []net.IP{net.ParseIP("2002:0a00:0001::1")}, nil
		case "nat64.example.com":
			return []net.IP{net.ParseIP("64:ff9b::10.0.0.1")}, nil
		}
		return nil, errors.New("no such host")
	}
	hosted := Policy{LookupIP: lookup}
	g := New()
	g.Resolve = lookup
	g.Dial = func(context.Context, string, string) (net.Conn, error) { return nil, errDialReached }
	for _, host := range []string{"sixtofour.example.com", "nat64.example.com"} {
		assert.ErrorIs(t, hosted.CheckHost(context.Background(), host), ErrBlockedAddress, host)
		_, err := g.DialContext(context.Background(), "tcp", host+":443")
		require.Error(t, err, host)
		assert.True(t, strings.Contains(err.Error(), "blocked"), "%s: %v", host, err)
	}
}

// Guard's loopback exemption is for httptest servers only; it must not turn
// a transition address that merely embeds loopback into a reachable one.
func TestGuardAllowLoopbackDoesNotAllowEmbeddedLoopback(t *testing.T) {
	g := New()
	g.AllowLoopback = true
	g.Dial = func(context.Context, string, string) (net.Conn, error) { return nil, errDialReached }

	_, err := g.DialContext(context.Background(), "tcp", "127.0.0.1:80")
	assert.ErrorIs(t, err, errDialReached)
	_, err = g.DialContext(context.Background(), "tcp", "[::1]:80")
	assert.ErrorIs(t, err, errDialReached)

	for _, addr := range []string{"[64:ff9b::127.0.0.1]:80", "[2002:7f00:0001::1]:80", "[::127.0.0.1]:80"} {
		_, err = g.DialContext(context.Background(), "tcp", addr)
		require.Error(t, err, addr)
		assert.NotErrorIs(t, err, errDialReached, addr)
	}
}

func TestIPBlockedNilAndGarbage(t *testing.T) {
	assert.True(t, IPBlocked(nil))
	assert.True(t, IPBlocked(net.IP{1, 2, 3}))
	assert.False(t, Policy{}.IPAllowed(nil))
	assert.False(t, Policy{}.IPAllowed(net.IP{1, 2, 3}))
}
