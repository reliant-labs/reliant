// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/egresslimit"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/nomachine"
)

// A PlacementPreferDaemon tool runs on the run's machine when it has one and on
// the server when it does not, and a failed dispatch is run on the server
// rather than failed. The tests drive fetch (the registry's first user) and a
// hypothetical second tool that adopts the placement with a declaration only.

// fetchOnServerInput makes the real fetch tool fail fast and offline when it
// runs on the server (it refuses an empty URL before dialing), which is the
// observable difference from the router's canned daemon answer.
const fetchOnServerInput = `{"url":"","format":"text"}`

const serverFetchRefusal = "URL parameter is required"

// preferDaemonRouter answers tool requests like a daemon would, or fails like
// a transport.
type preferDaemonRouter struct {
	recordingDaemonRouter
	resp *ToolExecutionResponse
	err  error
}

func (r *preferDaemonRouter) SendToolRequestSync(ctx context.Context, user string, req *ToolExecutionRequest) (*ToolExecutionResponse, error) {
	r.toolsSent = append(r.toolsSent, req.ToolName)
	return r.resp, r.err
}

func (r *preferDaemonRouter) SendToolRequestSyncWithSelector(ctx context.Context, user string, req *ToolExecutionRequest, _ *DaemonSelector) (*ToolExecutionResponse, error) {
	return r.SendToolRequestSync(ctx, user, req)
}

func daemonAnswer(content string) *ToolExecutionResponse {
	return &ToolExecutionResponse{Success: true, Content: content, Metadata: `{"status_code":200}`}
}

func newPreferDaemonExecutor(router DaemonRouter) (*RemoteExecutor, *daemonClientProbe) {
	probe := &daemonClientProbe{}
	executor := NewRemoteExecutor(router)
	executor.SetServerExecutor(NewLocalToolExecutor(tools.NewToolsFactory(&tools.ToolsOptions{})))
	executor.SetDaemonClientFactory(func(string, *DaemonSelector) daemon.Client {
		probe.handed++
		return daemon.NewLocalClient()
	})
	return executor, probe
}

type daemonClientProbe struct{ handed int }

func fetchRequest() *ToolRequest {
	req := placementTestRequest(tools.ToolFetch)
	req.ToolInput = fetchOnServerInput
	return req
}

func placementOfResult(t *testing.T, res *ToolResult) map[string]any {
	t.Helper()
	var doc map[string]any
	require.NoError(t, json.Unmarshal([]byte(res.Metadata), &doc), res.Metadata)
	got, ok := doc[tools.PlacementMetadataKey].(map[string]any)
	require.True(t, ok, "metadata carries %s: %s", tools.PlacementMetadataKey, res.Metadata)
	return got
}

func TestPreferDaemon_RunWithAMachineRunsOnTheDaemon(t *testing.T) {
	router := &preferDaemonRouter{resp: daemonAnswer("from the daemon")}
	executor, _ := newPreferDaemonExecutor(router)

	res, err := executor.ExecuteTool(context.Background(), fetchRequest())
	require.NoError(t, err)

	assert.Equal(t, []string{tools.ToolFetch}, router.toolsSent)
	assert.Equal(t, "from the daemon", res.Content)
	assert.True(t, res.Success)
	assert.True(t, res.RanOnDaemon, "a daemon round trip proves the machine reachable")
	assert.Equal(t, ExecutedAtDaemon, res.ExecutedAt)
	assert.Empty(t, res.FallbackReason)
	meta := placementOfResult(t, res)
	assert.Equal(t, "daemon", meta["ran_at"])
	assert.Equal(t, float64(200), mustMetadata(t, res)["status_code"], "the tool's own metadata is kept")
}

func TestPreferDaemon_NoMachineRunsOnTheServer(t *testing.T) {
	router := &preferDaemonRouter{resp: daemonAnswer("from the daemon")}
	executor, probe := newPreferDaemonExecutor(router)
	ctx := nomachine.With(context.Background())

	res, err := executor.ExecuteTool(ctx, fetchRequest())
	require.NoError(t, err)

	assert.Empty(t, router.toolsSent, "a run with no machine must never reach the router")
	assert.Contains(t, res.Content, serverFetchRefusal, "the server ran the real tool")
	assert.Equal(t, ExecutedAtServer, res.ExecutedAt)
	assert.Equal(t, FallbackNoMachine, res.FallbackReason)
	assert.False(t, res.RanOnDaemon)
	assert.Zero(t, probe.handed, "a no-machine run is not handed a daemon client")
}

