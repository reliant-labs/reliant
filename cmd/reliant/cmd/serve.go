// yours: scaffolded once, never touched again — forge will not overwrite this file
//
// THE PROCESS POLICY — the one every server root runs under, and nothing else.
//
// What a process RUNS is one root of the composition graph
// (internal/app/graph_gen.go): `reliant server api` runs the api root
// (internal/app/roots_api.go), `reliant server gateway` the gateway root
// (internal/app/roots_gateway.go), `reliant server worker` the worker root
// (internal/app/roots_worker.go), and every //forge:root in internal/app
// gets a command of its own (roots_gen.go, generated). Each node is
// constructed once, from its own Deps, in internal/app/compose.go.
//
// runRoot is the policy around whichever root a command names, and what
// reliant keeps per root (rootPolicies): load and validate config, set the
// environment tier, open the role's rotating log file, hold the health port
// with boot probes while dependencies come up, install telemetry under the
// role's service name, then run the root until a signal — readiness flips to
// draining, the pre-stop pause runs, and every node drains and closes in
// reverse dependency order inside the shutdown budget, pending error reports
// and telemetry flushed last.
//
// The api-server owns the schema: its root declares migrate=apply, so its
// process applies the migrations here, after the boot probes bind and before
// the root is built (migrateSchema). Every other root declares migrate=wait,
// and its database provider waits for them (db.MigrateWait).
package cmd

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/reliant-labs/forge/pkg/lifecyclekit"
	"github.com/reliant-labs/forge/pkg/serverkit"

	"github.com/reliant-labs/reliant/internal/app"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/observability"
	"github.com/reliant-labs/reliant/internal/runenv"
	"github.com/reliant-labs/reliant/internal/telemetry"
	"github.com/reliant-labs/reliant/pkg/config"
)

// rootPolicy is what differs between the roots' processes.
type rootPolicy struct {
	// serviceName is the role's telemetry identity: dashboards and alerts
	// key on it.
	serviceName string
	// logFile is the role's rotating log file, under <data_dir>/logs.
	logFile string
}

// rootPolicies is each root's policy, by root name.
var rootPolicies = map[string]rootPolicy{
	"api":     {serviceName: "reliant-api-server", logFile: "reliant.log"},
	"gateway": {serviceName: "daemon-gateway", logFile: "daemon-gateway.log"},
	"worker":  {serviceName: "reliant-temporal-worker", logFile: "temporal-worker.log"},
}

