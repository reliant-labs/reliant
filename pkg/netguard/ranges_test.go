package netguard

import (
	"net/netip"
	"strings"
	"testing"
)

// rangeCases names every refused range, in both families, with an address
// inside it, and the public neighbours that must stay reachable. One table
// drives every primitive (Refused, RefusedHost on a literal, CheckAddress, the
// dial-time control hook), so they cannot drift apart.
var rangeCases = []struct {
	name    string
	addr    string
	refused bool
}{
	// IPv4.
	{"this-network 0/8", "0.1.2.3", true},
	{"unspecified 0.0.0.0", "0.0.0.0", true},
	{"RFC1918 10/8", "10.1.2.3", true},
	{"RFC1918 10/8 upper edge", "10.255.255.255", true},
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
	{"deprecated 6to4 relay 192.88.99.0/24", "192.88.99.1", true},
	{"RFC1918 192.168/16", "192.168.1.1", true},
	{"benchmarking 198.18/15", "198.18.0.1", true},
	{"benchmarking 198.18/15 upper edge", "198.19.255.254", true},
	{"TEST-NET-2 198.51.100.0/24", "198.51.100.7", true},
	{"TEST-NET-3 203.0.113.0/24", "203.0.113.9", true},
	{"multicast 224/4", "224.0.0.1", true},
	{"multicast 239.255.255.250", "239.255.255.250", true},
	{"reserved 240/4", "240.0.0.1", true},
	{"limited broadcast", "255.255.255.255", true},
	{"Private Google Access 199.36.153.4/30", "199.36.153.4", true},
	{"Private Google Access 199.36.153.4/30 upper", "199.36.153.7", true},
	{"Private Google Access 199.36.153.8/30", "199.36.153.8", true},
	{"Private Google Access 199.36.153.8/30 upper", "199.36.153.11", true},

	// IPv6.
	{"unspecified ::/128", "::", true},
	{"loopback ::1", "::1", true},
	{"discard-only 100::/64", "100::1", true},
	{"documentation 2001:db8::/32", "2001:db8::1", true},
	{"documentation 3fff::/20", "3fff::1", true},
	{"benchmarking 2001:2::/48", "2001:2::1", true},
	{"SRv6 SIDs 5f00::/16", "5f00::1", true},
	{"unique local fc00::/7", "fc00::1", true},
	{"unique local fd00:ec2::254 (AWS IMDS)", "fd00:ec2::254", true},
	{"link-local fe80::/10", "fe80::1", true},
	{"link-local fe80::/10 upper edge", "febf::1", true},
	{"site-local fec0::/10", "fec0::1", true},
	{"multicast ff00::/8", "ff02::1", true},

	// An IPv4 address inside an IPv6 one is judged by the IPv4 inside.
	{"IPv4-mapped private", "::ffff:10.0.0.1", true},
	{"IPv4-mapped metadata", "::ffff:169.254.169.254", true},
	{"IPv4-mapped loopback", "::ffff:127.0.0.1", true},
	{"IPv4-mapped Private Google Access", "::ffff:199.36.153.4", true},
	{"IPv4-mapped public", "::ffff:8.8.8.8", false},
	{"IPv4-compatible private ::a.b.c.d", "::10.0.0.1", true},
	{"IPv4-compatible metadata", "::169.254.169.254", true},
	{"IPv4-translated private ::ffff:0:a.b.c.d", "::ffff:0:10.0.0.1", true},
	{"IPv4-translated public", "::ffff:0:8.8.8.8", false},
	{"NAT64 64:ff9b::/96 private", "64:ff9b::10.0.0.1", true},
	{"NAT64 64:ff9b::/96 metadata", "64:ff9b::169.254.169.254", true},
	{"NAT64 64:ff9b::/96 loopback", "64:ff9b::127.0.0.1", true},
	// The refused set lists 64:ff9b::/96 whole, so a public embedding is
	// refused too: a hub has no business dialing through a translator.
	{"NAT64 64:ff9b::/96 public embedding", "64:ff9b::8.8.8.8", true},
	// RFC 8215 local-use NAT64: the embedding layout depends on the prefix
	// length the translator uses and the address does not carry it.
	{"local-use NAT64 64:ff9b:1::/48 base", "64:ff9b:1::", true},
	{"local-use NAT64 64:ff9b:1::/48 private", "64:ff9b:1:a00:1:200::", true},
	{"local-use NAT64 64:ff9b:1::/48 public-looking", "64:ff9b:1:808:8:800::", true},
	{"local-use NAT64 64:ff9b:1::/48 96-bit layout", "64:ff9b:1::8.8.8.8", true},
	{"just above 64:ff9b:1::/48", "64:ff9b:2::1", false},
	{"6to4 2002::/16 private", "2002:0a00:0001::1", true},
	{"6to4 2002::/16 metadata", "2002:a9fe:a9fe::1", true},
	{"6to4 2002::/16 loopback", "2002:7f00:0001::1", true},
	{"6to4 2002::/16 public", "2002:0808:0808::1", false},
	// Teredo (RFC 4380): server v4 in bytes 4-7, client v4 in bytes 12-15, bit-inverted.
	{"Teredo private client 10.0.0.1", "2001:0:4136:e378:8000:63bf:f5ff:fffe", true},
	{"Teredo metadata client 169.254.169.254", "2001:0:4136:e378:8000:63bf:5601:5601", true},
	{"Teredo private server 10.0.0.1", "2001:0:0a00:0001:8000:63bf:f7f7:f7f7", true},
	{"Teredo documentation client 192.0.2.8", "2001:0:4136:e378:8000:63bf:3f57:fef7", true},
	{"Teredo public server and client", "2001:0:4136:e378:8000:63bf:f7f7:f7f7", false},

	// Public neighbours of the ranges above stay reachable.
	{"public 8.8.8.8", "8.8.8.8", false},
	{"public 1.1.1.1", "1.1.1.1", false},
	{"below 10/8", "9.255.255.255", false},
	{"above 10/8", "11.0.0.1", false},
	{"below 100.64/10", "100.63.255.255", false},
	{"above 100.64/10", "100.128.0.1", false},
	{"below 127/8", "126.255.255.255", false},
	{"above 127/8", "128.0.0.1", false},
	{"below 169.254/16", "169.253.255.255", false},
	{"above 169.254/16", "169.255.0.1", false},
	{"below 172.16/12", "172.15.255.255", false},
	{"above 172.16/12", "172.32.0.1", false},
	{"below 192.168/16", "192.167.255.255", false},
	{"above 192.168/16", "192.169.0.1", false},
	{"below 198.18/15", "198.17.255.255", false},
	{"above 198.18/15", "198.20.0.1", false},
	{"below 224/4", "223.255.255.255", false},
	{"Private Google Access neighbour below", "199.36.153.3", false},
	{"Private Google Access neighbour between", "199.36.153.12", false},
	{"Private Google Access /24 neighbour", "199.36.154.4", false},
	{"public v6 Cloudflare", "2606:4700:4700::1111", false},
	{"public v6 Google", "2001:4860:4860::8888", false},
	{"below fc00::/7", "fbff::1", false},
	{"above fc00::/7", "fe00::1", false},
}