func TestPreferDaemon_NoRouterRunsOnTheServer(t *testing.T) {
	executor, _ := newPreferDaemonExecutor(&preferDaemonRouter{})
	executor.router = nil

	res, err := executor.ExecuteTool(context.Background(), fetchRequest())
	require.NoError(t, err)
	assert.Contains(t, res.Content, serverFetchRefusal)
	assert.Equal(t, ExecutedAtServer, res.ExecutedAt)
}

func TestPreferDaemon_DispatchFailureFallsBackToTheServer(t *testing.T) {
	cases := map[string]*preferDaemonRouter{
		"daemon disconnected mid-run": {err: errors.New("no daemon connected for user")},
		"machine still starting":      {err: ErrDaemonPending},
		"gateway could not deliver": {resp: &ToolExecutionResponse{
			Success: false, IsError: true, ErrorMessage: "gateway busy", ErrorCode: ErrorCodeDaemonRoundTrip}},
		"connection dropped under the request": {resp: &ToolExecutionResponse{
			Success: false, IsError: true, ErrorMessage: "dropped", ErrorCode: ErrorCodeDaemonInterrupted}},
		"daemon predates the tool": {resp: &ToolExecutionResponse{
			Success: false, IsError: true, ErrorMessage: "tool not found: fetch", ErrorCode: "TOOL_NOT_FOUND"}},
	}
	for name, router := range cases {
		t.Run(name, func(t *testing.T) {
			executor, _ := newPreferDaemonExecutor(router)

			res, err := executor.ExecuteTool(context.Background(), fetchRequest())
			require.NoError(t, err)

			assert.Equal(t, []string{tools.ToolFetch}, router.toolsSent, "the daemon was tried first")
			assert.Contains(t, res.Content, serverFetchRefusal, "then the server ran the tool")
			assert.Equal(t, ExecutedAtServer, res.ExecutedAt)
			assert.Equal(t, FallbackDispatchFailed, res.FallbackReason)
			assert.False(t, res.DaemonPending, "fetch does not need the machine, so a dead one must not pause the run")
			assert.False(t, res.RanOnDaemon)
			assert.Equal(t, "server", placementOfResult(t, res)["ran_at"])
			assert.Equal(t, FallbackDispatchFailed, placementOfResult(t, res)["fallback_reason"])
		})
	}
}

// A tool that ran on the daemon and failed on its own terms is an answer. Running
// it again on the server would hide the daemon's real result (and, on a local
// daemon, answer from a different network than the user asked about).
func TestPreferDaemon_ToolFailureOnTheDaemonIsReturnedNotRetried(t *testing.T) {
	router := &preferDaemonRouter{resp: &ToolExecutionResponse{
		Success: true, IsError: true, Content: "connection refused: 127.0.0.1:3000"}}
	executor, _ := newPreferDaemonExecutor(router)

	res, err := executor.ExecuteTool(context.Background(), fetchRequest())
	require.NoError(t, err)

	assert.Equal(t, "connection refused: 127.0.0.1:3000", res.Content)
	assert.True(t, res.IsError)
	assert.Equal(t, ExecutedAtDaemon, res.ExecutedAt)
	assert.Equal(t, []string{tools.ToolFetch}, router.toolsSent)
}

// An abandoned call must not be quietly re-run on the server.
func TestPreferDaemon_CancelledCallDoesNotFallBack(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	router := &cancelThenFailRouter{cancel: cancel}
	executor, _ := newPreferDaemonExecutor(router)

	res, err := executor.ExecuteTool(ctx, fetchRequest())
	require.NoError(t, err)

	assert.True(t, res.IsError)
	assert.Equal(t, ExecutedAtDaemon, res.ExecutedAt, "the attempt was the daemon's")
	assert.NotContains(t, res.Content, serverFetchRefusal)
}

