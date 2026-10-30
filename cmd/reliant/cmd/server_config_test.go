// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"os"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/serverkit"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/egresslimit"
	"github.com/reliant-labs/reliant/internal/natsutil"
	"github.com/reliant-labs/reliant/pkg/config"
)

// serverRoleEnv is every variable the server roles read through
// ServerProcessConfig, DatabaseConfig and their own role configs besides the
// control plane, JWT and forge-API settings other tests own.
var serverRoleEnv = []string{
	"DATABASE_URL", "DATABASE_DIRECT_URL", "DATABASE_DRIVER",
	"NATS_URL", "NATS_USER", "NATS_PASSWORD",
	"TEMPORAL_HOST", "TEMPORAL_PORT", "TEMPORAL_NAMESPACE",
	"TLS_CERT_FILE", "TLS_KEY_FILE", "DISABLE_TLS",
	"PRE_STOP_DELAY", "SHUTDOWN_TIMEOUT",
	"GRPC_PORT", "HEALTH_PORT", "PPROF_PORT", "PPROF_ADDR", "BIND_ADDRESS", "DATA_DIR",
	"STREAMING_DRIVER", "CORS_ALLOWED_ORIGINS", "TOOLS_DAEMON_PORT",
}

// The two settings every server role requires, set to placeholders so a test
// about something else loads.
const (
	testDatabaseURL = "postgres://reliant@db.test/reliant"
	testNATSURL     = "nats://nats.test:4222"
)

// isolateServerRoleEnv clears every server-role variable, then sets the two
// every role requires.
func isolateServerRoleEnv(t *testing.T) {
	t.Helper()
	for _, name := range serverRoleEnv {
		t.Setenv(name, "") // restores the original afterwards
		require.NoError(t, os.Unsetenv(name))
	}
	t.Setenv("DATABASE_URL", testDatabaseURL)
	t.Setenv("NATS_URL", testNATSURL)
}

// serverRole runs what one server role's RunE does with its typed config,
// after parsing args, and returns the Options it would hand its package
// (apiOptions), or for the roots, what each root gets (workerOptions,
// gatewayRole).
type serverRole struct {
	path []string
	load func(t *testing.T, args ...string) (any, error)
}

var serverRoles = []serverRole{
	{path: []string{"server", "api"}, load: func(t *testing.T, args ...string) (any, error) {
		var opts apiOptions
		err := loadServerAPIConfig(findServerCmd(t, "api", args...), &opts)
		return opts, err
	}},
	{path: []string{"server", "worker"}, load: func(t *testing.T, args ...string) (any, error) {
		var opts workerOptions
		err := loadServerWorkerConfig(findServerCmd(t, "worker", args...), &opts)
		return opts, err
	}},
	{path: []string{"server", "gateway"}, load: func(t *testing.T, args ...string) (any, error) {
		return loadGatewayRole(findServerCmd(t, "gateway", args...))
	}},
}

func findServerCmd(t *testing.T, role string, args ...string) *cobra.Command {
	t.Helper()
	cmd, _, err := NewRootCmd().Find([]string{"server", role})
	require.NoError(t, err)
	require.NoError(t, cmd.ParseFlags(args))
	return cmd
}

