// Copyright (c) 2025 Reliant Labs

package app

import (
	grpcserver "github.com/reliant-labs/reliant/internal/grpc"
)

// The api root's field order is its STOP order: the first field stops first,
// and a field's closure is built just before it. That order is load-bearing
// (TestAPIStopsTakingCallsFirst):
//
//  1. api_listener: stops taking calls first. Its drain ends every
//     long-lived stream as it begins (each with the status its client
//     reconnects on, against a replica that is still serving) and waits for
//     the unary calls in flight, while everything they use is still running.
//  2. the loops — trigger redrive, reconcile and startup sync, the worktree
//     sweep, queued delivery and the daemon-events consumer, the GitHub
//     access refresh, the workflow reconciler, the Reliant-key sweep and the
//     payload GC — each ending with its context. None takes traffic, and none
//     depends on another. A failure in one is logged, not fatal: each is
//     repaired by its next pass or the next replica.
//
// Then the infrastructure, each after everything that uses it — the daemon
// router, the update hubs, the streaming hub and NATS, the Temporal client,
// the diagnostics listener, the database — which is why each is a field too:
// reached first through the listener or a loop, it would stop between them.
// The probe listener (HEALTH_PORT) answers for the whole of boot and drain:
// the readiness paths say "draining" from the first moment of the stop.
//
// The last fields are the process-wide state, so they are built FIRST:
// APISettings (the api-server's own config) and Settings (config only)
// before any other node, Runtime (the database-backed globals) right
// after the database.
//
// The api-server is the ONE process that applies migrations (migrate=apply):
// its process policy runs them before the root is built, and the worker and
// the gateway wait for them (db.MigrationPolicy). Keep it that way — a second
// migrator reintroduces the startup race that policy exists to remove.

// API's doc comment is `reliant server api`'s help (roots_gen.go): the first
// paragraph its Short, the rest its Long.

// Run the stateless HTTP + gRPC API server
//
// Starts the Reliant API server for cloud deployments. This is a stateless
// server that handles HTTP REST and gRPC/ConnectRPC requests, connecting to
// external Temporal, Postgres, and NATS services.
//
// Designed to run as N replicas behind a load balancer.
//
//forge:root api command=server,api migrate=apply
type API struct {
	Listener          *grpcserver.APIListener
	TriggerRedriver   *TriggerRedriver
	TriggerReconciler *TriggerReconciler
	ScheduleSync      *TriggerScheduleSync
	WorktreeSweeper   *WorktreeSweeper
	QueuedDelivery    *QueuedDelivery
	DaemonEvents      *DaemonEventsConsumer
	// GitHubAccess is nil without RELIANT_GITHUB_WEBHOOK_SECRET.
	GitHubAccess *GitHubAccessLoop
	Reconciler   *WorkflowReconciler
	// KeySweep is nil without a control plane.
	KeySweep  *ReliantKeySweep
	PayloadGC *PayloadGC
	// LLMUsageRetention prunes per-call LLM usage past its retention period.
	LLMUsageRetention *LLMUsageRetention
	Router            *APIDaemonRouter
	// Feed publishes the repository's user and chat updates; it has no
	// lifecycle, and orders the hubs after the router.
	Feed      *RepoUpdateFeed
	Streaming *StreamingHub
	Temporal  *TemporalClient
	// Debug is off with PPROF_PORT=0.
	Debug       *APIDebugServer
	Runtime     *ProcessRuntime
	Settings    *ProcessSettings
	APISettings *APISettings
}
