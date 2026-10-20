// Copyright (c) 2025 Reliant Labs
package serverapi

import (
	"testing"

	"github.com/stretchr/testify/require"

	grpcserver "github.com/reliant-labs/reliant/internal/grpc"
	"github.com/reliant-labs/reliant/internal/integrations/ghaccess"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
)

// Supervise stops the nodes in reverse: the API listener stops taking calls
// first — its drain ends the long-lived streams and waits for the unary calls
// in flight while everything they use still runs — and the loops after it.
func TestAPINodesStopTakingCallsFirst(t *testing.T) {
	nodes := apiNodes(apiComponents{
		keyHealer:    &drivers.ReliantKeyHealer{},
		gitHubAccess: &ghaccess.Refresher{},
		listener:     &grpcserver.APIListener{},
	})
	stop := make([]string, 0, len(nodes))
	for i := len(nodes) - 1; i >= 0; i-- {
		stop = append(stop, nodes[i].Name)
	}
	require.Equal(t, []string{
		"api-listener",
		"trigger-redriver",
		"trigger-reconciler",
		"trigger-schedule-sync",
		"worktree-sweeper",
		"queued-delivery",
		"daemon-events-consumer",
		"github-access",
		"workflow-reconciler",
		"reliant-key-sweep",
		"payload-gc",
	}, stop)

	listener := nodes[len(nodes)-1]
	require.NotNil(t, listener.Run, "the listener serves")
	require.NotNil(t, listener.Drain, "the listener drains: that is what ends its streams with a status")
	require.NotNil(t, listener.Close, "the listener ends what its drain left")
	require.NotNil(t, listener.Ready, "the listener gates readiness")
	for _, loop := range nodes[:len(nodes)-1] {
		require.NotNil(t, loop.Run, "%s runs", loop.Name)
		require.Nil(t, loop.Ready, "%s does not gate readiness", loop.Name)
	}
	require.NotNil(t, nodes[2].Close, "the workflow reconciler waits for its pass to stop")
}

// A deployment with no control plane has no Reliant key to heal, and one
// receiving no GitHub events has no access to refresh: neither runs a loop.
func TestAPINodesOmitAbsentLoops(t *testing.T) {
	for _, n := range apiNodes(apiComponents{listener: &grpcserver.APIListener{}}) {
		require.NotContains(t, []string{"reliant-key-sweep", "github-access"}, n.Name)
	}
}
