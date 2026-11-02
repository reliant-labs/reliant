package daemonruntime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

const outboxSecret = "stg_live/secret+value&k=1"

// newShellDaemon is a daemon with a real shell, a real on-disk result outbox and
// a gateway session whose sends are captured.
func newShellDaemon(t *testing.T) (*daemonClient, string) {
	t.Helper()
	testkit.Slow(t, "runs a real shell against an on-disk result outbox")
	dir := filepath.Join(t.TempDir(), "outbox")
	local := toolexec.NewLocalToolExecutor(tools.NewToolsFactory(&tools.ToolsOptions{
		ShellPlatform: tools.ShellPlatformFromGOOS(runtime.GOOS),
	}))
	local.SetDaemonClient(daemon.NewLocalClient())

	d := newTestDaemonClient("daemon-mask", "user-mask")
	d.localExecutor = local
	d.backgroundByReq = make(map[string]string)
	useTestSession(d, 8)
	store, err := openOutboxStore(dir)
	require.NoError(t, err)
	d.results.restore(store)
	return d, dir
}

// shellRequest is exactly what the worker's RemoteRunExecutor sends for a run
// step: the command in ToolInput, the secret in its env, and the values to mask
// in the server-built context (never in ToolInput).
func maskedShellRequest(t *testing.T, requestID, command string, env map[string]string, mask []string) *reliantv1.ToolRequest {
	t.Helper()
	req := &toolexec.ToolRequest{
		ToolName: "shell", UserID: "user-mask", ChatID: "chat-mask", ProjectID: "proj", RunStep: true,
		ProjectPath: t.TempDir(), MaskValues: mask,
	}
	input, err := json.Marshal(map[string]any{"command": command, "env": env, "timeout": 20000})
	require.NoError(t, err)
	ctxJSON, err := json.Marshal(req.ContextMap())
	require.NoError(t, err)
	return &reliantv1.ToolRequest{
		RequestId: requestID, ToolCallId: requestID, ToolName: "shell",
		ToolInput: string(input), ContextJson: string(ctxJSON), TimeoutMs: 20000,
	}
}

func filesUnder(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	require.NoError(t, filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		require.NoError(t, err)
		out[p] = b
		return nil
	}))
	return out
}

// A command that echoes its secret environment: the daemon masks the result
// before the result outbox writes it to disk, and before it is sent. No byte on
// disk, and no message sent, holds the value.
func TestShellResultIsMaskedBeforeItReachesTheOutboxOrTheWire(t *testing.T) {
	d, outboxDir := newShellDaemon(t)
	req := maskedShellRequest(t, "req-masked", `echo "key=$TRADER_KEY"; echo "err=$TRADER_KEY" >&2; exit 3`,
		map[string]string{"TRADER_KEY": outboxSecret}, []string{outboxSecret})

	d.executeTool(nil, req)

	resp := toolResponse(t, d)
	assert.NotContains(t, resp.Content, outboxSecret, "the response sent to the gateway")
	assert.NotContains(t, resp.ErrorMessage, outboxSecret)
	assert.NotContains(t, resp.Metadata, outboxSecret)
	assert.Contains(t, resp.Content, "key=", "the command did run and its output survives, masked")

	files := filesUnder(t, outboxDir)
	var resultFiles int
	for name, data := range files {
		assert.NotContains(t, string(data), outboxSecret, "on disk: %s", filepath.Base(name))
		if strings.HasSuffix(name, resultFileExt) {
			resultFiles++
		}
	}
	require.Equal(t, 1, resultFiles, "the result was persisted (masked), so a restart can still answer it: %v", keys(files))

	// A restart reloads the persisted result: it is the masked one.
	store, err := openOutboxStore(outboxDir)
	require.NoError(t, err)
	reloaded := &resultOutbox{}
	reloaded.restore(store)
	held := reloaded.matching(func(*heldResult) bool { return true })
	require.Len(t, held, 1)
	assert.NotContains(t, held[0].msg.GetToolResponse().GetContent(), outboxSecret)
}

// Control: with no values to mask, the daemon returns what the command printed.
func TestShellResultWithNoMaskValuesIsUnchanged(t *testing.T) {
	d, _ := newShellDaemon(t)
	req := maskedShellRequest(t, "req-plain", `echo "visible=$TRADER_KEY"`, map[string]string{"TRADER_KEY": outboxSecret}, nil)
	d.executeTool(nil, req)
	assert.Contains(t, toolResponse(t, d).Content, `visible=stg_live/secret+value\u0026k=1`, "the shell tool JSON-encodes stdout")
}

// A user's "move to background" for a secret-bearing command is not honoured:
// adopted, its output would sit in the background manager's buffer, unmasked,
// for shell_output to read. The command finishes in the foreground and its
// (masked) result is the reply.
func TestSecretBearingCommandIsNotAdoptedIntoTheBackground(t *testing.T) {
	d, _ := newShellDaemon(t)
	req := maskedShellRequest(t, "req-adopt", `sleep 1; echo "key=$TRADER_KEY"`,
		map[string]string{"TRADER_KEY": outboxSecret}, []string{outboxSecret})
	// Asked before the command starts, so the first poll sees it.
	d.markToolExecutionBackgrounded(req.RequestId, req.ToolCallId)

	d.executeTool(nil, req)

	resp := toolResponse(t, d)
	assert.NotContains(t, resp.Content, outboxSecret)
	assert.Contains(t, resp.Content, "key=", "the command ran to completion in the foreground")
	assert.NotContains(t, resp.Content, "process_id", "the command was adopted into the background")
}

