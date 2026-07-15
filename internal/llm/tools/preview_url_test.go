// Copyright (c) 2025 Reliant Labs
package tools

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/stretchr/testify/require"
)

func TestIsPreviewablePort(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"0.0.0.0", true},
		{"::", true},
		{"[::]", true},
		{"*", true},
		{"", true},
		// Loopback binds are previewable via the in-pod preview forwarder
		// (the forwarder dials 127.0.0.1/::1 inside the pod netns).
		{"127.0.0.1", true},
		{"localhost", true},
		{"::1", true},
		{"[::1]", true},
		// A specific non-loopback interface bind is not routed by the
		// forwarder (it only dials loopback) nor guaranteed pod-reachable.
		{"192.168.1.5", false},
	}
	for _, c := range cases {
		got := isPreviewablePort(daemon.PortInfo{Address: c.addr, State: "LISTEN"})
		require.Equalf(t, c.want, got, "addr=%q", c.addr)
	}

	// Non-LISTEN sockets are never previewable even on a wildcard bind.
	require.False(t, isPreviewablePort(daemon.PortInfo{Address: "0.0.0.0", State: "ESTABLISHED"}))
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

	// 127.0.0.1-only bind IS previewable: the in-pod preview forwarder
	// terminates preview traffic inside the pod netns and dials loopback.
	loopbackOnly := &daemon.ProcessInfo{
		Status:   "running",
		DaemonID: "daemon-abc",
		Ports:    []daemon.PortInfo{{Port: 3000, Address: "127.0.0.1", State: "LISTEN"}},
	}
	require.Equal(t,
		[]string{"Preview URL (port 3000): http://localhost:28080/proxy/daemon-abc/3000/"},
		previewURLsForProcess(loopbackOnly))

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

	// Mixed ports: both loopback and wildcard binds surface, deduped by port.
	mixed := &daemon.ProcessInfo{
		Status:   "running",
		DaemonID: "daemon-abc",
		Ports: []daemon.PortInfo{
			{Port: 8080, Address: "192.168.1.5", State: "LISTEN"},
			{Port: 3000, Address: "0.0.0.0", State: "LISTEN"},
			{Port: 3000, Address: "127.0.0.1", State: "LISTEN"},
		},
	}
	require.Equal(t,
		[]string{"Preview URL (port 3000): http://localhost:28080/proxy/daemon-abc/3000/"},
		previewURLsForProcess(mixed))
}
