// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/reliant/internal/llm/tools"
)

// fetch runs on the daemon, so its dial policy is a decision about the machine
// the daemon is on. A user's own machine may reach the user's own localhost and
// LAN; a managed workspace pod may not (its NetworkPolicy already limits it to
// public 80/443, and the app-level check is the second layer).
func TestWebToolsPolicy_ByWhereTheDaemonRuns(t *testing.T) {
	loopback, lan, metadata := net.ParseIP("127.0.0.1"), net.ParseIP("192.168.1.20"), net.ParseIP("169.254.169.254")

	for name, host := range map[string]Host{
		"laptop (platform says nothing)": {},
		"declared self-hosted":           {DaemonType: daemonTypeSelfHosted},
	} {
		p := webToolsPolicy(host)
		assert.True(t, p.IPAllowed(loopback), name)
		assert.True(t, p.IPAllowed(lan), name)
	}

	managed := webToolsPolicy(Host{DaemonType: daemonTypeManaged, RuntimeType: "kata"})
	assert.False(t, managed.IPAllowed(loopback))
	assert.False(t, managed.IPAllowed(lan))
	assert.False(t, managed.IPAllowed(metadata))
	assert.True(t, managed.IPAllowed(net.ParseIP("93.184.216.34")), "public addresses stay reachable")
}

// The daemon's tool registry is built with that policy, so the fetch it runs
// is bounded by it and not by the hosted-worker default.
func TestDaemonToolsFactoryAdvertisesFetchAndUsesTheHostPolicy(t *testing.T) {
	factory := newDaemonToolsFactory(Host{DaemonType: daemonTypeManaged})
	assert.Contains(t, factory.ListAvailableToolsForPlacement(tools.PlacementDaemon), tools.ToolFetch,
		"a prefer-daemon tool must be advertised in the daemon's capabilities")
	assert.NotNil(t, factory.GetToolByName(tools.ToolFetch, nil))
}