// runRoot runs one root (roots_gen.go hands it the root a command names):
// every node it reaches, supervised until a signal or a failing node.
func runRoot(ctx context.Context, deps Deps, root rootSpec) error {
	policy, ok := rootPolicies[root.Name]
	if !ok {
		return fmt.Errorf("root %q has no process policy (cmd/reliant/cmd/serve.go rootPolicies)", root.Name)
	}
	// Load validates: a value the config's annotations reject fails here.
	// Only the fields the root reaches are demanded (root.Config, from
	// roots_gen.go).
	cfg, err := config.LoadReach(deps.Cmd, root.Config)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	runenv.Configure(cfg.GetRuntime().GetEnvironment())
	skCfg, err := serverkitConfig(cfg, root.Configs)
	if err != nil {
		return err
	}
	process := root.Configs.ServerProcessConfig

	logSet := logSettings(cfg)
	logging.SetupWithRotation(logSet, logging.GetLogLevel(logSet), false, &logging.RotationConfig{
		Filename:   filepath.Join(process.GetDataDir(), "logs", policy.logFile),
		MaxSizeMB:  50,
		MaxBackups: 3,
		MaxAgeDays: 30,
		Compress:   true,
	})
	defer logging.Close() //nolint:errcheck
	logger := slog.Default()

	// SIGINT/SIGTERM begin the stop. The same context is the graph's boot
	// context, so a signal during boot also abandons a dependency still
	// being waited on.
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// BOOT PROBES. Bind the health port NOW, before any dependency is
	// touched: until the root's probe listener takes the address over, the
	// liveness paths answer 200 and the readiness paths 503, naming the
	// dependency being waited on.
	ctx, bootProbes, err := serverkit.StartBootProbes(ctx, skCfg, logger)
	if err != nil {
		return err
	}
	defer bootProbes.Close()

	// TELEMETRY before the graph, so every instrumented client is created
	// against the installed providers; flushed LAST on the way down, so the
	// shutdown's own spans and metrics are exported.
	obs, err := observability.Init(observability.NewConfig(policy.serviceName, observabilitySettings(process.GetObservability(), cfg)))
	if err != nil {
		return fmt.Errorf("initialize observability: %w", err)
	}

	// MIGRATIONS, before anything reads the schema: only a root that
	// declares migrate=apply (the api-server, the schema's one owner) runs
	// them; the boot probes answer meanwhile.
	if root.Migrate {
		if err := migrateSchema(ctx, skCfg, root.Configs.DatabaseConfig, logger); err != nil {
			return err
		}
	}

	// THE ROOT POLICY: the readiness gate and stop budget (PRE_STOP_DELAY,
	// SHUTDOWN_TIMEOUT), and the probes on the health port. Every node that
	// reports Ready joins the readiness paths.
	drainer := serverkit.NewDrainer(skCfg)
	drainer.Logger = logger
	g := &app.Graph{
		Ctx:       ctx,
		Cfg:       cfg,
		Log:       logger,
		ServerCfg: skCfg,
		Probes: &serverkit.Probes{
			Drainer: drainer,
			Metrics: observability.MetricsHandler(),
			Timeout: skCfg.ReadinessTimeout,
			Logger:  logger,
			Paths:   skCfg.Probes,
		},
	}
	logger.InfoContext(ctx, "running root", "root", root.Name, "health_addr", skCfg.Addr)
	if err := root.Run(g, ctx, lifecyclekit.Policy{
		Drainer:       drainer,
		Logger:        logger,
		FailurePolicy: skCfg.FailurePolicy,
		Flush:         flushTelemetry(obs),
	}); err != nil {
		return fmt.Errorf("%s: %w", policy.serviceName, err)
	}
	logger.InfoContext(ctx, "shut down gracefully", "root", root.Name)
	return nil
}

// flushTelemetry is the root's last step, after every node has stopped: the
// pending Sentry events (a no-op where no reporter is installed), then the
// OTel providers. Product events need no flush: each is written to the log as
// it happens.
func flushTelemetry(obs *observability.Provider) func(context.Context) error {
	return func(context.Context) error {
		telemetry.Flush(5)
		return obs.Shutdown()
	}
}

// migrateSchema applies every migration this binary embeds (db.ApplyOnBoot),
// on a connection of its own to the session-capable
// DATABASE_DIRECT_URL when one is set (db.DatabaseConfig.MigrationURL), never
// the serving pool. The connect retries within DB_CONNECT_TIMEOUT — a database
// that comes up seconds after the api-server is a wait, with the database
// pending in the boot readiness body — and the migration itself does not: a
// failed or dirty one is a human's problem, not a transient.
func migrateSchema(ctx context.Context, skCfg serverkit.Config, database *config.DatabaseConfig, logger *slog.Logger) error {
	_, url, directURL := app.DatabaseURLs(database)
	if url == "" {
		return fmt.Errorf("config: DATABASE_URL is required")
	}
	migrateCfg := skCfg
	migrateCfg.AutoMigrate = true
	migrateCfg.DatabaseURL = db.DatabaseConfig{URL: url, DirectURL: directURL}.MigrationURL()
	return serverkit.AutoMigrateContext(ctx, migrateCfg, logger, func(pool *sql.DB, logger *slog.Logger) error {
		return db.ApplyOnBoot(ctx, pool, logger)
	})
}