// TestServerRoleDefaults pins every role's defaults: the values the flag
// defaults they replaced carried.
func TestServerRoleDefaults(t *testing.T) {
	isolateServerRoleEnv(t)

	api := loadServerAPIOptions(t)
	assert.Equal(t, 9090, api.GRPCPort)
	assert.Equal(t, "0.0.0.0:8081", api.HealthAddr, "the api probes on BIND_ADDRESS, as it always has")
	assert.Equal(t, []string{"/health", "/healthz"}, api.LivenessPaths, "control-plane probes /health")
	assert.Equal(t, []string{"/ready", "/readyz"}, api.ReadinessPaths, "control-plane probes /ready")
	assert.Equal(t, 6060, api.PprofPort)
	assert.Equal(t, "0.0.0.0", api.BindAddress)
	assert.Equal(t, "./data", api.DataDir)
	assert.Equal(t, "postgres", api.DatabaseDriver)
	assert.Equal(t, testDatabaseURL, api.DatabaseURL)
	assert.Empty(t, api.DatabaseDirectURL)
	assert.Equal(t, "localhost", api.TemporalHost)
	assert.Equal(t, 7233, api.TemporalPort)
	assert.Equal(t, "reliant", api.TemporalNamespace)
	assert.Equal(t, natsutil.Endpoint{URL: testNATSURL}, api.NATS)
	assert.Equal(t, "nats", api.StreamingDriver)
	assert.Equal(t, []string{"*"}, api.CORSAllowedOrigins)
	assert.False(t, api.DisableTLS)

	opts, err := serverRoles[1].load(t)
	require.NoError(t, err)
	worker := opts.(workerOptions)
	assert.Equal(t, ":8081", worker.HealthAddr, "the worker probes on every address: it has no BIND_ADDRESS")
	assert.Equal(t, []string{"/health", "/healthz"}, worker.LivenessPaths, "control-plane probes /health")
	assert.Equal(t, []string{"/ready", "/readyz"}, worker.ReadinessPaths, "control-plane probes /ready")
	assert.Equal(t, 6060, worker.PprofPort)
	assert.Equal(t, "./data", worker.DataDir)

	opts, err = serverRoles[2].load(t)
	require.NoError(t, err)
	gateway := opts.(gatewayRole)
	assert.Equal(t, int32(9190), gateway.gateway.GetDaemonPort())
	assert.Equal(t, "0.0.0.0:8080", gateway.server.Addr, "the gateway's health port default differs from the other roles'")
	assert.Equal(t, "0.0.0.0", gateway.gateway.GetBindAddress())
	assert.Empty(t, gateway.server.PprofAddr, "no pprof server unless PPROF_ADDR names one")
	assert.Equal(t, []string{"/health", "/healthz"}, gateway.server.Probes.Live, "control-plane probes /health")
	assert.Equal(t, []string{"/ready", "/readyz"}, gateway.server.Probes.Ready, "control-plane probes /ready")
}

// TestServerRoleEnvContract pins the names control-plane sets on reliant's
// server workloads (deploy/kcl dev, e2e, lib/env.k) to the Options fields they
// reach.
func TestServerRoleEnvContract(t *testing.T) {
	isolateServerRoleEnv(t)
	for name, value := range map[string]string{
		"DATABASE_DIRECT_URL":  "postgres://reliant@db-direct.test/reliant",
		"DATABASE_DRIVER":      "postgres",
		"NATS_USER":            "reliant",
		"NATS_PASSWORD":        "secret",
		"TEMPORAL_HOST":        "temporal.test",
		"TEMPORAL_PORT":        "7234",
		"TEMPORAL_NAMESPACE":   "default",
		"DISABLE_TLS":          "true",
		"GRPC_PORT":            "19090",
		"HEALTH_PORT":          "18081",
		"PPROF_PORT":           "16060",
		"BIND_ADDRESS":         "127.0.0.1",
		"DATA_DIR":             "/data",
		"STREAMING_DRIVER":     "nats",
		"CORS_ALLOWED_ORIGINS": "http://localhost:3000, http://localhost:3001",
	} {
		t.Setenv(name, value)
	}

	api := loadServerAPIOptions(t)
	assert.Equal(t, "postgres://reliant@db-direct.test/reliant", api.DatabaseDirectURL)
	assert.Equal(t, natsutil.Endpoint{URL: testNATSURL, User: "reliant", Password: "secret"}, api.NATS)
	assert.Equal(t, "temporal.test", api.TemporalHost)
	assert.Equal(t, 7234, api.TemporalPort)
	assert.Equal(t, "default", api.TemporalNamespace)
	assert.True(t, api.DisableTLS)
	assert.Equal(t, 19090, api.GRPCPort)
	assert.Equal(t, "127.0.0.1:18081", api.HealthAddr)
	assert.Equal(t, 16060, api.PprofPort)
	assert.Equal(t, "127.0.0.1", api.BindAddress)
	assert.Equal(t, "/data", api.DataDir)
	assert.Equal(t, []string{"http://localhost:3000", "http://localhost:3001"}, api.CORSAllowedOrigins)

	t.Setenv("TOOLS_DAEMON_PORT", "19190")
	t.Setenv("PPROF_ADDR", "0.0.0.0:6060")
	opts, err := serverRoles[2].load(t)
	require.NoError(t, err)
	gateway := opts.(gatewayRole)
	assert.Equal(t, int32(19190), gateway.gateway.GetDaemonPort())
	assert.Equal(t, "127.0.0.1:18081", gateway.server.Addr, "HEALTH_PORT on BIND_ADDRESS")
	assert.Equal(t, "0.0.0.0:6060", gateway.server.PprofAddr)
	assert.True(t, gateway.process.GetTls().GetDisable())
	assert.Empty(t, gateway.server.TLSCertPath, "the probe address is plain HTTP whatever the role listeners' TLS")
}

