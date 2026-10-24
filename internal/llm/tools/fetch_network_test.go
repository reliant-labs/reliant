// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/netguard"
	"github.com/reliant-labs/reliant/internal/rctx"
)

const optInSetting = "RELIANT_FETCH_ALLOW_PRIVATE_NETWORK"

func localDefault() FetchNetwork { return FetchNetwork{OptInSetting: optInSetting} }
func localOptedIn() FetchNetwork {
	return FetchNetwork{AllowPrivate: true, OptInSetting: optInSetting}
}

// Link-local and cloud-metadata destinations are refused on every daemon, opt-in or not.
func TestFetchNetwork_MetadataAndLinkLocalAreAlwaysBlocked(t *testing.T) {
	for _, ip := range []string{
		"169.254.169.254", "169.254.0.1", "169.254.170.2", // GCP/AWS/ECS, link-local v4
		"fe80::1", "fe80::a9fe:a9fe", // link-local v6
		"fd00:ec2::254",          // AWS IMDS over IPv6 (in the unique-local range, which the opt-in otherwise allows)
		"::ffff:169.254.169.254", // v4-mapped
		"64:ff9b::a9fe:a9fe",     // NAT64 embedding 169.254.169.254
		"2002:a9fe:a9fe::1",      // 6to4 embedding 169.254.169.254
		"100.100.100.200",        // Alibaba metadata (inside CGNAT, which the opt-in otherwise allows)
		"168.63.129.16",          // Azure wire server (a public-looking address)
	} {
		for name, n := range map[string]FetchNetwork{"default": localDefault(), "opted in": localOptedIn()} {
			err := n.CheckIP(net.ParseIP(ip))
			require.Error(t, err, "%s must be blocked (%s)", ip, name)
			assert.ErrorIs(t, err, netguard.ErrBlockedAddress, ip)
			assert.NotContains(t, err.Error(), optInSetting, "no setting unblocks %s", ip)
		}
	}
}

func TestFetchNetwork_MetadataHostnamesAreAlwaysBlocked(t *testing.T) {
	for _, host := range []string{"metadata.google.internal", "METADATA.GOOGLE.INTERNAL.", "metadata", "metadata.goog", "instance-data", "instance-data.ec2.internal"} {
		for name, n := range map[string]FetchNetwork{"default": localDefault(), "opted in": localOptedIn()} {
			err := n.checkHost(host)
			require.Error(t, err, "%s (%s)", host, name)
			assert.ErrorIs(t, err, netguard.ErrBlockedAddress)
			assert.NotContains(t, err.Error(), optInSetting)
		}
	}
	assert.NoError(t, localOptedIn().checkHost("example.com"))
}

func TestFetchNetwork_PrivateIsBlockedByDefaultAndAllowedWithTheOptIn(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.1.2.3", "172.16.0.9", "192.168.1.20", "::1", "fc00::1", "100.64.0.1", "0.0.0.0"} {
		err := localDefault().CheckIP(net.ParseIP(ip))
		require.Error(t, err, "%s must be blocked by default", ip)
		assert.ErrorIs(t, err, netguard.ErrBlockedAddress)
		assert.NoError(t, localOptedIn().CheckIP(net.ParseIP(ip)), "%s is allowed once the user opts in", ip)
	}
	for _, ip := range []string{"93.184.216.34", "2606:4700:4700::1111"} {
		assert.NoError(t, localDefault().CheckIP(net.ParseIP(ip)), ip)
	}
}

// The refusal tells the user which setting lets them choose otherwise, and says
// nothing of the sort for what no setting can unblock or for a managed daemon.
func TestFetchNetwork_RefusalNamesTheSetting(t *testing.T) {
	err := localDefault().CheckIP(net.ParseIP("127.0.0.1"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), optInSetting)
	assert.Contains(t, err.Error(), "127.0.0.1")

	managed := FetchNetwork{} // no opt-in surface
	err = managed.CheckIP(net.ParseIP("10.0.0.1"))
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "RELIANT_")
	assert.ErrorIs(t, err, netguard.ErrBlockedAddress)
}

