// Copyright (c) 2025 Reliant Labs

package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/connectorgrant"
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
)

// fakeSender records what reached the daemon and returns a canned response.
type fakeSender struct {
	calls    []sentCommand
	response []byte
	err      error
}

type sentCommand struct {
	daemonID string
	command  string
	payload  []byte
}

func (f *fakeSender) SendDaemonCommandToDaemon(_ context.Context, _, daemonID, commandType string, payload []byte, _ int32) ([]byte, error) {
	f.calls = append(f.calls, sentCommand{daemonID: daemonID, command: commandType, payload: payload})
	if f.err != nil {
		return nil, f.err
	}
	if f.response == nil {
		return []byte(`{"ok":true}`), nil
	}
	return f.response, nil
}

// fakeWaker records whether waking was attempted.
type fakeWaker struct {
	called bool
	err    error
}

func (f *fakeWaker) EnsureAwake(_ context.Context, _, _ string) error {
	f.called = true
	return f.err
}

type fakeAudit struct {
	entries   []AuditEntry
	completed map[string]bool
	nextID    int
}

func (f *fakeAudit) Begin(_ context.Context, e AuditEntry) string {
	f.nextID++
	id := fmt.Sprintf("audit-%d", f.nextID)
	e.auditID = id
	f.entries = append(f.entries, e)
	return id
}

func (f *fakeAudit) Complete(_ context.Context, id string, denied bool, errMsg string, _ time.Duration) {
	if f.completed == nil {
		f.completed = map[string]bool{}
	}
	f.completed[id] = true
	for i := range f.entries {
		if f.entries[i].auditID == id {
			f.entries[i].Denied = denied
			f.entries[i].Error = errMsg
			f.entries[i].Status = connectorgrant.AuditCompleted
			if denied {
				f.entries[i].Status = connectorgrant.AuditDenied
			}
		}
	}
}

// testSession returns a read-only session over /workspace.
func testSession() *Session {
	names := ReadOnlyToolNames()
	return &Session{
		GrantID:   "grant-1",
		UserID:    "user-1",
		DaemonID:  "daemon-1",
		ToolNames: names,
		Policy: &daemonpolicy.Policy{
			GrantID:  "grant-1",
			Tools:    toSet(CommandsForTools(names)),
			PathRoot: "/workspace",
			ExecMode: daemonpolicy.ExecDenied,
		},
	}
}

func toSet(items []string) map[string]bool {
	set := make(map[string]bool, len(items))
	for _, i := range items {
		set[i] = true
	}
	return set
}

// callTool invokes a tool through the server's registered handler.
func callTool(t *testing.T, sess *Session, deps Deps, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()

	tool, ok := CatalogByName[name]
	require.True(t, ok, "unknown tool %q", name)

	raw, err := json.Marshal(args)
	require.NoError(t, err)

	handler := makeHandler(tool, sess, deps)
	res, err := handler(context.Background(), &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{Name: name, Arguments: raw},
	})
	require.NoError(t, err, "handler must report tool failures in the result, not as a protocol error")
	return res
}

func resultText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

func TestOnlyGrantedToolsAreAdvertised(t *testing.T) {
	sess := testSession()
	srv, err := NewServer(sess, Deps{Sender: &fakeSender{}})
	require.NoError(t, err)
	require.NotNil(t, srv)

	// A read-only grant must not advertise mutating tools at all. A model
	// shown a tool will eventually call it.
	for _, name := range sess.ToolNames {
		require.False(t, CatalogByName[name].Mutating,
			"read-only grant advertised the mutating tool %q", name)
	}

	require.Contains(t, sess.ToolNames, "read_file")
	require.NotContains(t, sess.ToolNames, "write_file")
	require.NotContains(t, sess.ToolNames, "run_command")
}