// serverkitConfig projects reliant's configs onto the vendor-neutral
// serverkit.Config: the probe address, the drain budget, pprof and the probe
// paths, from the command config of the role the root runs as.
//
// The gateway's probe address is HEALTH_PORT on BIND_ADDRESS, the worker's
// HEALTH_PORT on every address (it has no BIND_ADDRESS), plain HTTP: the shared
// `tls` block (ServerTLSConfig) is the role listeners', and never turns TLS on
// here — kubelet, the GKE health check and docker-compose probe it in
// plaintext. (forge's own `port` field is the API port, which for reliant is
// the role listener's, so it is not read either.)
func serverkitConfig(cfg *config.Config, configs rootConfigs) (serverkit.Config, error) {
	skCfg := serverkit.Config{
		// ServiceName is APP IDENTITY (the project name baked into a
		// generated constant), NOT a config field.
		ServiceName: ServiceName,
		LogFormat:   cfg.GetLog().GetFormat(),
		Environment: cfg.GetRuntime().GetEnvironment(),
	}
	if c := configs.DatabaseConfig; c != nil {
		skCfg.DatabaseURL = c.GetDatabaseUrl()
		skCfg.DBPoolTuning.MaxOpenConns = int(c.GetDbMaxOpenConns())
		skCfg.DBPoolTuning.MaxIdleConns = int(c.GetDbMaxIdleConns())
		skCfg.DBConnectTimeout = c.GetDbConnectTimeout().AsDuration()
	}
	if c := configs.GatewayConfig; c != nil {
		skCfg.Addr = fmt.Sprintf("%s:%d", c.GetBindAddress(), c.GetHealthPort())
		skCfg.PprofAddr = c.GetPprofAddr()
		skCfg.Probes.Live = serverkit.ProbePathList(c.GetLivenessPaths())
		skCfg.Probes.Ready = serverkit.ProbePathList(c.GetReadinessPaths())
		// Zero is serverkit's default (5s / 30s), negative none: the rule
		// forge's grace-period arithmetic applies to the same variables.
		skCfg.PreStopDelay = c.GetPreStopDelay().AsDuration()
		skCfg.ShutdownTimeout = c.GetShutdownTimeout().AsDuration()
	}
	if c := configs.ApiServerConfig; c != nil {
		skCfg.Addr = fmt.Sprintf("%s:%d", c.GetBindAddress(), c.GetHealthPort())
		skCfg.Probes.Live = serverkit.ProbePathList(c.GetLivenessPaths())
		skCfg.Probes.Ready = serverkit.ProbePathList(c.GetReadinessPaths())
		// The pause keeps the API serving while EndpointSlices and the GKE
		// NEG drop the pod: without it the listener closes while the pod is
		// still a routing target, which is connection-refused on every
		// rollout.
		skCfg.PreStopDelay = c.GetPreStopDelay().AsDuration()
		skCfg.ShutdownTimeout = c.GetShutdownTimeout().AsDuration()
	}
	if c := configs.WorkerConfig; c != nil {
		skCfg.Addr = fmt.Sprintf(":%d", c.GetHealthPort())
		skCfg.Probes.Live = serverkit.ProbePathList(c.GetLivenessPaths())
		skCfg.Probes.Ready = serverkit.ProbePathList(c.GetReadinessPaths())
		// Nothing routes to the worker, so its pre-stop pause buys nothing —
		// the Temporal worker keeps polling through it — and PRE_STOP_DELAY=-1s
		// on the workload removes it. The budget bounds the Temporal worker's
		// drain, which must fit the pod's grace period.
		skCfg.PreStopDelay = c.GetPreStopDelay().AsDuration()
		skCfg.ShutdownTimeout = c.GetShutdownTimeout().AsDuration()
	}
	if skCfg.Addr == "" {
		return serverkit.Config{}, fmt.Errorf("serverkitConfig: no command config of this root names its health port")
	}

	// Normalize LAST, so nothing downstream can observe a zero-valued
	// cap/timeout/driver.
	skCfg.Normalize()
	if err := skCfg.Probes.Validate(); err != nil {
		return serverkit.Config{}, fmt.Errorf("config: LIVENESS_PATHS / READINESS_PATHS: %w", err)
	}
	return skCfg, nil
}
