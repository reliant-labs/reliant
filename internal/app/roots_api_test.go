// Copyright (c) 2025 Reliant Labs
package app

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

// The api-server stops taking calls first: the listener's drain ends the
// long-lived streams and waits for the unary calls in flight while
// everything they use still runs. Then the loops, then the infrastructure,
// each after everything that uses it. The probe listener answers for the
// whole of it.
//
// The listener and the loops are today's stop order, node for node, from
// before the api-server was a root (serverapi's apiNodes: api-listener,
// trigger-redriver, … payload-gc), and the infrastructure is what Run's
// defers closed after them — the daemon router, the update hubs, NATS and
// the streaming hub, the Temporal client, the database — in that order but
// for one pair: the streaming hub now closes before NATS, which its first
// connect waits for. The diagnostics listener, which nothing used to stop,
// stops after them all but the database.
func TestAPIStopsTakingCallsFirst(t *testing.T) {
	plan := PlanAPI()
	require.Equal(t, []string{
		"api_listener",
		"trigger_redriver",
		"trigger_reconciler",
		"trigger_schedule_sync",
		"worktree_sweeper",
		"queued_delivery",
		"daemon_events_consumer",
		"github_access_loop",
		"workflow_reconciler",
		"reliant_key_sweep",
		"payload_gc",
		"llm_usage_retention",
		"api_daemon_router",
		"chat_update_hub",
		"user_update_hub",
		"streaming_hub",
		"nats_conn",
		"temporal_client",
		"api_debug_server",
		"db_repo",
		"probe_listener",
	}, plan.Stop())

	loops := []string{
		"trigger_redriver", "trigger_reconciler", "trigger_schedule_sync", "worktree_sweeper", "queued_delivery",
		"daemon_events_consumer", "github_access_loop", "workflow_reconciler", "reliant_key_sweep", "payload_gc", "llm_usage_retention",
	}
	for _, n := range plan {
		switch {
		case n.Name == "api_listener":
			require.True(t, n.Run, "the listener serves")
			require.True(t, n.Drain, "the listener drains: that is what ends its streams with a status")
			require.True(t, n.Close, "the listener ends what its drain left")
			require.True(t, n.Ready, "the listener gates readiness")
		case slices.Contains(loops, n.Name):
			require.True(t, n.Run, "%s runs", n.Name)
			require.False(t, n.Ready, "%s does not gate readiness", n.Name)
			require.False(t, n.Drain, "%s ends with its context", n.Name)
		}
		if n.Name == "workflow_reconciler" {
			require.True(t, n.Close, "the workflow reconciler waits for its pass to stop")
		}
	}
}

// Readiness while the api-server runs is what the hand-written /ready
// checked — the database, NATS and the streaming hub — plus Temporal, which
// only the worker checked before (a Temporal that does not answer now takes
// an api replica out of rotation too), and the listener, behind the drain
// gate.
func TestAPIReadiness(t *testing.T) {
	var ready []string
	for _, n := range PlanAPI() {
		if n.Ready {
			ready = append(ready, n.Name)
		}
	}
	require.Equal(t, []string{"probe_listener", "db_repo", "temporal_client", "nats_conn", "streaming_hub", "api_listener"}, ready)
}

// The process-wide state is installed before anything could read it: the
// api-server's own config and the config-only settings before
// every other node, the database-backed globals (the LLM key provider and
// Reliant-key healer, analytics, the error reporter) right after the
// database they read.
func TestAPIInstallsProcessStateFirst(t *testing.T) {
	names := PlanAPI().Names()
	require.Equal(t, []string{"probe_listener", "api_settings", "integration_settings", "process_settings", "db_repo", "database", "process_runtime"}, names[:7])
}

// The api root reaches only the api's variants: never the worker's router —
// the one that may fall back to a trigger's delegated token — nor anything
// that executes tools, and none of its own nodes reach another root.
func TestAPIVariantsStayInTheAPI(t *testing.T) {
	api := PlanAPI().Names()
	for _, name := range []string{
		"worker_daemon_router", "worker_tools_factory", "worker_chat", "worker_run", "worker_trigger",
		"worker_catalog_search", "worker_integration_credentials", "remote_executor", "temporal_worker",
	} {
		require.False(t, slices.Contains(api, name), "the api root builds the worker's %s", name)
	}
	require.Contains(t, api, "api_daemon_router")

	others := append(PlanWorker().Names(), PlanGateway().Names()...)
	for _, name := range []string{
		"api_settings", "api_daemon_router", "api_tools_factory", "api_catalog_search", "api_connections",
		"user_auth", "user_chain", "user_routes", "http_routes", "api_listener", "api_debug_server",
		"reset_guard", "queued_deliverer", "github_access", "trigger_inbound", "trigger_senders",
		"trigger_redriver", "trigger_reconciler", "trigger_schedule_sync", "worktree_sweeper", "queued_delivery",
		"daemon_events_consumer", "github_access_loop", "workflow_reconciler", "reliant_key_sweep", "payload_gc", "llm_usage_retention",
	} {
		require.Contains(t, api, name)
		require.False(t, slices.Contains(others, name), "another root builds the api's %s", name)
	}
}
