// Copyright (c) 2025 Reliant Labs
package servergateway

import (
	"testing"

	"github.com/stretchr/testify/require"

	grpcserver "github.com/reliant-labs/reliant/internal/grpc"
)

// Supervise stops the nodes in reverse, and that order is the daemon
// hand-over: outbound dials stop before the listener drains (the connector
// must not redial a daemon this replica is dropping), the listener drains
// before the NATS sides close (a daemon still moving keeps its routes), and
// the service closes last (whatever the drain left).
func TestGatewayNodesStopTheHandOverFirst(t *testing.T) {
	nodes := gatewayNodes(gatewayComponents{listener: &grpcserver.DaemonListener{}})
	stop := make([]string, 0, len(nodes))
	for i := len(nodes) - 1; i >= 0; i-- {
		stop = append(stop, nodes[i].Name)
	}
	require.Equal(t, []string{
		"managed-reconciler",
		"daemon-listener",
		"activity-subscriber",
		"tool-bridge",
		"state-derivation",
		"tools-daemon",
	}, stop)

	listener := nodes[len(nodes)-2]
	require.NotNil(t, listener.Run, "the listener serves")
	require.NotNil(t, listener.Drain, "the listener drains: that is the hand-over")
	require.NotNil(t, listener.Ready, "the listener reports readiness")
}
