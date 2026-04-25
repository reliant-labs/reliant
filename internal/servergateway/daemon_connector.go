// Copyright (c) 2025 Reliant Labs

package servergateway

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"golang.org/x/net/http2"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/grpc/services"
	"github.com/reliant-labs/reliant/internal/logging"
)

const (
	connectorLogPrefix = "[DaemonConnector]"

	// JetStream stream and subjects for daemon connect/disconnect commands.
	streamDaemonCommands     = "DAEMON_COMMANDS"
	subjectCommandsAll       = "daemon.v1.commands.*"
	subjectCommandConnect    = "daemon.v1.commands.connect"
	subjectCommandDisconnect = "daemon.v1.commands.disconnect"

	// Durable consumer name for this gateway instance.
	consumerDaemonCommands = "gateway-daemon-commands"

	// Reconnection backoff parameters.
	reconnectMinDelay = 1 * time.Second
	reconnectMaxDelay = 15 * time.Second
)

// DaemonConnectCommand is the NATS message format for a connect command.
type DaemonConnectCommand struct {
	Version  int    `json:"version"`
	DaemonID string `json:"daemonId"`
	UserID   string `json:"userId"`
	PodIP    string `json:"podIp"`
	Port     int    `json:"port"`
}

// DaemonDisconnectCommand is the NATS message format for a disconnect command.
type DaemonDisconnectCommand struct {
	Version  int    `json:"version"`
	DaemonID string `json:"daemonId"`
	UserID   string `json:"userId"`
}

// DaemonConnector subscribes to NATS JetStream for daemon connect/disconnect
// commands and initiates outbound gRPC connections to daemon pods.
type DaemonConnector struct {
	nc            jetstream.JetStream
	daemonService *services.ToolsDaemonService

	mu          sync.Mutex
	activeConns map[string]context.CancelFunc // daemonID → cancel
}

// NewDaemonConnector creates a new DaemonConnector.
func NewDaemonConnector(js jetstream.JetStream, daemonService *services.ToolsDaemonService) *DaemonConnector {
	return &DaemonConnector{
		nc:            js,
		daemonService: daemonService,
		activeConns:   make(map[string]context.CancelFunc),
	}
}

// Start subscribes to the DAEMON_COMMANDS JetStream stream and processes
// connect/disconnect commands. It blocks until ctx is cancelled.
func (dc *DaemonConnector) Start(ctx context.Context) error {
	// Ensure the stream exists.
	if _, err := dc.nc.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      streamDaemonCommands,
		Subjects:  []string{subjectCommandsAll},
		Retention: jetstream.LimitsPolicy,
		Storage:   jetstream.FileStorage,
		MaxAge:    24 * time.Hour,
		MaxMsgs:   100000,
	}); err != nil {
		return fmt.Errorf("ensure %s stream: %w", streamDaemonCommands, err)
	}

	consumer, err := dc.nc.CreateOrUpdateConsumer(ctx, streamDaemonCommands, jetstream.ConsumerConfig{
		Durable:        consumerDaemonCommands,
		AckPolicy:      jetstream.AckExplicitPolicy,
		FilterSubjects: []string{subjectCommandConnect, subjectCommandDisconnect},
	})
	if err != nil {
		return fmt.Errorf("create consumer: %w", err)
	}

	logging.Info(connectorLogPrefix+" Started — consuming from "+streamDaemonCommands,
		"subjects", []string{subjectCommandConnect, subjectCommandDisconnect},
	)

	// Consume messages until ctx is cancelled.
	cc, err := consumer.Consume(func(msg jetstream.Msg) {
		dc.handleMessage(ctx, msg)
	})
	if err != nil {
		return fmt.Errorf("start consuming: %w", err)
	}
	defer cc.Stop()

	<-ctx.Done()

	// Cancel all active connections on shutdown.
	dc.mu.Lock()
	for daemonID, cancel := range dc.activeConns {
		cancel()
		delete(dc.activeConns, daemonID)
	}
	dc.mu.Unlock()

	logging.Info(connectorLogPrefix + " Stopped")
	return nil
}

