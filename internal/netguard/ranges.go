// Copyright (c) 2025 Reliant Labs

package netguard

import (
	"net"
	"net/netip"
)

// IPBlocked reports whether ip is not a routable public unicast address: the
// one predicate behind both Policy and Guard, so the two cannot disagree.
//
// An IPv6 address that carries an IPv4 address (IPv4-mapped, IPv4-compatible,
// IPv4-translated, NAT64, 6to4, Teredo) is judged by the IPv4 address inside
// it as well as by its own range, so wrapping 169.254.169.254 in a transition
// prefix does not make it reachable through a dual-stack or translating path.
// A nil or malformed ip is blocked.
func IPBlocked(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	return addrBlocked(addr)
}

func addrBlocked(addr netip.Addr) bool {
	addr = addr.Unmap() // ::ffff:a.b.c.d is a.b.c.d
	if !addr.IsValid() {
		return true
	}
	if addr.Is4() {
		return inAny(addr, blockedV4)
	}
	if addr.Zone() != "" {
		addr = addr.WithZone("")
	}
	if inAny(addr, blockedV6) {
		return true
	}
	for _, inner := range embeddedV4(addr) {
		if inner.IsValid() && addrBlocked(inner) {
			return true
		}
	}
	return false
}

func inAny(addr netip.Addr, prefixes []netip.Prefix) bool {
	for _, p := range prefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// Transition prefixes whose low or inner bits carry an IPv4 address.
var (
	prefixIPv4Compatible = netip.MustParsePrefix("::/96")           // deprecated ::a.b.c.d
	prefixIPv4Translated = netip.MustParsePrefix("::ffff:0:0:0/96") // RFC 2765 SIIT ::ffff:0:a.b.c.d
	prefixNAT64          = netip.MustParsePrefix("64:ff9b::/96")    // RFC 6052 well-known prefix
	prefix6to4           = netip.MustParsePrefix("2002::/16")       // RFC 3056
	prefixTeredo         = netip.MustParsePrefix("2001::/32")       // RFC 4380
)

// embeddedV4 returns the IPv4 addresses addr carries, if it is in a
// transition range. Teredo carries two: the server and the (obfuscated)
// client.
func embeddedV4(addr netip.Addr) []netip.Addr {
	b := addr.As16()
	v4 := func(o [4]byte) netip.Addr { return netip.AddrFrom4(o) }
	switch {
	case prefixIPv4Compatible.Contains(addr), prefixIPv4Translated.Contains(addr), prefixNAT64.Contains(addr):
		return []netip.Addr{v4([4]byte{b[12], b[13], b[14], b[15]})}
	case prefix6to4.Contains(addr):
		return []netip.Addr{v4([4]byte{b[2], b[3], b[4], b[5]})}
	case prefixTeredo.Contains(addr):
		// The client address is stored bit-inverted (RFC 4380 section 4).
		return []netip.Addr{
			v4([4]byte{b[4], b[5], b[6], b[7]}),
			v4([4]byte{^b[12], ^b[13], ^b[14], ^b[15]}),
		}
	}
	return nil
}

var blockedV4 = mustPrefixes(
	"0.0.0.0/8",       // "this network", incl. 0.0.0.0
	"10.0.0.0/8",      // RFC 1918
	"100.64.0.0/10",   // CGNAT / shared address space (Tailscale lives here)
	"127.0.0.0/8",     // loopback
	"169.254.0.0/16",  // link-local, incl. the 169.254.169.254 metadata service
	"172.16.0.0/12",   // RFC 1918
	"192.0.0.0/24",    // IETF protocol assignments
	"192.0.2.0/24",    // TEST-NET-1
	"192.88.99.0/24",  // deprecated 6to4 relay anycast
	"192.168.0.0/16",  // RFC 1918
	"198.18.0.0/15",   // benchmarking
	"198.51.100.0/24", // TEST-NET-2
	"203.0.113.0/24",  // TEST-NET-3
	"224.0.0.0/4",     // multicast
	"240.0.0.0/4",     // reserved, incl. 255.255.255.255
)

var blockedV6 = mustPrefixes(
	"::/128",         // unspecified
	"::1/128",        // loopback
	"64:ff9b:1::/48", // RFC 8215 local-use NAT64: the embedding depends on the translator's prefix length, so refuse it whole
	"100::/64",       // discard-only
	"2001:2::/48",    // benchmarking
	"2001:db8::/32",  // documentation
	"3fff::/20",      // documentation
	"5f00::/16",      // SRv6 SIDs
	"fc00::/7",       // unique local, incl. fd00:ec2::254 (AWS IMDS over IPv6)
	"fe80::/10",      // link-local
	"fec0::/10",      // deprecated site-local
	"ff00::/8",       // multicast
)

func mustPrefixes(cidrs ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(cidrs))
	for i, c := range cidrs {
		out[i] = netip.MustParsePrefix(c)
	}
	return out
}
