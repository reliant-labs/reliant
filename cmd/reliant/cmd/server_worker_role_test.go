// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/reliant/internal/analytics"
	"github.com/reliant-labs/reliant/internal/app"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/egresslimit"
	"github.com/reliant-labs/reliant/internal/integrations/webhook"
	"github.com/reliant-labs/reliant/internal/integrations/webhook/github"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/natsutil"
	"github.com/reliant-labs/reliant/internal/observability"
	"github.com/reliant-labs/reliant/internal/telemetry"
	"github.com/reliant-labs/reliant/pkg/config"
)

// workerOptions is what `reliant server worker` hands its root — the configs
// roots_gen.go loads, as the process policy (serverkitConfig) and the
// worker's providers (internal/app) read them — laid out as the Options
// serverworker.Run took before the worker became a root, so the role-surface
// golden shows the same settings landing in the same places, plus what the
// root added: the probe address and paths.
type workerOptions struct {
	DatabaseDriver     string
	DatabaseURL        string
	DatabaseDirectURL  string
	DatabasePool       db.Pool
	DataDir            string
	TemporalHost       string
	TemporalPort       int
	TemporalNamespace  string
	NATS               natsutil.Endpoint
	StreamingDriver    string
	HealthAddr         string
	LivenessPaths      []string
	ReadinessPaths     []string
	PreStopDelay       time.Duration
	ShutdownTimeout    time.Duration
	PprofPort          int
	ControlPlane       controlplane.Endpoint
	AllowPrivateEgress bool
	Log                logging.Settings
	PublicURL          string
	VaultKey           string
	Webhooks           webhook.RegistryConfig
	GitHubAppSlug      string
	GitHubBot          github.OurBot
	Observability      observability.Settings
	Sentry             telemetry.SentryConfig
	Analytics          analytics.Settings
	LLM                drivers.Settings
	Preview            tools.PreviewSettings
	// As NewTemporalWorker hands them to workersetup (0: its default).
	WorkflowTaskSlots   int
	WorkflowTaskPollers int
	// As compose.go hands them to NewRemoteExecutor (0: the limiter's default).
	Egress egresslimit.Limits
}

// loadServerWorkerConfig loads the worker's configs as its command does — the
// root config, the `server` subtree configs and its own WorkerConfig — and
// fills opts as its policy and providers read them, with the first error any
// of those would stop the process with.
func loadServerWorkerConfig(cmd *cobra.Command, opts *workerOptions) error {
	base, err := loadRootConfig(cmd)
	if err != nil {
		return err
	}
	process, err := config.LoadServerProcessConfig(cmd)
	if err != nil {
		return err
	}
	database, err := config.LoadDatabaseConfig(cmd)
	if err != nil {
		return err
	}
	worker, err := config.LoadWorkerConfig(cmd)
	if err != nil {
		return err
	}
	server, err := serverkitConfig(base, rootConfigs{
		DatabaseConfig: database, ServerProcessConfig: process, WorkerConfig: worker,
	})
	if err != nil {
		return err
	}
	// What the worker root's providers read (internal/app/roots_worker.go);
	// a value they cannot use was refused by the loads above.
	in := app.Integrations(process.GetIntegrations())
	opts.LLM = app.LLMSettings(process.GetLlm())
	opts.Preview = app.PreviewSettings(process.GetPreview())
	opts.DatabasePool = app.DatabasePool(database)
	opts.Sentry = app.SentrySettings(process.GetSentry(), process.GetObservability())
	opts.TemporalHost, opts.TemporalPort, opts.TemporalNamespace = app.TemporalEndpoint(process.GetTemporal())
	opts.DatabaseDriver, opts.DatabaseURL, opts.DatabaseDirectURL = app.DatabaseURLs(database)
	opts.DataDir = process.GetDataDir()
	opts.NATS = app.NATSEndpoint(process.GetNats())
	opts.StreamingDriver = process.GetStreamingDriver()
	opts.HealthAddr = server.Addr
	opts.LivenessPaths, opts.ReadinessPaths = server.Probes.Live, server.Probes.Ready
	opts.PreStopDelay, opts.ShutdownTimeout = server.PreStopDelay, server.ShutdownTimeout
	opts.PprofPort = int(worker.GetPprofPort())
	opts.WorkflowTaskSlots = int(worker.GetMaxConcurrentWorkflowTasks())
	opts.WorkflowTaskPollers = int(worker.GetWorkflowTaskPollers())
	opts.Egress = egresslimit.Limits{
		Replicas:          int(worker.GetEgressReplicas()),
		UserPerMinute:     int(worker.GetEgressUserPerMinute()),
		UserBurst:         int(worker.GetEgressUserBurst()),
		UserMaxConcurrent: int(worker.GetEgressUserMaxConcurrent()),
		HostMaxConcurrent: int(worker.GetEgressHostMaxConcurrent()),
		TurnMaxParallel:   int(worker.GetEgressTurnMaxParallel()),
	}
	opts.ControlPlane = app.ControlPlaneEndpoint(process.GetControlPlane())
	opts.AllowPrivateEgress = app.AllowPrivateEgress(process.GetNetwork())
	opts.Log = logSettings(base)
	opts.PublicURL, opts.VaultKey, opts.Webhooks = in.PublicURL, in.VaultKey, in.Webhooks
	opts.GitHubAppSlug, opts.GitHubBot = in.GitHubAppSlug, in.GitHubBot
	opts.Observability = observabilitySettings(process.GetObservability(), base)
	opts.Analytics = app.AnalyticsSettings(process.GetAnalytics())
	return nil
}
