// Copyright (c) 2025 Reliant Labs

package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/connectorgrant"
)

// memStore is an in-memory grant store. The SQL store has its own integration
// tests against Postgres; these tests are about the HTTP and protocol layer.
type memStore struct {
	mu       sync.Mutex
	grants   map[string]*connectorgrant.Grant         // keyed by token hash
	bindings map[string]*connectorgrant.ClientBinding // keyed by userID|clientID
	audit    []*connectorgrant.AuditRecord
	auditC   chan struct{}
}

func newMemStore() *memStore {
	return &memStore{
		grants:   map[string]*connectorgrant.Grant{},
		bindings: map[string]*connectorgrant.ClientBinding{},
		auditC:   make(chan struct{}, 16),
	}
}

func (m *memStore) CreateGrant(_ context.Context, g *connectorgrant.Grant) error {
	m.grants[g.TokenHash] = g
	return nil
}

func (m *memStore) GetGrantByTokenHash(_ context.Context, hash string) (*connectorgrant.Grant, error) {
	g, ok := m.grants[hash]
	if !ok {
		return nil, connectorgrant.ErrNotFound
	}
	if err := g.IsLive(time.Now()); err != nil {
		return nil, connectorgrant.ErrNotFound
	}
	return g, nil
}

func (m *memStore) ListGrantsByUser(_ context.Context, userID string) ([]*connectorgrant.Grant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*connectorgrant.Grant
	for _, g := range m.grants {
		if g.UserID == userID {
			out = append(out, g)
		}
	}
	return out, nil
}
func (m *memStore) GetGrant(context.Context, string, string) (*connectorgrant.Grant, error) {
	return nil, connectorgrant.ErrNotFound
}

func (m *memStore) GetGrantByID(_ context.Context, id string) (*connectorgrant.Grant, error) {
	for _, g := range m.grants {
		if g.ID != id {
			continue
		}
		if err := g.IsLive(time.Now()); err != nil {
			return nil, err
		}
		return g, nil
	}
	return nil, connectorgrant.ErrNotFound
}
func (m *memStore) RevokeGrant(context.Context, string, string) (bool, error) { return false, nil }
func (m *memStore) TouchGrant(context.Context, string) error                  { return nil }

func (m *memStore) GetBinding(_ context.Context, userID, clientID string) (*connectorgrant.ClientBinding, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b, ok := m.bindings[userID+"|"+clientID]; ok {
		return b, nil
	}
	return nil, connectorgrant.ErrNotFound
}

func (m *memStore) PutBinding(_ context.Context, b *connectorgrant.ClientBinding) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bindings[b.UserID+"|"+b.ClientID] = b
	return nil
}

func (m *memStore) DeleteBinding(_ context.Context, userID, clientID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := userID + "|" + clientID
	_, existed := m.bindings[key]
	delete(m.bindings, key)
	return existed, nil
}

func (m *memStore) ListBindingsByUser(_ context.Context, userID string) ([]*connectorgrant.ClientBinding, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*connectorgrant.ClientBinding
	for _, b := range m.bindings {
		if b.UserID == userID {
			out = append(out, b)
		}
	}
	return out, nil
}

func (m *memStore) RecordAudit(_ context.Context, rec *connectorgrant.AuditRecord) error {
	m.mu.Lock()
	m.audit = append(m.audit, rec)
	m.mu.Unlock()
	select {
	case m.auditC <- struct{}{}:
	default:
	}
	return nil
}

func (m *memStore) CompleteAudit(_ context.Context, id, status, errMsg string, durationMS int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, rec := range m.audit {
		if rec.ID == id && rec.Status == connectorgrant.AuditStarted {
			rec.Status = status
			rec.Denied = status == connectorgrant.AuditDenied
			rec.ErrorMsg = errMsg
			rec.DurationMS = durationMS
		}
	}
	select {
	case m.auditC <- struct{}{}:
	default:
	}
	return nil
}