func (dc *DaemonConnector) handleMessage(ctx context.Context, msg jetstream.Msg) {
	subject := msg.Subject()

	switch subject {
	case subjectCommandConnect:
		var cmd DaemonConnectCommand
		if err := json.Unmarshal(msg.Data(), &cmd); err != nil {
			logging.Error(connectorLogPrefix+" Failed to unmarshal connect command", "error", err)
			_ = msg.Nak()
			return
		}
		if cmd.DaemonID == "" || cmd.PodIP == "" || cmd.Port == 0 {
			logging.Warn(connectorLogPrefix+" Invalid connect command — missing required fields",
				"daemonId", cmd.DaemonID, "podIp", cmd.PodIP, "port", cmd.Port)
			_ = msg.Term()
			return
		}
		_ = msg.Ack()
		dc.startConnection(ctx, cmd)

	case subjectCommandDisconnect:
		var cmd DaemonDisconnectCommand
		if err := json.Unmarshal(msg.Data(), &cmd); err != nil {
			logging.Error(connectorLogPrefix+" Failed to unmarshal disconnect command", "error", err)
			_ = msg.Nak()
			return
		}
		_ = msg.Ack()
		dc.stopConnection(cmd.DaemonID)

	default:
		logging.Warn(connectorLogPrefix+" Unknown command subject", "subject", subject)
		_ = msg.Term()
	}
}

// startConnection initiates a new outbound connection to a daemon pod.
// If a connection already exists for this daemonID, it is cancelled first.
func (dc *DaemonConnector) startConnection(parentCtx context.Context, cmd DaemonConnectCommand) {
	dc.mu.Lock()
	if cancel, ok := dc.activeConns[cmd.DaemonID]; ok {
		cancel()
		delete(dc.activeConns, cmd.DaemonID)
	}
	connCtx, connCancel := context.WithCancel(parentCtx)
	dc.activeConns[cmd.DaemonID] = connCancel
	dc.mu.Unlock()

	logging.Info(connectorLogPrefix+" Starting outbound connection",
		"daemonId", cmd.DaemonID, "userId", cmd.UserID,
		"podIp", cmd.PodIP, "port", cmd.Port)

	go dc.connectWithRetry(connCtx, cmd)
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
func (dc *DaemonConnector) connectWithRetry(ctx context.Context, cmd DaemonConnectCommand) {
	delay := reconnectMinDelay
	for {
		err := dc.connectOnce(ctx, cmd)
		if ctx.Err() != nil {
			return
		}

		logging.Warn(connectorLogPrefix+" Outbound connection lost, retrying",
			"daemonId", cmd.DaemonID, "error", err, "retryIn", delay)

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
func (dc *DaemonConnector) connectOnce(ctx context.Context, cmd DaemonConnectCommand) error {
	baseURL := fmt.Sprintf("http://%s:%d", cmd.PodIP, cmd.Port)

	client := reliantv1connect.NewToolsDaemonServiceClient(
		h2cClient(),
		baseURL,
	)

	stream := client.ConnectGateway(ctx)

	// The daemon sends DaemonRegister as the first message.
	msg, err := stream.Receive()
	if err != nil {
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

	logging.Info(connectorLogPrefix+" Received registration from daemon",
		"daemonId", reg.DaemonId, "userId", reg.UserId,
		"hostname", reg.Hostname, "daemonType", reg.DaemonType)

	// Send registration ack.
	regAck := &reliantv1.ServerMessage{
		Message: &reliantv1.ServerMessage_RegistrationAck{
			RegistrationAck: &reliantv1.RegistrationAck{
				Accepted: true,
			},
		},
	}
	if err := stream.Send(regAck); err != nil {
		_ = stream.CloseRequest()
		_ = stream.CloseResponse()
		return fmt.Errorf("sending registration ack: %w", err)
	}

	// Register with ToolsDaemonService — this sets up the connection in the
	// internal maps, starts sender/heartbeat goroutines, and notifies listeners
	// (NATSToolBridge) so NATS subscriptions are created.
	outbound, err := dc.daemonService.RegisterOutboundConnection(ctx, reg, stream)
	if err != nil {
		_ = stream.CloseRequest()
		_ = stream.CloseResponse()
		return fmt.Errorf("registering outbound connection: %w", err)
	}

	// Run the receive loop — blocks until the stream ends.
	recvErr := outbound.HandleIncoming(ctx)

	// Clean up the connection from ToolsDaemonService state.
	outbound.Disconnect()

	_ = stream.CloseResponse()

	return recvErr
}

// h2cClient returns an HTTP client configured for h2c (HTTP/2 cleartext),
// used for intra-cluster connections to daemon pods without TLS.
func h2cClient() *http.Client {
	return &http.Client{
		Transport: &http2.Transport{
			AllowHTTP:       true,
			ReadIdleTimeout: 60 * time.Second,
			PingTimeout:     15 * time.Second,
			DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, addr)
			},
		},
	}
}
