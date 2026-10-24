// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/egresslimit"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// A daemon bounds its own egress. A managed (hosted) daemon leaves through the
// hub's shared NAT, so it gets the server limiter's defaults and no knob; a
// local daemon's traffic leaves from the user's own address, so it gets a
// higher ceiling the user may move. Both are an abuse brake for a well-behaved
// daemon, not a security boundary: the daemon is user-controlled.

func TestDaemonEgressLimits_ManagedGetsTheServerDefaultsWhateverIsConfigured(t *testing.T) {
	want := egresslimit.Limits{}.WithDefaults()
	for name, host := range map[string]Host{
		"unconfigured": {DaemonType: daemonTypeManaged},
		"configured":   {DaemonType: daemonTypeManaged, Egress: egresslimit.Limits{UserPerMinute: 100000, UserBurst: 100000, UserMaxConcurrent: 1000, HostMaxConcurrent: 1000}},
	} {
		assert.Equal(t, want, daemonEgressLimits(host).WithDefaults(), name)
	}
}

func TestDaemonEgressLimits_LocalCeilingIsHigherThanManagedAndConfigurable(t *testing.T) {
	managed := daemonEgressLimits(Host{DaemonType: daemonTypeManaged}).WithDefaults()
	for name, host := range map[string]Host{
		"laptop":      {},
		"self-hosted": {DaemonType: daemonTypeSelfHosted},
	} {
		local := daemonEgressLimits(host).WithDefaults()
		assert.Greater(t, local.UserPerMinute, managed.UserPerMinute, name)
		assert.Greater(t, local.UserBurst, managed.UserBurst, name)
		assert.Greater(t, local.UserMaxConcurrent, managed.UserMaxConcurrent, name)
		assert.Greater(t, local.HostMaxConcurrent, managed.HostMaxConcurrent, name)
		assert.Equal(t, 1, local.Replicas, "a daemon is one process: nothing shares its budget")
	}

	got := daemonEgressLimits(Host{Egress: egresslimit.Limits{UserPerMinute: 5, UserMaxConcurrent: 3}}).WithDefaults()
	assert.Equal(t, 5, got.UserPerMinute, "a configured value replaces the local default")
	assert.Equal(t, 3, got.UserMaxConcurrent)
	assert.Equal(t, daemonEgressLimits(Host{}).WithDefaults().UserBurst, got.UserBurst, "an unset one keeps it")
}

// The limiter is on the executor the daemon really runs tools through, so a
// fetch past the limit comes back as the fail-soft tool result and never dials.
func TestNewDaemonClient_FetchIsEgressLimited(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	client, err := newDaemonClient(testBootstrapConfig(t.TempDir()), Host{
		WorkingDir:        t.TempDir(),
		AllowPrivateFetch: true,
		Egress:            egresslimit.Limits{UserPerMinute: 60, UserBurst: 2},
	}, Guards{})
	require.NoError(t, err)

	fetch := func() *toolexec.ExecutionResult {
		return client.localExecutor.ExecuteTool(t.Context(), tools.ToolFetch,
			fmt.Sprintf(`{"url":%q,"format":"text"}`, srv.URL), "call", 0, map[string]any{})
	}
	for i := 0; i < 2; i++ {
		require.False(t, fetch().IsError)
	}
	res := fetch()
	require.True(t, res.IsError)
	require.Equal(t, toolexec.ErrorCodeEgressRateLimited, res.ErrorCode)
	require.Contains(t, res.Content, "retry after")
	require.Equal(t, 2, hits)
}
