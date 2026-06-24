// Copyright (c) 2025 Reliant Labs

package servergateway

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/net/http2"

	"github.com/reliant-labs/reliant/internal/daemonevents"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/grpc/services"
	"github.com/reliant-labs/reliant/internal/logging"
)

// connectFailedPublisher is the narrow surface the connector uses to report
// outbound connect failures upstream. Satisfied by *daemonevents.Publisher.
type connectFailedPublisher interface {
	OnDaemonConnectFailed(userID, daemonID, reason string)
}

const (
	connectorLogPrefix = "[DaemonConnector]"

	// Reconnection backoff parameters.
	reconnectMinDelay = 1 * time.Second
	reconnectMaxDelay = 15 * time.Second
)

// DaemonConnector initiates outbound gRPC connections to daemon pods. It is
// driven by ManagedDaemonReconciler — the reconciler calls startConnection /
// stopConnection based on the authoritative DAEMON_STATE snapshot stream.
type DaemonConnector struct {
	daemonService *services.ToolsDaemonService
	// failPublisher emits NATS events for outbound connect failures so the
	// control plane can persist a reason on the daemon row. Nil is allowed
	// (no-op) — keeps tests simple.
	failPublisher connectFailedPublisher

	mu          sync.Mutex
	activeConns map[string]context.CancelFunc // daemonID → cancel
}

// NewDaemonConnector creates a new DaemonConnector. The failPublisher is
// optional; pass nil to disable connect-failure event emission.
func NewDaemonConnector(daemonService *services.ToolsDaemonService, failPublisher *daemonevents.Publisher) *DaemonConnector {
	dc := &DaemonConnector{
		daemonService: daemonService,
		activeConns:   make(map[string]context.CancelFunc),
	}
	if failPublisher != nil {
		dc.failPublisher = failPublisher
	}
	return dc
}

// CloseAll cancels every outbound connection currently managed by the connector.
// Call it on gateway shutdown to drain streams cleanly.
func (dc *DaemonConnector) CloseAll() {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	for daemonID, cancel := range dc.activeConns {
		cancel()
		delete(dc.activeConns, daemonID)
	}
}

// startConnection initiates a new outbound connection to a daemon pod.
// If a connection already exists for this daemonID, it is cancelled first.
func (dc *DaemonConnector) startConnection(parentCtx context.Context, daemonID, userID, podIP string, podPort int) {
	dc.mu.Lock()
	if cancel, ok := dc.activeConns[daemonID]; ok {
		cancel()
		delete(dc.activeConns, daemonID)
	}
	connCtx, connCancel := context.WithCancel(parentCtx)
	dc.activeConns[daemonID] = connCancel
	dc.mu.Unlock()

	logging.Info(connectorLogPrefix+" starting outbound connection",
		"daemonId", daemonID, "userId", userID, "podIp", podIP, "port", podPort)

	go dc.connectWithRetry(connCtx, daemonID, userID, podIP, podPort)
}

// stopConnection cancels the context for an active daemon connection.
func (dc *DaemonConnector) stopConnection(daemonID string) {
	dc.mu.Lock()
	if cancel, ok := dc.activeConns[daemonID]; ok {
		cancel()
		delete(dc.activeConns, daemonID)
		logging.Info(connectorLogPrefix+" Stopping outbound connection", "daemonId", daemonID)
	}
	dc.mu.Unlock()
}

// connectWithRetry connects to the daemon pod and retries with backoff on failure.
// Each failure is also surfaced via the connectFailedPublisher (if configured)
// so the control plane can show *why* the daemon can't connect instead of a
// generic "Disconnected" badge.
func (dc *DaemonConnector) connectWithRetry(ctx context.Context, daemonID, userID, podIP string, podPort int) {
	delay := reconnectMinDelay
	for {
		err := dc.connectOnce(ctx, daemonID, userID, podIP, podPort)
		if ctx.Err() != nil {
			return
		}

		// Surface the failure upstream. publish() is best-effort — never
		// blocks the retry loop.
		if dc.failPublisher != nil && err != nil {
			dc.failPublisher.OnDaemonConnectFailed(userID, daemonID, err.Error())
		}

		logging.Warn(connectorLogPrefix+" Outbound connection lost, retrying",
			"daemonId", daemonID, "error", err, "retryIn", delay)

		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}

		// Exponential backoff: 1s, 2s, 4s, 8s, 15s max.
		delay = delay * 2
		if delay > reconnectMaxDelay {
			delay = reconnectMaxDelay
		}
	}
}

