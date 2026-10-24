// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/netguard"
)

// FetchNetwork is where fetch may connect when it runs on a daemon (it is
// PlacementPreferDaemon). The server-side fetch keeps netguard.Policy; this is
// the daemon's, and it differs in one way that matters: a daemon is on the
// user's own machine or network, which is both what a user may want fetch to
// reach and what a prompt-injected page must not be able to reach unprompted.
// fetch is usually auto-approved, so a fetched page can steer the next fetch.
//
// Three tiers, in order:
//
//  1. Link-local and cloud-metadata destinations are refused always, on every
//     daemon, with no setting that unblocks them: 169.254.0.0/16, fe80::/10,
//     the metadata hostnames, and the IPv6 and non-link-local metadata
//     addresses (fd00:ec2::/32, fd20:ce::254, 100.100.100.200, 168.63.129.16),
//     including when one is embedded in a v4-mapped, NAT64, 6to4 or Teredo
//     IPv6 address. A self-run daemon on a cloud VM would otherwise hand its
//     instance credentials to the first page that asked.
//  2. Loopback and private ranges are refused unless the user opted in
//     (AllowPrivate), through the setting OptInSetting names. The refusal is
//     a tool result, not a failed call, and it names that setting.
//  3. Everything else public is allowed.
//
// The zero value is the strict policy a managed daemon uses: tier 1 and tier
// 2 with no opt-in to offer.
type FetchNetwork struct {
	// AllowPrivate permits loopback and private-network destinations (tier 2).
	AllowPrivate bool
	// OptInSetting names the setting that sets AllowPrivate, for the refusal
	// message. Empty means there is no opt-in on this daemon (a managed one),
	// and the message does not suggest one.
	OptInSetting string
}

var alwaysBlockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("169.254.0.0/16"),   // link-local v4: GCP, AWS, Azure, ECS task metadata
	netip.MustParsePrefix("fe80::/10"),        // link-local v6
	netip.MustParsePrefix("fd00:ec2::/32"),    // AWS IMDS over IPv6
	netip.MustParsePrefix("fd20:ce::254/128"), // GCE metadata over IPv6
	netip.MustParsePrefix("100.100.100.200/32"),
	netip.MustParsePrefix("168.63.129.16/32"), // Azure wire server
}

var metadataHosts = map[string]bool{
	"metadata":                   true,
	"metadata.goog":              true,
	"metadata.google.internal":   true,
	"instance-data":              true,
	"instance-data.ec2.internal": true,
}

func (n FetchNetwork) checkHost(host string) error {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(strings.Trim(host, "[]"))), ".")
	if host == "" {
		return fmt.Errorf("%w: empty host", netguard.ErrBlockedAddress)
	}
	if metadataHosts[host] || strings.HasSuffix(host, ".metadata.google.internal") {
		return n.alwaysBlocked(host)
	}
	if ip := net.ParseIP(host); ip != nil {
		return n.CheckIP(ip)
	}
	return nil
}

func (n FetchNetwork) CheckIP(ip net.IP) error {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return fmt.Errorf("%w: not an IP address", netguard.ErrBlockedAddress)
	}
	addrs := embeddedAddrs(addr.Unmap())
	for _, a := range addrs {
		for _, p := range alwaysBlockedPrefixes {
			if p.Contains(a) {
				return n.alwaysBlocked(ip.String())
			}
		}
	}
	if n.AllowPrivate {
		return nil
	}
	for _, a := range addrs {
		if netguard.IPBlocked(net.IP(a.AsSlice())) {
			return n.privateBlocked(ip.String())
		}
	}
	return nil
}

// embeddedAddrs is addr plus every IPv4 address it carries inside an IPv6
// transition form, so that 64:ff9b::a9fe:a9fe cannot be used to reach
// 169.254.169.254.
func embeddedAddrs(addr netip.Addr) []netip.Addr {
	out := []netip.Addr{addr}
	if !addr.Is6() {
		return out
	}
	b := addr.As16()
	v4 := func(o ...byte) netip.Addr { return netip.AddrFrom4([4]byte{o[0], o[1], o[2], o[3]}) }
	switch {
	case b[0] == 0x00 && b[1] == 0x64 && b[2] == 0xff && b[3] == 0x9b && allZero(b[4:12]): // 64:ff9b::/96 NAT64
		out = append(out, v4(b[12:16]...))
	case b[0] == 0x20 && b[1] == 0x02: // 2002::/16 6to4
		out = append(out, v4(b[2:6]...))
	case b[0] == 0x20 && b[1] == 0x01 && b[2] == 0 && b[3] == 0: // 2001::/32 Teredo: server, and client xor 0xff
		out = append(out, v4(b[4:8]...), v4(b[12]^0xff, b[13]^0xff, b[14]^0xff, b[15]^0xff))
	case allZero(b[:12]): // ::a.b.c.d, IPv4-compatible
		out = append(out, v4(b[12:16]...))
	}
	return out
}

func allZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}

func (n FetchNetwork) alwaysBlocked(dest string) error {
	return fmt.Errorf("%w: %s is a link-local or cloud-metadata address; fetch never connects to these, whatever the settings", netguard.ErrBlockedAddress, dest)
}

func (n FetchNetwork) privateBlocked(dest string) error {
	if n.OptInSetting == "" {
		return fmt.Errorf("%w: %s is a loopback or private-network address; fetch on this machine cannot reach it", netguard.ErrBlockedAddress, dest)
	}
	return fmt.Errorf("%w: %s is a loopback or private-network address. By default fetch only reaches public addresses. "+
		"To let it reach this machine's own localhost and network, set %s=true and restart the daemon",
		netguard.ErrBlockedAddress, dest, n.OptInSetting)
}

// NewFetchToolWithNetwork builds fetch for a daemon, under that daemon's FetchNetwork.
func NewFetchToolWithNetwork(n FetchNetwork) Tool {
	newClient := func(timeoutSecs int) *http.Client {
		c := newFetchNetworkClient(n, time.Duration(timeoutSecs)*time.Second)
		c.CheckRedirect = fetchCheckRedirect
		return c
	}
	return NewToolWrapper[FetchParams, ToolResponse](&fetchTool{
		client:    newClient(fetchDefaultTimeout),
		newClient: newClient,
	})
}
