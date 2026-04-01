// Copyright (c) 2025 Reliant Labs
package temporal

import (
	"database/sql"
	"fmt"
	"math/rand"
	"net"
	"os"
	"path/filepath"
	"time"

	"go.temporal.io/server/common/authorization"
	"go.temporal.io/server/common/cluster"
	"go.temporal.io/server/common/config"
	"go.temporal.io/server/common/dynamicconfig"
	"go.temporal.io/server/common/log"
	"go.temporal.io/server/common/metrics"
	sqliteplugin "go.temporal.io/server/common/persistence/sql/sqlplugin/sqlite"
	"go.temporal.io/server/common/testing/freeport"
	"go.temporal.io/server/schema/sqlite"
	"go.temporal.io/server/temporal"

	// Import sqlite driver
	_ "github.com/ncruces/go-sqlite3/driver"
)

const localBroadcastAddress = "127.0.0.1"

// EmbeddedServer is a high level wrapper for Temporal Server that automatically configures a SQLite backend.
type EmbeddedServer struct {
	server           temporal.Server
	frontendHostPort string
	config           *ServerConfig
}

// NewEmbeddedServer creates a new embedded Temporal server
func NewEmbeddedServer(cfg *ServerConfig) (*EmbeddedServer, error) {
	if cfg == nil {
		cfg = DefaultConfig()
	}

	// Apply defaults
	applyDefaults(cfg)

	// Validate config
	if err := validateConfig(cfg); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	// Build Temporal server config
	temporalConfig := buildTemporalConfig(cfg)

	// Set up SQLite schema if needed
	if !cfg.Ephemeral {
		if err := setupSchema(cfg); err != nil {
			return nil, fmt.Errorf("error setting up schema: %w", err)
		}

		// Force cleanup of all prior Temporal leases and state from previous runs
		// This prevents "no workers available" errors when cleanup didn't happen properly
		if err := cleanupStaleClusterMembership(cfg.DatabasePath); err != nil {
			return nil, fmt.Errorf("failed to cleanup stale Temporal state: %w", err)
		}
	}

	// Create namespaces
	sqlConfig := temporalConfig.Persistence.DataStores[sqliteplugin.PluginName].SQL
	if err := createNamespaces(cfg, sqlConfig); err != nil {
		return nil, fmt.Errorf("error creating namespaces: %w", err)
	}

	// Create logger
	logger := createLogger(cfg)

	// Get authorizer and claim mapper
	authorizer, err := authorization.GetAuthorizerFromConfig(&temporalConfig.Global.Authorization)
	if err != nil {
		return nil, fmt.Errorf("unable to instantiate authorizer: %w", err)
	}

	claimMapper, err := authorization.GetClaimMapperFromConfig(&temporalConfig.Global.Authorization, logger)
	if err != nil {
		return nil, fmt.Errorf("unable to instantiate claim mapper: %w", err)
	}

	// Create Temporal server
	server, err := temporal.NewServer(
		temporal.WithConfig(temporalConfig),
		temporal.ForServices(temporal.DefaultServices),
		temporal.WithLogger(logger),
		temporal.WithAuthorizer(authorizer),
		temporal.WithClaimMapper(func(cfg *config.Config) authorization.ClaimMapper {
			return claimMapper
		}),
		temporal.WithDynamicConfigClient(dynamicconfig.StaticClient{
			dynamicconfig.EnableNexus.Key(): []dynamicconfig.ConstrainedValue{{Value: false}},
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("unable to instantiate server: %w", err)
	}

	return &EmbeddedServer{
		server:           server,
		frontendHostPort: temporalConfig.PublicClient.HostPort,
		config:           cfg,
	}, nil
}

// mustGetConsecutiveFreePorts finds N consecutive free ports
// This is needed for Temporal services which use consecutive ports (frontend, history, matching, worker)
func mustGetConsecutiveFreePorts(count int) int {
	// Use random starting point to avoid collisions when multiple test processes start simultaneously
	// Range: 10000-50000 to avoid common services and leave room for consecutive ports
	minPort := 10000
	maxStartPort := 50000
	maxPort := 65535 - count

	// Try from random starting point first, then wrap around
	startPort := minPort + rand.Intn(maxStartPort-minPort)

	// Try from startPort to maxPort
	for basePort := startPort; basePort <= maxPort; basePort++ {
		if tryConsecutivePorts(basePort, count) {
			return basePort
		}
	}

	// Wrap around: try from minPort to startPort
	for basePort := minPort; basePort < startPort; basePort++ {
		if tryConsecutivePorts(basePort, count) {
			return basePort
		}
	}

	panic(fmt.Errorf("failed to find %d consecutive free ports", count))
}

// tryConsecutivePorts checks if count consecutive ports starting at basePort are all free
func tryConsecutivePorts(basePort, count int) bool {
	for offset := 0; offset < count; offset++ {
		port := basePort + offset
		if !isPortFree(port) {
			return false
		}
	}
	return true
}

// isPortFree checks if a specific port is available for listening
func isPortFree(port int) bool {
	// Try to bind to the port
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	listener.Close()
	return true
}

// Start starts the embedded Temporal server
func (s *EmbeddedServer) Start() error {
	return s.server.Start()
}

// Stop stops the embedded Temporal server
func (s *EmbeddedServer) Stop() error {
	return s.server.Stop()
}

// FrontendHostPort returns the host:port for this server
func (s *EmbeddedServer) FrontendHostPort() string {
	return s.frontendHostPort
}

// buildTemporalConfig builds a Temporal server configuration from ServerConfig
func buildTemporalConfig(cfg *ServerConfig) *config.Config {
	// Build SQLite config
	sqliteConfig := config.SQL{
		PluginName:        sqliteplugin.PluginName,
		ConnectAttributes: make(map[string]string),
		DatabaseName:      cfg.DatabasePath,
	}

	if cfg.Ephemeral {
		sqliteConfig.ConnectAttributes["mode"] = "memory"
		sqliteConfig.ConnectAttributes["cache"] = "shared"
		sqliteConfig.DatabaseName = fmt.Sprintf("%d", rand.Intn(9999999))
	} else {
		sqliteConfig.ConnectAttributes["mode"] = "rwc"
	}

	// Set pragmas
	for k, v := range cfg.SQLitePragmas {
		sqliteConfig.ConnectAttributes["_"+k] = v
	}

	// Auto-assign ports if set to 0
	// We need 4 consecutive ports for frontend, history, matching, and worker services
	frontendPort := cfg.FrontendPort
	if frontendPort == 0 {
		frontendPort = mustGetConsecutiveFreePorts(4)
	}

	metricsPort := cfg.MetricsPort
	if metricsPort == 0 {
		metricsPort = freeport.MustGetFreePort()
	}

	pprofPort := freeport.MustGetFreePort()

	temporalConfig := &config.Config{
		Global: config.Global{
			Membership: config.Membership{
				MaxJoinDuration:  30 * time.Second,
				BroadcastAddress: localBroadcastAddress,
			},
			Metrics: &metrics.Config{
				Prometheus: &metrics.PrometheusConfig{
					ListenAddress: fmt.Sprintf("%s:%d", localBroadcastAddress, metricsPort),
					HandlerPath:   "/metrics",
				},
			},
			PProf: config.PProf{Port: pprofPort},
		},
		Persistence: config.Persistence{
			DefaultStore:     sqliteplugin.PluginName,
			VisibilityStore:  sqliteplugin.PluginName,
			NumHistoryShards: 1,
			DataStores: map[string]config.DataStore{
				sqliteplugin.PluginName: {SQL: &sqliteConfig},
			},
		},
		ClusterMetadata: &cluster.Config{
			EnableGlobalNamespace:    false,
			FailoverVersionIncrement: 10,
			MasterClusterName:        "active",
			CurrentClusterName:       "active",
			ClusterInformation: map[string]cluster.ClusterInformation{
				"active": {
					Enabled:                true,
					InitialFailoverVersion: 1,
					RPCAddress:             fmt.Sprintf("%s:%d", localBroadcastAddress, frontendPort),
				},
			},
		},
		DCRedirectionPolicy: config.DCRedirectionPolicy{
			Policy: "noop",
		},
		Services: map[string]config.Service{
			"frontend": createService(frontendPort, 0),
			"history":  createService(frontendPort, 1),
			"matching": createService(frontendPort, 2),
			"worker":   createService(frontendPort, 3),
		},
		Archival: config.Archival{
			History: config.HistoryArchival{
				State:      "disabled",
				EnableRead: false,
				Provider:   nil,
			},
			Visibility: config.VisibilityArchival{
				State:      "disabled",
				EnableRead: false,
				Provider:   nil,
			},
		},
		PublicClient: config.PublicClient{
			HostPort: fmt.Sprintf("%s:%d", localBroadcastAddress, frontendPort),
		},
		NamespaceDefaults: config.NamespaceDefaults{
			Archival: config.ArchivalNamespaceDefaults{
				History: config.HistoryArchivalNamespaceDefaults{
					State: "disabled",
				},
				Visibility: config.VisibilityArchivalNamespaceDefaults{
					State: "disabled",
				},
			},
		},
	}

	return temporalConfig
}

// createService creates a service configuration
func createService(frontendPort int, offset int) config.Service {
	// All services must use consistent port offsets so they can find each other
	// Frontend: base port, History: base+1, Matching: base+2, Worker: base+3
	return config.Service{
		RPC: config.RPC{
			GRPCPort:        frontendPort + offset,
			MembershipPort:  freeport.MustGetFreePort(),
			BindOnLocalHost: true,
			BindOnIP:        "",
		},
	}
}

// setupSchema sets up the SQLite schema if the database doesn't exist
func setupSchema(cfg *ServerConfig) error {
	if _, err := os.Stat(cfg.DatabasePath); os.IsNotExist(err) {
		// Create parent directory if needed
		dir := filepath.Dir(cfg.DatabasePath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("error creating directory: %w", err)
		}

		// Apply schema
		sqlConfig := &config.SQL{
			PluginName:   sqliteplugin.PluginName,
			DatabaseName: cfg.DatabasePath,
		}
		if err := sqlite.SetupSchema(sqlConfig); err != nil {
			return fmt.Errorf("error applying schema: %w", err)
		}
	}
	return nil
}

// createNamespaces creates the configured namespaces
func createNamespaces(cfg *ServerConfig, sqlConfig *config.SQL) error {
	var namespaces []*sqlite.NamespaceConfig
	for _, ns := range cfg.Namespaces {
		nsConfig, err := sqlite.NewNamespaceConfig("active", ns, false, nil)
		if err != nil {
			return fmt.Errorf("error creating namespace config for %s: %w", ns, err)
		}
		namespaces = append(namespaces, nsConfig)
	}

	if err := sqlite.CreateNamespaces(sqlConfig, namespaces...); err != nil {
		return fmt.Errorf("error creating namespaces: %w", err)
	}

	return nil
}

// createLogger creates a logger from the config
func createLogger(cfg *ServerConfig) log.Logger {
	// "silent" level disables all logging (for tests)
	if cfg.LogLevel == "silent" {
		return log.NewNoopLogger()
	}

	zapConfig := log.Config{
		Stdout:     true,
		Level:      cfg.LogLevel,
		OutputFile: "",
	}
	return log.NewZapLogger(log.BuildZapLogger(zapConfig))
}

// cleanupStaleClusterMembership removes stale cluster membership entries from previous runs.
// This is called on startup to prevent "Not enough hosts" errors caused by accumulated stale entries.
//
// IMPORTANT: We only clean cluster_membership (server registration), NOT task_queues.
// Deleting task_queues breaks Temporal's internal consistency because the tasks table
// and workflow history still reference those task queues, causing workflows to get
// into unrecoverable states.
func cleanupStaleClusterMembership(dbPath string) error {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer db.Close()

	// Clean cluster membership - this is server registration and is safe to clear.
	// For a single-process desktop app, any entry from a previous run is stale.
	_, err = db.Exec(`DELETE FROM cluster_membership`)
	if err != nil {
		return fmt.Errorf("failed to delete cluster membership entries: %w", err)
	}

	return nil
}