// connectOnce opens a single ConnectGateway bidi stream to the daemon pod,
// registers it with ToolsDaemonService, and runs the message loops until
// the stream ends.
func (dc *DaemonConnector) connectOnce(ctx context.Context, daemonID, userID, podIP string, podPort int) error {
	baseURL := fmt.Sprintf("http://%s:%d", podIP, podPort)

	client := reliantv1connect.NewToolsDaemonServiceClient(
		h2cClient(),
		baseURL,
	)

	// Use a derived context that is cancelled only if the daemon takes too
	// long to send its DaemonRegister. The stream is bound to regCtx for its
	// entire lifetime — regCtx is also cancelled when the parent ctx is, so
	// the stream still dies on shutdown / stopConnection. Once registration
	// succeeds we stop the timer so the cancel is never invoked and the
	// stream can run indefinitely.
	regCtx, regCancel := context.WithCancel(ctx)
	regTimer := time.AfterFunc(10*time.Second, regCancel)

	stream := client.ConnectGateway(regCtx)

	// Kick the stream so the underlying HTTP request actually gets dispatched.
	// connect-go's bidi defers makeRequest() until the first Send/CloseRequest;
	// our protocol is server-first (daemon speaks first), so without this we'd
	// block forever waiting for a request that was never sent. We send an
	// intentionally-empty GatewayHello rather than identity-bearing data: the
	// gateway must NEVER transmit identity over this channel — the daemon is
	// untrusted, and the (daemonID, conn) mapping lives only in this process's
	// memory so the daemon can't assert an identity to anyone.
	hello := &reliantv1.ServerMessage{
		Message: &reliantv1.ServerMessage_Hello{Hello: &reliantv1.GatewayHello{}},
	}
	if err := stream.Send(hello); err != nil {
		regCancel()
		_ = stream.CloseRequest()
		_ = stream.CloseResponse()
		return fmt.Errorf("sending gateway hello: %w", err)
	}

	// The daemon sends DaemonRegister as the first message.
	msg, err := stream.Receive()
	// Stop returns false if the timer already fired (or was already stopped).
	// If it fired, regCancel has been invoked (or is about to be) and the
	// stream is dead — treat as a registration timeout regardless of what
	// Receive returned.
	if !regTimer.Stop() {
		regCancel()
		_ = stream.CloseRequest()
		_ = stream.CloseResponse()
		return fmt.Errorf("receiving registration: timed out after 10s")
	}
	if err != nil {
		regCancel()
		_ = stream.CloseRequest()
		_ = stream.CloseResponse()
		return fmt.Errorf("receiving registration: %w", err)
	}

	reg := msg.GetRegister()
	if reg == nil {
		_ = stream.CloseRequest()
		_ = stream.CloseResponse()
		return fmt.Errorf("first message was not DaemonRegister")
	}

	// Identity comes from the reconciler snapshot, not from the daemon.
	// The daemon is untrusted — the gateway is the authority.
	logging.Info(connectorLogPrefix+" Received registration from daemon",
		"daemonId", daemonID, "userId", userID,
		"hostname", reg.Hostname, "daemonType", reg.DaemonType)

	// Send registration ack with the assigned daemon_id.
	regAck := &reliantv1.ServerMessage{
		Message: &reliantv1.ServerMessage_RegistrationAck{
			RegistrationAck: &reliantv1.RegistrationAck{
				Accepted: true,
				DaemonId: daemonID,
			},
		},
	}
	if err := stream.Send(regAck); err != nil {
		_ = stream.CloseRequest()
		_ = stream.CloseResponse()
		return fmt.Errorf("sending registration ack: %w", err)
	}

	// Register with ToolsDaemonService — identity comes from the reconciler,
	// capabilities/platform from the daemon.
	outbound, err := dc.daemonService.RegisterOutboundConnection(ctx, userID, daemonID, podIP, podPort, reg, stream)
	if err != nil {
		_ = stream.CloseRequest()
		_ = stream.CloseResponse()
		return fmt.Errorf("registering outbound connection: %w", err)
	}

	// Cleanup via defer so the connections map is freed even if
	// HandleIncoming panics. Disconnect is idempotent — if the sweeper
	// already nuked this connection, this is a no-op.
	defer outbound.Disconnect()
	defer func() { _ = stream.CloseResponse() }()

	// Run the receive loop — blocks until the stream ends.
	return outbound.HandleIncoming(ctx)
}

// h2cClient returns an HTTP client configured for h2c (HTTP/2 cleartext),
// used for intra-cluster connections to daemon pods without TLS. This follows
// the canonical connect-go h2c pattern: DialTLS (not DialTLSContext) and no
// extra fields on http2.Transport. Deviating from this shape has been observed
// to cause SETTINGS frames to silently not reach the server.
func h2cClient() *http.Client {
	return &http.Client{
		Transport: &http2.Transport{
			AllowHTTP: true,
			DialTLS: func(network, addr string, _ *tls.Config) (net.Conn, error) {
				var d net.Dialer
				return d.Dial(network, addr)
			},
		},
	}
}
