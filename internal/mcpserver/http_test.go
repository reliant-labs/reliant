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

	fat "github.com/reliant-labs/forge/pkg/accesstoken"

	"github.com/reliant-labs/reliant/internal/connectorgrant"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
)

// memStore is an in-memory grant store. The SQL store has its own integration
// tests against Postgres; these tests are about the HTTP and protocol layer.
type memStore struct {
	mu       sync.Mutex
	grants   map[string]*connectorgrant.Grant         // keyed by grant id
	bindings map[string]*connectorgrant.ClientBinding // keyed by userID|clientID
	audit    []*connectorgrant.AuditRecord
	auditC   chan struct{}

	// creds is the token authority connector credentials are minted in.
	creds *tokenauthority.Memory
}

func newMemStore() *memStore {
	return &memStore{
		grants:   map[string]*connectorgrant.Grant{},
		bindings: map[string]*connectorgrant.ClientBinding{},
		auditC:   make(chan struct{}, 16),
		creds:    tokenauthority.NewMemory(),
	}
}

// credentialFor mints the `rlat_` connector credential for grantID, exactly as
// ConnectorService does: mcp:connector, bound to connector:<grantID>, acting
// as the grant's owner.
func (m *memStore) credentialFor(t *testing.T, grantID, userID string) string {
	t.Helper()
	minted, err := m.creds.MintForUser(context.Background(), tokenauthority.MintRequest{
		UserID: userID, Name: "connector:" + grantID,
		Scopes:   []fat.Scope{fat.ScopeMCPConnector},
		Resource: &fat.Resource{Kind: fat.ResourceConnector, ID: grantID},
	})
	require.NoError(t, err)
	return minted.Plaintext
}

func (m *memStore) SetTokenPrefix(_ context.Context, id, prefix string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if g, ok := m.grants[id]; ok {
		g.TokenPrefix = prefix
	}
	return nil
}

func (m *memStore) CreateGrant(_ context.Context, g *connectorgrant.Grant) error {
	m.grants[g.ID] = g
	return nil
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
	raw := store.credentialFor(t, "grant-http", "user-1")

	require.NoError(t, store.CreateGrant(context.Background(), &connectorgrant.Grant{
		ID:           "grant-http",
		UserID:       "user-1",
		DaemonID:     "daemon-1",
		Name:         "test connector",
		AllowedTools: ReadOnlyToolNames(),
		PathRoot:     "/workspace",
		ExecMode:     connectorgrant.ExecDeny,
	}))

	handler, err := NewHTTPHandler(HTTPDeps{Store: store, Credentials: store.creds, Sender: sender})
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
	handler, err := NewHTTPHandler(HTTPDeps{Store: store, Credentials: store.creds, Sender: &fakeSender{}})
	require.NoError(t, err)

	unknown, err := fat.Mint()
	require.NoError(t, err)
	apiOnly, err := store.creds.MintForUser(context.Background(), tokenauthority.MintRequest{
		UserID: "user-1", Name: "api", Scopes: []fat.Scope{fat.ScopeReliantAPI},
	})
	require.NoError(t, err)
	// A live grant owned by user-1, and a credential bound to it but acting
	// as user-2: the binding alone must not be enough.
	require.NoError(t, store.CreateGrant(context.Background(), &connectorgrant.Grant{
		ID: "grant-other", UserID: "user-1", DaemonID: "daemon-1",
		AllowedTools: ReadOnlyToolNames(), PathRoot: "/workspace", ExecMode: connectorgrant.ExecDeny,
	}))

	srv := httptest.NewServer(handler)
	defer srv.Close()

	cases := []struct {
		name   string
		header string
	}{
		{"no credential", ""},
		{"not a bearer", "Basic abc123"},
		{"retired connector credential", "Bearer rlnt_conn_0000000000000000000000000000000000000000"},
		{"unknown access token", "Bearer " + unknown.Plaintext},
		{"access token without mcp:connector", "Bearer " + apiOnly.Plaintext},
		{"connector token for another user's grant", "Bearer " + store.credentialFor(t, "grant-other", "user-2")},
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
	raw := store.credentialFor(t, "grant-revoke", "user-1")

	grant := &connectorgrant.Grant{
		ID:           "grant-revoke",
		UserID:       "user-1",
		DaemonID:     "daemon-1",
		AllowedTools: ReadOnlyToolNames(),
		PathRoot:     "/workspace",
		ExecMode:     connectorgrant.ExecDeny,
	}
	require.NoError(t, store.CreateGrant(context.Background(), grant))

	handler, err := NewHTTPHandler(HTTPDeps{Store: store, Credentials: store.creds, Sender: &fakeSender{}})
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

// TestRevokedTokenStopsWorkingImmediately is the other half: the grant is
// still live, but its credential was revoked at the token authority (what
// RevokeConnector does via RevokeResource). The next request must fail.
func TestRevokedTokenStopsWorkingImmediately(t *testing.T) {
	store := newMemStore()
	require.NoError(t, store.CreateGrant(context.Background(), &connectorgrant.Grant{
		ID: "grant-token-revoke", UserID: "user-1", DaemonID: "daemon-1",
		AllowedTools: ReadOnlyToolNames(), PathRoot: "/workspace", ExecMode: connectorgrant.ExecDeny,
	}))
	raw := store.credentialFor(t, "grant-token-revoke", "user-1")

	handler, err := NewHTTPHandler(HTTPDeps{Store: store, Credentials: store.creds, Sender: &fakeSender{}})
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
	n, err := store.creds.RevokeResource(context.Background(),
		fat.Resource{Kind: fat.ResourceConnector, ID: "grant-token-revoke"})
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	require.Equal(t, http.StatusUnauthorized, doRequest(),
		"a credential revoked at the authority must stop working on the very next request")
}
