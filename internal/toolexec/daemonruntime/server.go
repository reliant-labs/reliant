// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"fmt"
	"net"
	"net/http"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/toolexec/daemonstate"
	"github.com/reliant-labs/reliant/internal/toolexec/transport"
)

const serverLogPrefix = "[🔧 DaemonServer]"

// daemonServer implements the ConnectGateway handler so the daemon can accept
// incoming connections from the gateway instead of dialing out.
type daemonServer struct {
	reliantv1connect.UnimplementedToolsDaemonServiceHandler
	client *daemonClient
}

// ConnectGateway handles an incoming bidi stream from the gateway.
// The stream carries ServerMessages (from gateway) and DaemonMessages (from daemon)
// — the same message types as ConnectDaemon, just with the connection direction reversed.
func (s *daemonServer) ConnectGateway(
	ctx context.Context,
	stream *connect.BidiStream[reliantv1.ServerMessage, reliantv1.DaemonMessage],
) error {
	logging.Info(serverLogPrefix + " Gateway connected")

	d := s.client

	// Send registration as the first message.
	// Send capabilities/platform info only — the gateway assigns identity
	// (daemonID, userID) from the NATS connect command.
	register := &reliantv1.DaemonMessage{
		Message: &reliantv1.DaemonMessage_Register{Register: &reliantv1.DaemonRegister{
			Hostname:     d.hostname,
			Platform:     d.platform,
			WorkingDir:   d.cwd,
			Capabilities: d.capabilities,
			Name:         d.daemonName,
			// Server mode means the gateway dials in, which today only
			// happens for a managed pod — but let the platform say so
			// explicitly when it can. See resolveDaemonType.
			DaemonType: resolveDaemonType("cloud"),
			Labels:     d.registerLabels(),
		}},
	}
	if err := stream.Send(register); err != nil {
		d.recordStream(daemonstate.StreamListening, err.Error())
		return fmt.Errorf("sending registration: %w", err)
	}

	// In server mode the gateway dials in, so an attached stream is the
	// established-stream signal (there is no RegistrationAck to wait for).
	d.recordStream(daemonstate.StreamConnected, "")
	defer d.recordStream(daemonstate.StreamListening, "gateway stream ended")

	// Set up the send channel and sender goroutine (same pattern as client mode).
	d.sendCh = make(chan *reliantv1.DaemonMessage, 256)
	d.sendDone = make(chan struct{})
	d.sessionDone = make(chan struct{})
	go d.runServerSender(stream)
	defer func() {
		close(d.sessionDone) // signal send() and runServerSender to stop
		<-d.sendDone
		d.stopAllStreams()
	}()

	if err := d.sendProjectDiscovery(); err != nil {
		logging.Warn(serverLogPrefix+" Failed to send project discovery", "error", err)
	}

	heartbeatCtx, cancelHeartbeat := context.WithCancel(ctx)
	defer cancelHeartbeat()
	go d.runHeartbeats(heartbeatCtx)

	// Receive loop — same as runSession but reading from the server-side stream.
	for {
		msg, err := stream.Receive()
		if err != nil {
			logging.Info(serverLogPrefix+" Gateway stream ended", "error", err)
			return nil
		}
		if msg == nil {
			continue
		}
		if err := d.handleServerMessage(ctx, msg); err != nil {
			logging.Warn(serverLogPrefix+" Failed handling server message", "error", err)
		}
	}
}

// runServerSender drains sendCh and writes to the server-side bidi stream.
func (d *daemonClient) runServerSender(stream *connect.BidiStream[reliantv1.ServerMessage, reliantv1.DaemonMessage]) {
	defer close(d.sendDone)
	for {
		select {
		case msg := <-d.sendCh:
			if msg == nil {
				return
			}
			if err := stream.Send(msg); err != nil {
				logging.Warn(serverLogPrefix+" runServerSender: stream.Send failed", "error", err)
				return
			}
		case <-d.sessionDone:
			return
		}
	}
}

// runServerMode starts a gRPC/Connect server on the configured port and blocks
// until ctx is cancelled. The gateway dials in via the ConnectGateway RPC.
func (d *daemonClient) runServerMode(ctx context.Context) error {
	port := d.bootCfg.ListenPort
	if port == 0 {
		port = 9190
	}

	svc := &daemonServer{client: d}
	path, handler := reliantv1connect.NewToolsDaemonServiceHandler(svc)

	mux := http.NewServeMux()
	mux.Handle(path, handler)

	addr := fmt.Sprintf(":%d", port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}

	// The gateway dials in over cleartext with prior-knowledge HTTP/2
	// (http2.Transport{AllowHTTP: true}), which net/http serves natively once
	// UnencryptedHTTP2 is in the protocol set.
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)

	srv := &http.Server{
		Handler:           mux,
		Protocols:         protocols,
		ReadHeaderTimeout: transport.ServerReadHeaderTimeout,
		IdleTimeout:       transport.ServerIdleTimeout,
	}

	logging.Info(serverLogPrefix+" Listening for gateway connections",
		"addr", addr,
	)

	errCh := make(chan error, 1)
	go func() {
		if serveErr := srv.Serve(listener); serveErr != nil && serveErr != http.ErrServerClosed {
			errCh <- serveErr
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		logging.Info(serverLogPrefix + " Shutting down server")
		// Close only reports failures of the already-doomed listener and
		// connections; ctx.Err() is the outcome the caller acts on.
		_ = srv.Close()
		return ctx.Err()
	case err := <-errCh:
		return fmt.Errorf("server exited: %w", err)
	}
}
