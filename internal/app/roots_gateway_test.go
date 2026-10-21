// Copyright (c) 2025 Reliant Labs
package app

import (
	"fmt"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/daemonevents"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// The gateway's stop order is the daemon hand-over: outbound dials stop
// before the listener drains (the connector must not redial a daemon this
// replica is dropping), the listener drains before the NATS sides close (a
// daemon still moving keeps its routes), the service closes after them
// (whatever the drain left), and the infrastructure after everything that
// uses it. The probe listener answers for the whole of it.
func TestGatewayStopsTheHandOverFirst(t *testing.T) {
	plan := PlanGateway()
	require.Equal(t, []string{
		"managed_reconciler",
		"daemon_listener",
		"activity_subscriber",
		"tool_bridge",
		"state_derivation",
		"tools_daemon_sweeper",
		"user_update_hub",
		"nats_conn",
		"db_repo",
		"pprof",
		"probe_listener",
	}, plan.Stop())

	for _, n := range plan {
		if n.Name == "daemon_listener" {
			require.True(t, n.Run, "the listener serves")
			require.True(t, n.Drain, "the listener drains: that is the hand-over")
			require.True(t, n.Ready, "the listener reports readiness")
		}
	}
}

// Readiness while the gateway runs is the database, NATS and the daemon
// listener — the checks the hand-written /ready made — behind the drain gate.
func TestGatewayReadiness(t *testing.T) {
	var ready []string
	for _, n := range PlanGateway() {
		if n.Ready {
			ready = append(ready, n.Name)
		}
	}
	require.Equal(t, []string{"probe_listener", "db_repo", "nats_conn", "daemon_listener"}, ready)
}

// ToolsDaemonService's connection listeners hear a daemon connect in the
// order they registered: the tool bridge first (the daemon's routes exist
// before anything else hears of it), then the lifecycle publisher, the
// DAEMON_EVENTS publisher, the status responder and the per-user liveness
// responder.
func TestGatewayConnectionListenersRegisterInOrder(t *testing.T) {
	names := PlanGateway().Names()
	require.Less(t, slices.Index(names, "tool_bridge"), slices.Index(names, "connection_listeners"),
		"the bridge registers first: connection_listeners is built after it")

	reg := &recordingRegistrar{}
	events := &daemonevents.Publisher{}
	got := registerConnectionListeners(reg, nil, nil, events)
	require.Equal(t, []string{
		"*toolexec.DaemonLifecyclePublisher",
		"*daemonevents.Publisher",
		"*daemonquery.Responder",
		"*daemonquery.UserResponder",
	}, reg.types)
	require.Equal(t, reg.added, got.Registered)
	require.Same(t, events, got.Registered[1])
}

type recordingRegistrar struct {
	added []toolexec.DaemonConnectionListener
	types []string
}

func (r *recordingRegistrar) AddConnectionListener(l toolexec.DaemonConnectionListener) {
	r.added = append(r.added, l)
	r.types = append(r.types, fmt.Sprintf("%T", l))
}
