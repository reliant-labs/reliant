// Copyright (c) 2025 Reliant Labs
//
// These are Connect RPC handlers: the exported methods are the proto-defined
// service methods, and the package embeds the generated
// reliantv1connect.*ServiceHandler. The contract is the .proto service, so a
// contract.go here would duplicate the proto boundary.
//
//forge:exclude-contract: server bootstrap: builds the Connect/HTTP2 servers and mounts services; composition, not a component
package grpc

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/serverkit"

	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/grpc/interceptors"
	"github.com/reliant-labs/reliant/internal/handlers/tools_daemon"
	"github.com/reliant-labs/reliant/internal/logging"
)

// DaemonServer hosts daemon bidirectional streaming RPCs on a dedicated listener.
type DaemonServer struct {
	server      *http.Server
	streams     *serverkit.Streams
	tlsCertFile string
	tlsKeyFile  string

	toolsDaemonService *tools_daemon.Service
}

// DaemonConfig holds dedicated daemon listener configuration.
type DaemonConfig struct {
	Port        int
	BindAddress string // Bind address (default: "127.0.0.1", use "0.0.0.0" for containers)

	ToolsDaemonService *tools_daemon.Service

	// DaemonTokens resolves daemons' `rlat_` credentials (uncached).
	DaemonTokens auth.AccessTokenIntrospector

	TLSCertFile string
	TLSKeyFile  string
}

// NewDaemonServer creates a dedicated server for ToolsDaemonService streams.
func NewDaemonServer(cfg *DaemonConfig) *DaemonServer {
	mux := http.NewServeMux()

	if cfg == nil {
		panic("grpc daemon server config is required")
	}

	toolsDaemonService := cfg.ToolsDaemonService
	if toolsDaemonService == nil {
		panic("grpc daemon server requires a non-nil ToolsDaemonService")
	}

	daemonAuthInterceptor, err := interceptors.NewDaemonAuthInterceptor(cfg.DaemonTokens)
	if err != nil {
		panic(fmt.Sprintf("grpc daemon server auth interceptor setup failed: %v", err))
	}
	opts := newHandlerOptions(false, interceptors.NewTimeoutInterceptor().Interceptor(), daemonAuthInterceptor)
	// Innermost, so logging records the status the daemon received. A daemon
	// stream the drain ends finishes Unavailable (RedialOnDrain below), which
	// puts the daemon on its reconnect path to a live replica.
	opts = append(opts, connect.WithInterceptors(serverkit.StreamDrainInterceptor()))
	toolsDaemonPath, toolsDaemonHandler := reliantv1connect.NewToolsDaemonServiceHandler(toolsDaemonService, opts...)
	mux.Handle(toolsDaemonPath, toolsDaemonHandler)

	// A daemon redials on Unavailable (isFatalError in
	// internal/toolexec/daemonruntime/runtime.go treats it as recoverable), so
	// its streams end with that rather than the OK a watch ends with.
	streams := &serverkit.Streams{RedialOnDrain: []string{reliantv1connect.ToolsDaemonServiceName}}

	return &DaemonServer{
		server:             serverkit.NewHTTPServer(listenerConfig(cfg.BindAddress, cfg.Port), securityHeaders(mux), streams),
		streams:            streams,
		tlsCertFile:        cfg.TLSCertFile,
		tlsKeyFile:         cfg.TLSKeyFile,
		toolsDaemonService: toolsDaemonService,
	}
}

// Start binds the daemon port and serves on it in the background.
func (s *DaemonServer) Start() error {
	return listenAndServe(s.server, s.tlsCertFile, s.tlsKeyFile, "dedicated daemon Connect/gRPC server")
}

// IsTLS returns whether daemon server is configured for TLS.
func (s *DaemonServer) IsTLS() bool {
	return s.tlsCertFile != "" && s.tlsKeyFile != ""
}

// Stop gracefully stops the daemon server, make-before-break:
//
//  1. ToolsDaemonService.BeginDrain refuses new streams, stops routing each
//     held daemon's new requests here, and tells each daemon to move
//     (GatewayDrain), naming the moment its stream here will be ended. The
//     daemon opens a stream to another replica and keeps this one open for
//     the replies to what is in flight on it.
//  2. serverkit.DrainHTTP stops accepting and drains the listener. Every
//     ConnectDaemon stream claimed the drain's soft phase (it says goodbye
//     with GatewayDrain), so the drain waits for the daemons to close their
//     old streams — each does once it is registered elsewhere and has nothing
//     left in flight here — until ctx's deadline less the drain reserve, then
//     ends the streams still open with Unavailable, the daemon's redial
//     signal. A call cut off there is not lost: its daemon delivers the
//     result through the replica it moved to (see
//     internal/toolexec/tool_result_redelivery.go).
//  3. ToolsDaemonService.Close ends anything the service still holds that is
//     not an inbound stream (an outbound gateway→daemon connection).
//
// ORDER IS LOAD-BEARING. http.Server.Shutdown alone cannot end a daemon
// stream: it waits for active requests to complete, and a bidi stream never
// completes on its own, so it used to burn the entire budget and then the
// process exited with every daemon's TCP connection cut and no status at all.
//
// Callers must have completed the readiness flip and pre-stop pause before
// this runs, so that the daemons redialing on the drain notice reach a replica
// that is already serving rather than this one.
func (s *DaemonServer) Stop(ctx context.Context) error {
	logging.Info("Stopping dedicated daemon Connect/gRPC server", "streams_open", s.streams.Active())
	// The drain ends the streams still open at ctx's deadline less the drain
	// reserve (less, when the budget is under twice the reserve), so this is
	// the latest moment a daemon can rely on. No deadline: the drain waits
	// for every stream, and the notice names none.
	var hardEnd time.Time
	if deadline, ok := ctx.Deadline(); ok {
		hardEnd = deadline.Add(-serverkit.DefaultDrainReserve)
	}
	start := time.Now()
	moved := s.toolsDaemonService.BeginDrain(hardEnd)
	err := serverkit.DrainHTTP(ctx, s.server, s.streams)
	if moved > 0 {
		logging.Info("Daemon streams drained",
			"streams", moved, "still_open", s.toolsDaemonService.GetActiveConnections(),
			"waited", time.Since(start).Round(time.Millisecond))
	}
	logDrainReport("Dedicated daemon Connect/gRPC server drained", s.streams.Report())
	s.toolsDaemonService.Close()
	return err
}
