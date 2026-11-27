// Copyright (c) 2026 Reliant Labs
package cmd

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/client"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/forge/pkg/kcleval"
	"github.com/reliant-labs/forge/pkg/testkit"
	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/app"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/handlers/pipeline"
	"github.com/reliant-labs/reliant/internal/serveraction"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/workersetup"
	"github.com/reliant-labs/reliant/pkg/config"
)

const (
	pipelineSwitchEnv = "RELIANT_PIPELINE_ENABLED"
	// pipelineProbe is a server action no catalog of Reliant's declares.
	pipelineProbe = "reliant/test.probe@1"
)

// loadWorkerProcess loads `server worker`'s ServerProcessConfig with
// RELIANT_PIPELINE_ENABLED set to value, or unset when set is false.
func loadWorkerProcess(t *testing.T, value string, set bool) *config.ServerProcessConfig {
	t.Helper()
	return loadRoleProcess(t, "worker", value, set)
}

// loadRoleProcess loads `server <role>`'s ServerProcessConfig with
// RELIANT_PIPELINE_ENABLED set to value, or unset when set is false.
func loadRoleProcess(t *testing.T, role, value string, set bool) *config.ServerProcessConfig {
	t.Helper()
	isolateServerRoleEnv(t)
	t.Setenv(pipelineSwitchEnv, "") // restores the original afterwards
	require.NoError(t, os.Unsetenv(pipelineSwitchEnv))
	if set {
		t.Setenv(pipelineSwitchEnv, value)
	}
	process, err := config.LoadServerProcessConfig(findServerCmd(t, role))
	require.NoError(t, err)
	return process
}

// lazyTemporal is an SDK client that never dials, so a worker builds without
// a Temporal server.
func lazyTemporal(t *testing.T) client.Client {
	t.Helper()
	c, err := client.NewLazyClient(client.Options{HostPort: "127.0.0.1:1", Namespace: "reliant"})
	require.NoError(t, err)
	t.Cleanup(c.Close)
	return c
}

type countingResolver struct{ calls int }

func (r *countingResolver) Resolve(context.Context, serveraction.Invocation, serveraction.RunFacts) (serveraction.Principal, serveraction.Provenance, error) {
	r.calls++
	return serveraction.Principal{UserID: "u-1"}, serveraction.Provenance{Source: serveraction.SourceBuiltin}, nil
}

// assertWorkerServerActions follows a loaded ServerProcessConfig through the
// worker's wiring to the dispatcher its Action activity uses, and checks that
// a server action is refused before anything runs when the switch is off and
// dispatched when it is on.
func assertWorkerServerActions(t *testing.T, process *config.ServerProcessConfig, wantOn bool) {
	t.Helper()
	ctx := context.Background()

	// The worker `server worker` runs: app.NewTemporalWorker given the
	// pipeline block, as compose.go's newTemporalWorker passes it, building
	// workersetup's worker and its dispatcher. It binds no action yet, so an
	// enabled switch lets a call past the switch to the catalog, which
	// refuses the undeclared name.
	pipeline := process.GetPipeline()
	w, err := app.NewTemporalWorker(app.TemporalWorkerDeps{
		Temporal:    &app.TemporalClient{Client: lazyTemporal(t), Namespace: "reliant"},
		NATS:        &app.NATSConn{},
		Streaming:   &app.StreamingHub{},
		Tools:       &app.WorkerToolsFactory{},
		Executor:    &toolexec.RemoteExecutor{},
		Credentials: &app.WorkerIntegrationCredentials{},
		Recorder:    &app.AccountRecorder{},
		Worker:      &configv1.WorkerConfig{},
		Pipeline:    pipeline,
	})
	require.NoError(t, err)
	require.Equal(t, wantOn, w.ServerActions().Enabled(), "the worker's dispatcher switch")
	out := w.ServerActions().Dispatch(ctx, serveraction.Call{Uses: pipelineProbe})
	require.NotNil(t, out.Err)
	if wantOn {
		require.Equal(t, serveraction.CodeInvalidArgument, out.Err.Code, "on: past the switch, to the catalog: %v", out.Err)
		require.NotContains(t, out.Err.Message, serveraction.SwitchName)
	} else {
		require.Equal(t, serveraction.CodeFailedPrecondition, out.Err.Code, "off: refused by the switch: %v", out.Err)
		require.Contains(t, out.Err.Message, serveraction.SwitchName)
	}

	// The same handoff with a test action bound beside the worker's own
	// config (app.WorkerServerActions, what NewTemporalWorker hands
	// workersetup), so a call reaches a handler when the switch lets it.
	catalog, err := serveraction.NewCatalog(serveraction.Spec{Name: pipelineProbe, Description: "probe",
		Params: map[string]any{"type": "object"}})
	require.NoError(t, err)
	ran := 0
	resolver := &countingResolver{}
	actions := app.WorkerServerActions(pipeline)
	actions.Catalog = catalog
	actions.Resolver = resolver
	actions.Handlers = map[string]serveraction.Handler{pipelineProbe: serveraction.HandlerFunc(
		func(context.Context, *serveraction.Request) (*serveraction.Result, error) {
			ran++
			return &serveraction.Result{Content: "probed"}, nil
		})}
	bound, _, err := workersetup.NewWorker(&workersetup.Config{
		TemporalClient: lazyTemporal(t), TaskQueueSuffix: "pipeline-switch", ServerActions: actions,
	})
	require.NoError(t, err)
	out = bound.ServerActions().Dispatch(ctx, serveraction.Call{
		Uses: pipelineProbe, Params: map[string]any{},
		Invocation: serveraction.Invocation{RunID: "run-1", NodeID: "probe", NodePath: "probe"},
	})
	if wantOn {
		require.Nil(t, out.Err, "on: dispatched")
		require.Equal(t, "probed", out.Result.Content)
		require.Equal(t, 1, ran)
		require.Equal(t, 1, resolver.calls)
	} else {
		require.NotNil(t, out.Err)
		require.Equal(t, serveraction.CodeFailedPrecondition, out.Err.Code, "off: refused: %v", out.Err)
		require.Contains(t, out.Err.Message, serveraction.SwitchName)
		require.Zero(t, ran, "off: the handler never ran")
		require.Zero(t, resolver.calls, "off: the run's records were never read")
	}
}

