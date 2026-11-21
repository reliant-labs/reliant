package netguard

import (
	"fmt"
	"net/netip"
	"strings"
)

// A refusedRange is a prefix the guard never connects to, with the label that
// names it in an error.
type refusedRange struct {
	prefix netip.Prefix
	label  string
}

func ranges(rows ...[2]string) []refusedRange {
	out := make([]refusedRange, len(rows))
	for i, r := range rows {
		out[i] = refusedRange{netip.MustParsePrefix(r[0]), r[1]}
	}
	return out
}

var refusedV4 = ranges(
	[2]string{"0.0.0.0/8", "this network"},
	[2]string{"10.0.0.0/8", "RFC 1918 private"},
	[2]string{"100.64.0.0/10", "carrier-grade NAT"},
	[2]string{"127.0.0.0/8", "loopback"},
	[2]string{"169.254.0.0/16", "link-local, incl. the cloud metadata service"},
	[2]string{"172.16.0.0/12", "RFC 1918 private"},
	[2]string{"192.0.0.0/24", "IETF protocol assignments"},
	[2]string{"192.0.2.0/24", "documentation (TEST-NET-1)"},
	[2]string{"192.88.99.0/24", "deprecated 6to4 relay anycast"},
	[2]string{"192.168.0.0/16", "RFC 1918 private"},
	[2]string{"198.18.0.0/15", "benchmarking"},
	[2]string{"198.51.100.0/24", "documentation (TEST-NET-2)"},
	[2]string{"203.0.113.0/24", "documentation (TEST-NET-3)"},
	[2]string{"224.0.0.0/4", "multicast"},
	[2]string{"240.0.0.0/4", "reserved, incl. the limited broadcast address"},
	[2]string{"199.36.153.4/30", "Private Google Access VIPs (private.googleapis.com)"},
	[2]string{"199.36.153.8/30", "Private Google Access VIPs (restricted.googleapis.com)"},
)

var refusedV6 = ranges(
	[2]string{"::/128", "unspecified"},
	[2]string{"::1/128", "loopback"},
	// Deprecated IPv4-compatible addresses (::a.b.c.d) have no legitimate use;
	// the embedded address is judged first, so a private one is named by its
	// IPv4 range.
	[2]string{"::/96", "deprecated IPv4-compatible"},
	// The well-known NAT64 prefix is refused whole, public embeddings
	// included: a hub dials a destination, not a translator. The embedded
	// address is judged first for the error message.
	[2]string{"64:ff9b::/96", "NAT64 well-known prefix"},
	// RFC 8215 local-use NAT64: where the IPv4 address sits depends on the
	// prefix length the translator uses, which the address does not carry.
	[2]string{"64:ff9b:1::/48", "local-use NAT64 (RFC 8215)"},
	[2]string{"100::/64", "discard-only"},
	[2]string{"2001:2::/48", "benchmarking"},
	[2]string{"2001:db8::/32", "documentation"},
	[2]string{"3fff::/20", "documentation"},
	[2]string{"5f00::/16", "SRv6 segment identifiers"},
	[2]string{"fc00::/7", "unique local, incl. AWS IMDS over IPv6"},
	[2]string{"fe80::/10", "link-local"},
	[2]string{"fec0::/10", "deprecated site-local"},
	[2]string{"ff00::/8", "multicast"},
)

// Transition prefixes whose bits carry an IPv4 address.
var (
	prefixIPv4Compatible = netip.MustParsePrefix("::/96")
	prefixIPv4Translated = netip.MustParsePrefix("::ffff:0:0:0/96") // RFC 2765 SIIT: ::ffff:0:a.b.c.d
	prefixNAT64          = netip.MustParsePrefix("64:ff9b::/96")
	prefix6to4           = netip.MustParsePrefix("2002::/16")
	prefixTeredo         = netip.MustParsePrefix("2001::/32")
)

// embeddedV4 returns the IPv4 addresses a transition-range IPv6 address
// carries. Teredo carries two: the server and the (bit-inverted) client.
func embeddedV4(addr netip.Addr) []netip.Addr {
	b := addr.As16()
	v4 := func(o [4]byte) netip.Addr { return netip.AddrFrom4(o) }
	switch {
	case prefixIPv4Compatible.Contains(addr), prefixIPv4Translated.Contains(addr), prefixNAT64.Contains(addr):
		return []netip.Addr{v4([4]byte{b[12], b[13], b[14], b[15]})}
	case prefix6to4.Contains(addr):
		return []netip.Addr{v4([4]byte{b[2], b[3], b[4], b[5]})}
	case prefixTeredo.Contains(addr):
		return []netip.Addr{
			v4([4]byte{b[4], b[5], b[6], b[7]}),
			v4([4]byte{^b[12], ^b[13], ^b[14], ^b[15]}),
		}
	}
	return nil
}