// A flag still outranks its variable.
func TestServerRoleFlagsOutrankEnv(t *testing.T) {
	isolateServerRoleEnv(t)
	t.Setenv("GRPC_PORT", "19090")
	t.Setenv("CORS_ALLOWED_ORIGINS", "http://env.example")
	api := loadServerAPIOptions(t, "--grpc-port=29090", "--cors-origins=http://flag.example")
	assert.Equal(t, 29090, api.GRPCPort)
	assert.Equal(t, []string{"http://flag.example"}, api.CORSAllowedOrigins)
}

// Every role requires a database and NATS, and says which is missing at load.
func TestServerRolesRequireDatabaseAndNATS(t *testing.T) {
	for _, role := range serverRoles {
		for _, missing := range []string{"DATABASE_URL", "NATS_URL"} {
			t.Run(role.path[1]+" without "+missing, func(t *testing.T) {
				isolateServerRoleEnv(t)
				require.NoError(t, os.Unsetenv(missing))
				_, err := role.load(t)
				require.Error(t, err)
				assert.Contains(t, err.Error(), missing)
			})
		}
	}
}

// A value the server cannot use stops it at load, naming the setting — never
// a silent fall back to the default the old flag helpers took.
func TestServerRoleMisconfigurationFailsBoot(t *testing.T) {
	for _, tc := range []struct{ name, value, wantInErr string }{
		{"GRPC_PORT", "90o0", "GRPC_PORT"},
		{"HEALTH_PORT", "70000", "HEALTH_PORT"},
		{"TEMPORAL_PORT", "0", "TEMPORAL_PORT"},
		{"DISABLE_TLS", "yes", "DISABLE_TLS"},
		{"STREAMING_DRIVER", "memory", "streaming_driver"},
		{"DATABASE_DRIVER", "mysql", "driver"},
		{"PRE_STOP_DELAY", "five seconds", "PRE_STOP_DELAY"},
		{"SHUTDOWN_TIMEOUT", "${SHUTDOWN_TIMEOUT}", "SHUTDOWN_TIMEOUT"},
	} {
		t.Run(tc.name+"="+tc.value, func(t *testing.T) {
			isolateServerRoleEnv(t)
			t.Setenv(tc.name, tc.value)
			_, err := serverRoles[0].load(t)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantInErr)
		})
	}
}

// The drain budget comes from the env vars control-plane already sets and
// forge already derives terminationGracePeriodSeconds from, in every role.
func TestDrainBudgetReadFromEnvironment(t *testing.T) {
	for _, role := range serverRoles {
		t.Run(role.path[1], func(t *testing.T) {
			isolateServerRoleEnv(t)
			t.Setenv("PRE_STOP_DELAY", "7s")
			t.Setenv("SHUTDOWN_TIMEOUT", "45s")
			preStop, shutdown := drainOf(t, role)
			assert.Equal(t, 7*time.Second, preStop)
			assert.Equal(t, 45*time.Second, shutdown)
		})
	}
}

// Unset is serverkit's default budget, which is what forge's grace-period
// arithmetic assumes for a workload that sets neither variable.
func TestDrainBudgetDefaultsToServerkits(t *testing.T) {
	isolateServerRoleEnv(t)
	preStop, shutdown := drainOf(t, serverRoles[0])
	assert.Equal(t, 5*time.Second, preStop)
	d := &serverkit.Drainer{PreStopDelay: preStop, ShutdownTimeout: shutdown}
	ctx, cancel := d.ShutdownContext()
	defer cancel()
	deadline, ok := ctx.Deadline()
	require.True(t, ok, "the shutdown context has no deadline")
	if left := time.Until(deadline); left < 29*time.Second || left > 30*time.Second {
		t.Errorf("shutdown budget = %s, want serverkit's 30s default", left)
	}
}

