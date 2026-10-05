// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Local-model relay subjects. A local model server runs on a DAEMON's machine,
// so the worker's OpenAI-compatible HTTP is relayed worker -> NATS -> daemon
// gateway (bridge) -> daemon stream -> local server, and back.
//
// This is a dedicated subject family, deliberately NOT the daemon.command path:
// that one is request/reply bounded by a 330s timeout, while a generation can
// stream for many minutes and must be bounded by IDLE time instead.
//
// Every subject carries the userID, and the bridge only subscribes the subjects
// of daemons that user owns. A worker can therefore only reach its own user's
// daemons: authorization is the subject.
const (
	// daemon.localmodel.request.{userID}.{daemonID}: worker -> bridge, a
	// proto-encoded LocalModelHTTPRequest (chunk-assembled when oversize).
	// Carries a reply inbox; the bridge answers with a localModelAck.
	localModelRequestSubject = "daemon.localmodel.request"
	// daemon.localmodel.chunk.{userID}.{requestID}: bridge -> worker, one
	// proto-encoded LocalModelHTTPChunk per message, gapless sequence from 0.
	localModelChunkSubject = "daemon.localmodel.chunk"
	// daemon.localmodel.cancel.{userID}.{daemonID}: worker -> bridge,
	// {"request_id": "..."}; fire and forget.
	localModelCancelSubject = "daemon.localmodel.cancel"
	// daemon.localmodel.refresh.{userID}.{daemonID}: worker -> bridge request
	// asking the daemon to re-probe; replies with a localModelRefreshReply.
	localModelRefreshSubject = "daemon.localmodel.refresh"
)

// CommandLocalModelsSetEndpoints is the daemon command that replaces the
// user-configured local model endpoints in the daemon machine's
// ~/.reliant/config.yaml (models.providers.local). Payload:
// {"base_urls": ["http://localhost:11434/v1", ...]}.
const CommandLocalModelsSetEndpoints = "localmodels.set_endpoints"

const (
	// localModelAckTimeout bounds the wait for the bridge to accept a request.
	localModelAckTimeout = 10 * time.Second

	// LocalModelRefreshTimeout bounds how long a refresh waits for the next
	// inventory from the daemon.
	LocalModelRefreshTimeout = 10 * time.Second

	// defaultLocalModelIdleTimeout is how long a relayed request may go
	// without a single chunk before the worker gives up. Generous because a
	// cold model load before the first token can take minutes.
	defaultLocalModelIdleTimeout = 5 * time.Minute

	// localModelWorkerIdleGrace and localModelBridgeIdleGrace stagger the
	// idle deadlines when the request names one: daemon first (it has the
	// most informative error), then the worker, then the bridge as the
	// backstop that frees its in-flight slot.
	localModelWorkerIdleGrace = 5 * time.Second
	localModelBridgeIdleGrace = 10 * time.Second

	// maxInFlightLocalModelRequests bounds concurrent relayed requests per
	// bridge, separately from maxInFlightRequests so long generations cannot
	// starve tool calls (and vice versa).
	maxInFlightLocalModelRequests = 64
)

// ErrLocalModelDaemonUnavailable matches (errors.Is) any
// *LocalModelDaemonUnavailableError.
var ErrLocalModelDaemonUnavailable = errors.New("local model daemon unavailable")

// ErrLocalModelIdleTimeout means a relayed request produced no chunk for the
// whole idle window.
var ErrLocalModelIdleTimeout = errors.New("local model request idle timeout")

// LocalModelDaemonUnavailableError says a local model request could not be
// handed to the named daemon (offline, gateway overloaded, no answer). The
// request never started.
type LocalModelDaemonUnavailableError struct {
	DaemonID string
	Reason   string
}

func (e *LocalModelDaemonUnavailableError) Error() string {
	return fmt.Sprintf("local model daemon %s is unavailable: %s", e.DaemonID, e.Reason)
}

func (e *LocalModelDaemonUnavailableError) Is(target error) bool {
	return target == ErrLocalModelDaemonUnavailable
}

// LocalModelRelayError is a failure reported by the daemon (or its relay)
// after the request was accepted: the local server was unreachable, the
// endpoint was unknown, the idle timeout fired, the daemon disconnected.
type LocalModelRelayError struct {
	DaemonID string
	Message  string
}

func (e *LocalModelRelayError) Error() string {
	return fmt.Sprintf("local model relay via daemon %s failed: %s", e.DaemonID, e.Message)
}

type localModelAck struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

type localModelCancelWire struct {
	RequestID string `json:"request_id"`
}

// localModelRefreshReply carries a protojson LocalModelInventory.
type localModelRefreshReply struct {
	Inventory json.RawMessage `json:"inventory,omitempty"`
	Error     string          `json:"error,omitempty"`
}