func normalize(addr netip.Addr) netip.Addr {
	if !addr.IsValid() {
		return addr
	}
	return addr.WithZone("").Unmap()
}

// refusedReason is the allow-free check behind Refused.
func refusedReason(addr netip.Addr) (string, bool) {
	addr = normalize(addr)
	if !addr.IsValid() {
		return "not a valid IP address", true
	}
	if addr.Is4() {
		return match(addr, refusedV4)
	}
	// Name loopback and unspecified by their own range, not by the 0/8 address
	// the ::/96 compatibility rule would find inside them.
	if reason, refused := match(addr, refusedV6[:2]); refused {
		return reason, true
	}
	for _, inner := range embeddedV4(addr) {
		if reason, refused := refusedReason(inner); refused {
			return fmt.Sprintf("%s, embedded in %s", reason, addr), true
		}
	}
	return match(addr, refusedV6)
}

func match(addr netip.Addr, set []refusedRange) (string, bool) {
	for _, r := range set {
		if r.prefix.Contains(addr) {
			return fmt.Sprintf("%s (%s)", r.prefix, r.label), true
		}
	}
	return "", false
}

// Refused reports whether ip is not a routable public unicast address, and
// names the refused range. An IPv6 address that carries an IPv4 address
// (IPv4-mapped, -compatible and -translated, NAT64, 6to4, Teredo) is judged by
// the IPv4 address inside it too, so wrapping 169.254.169.254 in a transition
// prefix does not make it reachable. The zero Addr is refused.
func Refused(ip netip.Addr) (reason string, refused bool) {
	return refusedReason(ip)
}

// Refused is [Refused] with the exception list applied first: an address
// inside a prefix of p.Allow is admitted.
func (p Policy) Refused(ip netip.Addr) (reason string, refused bool) {
	n := normalize(ip)
	for _, a := range p.Allow {
		if n.IsValid() && a.Contains(n) {
			return "", false
		}
	}
	return refusedReason(ip)
}

// Hostname suffixes that name this machine or its cluster rather than the
// internet. A name equal to the suffix is refused too.
var refusedSuffixes = []string{
	"localhost",
	"internal", // metadata.google.internal
	"local",
	"svc",
	"cluster.local",
}

// RefusedHost applies the hostname rules to host, which is a bare host as it
// appears in a URL: a name, an IP literal, or a bracketed IPv6 literal. A
// literal is judged by [Refused]. A name is refused when it is localhost or
// under .localhost, .internal, .local, .svc or .cluster.local; when it has a
// single label (the system resolver would expand it through its search
// domains); or when it is a numeric spelling the system resolver may read as an
// IPv4 address (2130706433, 0x7f.0.0.1, 127.1). A name that passes still has to
// be resolved and every answer checked: see [Policy.CheckAddress].
func RefusedHost(host string) (reason string, refused bool) {
	return Policy{}.RefusedHost(host)
}

// RefusedHost is the package-level [RefusedHost] with p.Allow applied to IP
// literals. The hostname rules are never relaxed by Allow.
func (p Policy) RefusedHost(host string) (reason string, refused bool) {
	h := strings.TrimSpace(host)
	if h == "" {
		return "empty host", true
	}
	h = strings.ToLower(h)
	if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
		h = h[1 : len(h)-1]
	}
	if addr, err := netip.ParseAddr(h); err == nil {
		return p.Refused(addr)
	}
	h = strings.TrimSuffix(h, ".")
	if h == "" {
		return "empty host", true
	}
	for _, s := range refusedSuffixes {
		if h == s || strings.HasSuffix(h, "."+s) {
			return fmt.Sprintf("the name %q is under .%s, which names this machine or its cluster", h, s), true
		}
	}
	labels := strings.Split(h, ".")
	if len(labels) == 1 {
		return fmt.Sprintf("the single-label name %q resolves through search domains to an internal service", h), true
	}
	if isNumericLabel(labels[len(labels)-1]) {
		return fmt.Sprintf("%q is a numeric spelling of an IPv4 address", h), true
	}
	return "", false
}

// isNumericLabel reports whether l is all decimal digits or 0x-prefixed hex,
// the forms inet_aton reads as an address component. No top-level domain looks
// like that.
func isNumericLabel(l string) bool {
	if l == "" {
		return false
	}
	if strings.HasPrefix(l, "0x") {
		for _, c := range l[2:] {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				return false
			}
		}
		return true
	}
	for _, c := range l {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