func serveLoopback(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("the user's own dev server"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func runNetworkFetch(t *testing.T, n FetchNetwork, url string) ToolResponse {
	t.Helper()
	tool := NewFetchToolWithNetwork(n).(*ToolWrapper[FetchParams, ToolResponse])
	resp, err := tool.tool.Execute(&rctx.ToolContext{Context: context.Background()}, FetchParams{URL: url, Format: "text"})
	require.NoError(t, err, "a refusal is a tool result, never a Go error that would fail the call")
	return resp
}

func TestFetchWithNetwork_LoopbackIsRefusedWithTheSettingNamedAndAllowedWhenOptedIn(t *testing.T) {
	srv := serveLoopback(t)

	resp := runNetworkFetch(t, localDefault(), srv.URL)
	assert.True(t, resp.IsError)
	assert.Contains(t, resp.Content, optInSetting)
	assert.NotContains(t, resp.Content, "dev server")

	resp = runNetworkFetch(t, localOptedIn(), srv.URL)
	assert.False(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, "the user's own dev server")
}

func TestFetchWithNetwork_MetadataIsRefusedEvenWhenOptedIn(t *testing.T) {
	for _, url := range []string{
		"http://169.254.169.254/computeMetadata/v1/instance/service-accounts/default/token",
		"http://[fd00:ec2::254]/latest/meta-data/",
		"http://metadata.google.internal/computeMetadata/v1/",
		"http://[::ffff:169.254.169.254]/",
	} {
		resp := runNetworkFetch(t, localOptedIn(), url)
		assert.True(t, resp.IsError, url)
		assert.Contains(t, resp.Content, "not allowed", url)
		assert.NotContains(t, resp.Content, optInSetting, url)
	}
}

// A redirect is a dial like any other, so it passes through the same checks.
func TestFetchWithNetwork_RedirectIsCheckedLikeAnyDial(t *testing.T) {
	target := serveLoopback(t)
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()

	resp := runNetworkFetch(t, localOptedIn(), redirector.URL)
	assert.False(t, resp.IsError, resp.Content)

	resp = runNetworkFetch(t, localDefault(), redirector.URL)
	assert.True(t, resp.IsError)
	assert.True(t, strings.Contains(resp.Content, optInSetting), resp.Content)
}

// ToolsOptions.AllowPrivateEgress covers the web tools that run in a server
// role or on the daemon for the user's own network (websearch, sourcegraph,
// local model endpoints). fetch on a daemon takes FetchNetwork instead and
// must not inherit it.
func TestFactoryFetch_FetchNetworkOverridesAllowPrivateEgress(t *testing.T) {
	srv := serveLoopback(t)
	run := func(opts *ToolsOptions) ToolResponse {
		t.Helper()
		tool := NewToolsFactory(opts).Fetch().(*ToolWrapper[FetchParams, ToolResponse])
		resp, err := tool.tool.Execute(&rctx.ToolContext{Context: context.Background()}, FetchParams{URL: srv.URL, Format: "text"})
		require.NoError(t, err)
		return resp
	}

	// Without FetchNetwork, the daemon-wide flag is what decides (server roles, self-hosted).
	assert.False(t, run(&ToolsOptions{AllowPrivateEgress: true}).IsError)
	assert.True(t, run(&ToolsOptions{}).IsError)

	// With FetchNetwork, AllowPrivateEgress=true opens nothing for fetch.
	strict := localDefault()
	resp := run(&ToolsOptions{AllowPrivateEgress: true, FetchNetwork: &strict})
	assert.True(t, resp.IsError)
	assert.Contains(t, resp.Content, optInSetting)

	optedIn := localOptedIn()
	assert.False(t, run(&ToolsOptions{FetchNetwork: &optedIn}).IsError, "the fetch opt-in works with AllowPrivateEgress unset")

	// And never metadata, whichever flag is set.
	for _, opts := range []*ToolsOptions{
		{AllowPrivateEgress: true, FetchNetwork: &optedIn},
		{AllowPrivateEgress: true, FetchNetwork: &strict},
	} {
		tool := NewToolsFactory(opts).Fetch().(*ToolWrapper[FetchParams, ToolResponse])
		resp, err := tool.tool.Execute(&rctx.ToolContext{Context: context.Background()},
			FetchParams{URL: "http://169.254.169.254/latest/meta-data/", Format: "text"})
		require.NoError(t, err)
		assert.True(t, resp.IsError)
		assert.NotContains(t, resp.Content, optInSetting)
	}
}
