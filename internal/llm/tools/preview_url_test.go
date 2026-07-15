// Copyright (c) 2025 Reliant Labs
package tools

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/stretchr/testify/require"
)

func TestIsPubliclyBoundPort(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"0.0.0.0", true},
		{"::", true},
		{"[::]", true},
		{"*", true},
		{"", true},
		{"127.0.0.1", false},
		{"localhost", false},
		{"::1", false},
		{"192.168.1.5", false},
	}
	for _, c := range cases {
		got := isPubliclyBoundPort(daemon.PortInfo{Address: c.addr, State: "LISTEN"})
		require.Equalf(t, c.want, got, "addr=%q", c.addr)
	}

	// Non-LISTEN sockets are never previewable even on a wildcard bind.
	require.False(t, isPubliclyBoundPort(daemon.PortInfo{Address: "0.0.0.0", State: "ESTABLISHED"}))
}

func TestProxyPreviewURL(t *testing.T) {
	// Dev/test: path-based proxy URL on the local proxy host.
	require.Equal(t,
		"http://localhost:28080/proxy/daemon-123/3000/",
		proxyPreviewURL(config.EnvironmentDev, "daemon-123", 3000))

	// Prod (and other non-dev): subdomain-based workspace URL.
	require.Equal(t,
		"https://3000-daemon-123.workspaces.reliantapi.com",
		proxyPreviewURL(config.EnvironmentProd, "daemon-123", 3000))

	// No daemon identity (fully-local) → no proxy URL.
	require.Equal(t, "", proxyPreviewURL(config.EnvironmentProd, "", 3000))
	require.Equal(t, "", proxyPreviewURL(config.EnvironmentDev, "daemon-123", 0))
}

func TestPreviewURLsForProcess(t *testing.T) {
	t.Setenv("RELIANT_ENV", "dev")
	t.Setenv("RELIANT_PROXY_HOST", "")

	// Remote daemon, dev-server on 0.0.0.0 → proxied preview line.
	remote := &daemon.ProcessInfo{
		Status:   "running",
		DaemonID: "daemon-abc",
		Ports: []daemon.PortInfo{
			{Port: 3000, Address: "0.0.0.0", State: "LISTEN"},
		},
	}
	require.Equal(t,
		[]string{"Preview URL (port 3000): http://localhost:28080/proxy/daemon-abc/3000/"},
		previewURLsForProcess(remote))

	// 127.0.0.1-only bind is not previewable → skipped (agent must rebind).
	loopbackOnly := &daemon.ProcessInfo{
		Status:   "running",
		DaemonID: "daemon-abc",
		Ports:    []daemon.PortInfo{{Port: 3000, Address: "127.0.0.1", State: "LISTEN"}},
	}
	require.Nil(t, previewURLsForProcess(loopbackOnly))

	// Not running → nothing surfaced.
	stopped := &daemon.ProcessInfo{
		Status:   "completed",
		DaemonID: "daemon-abc",
		Ports:    []daemon.PortInfo{{Port: 3000, Address: "0.0.0.0", State: "LISTEN"}},
	}
	require.Nil(t, previewURLsForProcess(stopped))

	// Local daemon (no identity) → loopback URL is correct for the user.
	local := &daemon.ProcessInfo{
		Status:   "running",
		DaemonID: "",
		Ports:    []daemon.PortInfo{{Port: 5173, Address: "0.0.0.0", State: "LISTEN"}},
	}
	require.Equal(t,
		[]string{"Preview URL (port 5173): http://localhost:5173/"},
		previewURLsForProcess(local))

	// Mixed ports: only the public one is surfaced, deduped.
	mixed := &daemon.ProcessInfo{
		Status:   "running",
		DaemonID: "daemon-abc",
		Ports: []daemon.PortInfo{
			{Port: 8080, Address: "127.0.0.1", State: "LISTEN"},
			{Port: 3000, Address: "0.0.0.0", State: "LISTEN"},
			{Port: 3000, Address: "0.0.0.0", State: "LISTEN"},
		},
	}
	require.Equal(t,
		[]string{"Preview URL (port 3000): http://localhost:28080/proxy/daemon-abc/3000/"},
		previewURLsForProcess(mixed))
}
