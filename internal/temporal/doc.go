// Copyright (c) 2025 Reliant Labs
//
// Package temporal is reliant's Temporal SDK wiring: client construction
// (NewExternalClient), the payload data converter, the SDK logger adapter,
// and the two signal senders built on a client.Client (AgentMessageNotifier,
// SpawnStopper).
//
// The package's contract opt-out is declared here, once: forge reads a package
// directive from any file, and refuses two that disagree.
//
//forge:exclude-contract: Temporal SDK wiring (client construction, data converter, logger adapter, and the agent-message and spawn-cancel signal senders over a client.Client); no service of its own to bind
package temporal