// TestPipelineSwitchReachesTheWorkerDispatcher pins pipeline.enabled on the
// path production takes: an env's KCL (config_gen.workloadEnvMap for the
// worker root) projects RELIANT_PIPELINE_ENABLED, `server worker` loads its
// ServerProcessConfig from it, and app.NewTemporalWorker hands the pipeline
// block to workersetup, whose dispatcher the Action activity runs every
// `uses: reliant/...` through. At the schema default, and in this repo's dev
// and demo envs, the switch is off and a server action is refused before its
// resolver or handler runs; set, it is dispatched. The dispatcher's own tests
// set Enabled directly, so without this a flipped default, a dropped env-map
// entry or a lost handoff would go unseen.
func TestPipelineSwitchReachesTheWorkerDispatcher(t *testing.T) {
	testkit.Slow(t, "evaluates deploy/kcl through `forge env kcl eval`",
		testkit.Inputs("deploy/kcl/**"))

	const fixture = "deploy/kcl/testdata/pipeline_switch.k"
	project := kcleval.Options{ProjectDir: "../../..", Binary: pinnedForge(t)}
	for _, tc := range []struct {
		selector string
		wantEnv  string
		wantOn   bool
	}{
		{"switch_default", "false", false},
		{"dev_worker", "false", false},
		{"demo_worker", "false", false},
		{"switch_on", "true", true},
	} {
		t.Run(tc.selector, func(t *testing.T) {
			var env map[string]any
			require.NoError(t, kcleval.Select(context.Background(), fixture, tc.selector, &env, project))
			got, ok := env[pipelineSwitchEnv]
			require.True(t, ok, "the worker's projected env has no %s: %v", pipelineSwitchEnv, env)
			require.Equal(t, tc.wantEnv, got)

			process := loadWorkerProcess(t, got.(string), true)
			require.Equal(t, tc.wantOn, process.GetPipeline().GetEnabled(), "ServerProcessConfig.pipeline as loaded")
			assertWorkerServerActions(t, process, tc.wantOn)
		})
	}
}

// The fast half, for the merge tier: deploy/kcl/config_gen.k (read by this
// process, so the test cache sees an edit) still defaults the switch off and
// projects it into the worker root's env; with RELIANT_PIPELINE_ENABLED
// unset, as every env ships, the worker refuses a server action before
// anything runs; set, it dispatches one.
func TestPipelineSwitchIsOffByDefault(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "kcl", "config_gen.k"))
	require.NoError(t, err)
	kcl := string(raw)
	// require.True, not Contains: a failure would print the whole file.
	require.True(t, regexp.MustCompile(`(?m)^schema ServerProcessConfigPipeline:\n(?:    #.*\n)*    enabled: bool = False$`).MatchString(kcl),
		"config_gen.k: the KCL schema's pipeline.enabled default is no longer off")
	require.True(t, strings.Contains(kcl, `"`+pipelineSwitchEnv+`" = "true" if c.pipeline.enabled else "false"`),
		"config_gen.k: serverProcessConfigEnvMap no longer projects pipeline.enabled as %s", pipelineSwitchEnv)
	start := strings.Index(kcl, `    "worker" = Root {`)
	require.GreaterOrEqual(t, start, 0, "config_gen.k's ROOTS has no worker root")
	worker := kcl[start:]
	if next := strings.Index(worker[1:], "\n    \""); next >= 0 {
		worker = worker[:next+1]
	}
	require.True(t, strings.Contains(worker, `"`+pipelineSwitchEnv+`",`), "config_gen.k: the worker root's env no longer carries %s", pipelineSwitchEnv)

	process := loadWorkerProcess(t, "", false)
	require.False(t, process.GetPipeline().GetEnabled())
	assertWorkerServerActions(t, process, false)

	process = loadWorkerProcess(t, "true", true)
	require.True(t, process.GetPipeline().GetEnabled())
	assertWorkerServerActions(t, process, true)
}

