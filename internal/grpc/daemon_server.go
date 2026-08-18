// Copyright (c) 2025 Reliant Labs
//
// forge:exclude-contract
//
// These are Connect RPC handlers: the exported methods are the proto-defined
// service methods, and the package embeds the generated
// reliantv1connect.*ServiceHandler. The contract is the .proto service, so a
// contract.go here would duplicate the proto boundary.
package grpc

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"

	"golang.org/x/net/http2"

	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/grpc/interceptors"
	"github.com/reliant-labs/reliant/internal/grpc/services"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/toolexec/transport"
)

// DaemonServer hosts daemon bidirectional streaming RPCs on a dedicated listener.
type DaemonServer struct {
	server      *http.Server
	tlsCertFile string
	tlsKeyFile  string

	toolExecutor       *toolexec.RemoteExecutor
	toolsDaemonService *services.ToolsDaemonService
}

// DaemonConfig holds dedicated daemon listener configuration.
type DaemonConfig struct {
	Port        int
	BindAddress string // Bind address (default: "127.0.0.1", use "0.0.0.0" for containers)

	ToolsDaemonService *services.ToolsDaemonService
	ToolExecutor       *toolexec.RemoteExecutor

	PATValidator auth.PATValidator

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

	daemonAuthInterceptor, err := interceptors.NewDaemonAuthInterceptor(cfg.PATValidator)
	if err != nil {
		panic(fmt.Sprintf("grpc daemon server auth interceptor setup failed: %v", err))
	}
	toolsDaemonPath, toolsDaemonHandler := reliantv1connect.NewToolsDaemonServiceHandler(
		toolsDaemonService,
		newHandlerOptions(interceptors.NewTimeoutInterceptor().Interceptor(), daemonAuthInterceptor)...,
	)
	mux.Handle(toolsDaemonPath, toolsDaemonHandler)

	bindAddr := cfg.BindAddress
	if bindAddr == "" {
		bindAddr = "127.0.0.1"
	}
	addr := fmt.Sprintf("%s:%d", bindAddr, cfg.Port)

	// protocols stays nil under TLS so Start's http2.ConfigureServer owns the
	// HTTP/2 setup. Cleartext accepts prior-knowledge HTTP/2 alongside
	// HTTP/1.1 on the same port; net/http dispatches both through srv.Handler,
	// so securityHeaders applies to HTTP/2 requests too.
	var protocols *http.Protocols
	if cfg.TLSCertFile != "" && cfg.TLSKeyFile != "" {
		logging.Info("Daemon gRPC server configured for HTTPS/HTTP2", "cert", cfg.TLSCertFile)
	} else {
		protocols = cleartextHTTP2Protocols()
		logging.Info("Daemon gRPC server configured for h2c (HTTP/2 cleartext)")
	}

	srv := &http.Server{
		Addr:      addr,
		Handler:   securityHeaders(mux),
		Protocols: protocols,

		// IMPORTANT: ToolsDaemonService uses long-lived bidi streaming.
		ReadTimeout:       0,
		ReadHeaderTimeout: transport.ServerReadHeaderTimeout,
		WriteTimeout:      0,
		IdleTimeout:       transport.ServerIdleTimeout,
	}

	return &DaemonServer{
		server:             srv,
		tlsCertFile:        cfg.TLSCertFile,
		tlsKeyFile:         cfg.TLSKeyFile,
		toolExecutor:       cfg.ToolExecutor,
		toolsDaemonService: toolsDaemonService,
	}
}

// Start starts the dedicated daemon gRPC server.
func (s *DaemonServer) Start() error {
	if s.tlsCertFile != "" && s.tlsKeyFile != "" {
		cert, err := tls.LoadX509KeyPair(s.tlsCertFile, s.tlsKeyFile)
		if err != nil {
			return fmt.Errorf("failed to load TLS cert: %w", err)
		}

		tlsConfig := &tls.Config{
			Certificates: []tls.Certificate{cert},
			NextProtos:   []string{"h2", "http/1.1"},
			MinVersion:   tls.VersionTLS12,
		}
		s.server.TLSConfig = tlsConfig

		http2Server := &http2.Server{
			MaxConcurrentStreams: 1000,
			IdleTimeout:          transport.ServerIdleTimeout,
		}
		if err := http2.ConfigureServer(s.server, http2Server); err != nil {
			return fmt.Errorf("failed to configure HTTP/2: %w", err)
		}

		logging.Info("Starting dedicated daemon Connect/gRPC server with TLS (HTTPS/HTTP2)",
			"address", s.server.Addr,
			"maxStreams", http2Server.MaxConcurrentStreams,
		)

		ln, err := tls.Listen("tcp", s.server.Addr, s.server.TLSConfig)
		if err != nil {
			return fmt.Errorf("failed to bind daemon gRPC server: %w", err)
		}
		go func() {
			// Use Serve (not ServeTLS) because the listener is already TLS-wrapped.
			// ServeTLS would double-wrap with TLS, causing handshake failures.
			if err := s.server.Serve(ln); err != nil && err != http.ErrServerClosed {
				logging.Error("Dedicated daemon gRPC server failed", "error", err)
			}
		}()
	} else {
		logging.Info("Starting dedicated daemon Connect/gRPC server (h2c)", "address", s.server.Addr)

		ln, err := net.Listen("tcp", s.server.Addr)
		if err != nil {
			return fmt.Errorf("failed to bind daemon gRPC server: %w", err)
		}
		go func() {
			if err := s.server.Serve(ln); err != nil && err != http.ErrServerClosed {
				logging.Error("Dedicated daemon gRPC server failed", "error", err)
			}
		}()
	}

	return nil
}

// IsTLS returns whether daemon server is configured for TLS.
func (s *DaemonServer) IsTLS() bool {
	return s.tlsCertFile != "" && s.tlsKeyFile != ""
}

// Stop gracefully stops daemon server and daemon service workers.
func (s *DaemonServer) Stop(ctx context.Context) error {
	logging.Info("Stopping dedicated daemon Connect/gRPC server")
	if s.toolsDaemonService != nil {
		s.toolsDaemonService.Close()
	}
	return s.server.Shutdown(ctx)
}
