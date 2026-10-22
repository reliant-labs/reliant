// Copyright (c) 2025 Reliant Labs
package app

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

// The worker stops taking work first: the Temporal worker's Drain is what
// releases in-flight activities (workersetup.Worker), so it runs before
// anything those activities use is stopped. Then the loops, then the account
// recorder the activities record into, then the infrastructure, each after
// everything that uses it. The probe listener answers for the whole of it.
//
// This is today's stop order, node for node, from before the worker was a
// root (serverworker's workerNodes, and the deferred closes after it).
func TestWorkerStopsTakingWorkFirst(t *testing.T) {
	plan := PlanWorker()
	require.Equal(t, []string{
		"temporal_worker",
		"workflow_event_relay",
		"account_usage_poller",
		"write_ledger_prune",
		"account_recorder",
		"streaming_hub",
		"temporal_client",
		"chat_update_hub",
		"user_update_hub",
		"nats_conn",
		"debug_server",
		"db_repo",
		"probe_listener",
	}, plan.Stop())

	byName := map[string]bool{}
	for _, n := range plan {
		byName[n.Name] = true
		switch n.Name {
		case "temporal_worker":
			require.True(t, n.Run, "the Temporal worker polls")
			require.True(t, n.Drain, "the Temporal worker drains: that is what releases activities")
			require.True(t, n.Ready, "the Temporal worker gates readiness")
		case "workflow_event_relay", "account_usage_poller", "write_ledger_prune":
			require.True(t, n.Run, "%s runs", n.Name)
		case "account_recorder":
			require.True(t, n.Close, "the recorder flushes on close")
		}
	}
	require.True(t, byName["worker_daemon_router"], "the worker routes tools through its own router")
}

// Readiness while the worker runs is what the hand-written /ready checked —
// the database, Temporal, NATS and the streaming hub — plus the Temporal
// worker polling, behind the drain gate.
func TestWorkerReadiness(t *testing.T) {
	var ready []string
	for _, n := range PlanWorker() {
		if n.Ready {
			ready = append(ready, n.Name)
		}
	}
	require.Equal(t, []string{"probe_listener", "db_repo", "nats_conn", "temporal_client", "streaming_hub", "temporal_worker"}, ready)
}

// The process-wide state is installed before anything could read it: the
// config-only settings before every other node, the database-backed globals
// (the LLM key provider, analytics, the error reporter) right after the
// database they read.
func TestWorkerInstallsProcessStateFirst(t *testing.T) {
	names := PlanWorker().Names()
	require.Equal(t, []string{"probe_listener", "integration_settings", "process_settings", "db_repo", "database", "process_runtime"}, names[:6])
}

// The worker's variants never reach another root: each is
// //forge:root-only worker (forge generate fails otherwise), and the gateway
// builds none of them.
func TestWorkerVariantsStayInTheWorker(t *testing.T) {
	gateway := PlanGateway().Names()
	for _, name := range []string{
		"worker_daemon_router", "worker_tools_factory", "worker_chat", "worker_run", "worker_trigger",
		"worker_catalog_search", "worker_integration_credentials", "remote_executor", "temporal_worker",
	} {
		require.Contains(t, PlanWorker().Names(), name)
		require.False(t, slices.Contains(gateway, name), "the gateway builds %s", name)
	}
}