type cancelThenFailRouter struct {
	routerStub
	cancel context.CancelFunc
}

func (r *cancelThenFailRouter) SendToolRequestSync(context.Context, string, *ToolExecutionRequest) (*ToolExecutionResponse, error) {
	r.cancel()
	return nil, context.Canceled
}

// The dispatch must not wait for a disconnected daemon to come back: there is a
// server to run on, so waiting would only add the grace period to every fetch.
func TestPreferDaemon_DoesNotRideOutADisconnect(t *testing.T) {
	probe := &waitProbeRouter{}
	executor, _ := newPreferDaemonExecutor(probe)

	_, err := executor.ExecuteTool(context.Background(), fetchRequest())
	require.NoError(t, err)
	assert.False(t, probe.waitSeen, "prefer-daemon dispatch opts out of the reconnect wait")

	// Control: a daemon-placed tool keeps riding out a disconnect.
	probe.waitSeen = false
	_, err = executor.ExecuteTool(context.Background(), placementTestRequest("mcp__serena__find_symbol"))
	require.NoError(t, err)
	assert.True(t, probe.waitSeen)
}

type waitProbeRouter struct {
	routerStub
	waitSeen bool
}

func (r *waitProbeRouter) SendToolRequestSync(ctx context.Context, _ string, _ *ToolExecutionRequest) (*ToolExecutionResponse, error) {
	r.waitSeen = reconnectWaitFrom(ctx) != nil
	return nil, errors.New("no daemon connected")
}

// The same call yields the same result shape wherever it runs: the same
// success/error/content/code, and the placement note under the same key with
// the same fields.
func TestPreferDaemon_ResultShapeIsIdenticalAcrossPlacements(t *testing.T) {
	onDaemon, _ := newPreferDaemonExecutor(&preferDaemonRouter{resp: &ToolExecutionResponse{
		Success: true, IsError: true, Content: serverFetchRefusal}})
	onServer, _ := newPreferDaemonExecutor(&preferDaemonRouter{})

	d, err := onDaemon.ExecuteTool(context.Background(), fetchRequest())
	require.NoError(t, err)
	s, err := onServer.ExecuteTool(nomachine.With(context.Background()), fetchRequest())
	require.NoError(t, err)

	assert.Equal(t, d.Success, s.Success)
	assert.Equal(t, d.IsError, s.IsError)
	assert.Equal(t, d.ErrorCode, s.ErrorCode)
	assert.Equal(t, d.Content, s.Content)
	assert.Equal(t, d.Backgrounded, s.Backgrounded)

	dp, sp := placementOfResult(t, d), placementOfResult(t, s)
	assert.Equal(t, "daemon", dp["ran_at"])
	assert.Equal(t, "server", sp["ran_at"])
	assert.NotContains(t, dp, "fallback_reason", "no fallback happened")
	assert.Equal(t, FallbackNoMachine, sp["fallback_reason"])
	assert.Equal(t, mustMetadataWithout(t, d), mustMetadataWithout(t, s), "everything but the note is the tool's own")
}

func mustMetadataWithout(t *testing.T, res *ToolResult) map[string]any {
	t.Helper()
	doc := mustMetadata(t, res)
	delete(doc, tools.PlacementMetadataKey)
	return doc
}