// An explicit zero is the default, as it is everywhere a drain duration is
// read: forge sizes the pod's grace period for 5s / 30s when it sees "0s",
// so the server must spend the same.
func TestZeroDrainBudgetIsTheDefault(t *testing.T) {
	isolateServerRoleEnv(t)
	t.Setenv("PRE_STOP_DELAY", "0s")
	t.Setenv("SHUTDOWN_TIMEOUT", "0s")
	preStop, shutdown := drainOf(t, serverRoles[0])
	d := &serverkit.Drainer{PreStopDelay: preStop, ShutdownTimeout: shutdown}
	ctx, cancel := d.ShutdownContext()
	defer cancel()
	deadline, ok := ctx.Deadline()
	require.True(t, ok, "the shutdown context has no deadline")
	if left := time.Until(deadline); left < 29*time.Second || left > 30*time.Second {
		t.Errorf("shutdown budget = %s, want serverkit's 30s default", left)
	}
}

// Negative is none: no pre-stop pause, and a shutdown window that is over as
// it opens — which is what forge's grace period then leaves room for.
func TestNegativeDrainBudgetIsNone(t *testing.T) {
	isolateServerRoleEnv(t)
	t.Setenv("PRE_STOP_DELAY", "-1s")
	t.Setenv("SHUTDOWN_TIMEOUT", "-1s")
	preStop, shutdown := drainOf(t, serverRoles[0])
	d := &serverkit.Drainer{PreStopDelay: preStop, ShutdownTimeout: shutdown}
	start := time.Now()
	d.Begin()
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Begin paused %s for PRE_STOP_DELAY=-1s", elapsed)
	}
	ctx, cancel := d.ShutdownContext()
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > 0 {
		t.Errorf("SHUTDOWN_TIMEOUT=-1s left a shutdown window open")
	}
}

// drainOf loads role and returns the drain budget it hands its package.
func drainOf(t *testing.T, role serverRole) (preStop, shutdown time.Duration) {
	t.Helper()
	opts, err := role.load(t)
	require.NoError(t, err)
	switch o := opts.(type) {
	case apiOptions:
		return o.PreStopDelay, o.ShutdownTimeout
	case workerOptions:
		return o.PreStopDelay, o.ShutdownTimeout
	case gatewayRole:
		return o.server.PreStopDelay, o.server.ShutdownTimeout
	}
	t.Fatalf("unknown options %T", opts)
	return 0, 0
}

// The pool and schema-wait settings control-plane sets (dev host env: 8 open,
// 2 idle; the dev gateway pod: a 25s schema wait) reach every role's database
// config, and one it cannot honour stops the role at load.
func TestServerRoleDatabasePool(t *testing.T) {
	isolateServerRoleEnv(t)
	for _, name := range []string{"RELIANT_DB_MAX_OPEN_CONNS", "RELIANT_DB_MAX_IDLE_CONNS", "RELIANT_DB_SCHEMA_WAIT_SECONDS", "DB_CONNECT_TIMEOUT"} {
		t.Setenv(name, "")
		require.NoError(t, os.Unsetenv(name))
	}
	api := loadServerAPIOptions(t)
	assert.Equal(t, db.Pool{MaxOpenConns: 25, MaxIdleConns: 10, SchemaWait: 300 * time.Second, KeepIdleWarm: true}, api.DatabasePool)

	t.Setenv("RELIANT_DB_MAX_OPEN_CONNS", "8")
	t.Setenv("RELIANT_DB_MAX_IDLE_CONNS", "2")
	t.Setenv("RELIANT_DB_SCHEMA_WAIT_SECONDS", "25")
	t.Setenv("DB_CONNECT_TIMEOUT", "90s")
	want := db.Pool{MaxOpenConns: 8, MaxIdleConns: 2, ConnectTimeout: 90 * time.Second, SchemaWait: 25 * time.Second, KeepIdleWarm: true}
	for _, role := range serverRoles {
		opts, err := role.load(t)
		require.NoError(t, err)
		switch o := opts.(type) {
		case apiOptions:
			assert.Equal(t, want, o.DatabasePool, "api")
		case workerOptions:
			assert.Equal(t, want, o.DatabasePool, "worker")
		case gatewayRole:
			assert.Equal(t, want, o.pool, "gateway")
		}
	}

	for name, value := range map[string]string{
		"RELIANT_DB_MAX_OPEN_CONNS":      "lots",
		"RELIANT_DB_MAX_IDLE_CONNS":      "0",
		"RELIANT_DB_SCHEMA_WAIT_SECONDS": "-1",
		"DB_CONNECT_TIMEOUT":             "0s",
	} {
		t.Run(name+"="+value, func(t *testing.T) {
			t.Setenv(name, value)
			_, err := serverRoles[0].load(t)
			require.Error(t, err)
			assert.Contains(t, err.Error(), name)
		})
	}
}

