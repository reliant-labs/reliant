// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/netguard"
	"github.com/reliant-labs/reliant/internal/rctx"
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

// The daemon's other web tools and its local model endpoints may reach the
// user's own network (a daemon-wide egress setting), but fetch does not
// inherit it: it has its own policy. Built through the daemon's real factory,
// fetch refuses loopback and cloud metadata, and names its own setting.
func TestDaemonFetchDoesNotInheritTheDaemonWideEgressPolicy(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("private service"))
	}))
	defer local.Close()

	run := func(host Host, url string) tools.ToolResponse {
		t.Helper()
		resp, err := newDaemonToolsFactory(host).GetToolByName(tools.ToolFetch, nil).Run(
			&rctx.ToolContext{Context: context.Background()},
			tools.ToolCall{ID: "c", Name: tools.ToolFetch, Input: `{"url":"` + url + `","format":"text"}`})
		require.NoError(t, err)
		return resp
	}

	for _, host := range []Host{{}, {DaemonType: daemonTypeSelfHosted}, {DaemonType: daemonTypeManaged}} {
		resp := run(host, local.URL)
		assert.True(t, resp.IsError, "%+v", host)
		assert.NotContains(t, resp.Content, "private service", "%+v", host)

		for _, url := range []string{"http://169.254.169.254/latest/meta-data/", "http://[fd00:ec2::254]/", "http://metadata.google.internal/"} {
			resp := run(Host{AllowPrivateFetch: true}, url)
			assert.True(t, resp.IsError, url)
			assert.NotContains(t, resp.Content, FetchAllowPrivateSetting, "no setting unblocks %s", url)
		}
	}

	resp := run(Host{}, local.URL)
	assert.Contains(t, resp.Content, FetchAllowPrivateSetting, "the refusal names the opt-in")

	resp = run(Host{AllowPrivateFetch: true}, local.URL)
	assert.False(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, "private service")
}