func TestRefused_RangeTable(t *testing.T) {
	for _, tc := range rangeCases {
		t.Run(tc.name, func(t *testing.T) {
			addr := netip.MustParseAddr(tc.addr)
			reason, refused := Refused(addr)
			if refused != tc.refused {
				t.Fatalf("Refused(%s) = %v (%q), want %v", tc.addr, refused, reason, tc.refused)
			}
			if refused && reason == "" {
				t.Errorf("Refused(%s): refused with an empty reason", tc.addr)
			}
			if !refused && reason != "" {
				t.Errorf("Refused(%s): allowed but reason = %q", tc.addr, reason)
			}
		})
	}
}

func TestRefused_ReasonNamesTheRange(t *testing.T) {
	cases := []struct{ addr, want string }{
		{"10.1.2.3", "10.0.0.0/8"},
		{"169.254.169.254", "169.254.0.0/16"},
		{"199.36.153.5", "199.36.153.4/30"},
		{"fd00:ec2::254", "fc00::/7"},
		// An embedded address names the IPv4 range it carries.
		{"::ffff:10.0.0.1", "10.0.0.0/8"},
		{"2002:a9fe:a9fe::1", "169.254.0.0/16"},
		{"64:ff9b::10.0.0.1", "10.0.0.0/8"},
	}
	for _, tc := range cases {
		reason, refused := Refused(netip.MustParseAddr(tc.addr))
		if !refused || !strings.Contains(reason, tc.want) {
			t.Errorf("Refused(%s) = %q, %v; want a reason naming %s", tc.addr, reason, refused, tc.want)
		}
	}
}

func TestRefused_InvalidAndZoned(t *testing.T) {
	if _, refused := Refused(netip.Addr{}); !refused {
		t.Error("the zero Addr must be refused")
	}
	if _, refused := Refused(netip.MustParseAddr("fe80::1%eth0")); !refused {
		t.Error("a zoned link-local address must be refused")
	}
	if _, refused := Refused(netip.MustParseAddr("2606:4700:4700::1111%eth0")); refused {
		t.Error("a zone must not make a public address refused")
	}
}

func TestRefusedHost_Names(t *testing.T) {
	cases := []struct {
		host    string
		refused bool
	}{
		{"localhost", true},
		{"LOCALHOST", true},
		{"localhost.", true},
		{"foo.localhost", true},
		{"metadata.google.internal", true},
		{"anything.internal", true},
		{"printer.local", true},
		{"kubernetes.default.svc", true},
		{"api.team.svc", true},
		{"api.team.svc.cluster.local", true},
		{"kubernetes.default.svc.cluster.local.", true},
		// A single-label name resolves through the resolver's search domains.
		{"metadata", true},
		{"kubernetes", true},
		// Numeric spellings the system resolver may read as an IPv4 address.
		{"2130706433", true},
		{"0x7f.0.0.1", true},
		{"0177.0.0.1", true},
		{"127.1", true},
		{"", true},
		{" ", true},

		{"example.com", false},
		{"api.example.com", false},
		{"EXAMPLE.com.", false},
		{"notlocalhost.com", false},
		{"evil-local.com", false},
		{"internal.example.com", false},
		{"svc.example.com", false},
		{"my.svcs.example.com", false},
		{"cluster.local.example.com", false},
		{"xn--bcher-kva.example", false},
	}
	for _, tc := range cases {
		reason, refused := RefusedHost(tc.host)
		if refused != tc.refused {
			t.Errorf("RefusedHost(%q) = %q, %v; want refused=%v", tc.host, reason, refused, tc.refused)
		}
		if refused && reason == "" {
			t.Errorf("RefusedHost(%q): refused with an empty reason", tc.host)
		}
	}
}

func TestRefusedHost_LiteralsUseTheAddressTable(t *testing.T) {
	for _, tc := range rangeCases {
		for _, host := range []string{tc.addr, "[" + tc.addr + "]"} {
			_, refused := RefusedHost(host)
			if refused != tc.refused {
				t.Errorf("%s: RefusedHost(%q) refused=%v, want %v", tc.name, host, refused, tc.refused)
			}
		}
	}
}