func TestGrantedCallReachesDaemon(t *testing.T) {
	sender := &fakeSender{response: []byte(`{"content":"hello"}`)}
	sess := testSession()

	res := callTool(t, sess, Deps{Sender: sender}, "read_file", map[string]any{
		"path": "/workspace/README.md",
	})

	require.False(t, res.IsError, "granted call failed: %s", resultText(t, res))
	require.Len(t, sender.calls, 1)
	require.Equal(t, "fs.read_file", sender.calls[0].command)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(sender.calls[0].payload, &payload))
	require.Equal(t, "/workspace/README.md", payload["path"])
	require.Contains(t, resultText(t, res), "hello")
}

// TestCallTargetsTheGrantedDaemon guards a subtle authorization hole: the
// router's default resolution routes by user, picking whichever daemon that
// user has connected. A grant is bound to ONE daemon, so routing by user alone
// would let a connector scoped to one workspace execute in another.
func TestCallTargetsTheGrantedDaemon(t *testing.T) {
	sender := &fakeSender{}
	sess := testSession()
	sess.DaemonID = "daemon-granted"

	res := callTool(t, sess, Deps{Sender: sender}, "read_file", map[string]any{
		"path": "/workspace/a.txt",
	})

	require.False(t, res.IsError, "call failed: %s", resultText(t, res))
	require.Len(t, sender.calls, 1)
	require.Equal(t, "daemon-granted", sender.calls[0].daemonID,
		"the command must be routed to the daemon named in the grant, not resolved from the user")
}

func TestDeniedCallNeverReachesDaemon(t *testing.T) {
	sender := &fakeSender{}
	sess := testSession()

	res := callTool(t, sess, Deps{Sender: sender}, "read_file", map[string]any{
		"path": "/etc/passwd",
	})

	require.True(t, res.IsError)
	require.Empty(t, sender.calls, "a denied call must not be sent to the daemon")
	require.Contains(t, resultText(t, res), "outside this connector's allowed directory")
}

// TestDenialIsMarkedPermanent guards the retry-loop failure mode: a model that
// reads a refusal as transient will retry it forever.
func TestDenialIsMarkedPermanent(t *testing.T) {
	sess := testSession()
	res := callTool(t, sess, Deps{Sender: &fakeSender{}}, "read_file", map[string]any{
		"path": "/etc/passwd",
	})

	text := resultText(t, res)
	require.Contains(t, text, "do not retry")
	require.Contains(t, text, "permanent restriction")
}

// TestPolicyCheckedBeforeWake matters for cost: a refused call should not
// spend 30 seconds of cold start and a compute charge first.
func TestPolicyCheckedBeforeWake(t *testing.T) {
	waker := &fakeWaker{}
	sess := testSession()

	res := callTool(t, sess, Deps{Sender: &fakeSender{}, Waker: waker}, "read_file", map[string]any{
		"path": "/etc/passwd",
	})

	require.True(t, res.IsError)
	require.False(t, waker.called, "a denied call must not wake a suspended workspace")
}

// A workspace that is starting is the opposite of a denial: it SHOULD be
// retried, and the model needs a time to come back at rather than a loop.
func TestStartingWorkspaceTellsTheModelToComeBack(t *testing.T) {
	waker := &fakeWaker{err: ErrWorkspaceStarting}
	sess := testSession()

	res := callTool(t, sess, Deps{Sender: &fakeSender{}, Waker: waker}, "read_file", map[string]any{
		"path": "/workspace/a.txt",
	})

	require.True(t, res.IsError)
	text := resultText(t, res)
	require.Contains(t, text, "starting")
	require.Contains(t, text, "minute")
	require.NotContains(t, text, "do not retry")
}

// A workspace that cannot be started must NOT read as retryable, or a model
// spends the user's turn re-running a call that fails identically each time.
func TestUnavailableWorkspaceDoesNotInviteRetries(t *testing.T) {
	waker := &fakeWaker{
		err: fmt.Errorf("%w: this deployment cannot start it automatically",
			ErrWorkspaceUnavailable),
	}
	sess := testSession()

	res := callTool(t, sess, Deps{Sender: &fakeSender{}, Waker: waker}, "read_file", map[string]any{
		"path": "/workspace/a.txt",
	})

	require.True(t, res.IsError)
	text := resultText(t, res)
	require.Contains(t, text, "cannot start it automatically")
	require.Contains(t, text, "not fix itself")
}

