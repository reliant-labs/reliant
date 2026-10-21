// Copyright (c) 2025 Reliant Labs

package app

import (
	"github.com/reliant-labs/forge/pkg/serverkit"

	grpcserver "github.com/reliant-labs/reliant/internal/grpc"
)

// The gateway root's field order is its STOP order: the first field stops
// first. That order is the daemon hand-over, and it is load-bearing
// (TestGatewayStopsTheHandOverFirst):
//
//  1. managed_reconciler: outbound gateway→daemon dials stop first, so the
//     connector does not redial a daemon this replica is about to drop.
//  2. daemon_listener: its drain is make-before-break. As it begins, every
//     ConnectDaemon stream hands its daemon over (tools_daemon's
//     handOverOnDrain: stop routing here, then GatewayDrain naming the
//     drain's hard deadline); the daemon opens a stream to a replica that is
//     already serving and closes this one once nothing is in flight on it.
//     The drain waits for that, and ends what is still open at the deadline
//     with Unavailable, the daemon's redial signal.
//  3. activity_subscriber, tool_bridge, state_derivation: the NATS sides,
//     after no daemon stream is left to route to.
//  4. tools_daemon_sweeper: ends anything the service still holds (an
//     outbound connection), and stops the stale-connection sweeper.
//
// Then the infrastructure, each after everything that uses it: the
// user-updates hub, NATS, the database. The probe listener (HEALTH_PORT)
// answers for the whole of boot and drain: the readiness paths say
// "draining" from the first moment of the stop.
//
// The gateway never migrates: the api-server owns the schema, and the
// database provider waits for it (migrate=wait).

// Gateway's doc comment is `reliant server gateway`'s help (roots_gen.go):
// the first paragraph its Short, the rest its Long.

// Run the daemon connection gateway
//
// Starts the daemon gateway that manages bidirectional gRPC streams to
// tools-daemon processes. Routes tool execution requests from API servers
// and Temporal workers to the correct daemon via NATS.
//
// Designed to run as few stateful replicas.
//
//forge:root gateway command=server,gateway migrate=wait
type Gateway struct {
	Reconciler *ManagedReconciler
	Daemon     *grpcserver.DaemonListener
	Activity   *ActivitySubscriber
	// Listeners registers after the bridge (and stops with nothing: it has
	// no lifecycle of its own).
	Listeners   *ConnectionListeners
	Bridge      *ToolBridge
	Derivation  *StateDerivation
	ToolsDaemon *ToolsDaemonSweeper
	// FlowHealth serves /flow-health on the probe surface.
	FlowHealth *FlowHealth
	// Pprof is off unless PPROF_ADDR names an address; it stops last, so a
	// profile can be taken through the whole drain.
	Pprof *serverkit.Pprof
}
