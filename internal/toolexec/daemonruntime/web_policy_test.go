// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/netguard"
)

func fetchAllows(host Host, ip string) error {
	n := fetchNetwork(host)
	factory := tools.NewToolsFactory(&tools.ToolsOptions{FetchNetwork: &n})
	_ = factory
	return n.CheckIP(net.ParseIP(ip))
}

// fetch runs on the daemon, so what it may reach is a decision about the
// machine the daemon is on. Nowhere is private reachable by default, and
// link-local/metadata is never reachable.
func TestFetchNetwork_ByWhereTheDaemonRuns(t *testing.T) {
	for name, host := range map[string]Host{
		"laptop (platform says nothing)": {},
		"declared self-hosted":           {DaemonType: daemonTypeSelfHosted},
		"managed pod":                    {DaemonType: daemonTypeManaged, RuntimeType: "kata"},
	} {
		for _, ip := range []string{"127.0.0.1", "10.0.0.5", "192.168.1.20", "::1"} {
			err := fetchAllows(host, ip)
			require.Error(t, err, "%s: %s must be blocked by default", name, ip)
			assert.ErrorIs(t, err, netguard.ErrBlockedAddress)
		}
		assert.NoError(t, fetchAllows(host, "93.184.216.34"), name)
	}
}

func TestFetchNetwork_OptInIsHonouredOnlyOffManagedDaemons(t *testing.T) {
	for name, host := range map[string]Host{
		"laptop":      {AllowPrivateFetch: true},
		"self-hosted": {DaemonType: daemonTypeSelfHosted, AllowPrivateFetch: true},
	} {
		assert.NoError(t, fetchAllows(host, "127.0.0.1"), name)
		assert.NoError(t, fetchAllows(host, "192.168.1.20"), name)
		// Never, however it is set.
		for _, ip := range []string{"169.254.169.254", "fe80::1", "fd00:ec2::254"} {
			err := fetchAllows(host, ip)
			require.Error(t, err, "%s: %s", name, ip)
			assert.NotContains(t, err.Error(), FetchAllowPrivateSetting)
		}
	}

	managed := Host{DaemonType: daemonTypeManaged, AllowPrivateFetch: true}
	err := fetchAllows(managed, "10.0.0.5")
	require.Error(t, err, "a managed daemon stays strict whatever the setting says: its network is the platform's, not the user's")
	assert.NotContains(t, err.Error(), FetchAllowPrivateSetting, "and it offers no opt-in")
}

func TestFetchNetwork_RefusalNamesTheSetting(t *testing.T) {
	err := fetchAllows(Host{}, "127.0.0.1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), FetchAllowPrivateSetting)
	assert.Equal(t, "RELIANT_FETCH_ALLOW_PRIVATE_NETWORK", FetchAllowPrivateSetting)
}

// The daemon's tool registry is built with that policy, and fetch is advertised
// in its capabilities as a prefer-daemon tool.
func TestDaemonToolsFactoryAdvertisesFetch(t *testing.T) {
	factory := newDaemonToolsFactory(Host{DaemonType: daemonTypeManaged})
	assert.Contains(t, factory.ListAvailableToolsForPlacement(tools.PlacementDaemon), tools.ToolFetch,
		"a prefer-daemon tool must be advertised in the daemon's capabilities")
	assert.NotNil(t, factory.GetToolByName(tools.ToolFetch, nil))
}
