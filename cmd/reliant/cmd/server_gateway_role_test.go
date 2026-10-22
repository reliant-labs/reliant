// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"time"

	"github.com/reliant-labs/forge/pkg/serverkit"
	"github.com/spf13/cobra"

	"github.com/reliant-labs/reliant/internal/app"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/natsutil"
	"github.com/reliant-labs/reliant/internal/observability"
	"github.com/reliant-labs/reliant/pkg/config"
)

// gatewayRole is what `reliant server gateway` hands its root: the configs
// roots_gen.go loads, and what the process policy (serverkitConfig) and the
// gateway's providers (internal/app) make of them. It is the gateway's
// counterpart of the api's and worker's Options in the role tables.
type gatewayRole struct {
	// server is the process policy's view: the probe address, the drain
	// budget, pprof, the probe paths.
	server        serverkit.Config
	gateway       *config.GatewayConfig
	process       *config.ServerProcessConfig
	database      *config.DatabaseConfig
	pool          db.Pool
	nats          natsutil.Endpoint
	controlPlane  controlplane.Endpoint
	observability observability.Settings
	log           logging.Settings
}

// gatewayOptions is gatewayRole laid out as the Options servergateway.Run
// took before the gateway became a root, so the role-surface golden shows the
// same settings landing in the same places — plus what the root added: the
// probe address and paths.
type gatewayOptions struct {
	ToolsDaemonPort   int32
	HealthAddr        string
	LivenessPaths     []string
	ReadinessPaths    []string
	PreStopDelay      time.Duration
	ShutdownTimeout   time.Duration
	BindAddress       string
	DatabaseDriver    string
	DatabaseURL       string
	DatabaseDirectURL string
	DatabasePool      db.Pool
	DataDir           string
	NATS              natsutil.Endpoint
	PprofAddr         string
	TLSCertFile       string
	TLSKeyFile        string
	DisableTLS        bool
	Log               logging.Settings
	ControlPlane      controlplane.Endpoint
	Observability     observability.Settings
}

func (r gatewayRole) options() gatewayOptions {
	driver, url, directURL := app.DatabaseURLs(r.database)
	return gatewayOptions{
		ToolsDaemonPort:   r.gateway.GetDaemonPort(),
		HealthAddr:        r.server.Addr,
		LivenessPaths:     r.server.Probes.Live,
		ReadinessPaths:    r.server.Probes.Ready,
		PreStopDelay:      r.server.PreStopDelay,
		ShutdownTimeout:   r.server.ShutdownTimeout,
		BindAddress:       r.gateway.GetBindAddress(),
		DatabaseDriver:    driver,
		DatabaseURL:       url,
		DatabaseDirectURL: directURL,
		DatabasePool:      r.pool,
		DataDir:           r.process.GetDataDir(),
		NATS:              r.nats,
		PprofAddr:         r.server.PprofAddr,
		TLSCertFile:       r.process.GetTls().GetCertFile(),
		TLSKeyFile:        r.process.GetTls().GetKeyFile(),
		DisableTLS:        r.process.GetTls().GetDisable(),
		Log:               r.log,
		ControlPlane:      r.controlPlane,
		Observability:     r.observability,
	}
}

// loadGatewayRole loads the gateway's configs as its command does — the root
// config, the `server` subtree configs and its own GatewayConfig — and returns
// them as its policy and providers read them, with the first error any of
// those would boot with.
func loadGatewayRole(cmd *cobra.Command) (gatewayRole, error) {
	base, err := loadRootConfig(cmd)
	if err != nil {
		return gatewayRole{}, err
	}
	r := gatewayRole{}
	if r.process, err = config.LoadServerProcessConfig(cmd); err != nil {
		return gatewayRole{}, err
	}
	if r.database, err = config.LoadDatabaseConfig(cmd); err != nil {
		return gatewayRole{}, err
	}
	if r.gateway, err = config.LoadGatewayConfig(cmd); err != nil {
		return gatewayRole{}, err
	}
	if r.server, err = serverkitConfig(base, rootConfigs{
		DatabaseConfig: r.database, GatewayConfig: r.gateway, ServerProcessConfig: r.process,
	}); err != nil {
		return gatewayRole{}, err
	}
	r.pool = app.DatabasePool(r.database)
	r.nats = app.NATSEndpoint(r.process.GetNats())
	r.controlPlane = app.ControlPlaneEndpoint(r.process.GetControlPlane())
	r.observability = observabilitySettings(r.process.GetObservability(), base)
	r.log = logSettings(base)
	return r, nil
}