// records returns a snapshot of the audit log.
func (m *memStore) records() []*connectorgrant.AuditRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*connectorgrant.AuditRecord, len(m.audit))
	copy(out, m.audit)
	return out
}

func (m *memStore) ListAuditByUser(context.Context, string, int) ([]*connectorgrant.AuditRecord, error) {
	return m.audit, nil
}
func (m *memStore) ListAuditByGrant(context.Context, string, string, int) ([]*connectorgrant.AuditRecord, error) {
	return m.audit, nil
}

// waitForAudit waits for an audit row, since recording is detached from the
// request and so may land after the response.
func (m *memStore) waitForAudit(t *testing.T) {
	t.Helper()
	select {
	case <-m.auditC:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for an audit record")
	}
}

// newTestHTTPServer starts the MCP endpoint over a read-only grant and returns
// its URL plus the credential.
func newTestHTTPServer(t *testing.T, sender CommandSender) (string, string, *memStore) {
	t.Helper()

	store := newMemStore()
	raw, hash, prefix, err := connectorgrant.GenerateCredential()
	require.NoError(t, err)

	require.NoError(t, store.CreateGrant(context.Background(), &connectorgrant.Grant{
		ID:           "grant-http",
		UserID:       "user-1",
		DaemonID:     "daemon-1",
		Name:         "test connector",
		TokenHash:    hash,
		TokenPrefix:  prefix,
		AllowedTools: ReadOnlyToolNames(),
		PathRoot:     "/workspace",
		ExecMode:     connectorgrant.ExecDeny,
	}))

	handler, err := NewHTTPHandler(HTTPDeps{Store: store, Sender: sender})
	require.NoError(t, err)

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	return srv.URL, raw, store
}

// connectClient dials the endpoint with a real MCP client.
func connectClient(t *testing.T, url, credential string) *mcp.ClientSession {
	t.Helper()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	transport := &mcp.StreamableClientTransport{
		// The real mount path: the handler routes /mcp (streamable) and /sse
		// (legacy) separately, so a bare origin is not an MCP endpoint.
		Endpoint:   url + MountPath,
		HTTPClient: &http.Client{Transport: &bearerTransport{token: credential}},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)

	sess, err := client.Connect(ctx, transport, nil)
	require.NoError(t, err, "MCP client could not connect")
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

// bearerTransport attaches the connector credential, as a real client would.
type bearerTransport struct {
	token string
}

func (b *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(clone)
}

// TestMCPHandshakeAndToolList is the end-to-end proof: a real MCP client
// completes initialize and sees the granted tools.
func TestMCPHandshakeAndToolList(t *testing.T) {
	url, cred, _ := newTestHTTPServer(t, &fakeSender{})
	sess := connectClient(t, url, cred)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res, err := sess.ListTools(ctx, nil)
	require.NoError(t, err)
	require.NotEmpty(t, res.Tools)

	names := map[string]bool{}
	for _, tool := range res.Tools {
		names[tool.Name] = true
		require.NotEmpty(t, tool.Description, "tool %q has no description for the model", tool.Name)
	}

	require.True(t, names["read_file"], "read-only grant should expose read_file")
	require.True(t, names["search"])
	// A read-only grant must not even advertise mutating tools.
	require.False(t, names["write_file"], "read-only grant must not advertise write_file")
	require.False(t, names["run_command"])
}

// TestMCPToolCallReachesDaemon runs a full tools/call through the protocol.
func TestMCPToolCallReachesDaemon(t *testing.T) {
	sender := &fakeSender{response: []byte(`{"content":"file contents here"}`)}
	url, cred, store := newTestHTTPServer(t, sender)
	sess := connectClient(t, url, cred)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name:      "read_file",
		Arguments: map[string]any{"path": "/workspace/README.md"},
	})
	require.NoError(t, err)
	require.False(t, res.IsError, "tool call failed: %v", res.Content)

	require.Len(t, sender.calls, 1)
	require.Equal(t, "fs.read_file", sender.calls[0].command)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(sender.calls[0].payload, &payload))
	require.Equal(t, "/workspace/README.md", payload["path"])

	store.waitForAudit(t)
	require.Len(t, store.audit, 1)
	require.False(t, store.audit[0].Denied)
	require.Equal(t, "read_file", store.audit[0].ToolName)
}