// assertAPIPipelineService follows a loaded ServerProcessConfig to the
// PipelineService the API server mounts, built as compose.go's newPipeline
// builds it (the pipeline block of the process's ServerProcessConfig): off,
// every rpc is FailedPrecondition naming the switch before anything is read;
// on, a call gets past the switch (here to the caller check, as the call
// carries no user).
func assertAPIPipelineService(t *testing.T, process *config.ServerProcessConfig, wantOn bool) {
	t.Helper()
	store := &pipelineSwitchStore{}
	svc, err := pipeline.New(pipeline.Deps{
		Logger: testkit.DiscardLogger(), Config: &config.Config{},
		Pipeline: process.GetPipeline(), Store: store,
	})
	require.NoError(t, err)
	_, err = svc.GetPipelineState(context.Background(), connect.NewRequest(&reliantv1.GetPipelineStateRequest{RepoId: "r-1"}))
	require.Error(t, err)
	if wantOn {
		require.Equal(t, connect.CodeUnauthenticated, svcerr.Code(err), "on: past the switch: %v", err)
		return
	}
	require.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err), "off: refused by the switch: %v", err)
	require.Contains(t, err.Error(), serveraction.SwitchName)
	_, err = svc.EnrolRepository(context.Background(), connect.NewRequest(&reliantv1.EnrolRepositoryRequest{ProjectId: "p", Repository: "a/b"}))
	require.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err), "off: enrol refused by the switch: %v", err)
	require.Zero(t, store.calls, "off: nothing was read")
}

// pipelineSwitchStore is a PipelineService store that counts the reads it
// gets: the switch refuses before any (any other call panics on the nil
// embedded interface, which fails the test as loudly).
type pipelineSwitchStore struct {
	pipeline.Store
	calls int
}

func (s *pipelineSwitchStore) GetPipelineRepo(context.Context, string) (*core.PipelineRepo, error) {
	s.calls++
	return nil, nil
}

func (s *pipelineSwitchStore) GetProject(context.Context, string) (*core.Project, error) {
	s.calls++
	return nil, nil
}

// TestPipelineSwitchReachesTheAPIServer is TestPipelineSwitchReachesTheWorkerDispatcher
// for the API server (G4): an env's KCL projects RELIANT_PIPELINE_ENABLED into
// the api root's env, `server api` loads it, and the PipelineService it
// mounts refuses every rpc while it is off.
func TestPipelineSwitchReachesTheAPIServer(t *testing.T) {
	testkit.Slow(t, "evaluates deploy/kcl through `forge env kcl eval`",
		testkit.Inputs("deploy/kcl/**"))

	const fixture = "deploy/kcl/testdata/pipeline_switch.k"
	project := kcleval.Options{ProjectDir: "../../..", Binary: pinnedForge(t)}
	for _, tc := range []struct {
		selector string
		wantEnv  string
		wantOn   bool
	}{
		{"switch_default_api", "false", false},
		{"dev_api", "false", false},
		{"demo_api", "false", false},
		{"switch_on_api", "true", true},
	} {
		t.Run(tc.selector, func(t *testing.T) {
			var env map[string]any
			require.NoError(t, kcleval.Select(context.Background(), fixture, tc.selector, &env, project))
			got, ok := env[pipelineSwitchEnv]
			require.True(t, ok, "the API server's projected env has no %s: %v", pipelineSwitchEnv, env)
			require.Equal(t, tc.wantEnv, got)

			process := loadRoleProcess(t, "api", got.(string), true)
			require.Equal(t, tc.wantOn, process.GetPipeline().GetEnabled(), "ServerProcessConfig.pipeline as loaded")
			assertAPIPipelineService(t, process, tc.wantOn)
		})
	}
}

// The fast half for the API server: config_gen.k's api root carries the
// switch, and with it unset the PipelineService refuses; set, it serves.
func TestPipelineSwitchIsOffByDefaultOnTheAPIServer(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "kcl", "config_gen.k"))
	require.NoError(t, err)
	kcl := string(raw)
	start := strings.Index(kcl, `    "api" = Root {`)
	require.GreaterOrEqual(t, start, 0, "config_gen.k's ROOTS has no api root")
	api := kcl[start:]
	if next := strings.Index(api[1:], "\n    \""); next >= 0 {
		api = api[:next+1]
	}
	require.True(t, strings.Contains(api, `"`+pipelineSwitchEnv+`",`), "config_gen.k: the api root's env no longer carries %s", pipelineSwitchEnv)

	process := loadRoleProcess(t, "api", "", false)
	require.False(t, process.GetPipeline().GetEnabled())
	assertAPIPipelineService(t, process, false)

	process = loadRoleProcess(t, "api", "true", true)
	require.True(t, process.GetPipeline().GetEnabled())
	assertAPIPipelineService(t, process, true)
}