// The worker's egress limits reach the limiter's Limits with the documented
// defaults when unset, and a value below 1 stops the worker at load rather
// than silently disabling a limit.
func TestServerWorkerEgressLimits(t *testing.T) {
	isolateServerRoleEnv(t)
	names := []string{
		"RELIANT_WORKER_EGRESS_REPLICAS",
		"RELIANT_WORKER_EGRESS_USER_PER_MINUTE", "RELIANT_WORKER_EGRESS_USER_BURST",
		"RELIANT_WORKER_EGRESS_USER_MAX_CONCURRENT",
		"RELIANT_WORKER_EGRESS_HOST_MAX_CONCURRENT", "RELIANT_WORKER_EGRESS_TURN_MAX_PARALLEL",
	}
	for _, name := range names {
		t.Setenv(name, "")
		require.NoError(t, os.Unsetenv(name))
	}
	load := func(t *testing.T) (workerOptions, error) {
		var opts workerOptions
		err := loadServerWorkerConfig(findServerCmd(t, "worker"), &opts)
		return opts, err
	}

	opts, err := load(t)
	require.NoError(t, err)
	assert.Equal(t, egresslimit.Limits{
		Replicas:      1,
		UserPerMinute: egresslimit.DefaultUserPerMinute, UserBurst: egresslimit.DefaultUserBurst,
		UserMaxConcurrent: egresslimit.DefaultUserMaxConcurrent,
		HostMaxConcurrent: egresslimit.DefaultHostMaxConcurrent, TurnMaxParallel: egresslimit.DefaultTurnMaxParallel,
	}, opts.Egress, "the proto defaults and the limiter's defaults must agree")

	t.Setenv("RELIANT_WORKER_EGRESS_REPLICAS", "3")
	t.Setenv("RELIANT_WORKER_EGRESS_HOST_MAX_CONCURRENT", "5")
	opts, err = load(t)
	require.NoError(t, err)
	assert.Equal(t, 3, opts.Egress.Replicas)
	assert.Equal(t, 5, opts.Egress.HostMaxConcurrent)

	// Replicas 0 is "unset": the limiter divides by 1. Every other value must
	// be positive, or a limit would silently be off.
	t.Setenv("RELIANT_WORKER_EGRESS_REPLICAS", "0")
	opts, err = load(t)
	require.NoError(t, err)
	assert.Equal(t, 0, opts.Egress.Replicas, "0 reaches the limiter, which treats it as 1")

	for _, name := range names[1:] {
		t.Run(name+"=0", func(t *testing.T) {
			t.Setenv(name, "0")
			_, err := load(t)
			require.Error(t, err)
			assert.Contains(t, err.Error(), name)
		})
	}
}

// The worker's workflow-task limits reach what NewTemporalWorker hands
// workersetup (unset: 0, its CPU-derived cap, and 5 pollers), and the value
// the Temporal SDK would panic on, 1, stops the worker at load instead.
func TestServerWorkerWorkflowTaskLimits(t *testing.T) {
	isolateServerRoleEnv(t)
	names := []string{"RELIANT_WORKER_MAX_CONCURRENT_WORKFLOW_TASKS", "RELIANT_WORKER_WORKFLOW_TASK_POLLERS"}
	for _, name := range names {
		t.Setenv(name, "")
		require.NoError(t, os.Unsetenv(name))
	}
	load := func(t *testing.T) (workerOptions, error) {
		var opts workerOptions
		err := loadServerWorkerConfig(findServerCmd(t, "worker"), &opts)
		return opts, err
	}

	opts, err := load(t)
	require.NoError(t, err)
	assert.Equal(t, 0, opts.WorkflowTaskSlots)
	assert.Equal(t, 5, opts.WorkflowTaskPollers)

	t.Setenv(names[0], "2")
	t.Setenv(names[1], "4")
	opts, err = load(t)
	require.NoError(t, err)
	assert.Equal(t, 2, opts.WorkflowTaskSlots)
	assert.Equal(t, 4, opts.WorkflowTaskPollers)

	for _, name := range names {
		for _, value := range []string{"1", "-3"} {
			t.Run(name+"="+value, func(t *testing.T) {
				t.Setenv(name, value)
				_, err := load(t)
				require.Error(t, err)
				assert.Contains(t, err.Error(), name)
			})
		}
	}
}