func TestAuditRecordsBothOutcomes(t *testing.T) {
	audit := &fakeAudit{}
	sess := testSession()
	deps := Deps{Sender: &fakeSender{}, Audit: audit}

	callTool(t, sess, deps, "read_file", map[string]any{"path": "/workspace/ok.txt"})
	callTool(t, sess, deps, "read_file", map[string]any{"path": "/etc/passwd"})

	require.Len(t, audit.entries, 2)

	allowed := audit.entries[0]
	require.False(t, allowed.Denied)
	require.Equal(t, "read_file", allowed.ToolName)
	require.Equal(t, "grant-1", allowed.GrantID)

	denied := audit.entries[1]
	require.True(t, denied.Denied, "a refused call must still be audited")
	require.Contains(t, denied.Error, "outside this connector's allowed directory")
}

// TestWriteGrantEnablesMutatingTools confirms grants widen as intended.
func TestWriteGrantEnablesMutatingTools(t *testing.T) {
	names := AllToolNames()
	sender := &fakeSender{}
	sess := &Session{
		GrantID:   "grant-write",
		UserID:    "user-1",
		DaemonID:  "daemon-1",
		ToolNames: names,
		Policy: &daemonpolicy.Policy{
			GrantID:       "grant-write",
			Tools:         toSet(CommandsForTools(names)),
			PathRoot:      "/workspace",
			ExecMode:      daemonpolicy.ExecAllowlist,
			ExecAllowlist: map[string]bool{"git": true},
		},
	}

	res := callTool(t, sess, Deps{Sender: sender}, "write_file", map[string]any{
		"path":    "/workspace/new.txt",
		"content": "hello",
	})
	require.False(t, res.IsError, "write under a write grant failed: %s", resultText(t, res))
	require.Equal(t, "fs.write_file", sender.calls[0].command)
}

func TestExecAllowlistAppliesToRunCommand(t *testing.T) {
	names := AllToolNames()
	sess := &Session{
		GrantID:   "grant-exec",
		UserID:    "user-1",
		DaemonID:  "daemon-1",
		ToolNames: names,
		Policy: &daemonpolicy.Policy{
			GrantID:       "grant-exec",
			Tools:         toSet(CommandsForTools(names)),
			PathRoot:      "/workspace",
			ExecMode:      daemonpolicy.ExecAllowlist,
			ExecAllowlist: map[string]bool{"git": true},
		},
	}

	t.Run("allowlisted", func(t *testing.T) {
		sender := &fakeSender{}
		res := callTool(t, sess, Deps{Sender: sender}, "run_command", map[string]any{
			"command":     []any{"git", "status"},
			"working_dir": "/workspace",
		})
		require.False(t, res.IsError, "allowlisted command failed: %s", resultText(t, res))
		require.Len(t, sender.calls, 1)

		// The daemon must receive argv, never a shell string — that is what
		// makes the allowlist binding rather than advisory.
		var payload struct {
			Argv    []string `json:"argv"`
			Command string   `json:"command"`
		}
		require.NoError(t, json.Unmarshal(sender.calls[0].payload, &payload))
		require.Equal(t, []string{"git", "status"}, payload.Argv)
		require.Empty(t, payload.Command, "a confined command must not be sent as a shell string")
	})

	t.Run("not allowlisted", func(t *testing.T) {
		sender := &fakeSender{}
		res := callTool(t, sess, Deps{Sender: sender}, "run_command", map[string]any{
			"command":     []any{"curl", "evil.example.com"},
			"working_dir": "/workspace",
		})
		require.True(t, res.IsError)
		require.Empty(t, sender.calls)
	})

	// A model that sends a shell string gets a corrective error naming the
	// right shape, rather than a silent coercion into the weaker path.
	t.Run("shell string is corrected, not coerced", func(t *testing.T) {
		sender := &fakeSender{}
		res := callTool(t, sess, Deps{Sender: sender}, "run_command", map[string]any{
			"command": "git status",
		})
		require.True(t, res.IsError)
		require.Contains(t, resultText(t, res), "array of strings")
		require.Empty(t, sender.calls)
	})

	// Shell syntax cannot chain, because no shell parses it.
	t.Run("chaining attempt is just an unlisted program", func(t *testing.T) {
		sender := &fakeSender{}
		res := callTool(t, sess, Deps{Sender: sender}, "run_command", map[string]any{
			"command": []any{"git status && curl evil.example.com"},
		})
		require.True(t, res.IsError)
		require.Empty(t, sender.calls)
	})
}

