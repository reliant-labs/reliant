// Copyright (c) 2025 Reliant Labs

// Package netports detects TCP ports with LISTEN sockets inside the current
// network namespace by parsing the kernel's /proc/net/tcp and /proc/net/tcp6
// tables. It exists so the tools-daemon (which shares a netns with the user's
// dev servers in a workspace pod) can report "something is listening on port
// N" to the gateway, which surfaces it as a preview affordance in the UI.
//
// Only loopback and wildcard binds are reported — those are exactly the
// listeners the in-pod preview forwarder can reach on 127.0.0.1/[::1]. A
// socket bound to a single non-loopback address is skipped. IPv4 and IPv6
// duplicates of the same port collapse to one entry.
//
// On hosts without /proc/net/tcp (macOS, non-Linux) every operation degrades
// to "nothing detected" — no errors, no goroutine churn.
//
// forge:exclude-contract
//
// Leaf utility package: the exported surface is concrete helpers over the
// stdlib or the OS, with no collaborator to fake and no second implementation.
// An interface here would have exactly one implementor and one caller shape,
// which is indirection without a seam.
package netports

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
)

const (
	procTCP4Path      = "/proc/net/tcp"
	procTCP6Path      = "/proc/net/tcp6"
	procPortRangePath = "/proc/sys/net/ipv4/ip_local_port_range"

	// tcpListenState is the kernel's TCP_LISTEN in the st column (hex).
	tcpListenState = "0A"

	// defaultEphemeralLo/Hi are the Linux default local (ephemeral) port
	// range, used when ip_local_port_range can't be read.
	defaultEphemeralLo = 32768
	defaultEphemeralHi = 60999
)

// ListeningLoopbackPorts returns the sorted, deduplicated set of TCP ports
// with a LISTEN socket bound to loopback or the wildcard address, excluding
// any port in the exclude set. ok is false when the proc tables are absent
// (non-Linux hosts).
//
// Ports inside the kernel's local (ephemeral) port range are skipped: those
// are auto-assigned listeners — language servers, debug adapters, tool RPC
// sockets — that no user would type into a preview URL. A real workspace pod
// was observed with ~10 such loopback listeners alongside the one actual dev
// server (vite on 5174); without this filter the UI affordance is noise.
// Detection is an affordance, not a gate — an ephemeral-range server can
// still be previewed by entering its port manually.
func ListeningLoopbackPorts(exclude map[int]bool) (ports []uint32, ok bool) {
	return listeningLoopbackPorts(procTCP4Path, procTCP6Path, procPortRangePath, exclude)
}

func listeningLoopbackPorts(tcp4Path, tcp6Path, portRangePath string, exclude map[int]bool) ([]uint32, bool) {
	seen := map[int]bool{}
	anyTable := false
	for _, path := range []string{tcp4Path, tcp6Path} {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		anyTable = true
		parseProcTCP(f, seen)
		f.Close()
	}
	if !anyTable {
		return nil, false
	}

	ephLo, ephHi := localPortRange(portRangePath)
	ports := make([]uint32, 0, len(seen))
	for port := range seen {
		if exclude[port] || (port >= ephLo && port <= ephHi) {
			continue
		}
		ports = append(ports, uint32(port))
	}
	sort.Slice(ports, func(i, j int) bool { return ports[i] < ports[j] })
	return ports, true
}

// localPortRange reads the kernel's ephemeral port range ("<lo>\t<hi>"),
// falling back to the Linux default when unreadable.
func localPortRange(path string) (lo, hi int) {
	raw, err := os.ReadFile(path)
	if err == nil {
		fields := strings.Fields(string(raw))
		if len(fields) == 2 {
			l, errLo := strconv.Atoi(fields[0])
			h, errHi := strconv.Atoi(fields[1])
			if errLo == nil && errHi == nil && l > 0 && h >= l {
				return l, h
			}
		}
	}
	return defaultEphemeralLo, defaultEphemeralHi
}

// parseProcTCP scans one /proc/net/tcp{,6} table and records the ports of
// LISTEN sockets on loopback/wildcard addresses into seen. Malformed lines
// are skipped — the kernel format is stable, but a parse bug must never take
// the caller down.
func parseProcTCP(r io.Reader, seen map[int]bool) {
	scanner := bufio.NewScanner(r)
	scanner.Scan() // header line ("sl local_address rem_address st ...")
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		// fields: sl local_address rem_address st ...
		if len(fields) < 4 || fields[3] != tcpListenState {
			continue
		}
		ip, port, err := parseProcAddr(fields[1])
		if err != nil || port == 0 {
			continue
		}
		if !ip.IsLoopback() && !ip.IsUnspecified() {
			continue
		}
		seen[port] = true
	}
}

// parseProcAddr decodes a /proc/net/tcp local_address column ("HEXIP:HEXPORT").
// The IP is hex-encoded in host byte order per 32-bit word (little-endian on
// every platform we ship to), so each 4-byte group is reversed. The port is
// plain big-endian hex.
func parseProcAddr(field string) (net.IP, int, error) {
	ipHex, portHex, found := strings.Cut(field, ":")
	if !found {
		return nil, 0, fmt.Errorf("malformed proc address %q", field)
	}
	raw, err := hex.DecodeString(ipHex)
	if err != nil || (len(raw) != 4 && len(raw) != 16) {
		return nil, 0, fmt.Errorf("malformed proc IP %q", ipHex)
	}
	// Reverse each 32-bit word: the kernel prints __be32 words via %08X,
	// which renders the in-memory little-endian byte order.
	ip := make(net.IP, len(raw))
	for word := 0; word < len(raw); word += 4 {
		ip[word+0] = raw[word+3]
		ip[word+1] = raw[word+2]
		ip[word+2] = raw[word+1]
		ip[word+3] = raw[word+0]
	}
	port64, err := strconv.ParseUint(portHex, 16, 16)
	if err != nil {
		return nil, 0, fmt.Errorf("malformed proc port %q", portHex)
	}
	return ip, int(port64), nil
}