// The migrate commands load the servers' own DatabaseConfig, and run on the
// direct (session) URL when one is set.
func TestDBMigrateReadsTheServersDatabase(t *testing.T) {
	isolateServerRoleEnv(t)
	for _, sub := range []string{"up", "status", "force"} {
		cmd, _, err := NewRootCmd().Find([]string{"db", "migrate", sub})
		require.NoError(t, err)
		require.Equal(t, sub, cmd.Name())
	}
	cmd, _, err := NewRootCmd().Find([]string{"db", "migrate", "up"})
	require.NoError(t, err)
	cfg, err := config.LoadDatabaseConfig(cmd)
	require.NoError(t, err)
	assert.Equal(t, testDatabaseURL, migrationURL(cfg))

	t.Setenv("DATABASE_DIRECT_URL", " postgres://reliant@db-direct.test/reliant ")
	cfg, err = config.LoadDatabaseConfig(cmd)
	require.NoError(t, err)
	assert.Equal(t, "postgres://reliant@db-direct.test/reliant", migrationURL(cfg))

	require.NoError(t, os.Unsetenv("DATABASE_URL"))
	_, err = config.LoadDatabaseConfig(func() *cobra.Command {
		c, _, err := NewRootCmd().Find([]string{"db", "migrate", "status"})
		require.NoError(t, err)
		return c
	}())
	require.Error(t, err, "DATABASE_URL is required")
	assert.Contains(t, err.Error(), "DATABASE_URL")
}

// Each integration's OAuth client is the RELIANT_OAUTH_<ID>_CLIENT_ID /
// _CLIENT_SECRET env family, loaded as config by the roles that compose
// IntegrationsConfig, and looked up by integration id.
func TestServerOAuthClientsAreAnEnvFamily(t *testing.T) {
	isolateServerRoleEnv(t)
	t.Setenv("RELIANT_OAUTH_GITHUB_CLIENT_ID", " gh-id ")
	t.Setenv("RELIANT_OAUTH_GITHUB_CLIENT_SECRET", "gh-secret")
	t.Setenv("RELIANT_OAUTH_SLACK_CLIENT_ID", "slack-id")

	got, err := serverRoles[0].load(t)
	require.NoError(t, err)
	clients := got.(apiOptions).Webhooks.OAuthClients
	id, secret := clients("github")
	assert.Equal(t, "gh-id", id)
	assert.Equal(t, "gh-secret", secret)
	id, secret = clients("slack")
	assert.Equal(t, "slack-id", id)
	assert.Empty(t, secret, "an integration with no secret has none")
	id, secret = clients("gmail")
	assert.Empty(t, id+secret, "an integration with neither is not configured")
}

// The RELIANT_OAUTH_ prefix is owned: a variable in it that names no family
// is a typo, and the role refuses to start naming it instead of running with
// the client silently missing.
func TestServerOAuthClientTypoFailsBoot(t *testing.T) {
	for _, role := range serverRoles { // every role loads IntegrationsConfig (ServerProcessConfig)
		t.Run(role.path[1], func(t *testing.T) {
			isolateServerRoleEnv(t)
			t.Setenv("RELIANT_OAUTH_X_CLIENT_SECERT", "oops")
			_, err := role.load(t)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "RELIANT_OAUTH_X_CLIENT_SECERT")
		})
	}
}

// The desktop sign-in's provider defaults to the hosted one and follows
// RELIANT_AUTH_URL / RELIANT_AUTH_KEY.
func TestServerAPIOAuthProvider(t *testing.T) {
	isolateServerRoleEnv(t)
	t.Setenv("RELIANT_AUTH_URL", "")
	t.Setenv("RELIANT_AUTH_KEY", "")
	got, err := serverRoles[0].load(t)
	require.NoError(t, err)
	assert.Equal(t, "https://dash.reliantlabs.io", got.(apiOptions).OAuthProvider.URL)
	assert.NotEmpty(t, got.(apiOptions).OAuthProvider.Key)

	t.Setenv("RELIANT_AUTH_URL", "https://auth.example.com")
	t.Setenv("RELIANT_AUTH_KEY", "sb_publishable_example")
	got, err = serverRoles[0].load(t)
	require.NoError(t, err)
	assert.Equal(t, "https://auth.example.com", got.(apiOptions).OAuthProvider.URL)
	assert.Equal(t, "sb_publishable_example", got.(apiOptions).OAuthProvider.Key)
}