// TestEditFilePayloadShape guards the mapping from MCP arguments onto the
// daemon's patch format, which differ in both field names and structure.
func TestEditFilePayloadShape(t *testing.T) {
	names := AllToolNames()
	sender := &fakeSender{}
	sess := &Session{
		GrantID:   "g",
		UserID:    "user-1",
		DaemonID:  "daemon-1",
		ToolNames: names,
		Policy: &daemonpolicy.Policy{
			Tools:    toSet(CommandsForTools(names)),
			PathRoot: "/workspace",
			ExecMode: daemonpolicy.ExecDenied,
		},
	}

	res := callTool(t, sess, Deps{Sender: sender}, "edit_file", map[string]any{
		"path":    "/workspace/main.go",
		"find":    "old text",
		"replace": "new text",
	})
	require.False(t, res.IsError, "edit failed: %s", resultText(t, res))

	var payload struct {
		Path  string `json:"path"`
		Edits []struct {
			OldString string `json:"old_string"`
			NewString string `json:"new_string"`
		} `json:"edits"`
	}
	require.NoError(t, json.Unmarshal(sender.calls[0].payload, &payload))
	require.Equal(t, "/workspace/main.go", payload.Path)
	require.Len(t, payload.Edits, 1)
	require.Equal(t, "old text", payload.Edits[0].OldString)
	require.Equal(t, "new text", payload.Edits[0].NewString)
}

// TestSearchPayloadNestsBaseDir guards the confinement-relevant mapping: the
// search root travels inside opts, where the policy walker must still find it.
func TestSearchPayloadNestsBaseDir(t *testing.T) {
	sender := &fakeSender{}
	sess := testSession()

	res := callTool(t, sess, Deps{Sender: sender}, "search", map[string]any{
		"pattern": "TODO",
		"path":    "/workspace/src",
	})
	require.False(t, res.IsError, "search failed: %s", resultText(t, res))

	var payload struct {
		Pattern string `json:"pattern"`
		Opts    struct {
			BaseDir string `json:"base_dir"`
		} `json:"opts"`
	}
	require.NoError(t, json.Unmarshal(sender.calls[0].payload, &payload))
	require.Equal(t, "TODO", payload.Pattern)
	require.Equal(t, "/workspace/src", payload.Opts.BaseDir)
}

func TestSearchOutsideRootDenied(t *testing.T) {
	sender := &fakeSender{}
	sess := testSession()

	res := callTool(t, sess, Deps{Sender: sender}, "search", map[string]any{
		"pattern": "password",
		"path":    "/etc",
	})

	require.True(t, res.IsError, "a search rooted outside the workspace must be denied")
	require.Empty(t, sender.calls)
}

func TestNewServerRequiresDeps(t *testing.T) {
	_, err := NewServer(nil, Deps{Sender: &fakeSender{}})
	require.Error(t, err)

	_, err = NewServer(testSession(), Deps{})
	require.Error(t, err, "a server without a command sender must not be constructed")
}