// A secret equal to a field name of the shell envelope masks the output but
// leaves the envelope, and so the exit code, intact across the wire.
func TestSecretNamedLikeAnEnvelopeFieldKeepsTheVerdict(t *testing.T) {
	d, _ := newShellDaemon(t)
	req := maskedShellRequest(t, "req-envelope", `echo "$K"; exit 3`,
		map[string]string{"K": "exit_code"}, []string{"exit_code", "stdout"})
	d.executeTool(nil, req)
	resp := toolResponse(t, d)
	var shell struct {
		Stdout   string `json:"stdout"`
		ExitCode int    `json:"exit_code"`
	}
	require.NoError(t, json.Unmarshal([]byte(resp.Content), &shell), resp.Content)
	assert.Equal(t, 3, shell.ExitCode, resp.Content)
	assert.NotContains(t, shell.Stdout, "exit_code")
}

// The mask values travel in the server-built context map only. A model cannot
// set them, or read them back, through the tool input.
func TestMaskValuesAreNotAToolInputField(t *testing.T) {
	d, _ := newShellDaemon(t)
	req := maskedShellRequest(t, "req-model", `echo ok`, nil, nil)
	req.ToolInput = `{"command":"echo ok","secret_mask":["ok"]}`
	d.executeTool(nil, req)
	resp := toolResponse(t, d)
	assert.True(t, resp.IsError, "the shell tool refuses a field it does not define: %s", resp.Content)
	assert.Contains(t, resp.Content, "secret_mask")
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, filepath.Base(k))
	}
	return out
}

// wireRouter carries a worker's request to the daemon the way the gateway does:
// the context map is JSON-encoded into the request, and the daemon's own
// executeTool answers it.
type wireRouter struct {
	toolexec.DaemonRouter
	d *daemonClient
	t *testing.T
}

func (r *wireRouter) SendToolRequestSync(_ context.Context, _ string, req *toolexec.ToolExecutionRequest) (*toolexec.ToolExecutionResponse, error) {
	ctxJSON, err := json.Marshal(req.Context)
	require.NoError(r.t, err)
	r.d.executeTool(nil, &reliantv1.ToolRequest{
		RequestId: req.RequestID, ToolCallId: req.ToolCallID, ToolName: req.ToolName,
		ToolInput: req.ToolInput, ContextJson: string(ctxJSON), TimeoutMs: int32(req.TimeoutMs),
	})
	resp := toolResponse(r.t, r.d)
	return &toolexec.ToolExecutionResponse{
		RequestID: resp.RequestId, Success: resp.Success, IsError: resp.IsError, Content: resp.Content,
		Metadata: resp.Metadata, ErrorMessage: resp.ErrorMessage, ErrorCode: resp.ErrorCode,
	}, nil
}

func (r *wireRouter) SendToolRequestSyncWithSelector(ctx context.Context, userID string, req *toolexec.ToolExecutionRequest, _ *toolexec.DaemonSelector) (*toolexec.ToolExecutionResponse, error) {
	return r.SendToolRequestSync(ctx, userID, req)
}

// End to end across the transport: the worker's RemoteExecutor builds the
// request for a run step (its secrets in the shell env, their values listed to
// mask), the wire carries it, the daemon runs a real shell that echoes the
// secret, and the stored outbox files and the worker's result are free of it.
func TestRunStepRequestOverTheWireNeverStoresAnEchoedSecret(t *testing.T) {
	d, outboxDir := newShellDaemon(t)
	local := toolexec.NewLocalToolExecutor(tools.NewToolsFactory(&tools.ToolsOptions{ShellPlatform: tools.ShellPlatformFromGOOS(runtime.GOOS)}))
	remote := toolexec.NewRemoteExecutor(&wireRouter{d: d, t: t})
	remote.SetServerExecutor(local)

	input, err := json.Marshal(map[string]any{
		"command": `echo "key=$TRADER_KEY"; echo "b64=$(printf %s "$TRADER_KEY" | base64)"`,
		"env":     map[string]string{"TRADER_KEY": outboxSecret}, "timeout": 20000,
	})
	require.NoError(t, err)
	res, err := remote.ExecuteTool(context.Background(), &toolexec.ToolRequest{
		ToolName: "shell", ToolInput: string(input), UserID: "user-mask", ChatID: "chat-mask", ProjectID: "proj",
		ProjectPath: t.TempDir(), RunStep: true, Timeout: 20 * time.Second,
		MaskValues: []string{outboxSecret},
	})
	require.NoError(t, err)

	assert.NotContains(t, res.Content, outboxSecret, "what the worker received")
	assert.NotContains(t, res.Content, base64.StdEncoding.EncodeToString([]byte(outboxSecret)), "its base64 form too")
	assert.Contains(t, res.Content, "key=")

	files := filesUnder(t, outboxDir)
	require.NotEmpty(t, files)
	for name, data := range files {
		assert.NotContains(t, string(data), outboxSecret, "stored: %s", filepath.Base(name))
		assert.NotContains(t, string(data), base64.StdEncoding.EncodeToString([]byte(outboxSecret)), "stored: %s", filepath.Base(name))
	}
}