// TestMCPDeniedCallIsAudited proves a refusal reaches the client as a tool
// error and is recorded.
func TestMCPDeniedCallIsAudited(t *testing.T) {
	sender := &fakeSender{}
	url, cred, store := newTestHTTPServer(t, sender)
	sess := connectClient(t, url, cred)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name:      "read_file",
		Arguments: map[string]any{"path": "/etc/passwd"},
	})
	// A policy refusal is a tool-level error, not a protocol error: the model
	// must see it and adapt rather than have the session fail.
	require.NoError(t, err)
	require.True(t, res.IsError)
	require.Empty(t, sender.calls, "a denied call must never reach the daemon")

	store.waitForAudit(t)
	require.Len(t, store.audit, 1)
	require.True(t, store.audit[0].Denied)
	require.Contains(t, string(store.audit[0].Arguments), "/etc/passwd")
}

func TestUnauthenticatedRequestsRejected(t *testing.T) {
	store := newMemStore()
	handler, err := NewHTTPHandler(HTTPDeps{Store: store, Sender: &fakeSender{}})
	require.NoError(t, err)

	srv := httptest.NewServer(handler)
	defer srv.Close()

	cases := []struct {
		name   string
		header string
	}{
		{"no credential", ""},
		{"not a bearer", "Basic abc123"},
		{"wrong credential kind", "Bearer rlnt_pat_thisisapatnotaconnector"},
		{"unknown connector credential", "Bearer rlnt_conn_0000000000000000000000000000000000000000"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, srv.URL+MountPath, nil)
			require.NoError(t, err)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}

			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()

			require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
			require.NotEmpty(t, resp.Header.Get("WWW-Authenticate"),
				"clients need the auth scheme advertised to know how to authenticate")
		})
	}
}

// TestRevokedCredentialStopsWorkingImmediately is why authentication happens
// per request rather than once per session.
func TestRevokedCredentialStopsWorkingImmediately(t *testing.T) {
	store := newMemStore()
	raw, hash, prefix, err := connectorgrant.GenerateCredential()
	require.NoError(t, err)

	grant := &connectorgrant.Grant{
		ID:           "grant-revoke",
		UserID:       "user-1",
		DaemonID:     "daemon-1",
		TokenHash:    hash,
		TokenPrefix:  prefix,
		AllowedTools: ReadOnlyToolNames(),
		PathRoot:     "/workspace",
		ExecMode:     connectorgrant.ExecDeny,
	}
	require.NoError(t, store.CreateGrant(context.Background(), grant))

	handler, err := NewHTTPHandler(HTTPDeps{Store: store, Sender: &fakeSender{}})
	require.NoError(t, err)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	doRequest := func() int {
		req, err := http.NewRequest(http.MethodPost, srv.URL+MountPath, nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+raw)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode
	}

	require.NotEqual(t, http.StatusUnauthorized, doRequest(), "credential should work before revocation")

	now := time.Now()
	grant.RevokedAt = &now

	require.Equal(t, http.StatusUnauthorized, doRequest(),
		"a revoked credential must stop working on the very next request")
}

func TestNewHTTPHandlerRequiresDeps(t *testing.T) {
	_, err := NewHTTPHandler(HTTPDeps{Sender: &fakeSender{}})
	require.Error(t, err, "a handler without a grant store must not be constructed")

	_, err = NewHTTPHandler(HTTPDeps{Store: newMemStore()})
	require.Error(t, err, "a handler without a command sender must not be constructed")
}