// Adopting the placement is a declaration, nothing else: a tool this executor
// has no knowledge of runs on the daemon with a machine and on the server
// without one, and a dispatch failure falls back, with no per-tool code.
func TestPreferDaemon_ASecondToolAdoptsItByDeclarationOnly(t *testing.T) {
	const hypothetical = "hypothetical_lookup"
	declare := func(e *RemoteExecutor) {
		e.placementOf = func(name string) (tools.Placement, error) {
			if name == hypothetical {
				return tools.PlacementPreferDaemon, nil
			}
			return tools.PlacementOf(name)
		}
	}
	req := func() *ToolRequest {
		r := placementTestRequest(hypothetical)
		r.ToolInput = `{}`
		return r
	}
	// No server ToolsFactory knows the tool, so reaching the server executor
	// is visible as TOOL_NOT_FOUND.

	t.Run("machine: daemon", func(t *testing.T) {
		router := &preferDaemonRouter{resp: daemonAnswer("looked up on the daemon")}
		executor, _ := newPreferDaemonExecutor(router)
		declare(executor)
		res, err := executor.ExecuteTool(context.Background(), req())
		require.NoError(t, err)
		assert.Equal(t, []string{hypothetical}, router.toolsSent)
		assert.Equal(t, "looked up on the daemon", res.Content)
		assert.Equal(t, ExecutedAtDaemon, res.ExecutedAt)
	})
	t.Run("no machine: server", func(t *testing.T) {
		router := &preferDaemonRouter{resp: daemonAnswer("looked up on the daemon")}
		executor, _ := newPreferDaemonExecutor(router)
		declare(executor)
		res, err := executor.ExecuteTool(nomachine.With(context.Background()), req())
		require.NoError(t, err)
		assert.Empty(t, router.toolsSent)
		assert.Equal(t, "TOOL_NOT_FOUND", res.ErrorCode, "the server executor was reached")
		assert.Equal(t, ExecutedAtServer, res.ExecutedAt)
	})
	t.Run("dispatch failure: server", func(t *testing.T) {
		router := &preferDaemonRouter{err: errors.New("no daemon connected for user")}
		executor, _ := newPreferDaemonExecutor(router)
		declare(executor)
		res, err := executor.ExecuteTool(context.Background(), req())
		require.NoError(t, err)
		assert.Equal(t, []string{hypothetical}, router.toolsSent)
		assert.Equal(t, "TOOL_NOT_FOUND", res.ErrorCode)
		assert.Equal(t, ExecutedAtServer, res.ExecutedAt)
		assert.Equal(t, FallbackDispatchFailed, res.FallbackReason)
	})
}

// The hosted path: every existing daemon-placed tool still goes through the
// router untouched (see also TestExecuteTool_MCPToolsGoToTheDaemon).
func TestPreferDaemon_DoesNotChangeDaemonOrServerPlacedTools(t *testing.T) {
	router := &preferDaemonRouter{resp: daemonAnswer("ok")}
	executor, _ := newPreferDaemonExecutor(router)

	res, err := executor.ExecuteTool(context.Background(), placementTestRequest("shell"))
	require.NoError(t, err)
	assert.Equal(t, []string{"shell"}, router.toolsSent)
	assert.Empty(t, res.ExecutedAt, "only prefer-daemon calls report a split")
	assert.NotContains(t, res.Metadata, tools.PlacementMetadataKey)
}

func mustMetadata(t *testing.T, res *ToolResult) map[string]any {
	t.Helper()
	var doc map[string]any
	require.NoError(t, json.Unmarshal([]byte(res.Metadata), &doc))
	return doc
}

// A prefer-daemon call that ends up on the server goes through the same egress
// admission as any server-run egress tool; one that ran on the daemon is the
// daemon's own business (see the PR's note on a per-daemon cap).
func TestPreferDaemon_ServerFallbackIsRateLimitedLikeAnyServerEgress(t *testing.T) {
	limiter := &countingLimiter{}
	router := &preferDaemonRouter{err: errors.New("no daemon connected for user")}
	executor, _ := newPreferDaemonExecutor(router)
	executor.SetEgressLimiter(limiter)

	_, err := executor.ExecuteTool(nomachine.With(context.Background()), fetchRequest())
	require.NoError(t, err)
	_, err = executor.ExecuteTool(context.Background(), fetchRequest())
	require.NoError(t, err)
	assert.Equal(t, 2, limiter.acquired, "no-machine and dispatch-failure fallbacks are both admitted by the limiter")

	limiter.acquired = 0
	daemonOK := &preferDaemonRouter{resp: daemonAnswer("from the daemon")}
	executor, _ = newPreferDaemonExecutor(daemonOK)
	executor.SetEgressLimiter(limiter)
	_, err = executor.ExecuteTool(context.Background(), fetchRequest())
	require.NoError(t, err)
	assert.Zero(t, limiter.acquired, "a call that ran on the daemon never touches the server's limiter")
}

type countingLimiter struct{ acquired int }

func (l *countingLimiter) Acquire(context.Context, egresslimit.Request) (func(), error) {
	l.acquired++
	return func() {}, nil
}
