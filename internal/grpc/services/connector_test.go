// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/connectorgrant"
)

// stubStore is an in-memory grant store. The SQL store has its own Postgres
// integration tests; these cover the service's validation and scoping.
type stubStore struct {
	created []*connectorgrant.Grant
	byUser  map[string][]*connectorgrant.Grant
	audit   []*connectorgrant.AuditRecord

	bindings map[string]*connectorgrant.ClientBinding

	revokedUser string
	revokedID   string
	revokeOK    bool
}

func newStubStore() *stubStore {
	return &stubStore{
		byUser:   map[string][]*connectorgrant.Grant{},
		bindings: map[string]*connectorgrant.ClientBinding{},
	}
}

func (s *stubStore) CreateGrant(_ context.Context, g *connectorgrant.Grant) error {
	s.created = append(s.created, g)
	s.byUser[g.UserID] = append(s.byUser[g.UserID], g)
	return nil
}

func (s *stubStore) GetGrantByTokenHash(context.Context, string) (*connectorgrant.Grant, error) {
	return nil, connectorgrant.ErrNotFound
}

func (s *stubStore) ListGrantsByUser(_ context.Context, userID string) ([]*connectorgrant.Grant, error) {
	return s.byUser[userID], nil
}

func (s *stubStore) GetGrant(context.Context, string, string) (*connectorgrant.Grant, error) {
	return nil, connectorgrant.ErrNotFound
}

func (s *stubStore) GetGrantByID(context.Context, string) (*connectorgrant.Grant, error) {
	return nil, connectorgrant.ErrNotFound
}

func (s *stubStore) RevokeGrant(_ context.Context, userID, id string) (bool, error) {
	s.revokedUser, s.revokedID = userID, id
	return s.revokeOK, nil
}

func (s *stubStore) TouchGrant(context.Context, string) error { return nil }

func (s *stubStore) RecordAudit(_ context.Context, rec *connectorgrant.AuditRecord) error {
	s.audit = append(s.audit, rec)
	return nil
}

func (s *stubStore) GetBinding(_ context.Context, userID, clientID string) (*connectorgrant.ClientBinding, error) {
	if b, ok := s.bindings[userID+"|"+clientID]; ok {
		return b, nil
	}
	return nil, connectorgrant.ErrNotFound
}

func (s *stubStore) PutBinding(_ context.Context, b *connectorgrant.ClientBinding) error {
	if s.bindings == nil {
		s.bindings = map[string]*connectorgrant.ClientBinding{}
	}
	s.bindings[b.UserID+"|"+b.ClientID] = b
	return nil
}

func (s *stubStore) DeleteBinding(_ context.Context, userID, clientID string) (bool, error) {
	key := userID + "|" + clientID
	_, existed := s.bindings[key]
	delete(s.bindings, key)
	return existed, nil
}

func (s *stubStore) ListBindingsByUser(_ context.Context, userID string) ([]*connectorgrant.ClientBinding, error) {
	var out []*connectorgrant.ClientBinding
	for _, b := range s.bindings {
		if b.UserID == userID {
			out = append(out, b)
		}
	}
	return out, nil
}

func (s *stubStore) CompleteAudit(context.Context, string, string, string, int) error { return nil }

func (s *stubStore) ListAuditByUser(context.Context, string, int) ([]*connectorgrant.AuditRecord, error) {
	return s.audit, nil
}

func (s *stubStore) ListAuditByGrant(context.Context, string, string, int) ([]*connectorgrant.AuditRecord, error) {
	return s.audit, nil
}

// connectorCtx returns a context authenticated as userID. Distinct from the
// package's authedCtx helper because these tests need several users to verify
// that connectors are scoped to their owner.
func connectorCtx(userID string) context.Context {
	return context.WithValue(context.Background(), auth.UserIDContextKey, userID)
}

func validCreateRequest() *reliantv1.CreateConnectorRequest {
	return &reliantv1.CreateConnectorRequest{
		DaemonId:     "daemon-1",
		Name:         "ChatGPT on my phone",
		AllowedTools: []string{"read_file", "search"},
		PathRoot:     "/workspace",
		ExecMode:     reliantv1.ConnectorExecMode_CONNECTOR_EXEC_MODE_DENY,
	}
}

func TestCreateConnectorReturnsCredentialOnce(t *testing.T) {
	store := newStubStore()
	svc := NewConnectorService(store, "https://api.example.com")

	res, err := svc.CreateConnector(connectorCtx("user-1"), connect.NewRequest(validCreateRequest()))
	require.NoError(t, err)

	require.True(t, connectorgrant.IsCredentialFormat(res.Msg.GetCredential()))
	require.Equal(t, "https://api.example.com/mcp", res.Msg.GetMcpUrl())

	// Only the hash is persisted; the raw credential must never be stored.
	require.Len(t, store.created, 1)
	stored := store.created[0]
	require.NotEmpty(t, stored.TokenHash)
	require.NotEqual(t, res.Msg.GetCredential(), stored.TokenHash)
	require.Equal(t, connectorgrant.HashCredential(res.Msg.GetCredential()), stored.TokenHash)

	// The listed form carries the prefix but no credential material.
	require.Equal(t, stored.TokenPrefix, res.Msg.GetConnector().GetTokenPrefix())
	require.Equal(t, "user-1", stored.UserID)
}

func TestCreateConnectorRequiresAuth(t *testing.T) {
	svc := NewConnectorService(newStubStore(), "")

	_, err := svc.CreateConnector(context.Background(), connect.NewRequest(validCreateRequest()))
	require.Error(t, err)
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestCreateConnectorValidation(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*reliantv1.CreateConnectorRequest)
		reason string
	}{
		{
			name:   "no daemon binding",
			mutate: func(r *reliantv1.CreateConnectorRequest) { r.DaemonId = "" },
			reason: "a connector must name exactly one workspace",
		},
		{
			name:   "no tools",
			mutate: func(r *reliantv1.CreateConnectorRequest) { r.AllowedTools = nil },
			reason: "an empty tool list must not mean everything",
		},
		{
			name:   "unknown tool",
			mutate: func(r *reliantv1.CreateConnectorRequest) { r.AllowedTools = []string{"read_file", "rm_rf"} },
			reason: "silently dropping an unknown tool would create a quietly narrower grant",
		},
		{
			name:   "no path root",
			mutate: func(r *reliantv1.CreateConnectorRequest) { r.PathRoot = "" },
			reason: "an empty root must not mean unrestricted",
		},
		{
			name:   "relative path root",
			mutate: func(r *reliantv1.CreateConnectorRequest) { r.PathRoot = "workspace" },
			reason: "a relative root resolves against an unpredictable working directory",
		},
		{
			name:   "glob path root",
			mutate: func(r *reliantv1.CreateConnectorRequest) { r.PathRoot = "*" },
			reason: "the root is a prefix, not a pattern — \"*\" would otherwise look like a way to grant everything",
		},
		{
			name: "allowlist mode with no commands",
			mutate: func(r *reliantv1.CreateConnectorRequest) {
				r.AllowedTools = []string{"run_command"}
				r.ExecMode = reliantv1.ConnectorExecMode_CONNECTOR_EXEC_MODE_ALLOWLIST
				r.ExecAllowlist = nil
			},
			reason: "allowlist mode with no entries denies everything while claiming to permit something",
		},
		{
			name: "shell tool without exec mode",
			mutate: func(r *reliantv1.CreateConnectorRequest) {
				r.AllowedTools = []string{"read_file", "run_command"}
				r.ExecMode = reliantv1.ConnectorExecMode_CONNECTOR_EXEC_MODE_DENY
			},
			reason: "granting a shell tool while denying exec would refuse every call to it",
		},
		{
			name: "expiry in the past",
			mutate: func(r *reliantv1.CreateConnectorRequest) {
				past := time.Now().Add(-time.Hour).Format(time.RFC3339)
				r.ExpiresAt = &past
			},
			reason: "a grant that is born expired is a configuration mistake",
		},
		{
			name: "malformed expiry",
			mutate: func(r *reliantv1.CreateConnectorRequest) {
				bad := "next tuesday"
				r.ExpiresAt = &bad
			},
			reason: "an unparseable expiry must not silently become no expiry",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newStubStore()
			svc := NewConnectorService(store, "")

			req := validCreateRequest()
			tc.mutate(req)

			_, err := svc.CreateConnector(connectorCtx("user-1"), connect.NewRequest(req))
			require.Error(t, err, tc.reason)
			require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
			require.Empty(t, store.created, "an invalid request must not persist a grant")
		})
	}
}

func TestCreateConnectorAcceptsShellWithAllowlist(t *testing.T) {
	store := newStubStore()
	svc := NewConnectorService(store, "")

	req := validCreateRequest()
	req.AllowedTools = []string{"read_file", "run_command"}
	req.ExecMode = reliantv1.ConnectorExecMode_CONNECTOR_EXEC_MODE_ALLOWLIST
	req.ExecAllowlist = []string{"git", "go", "git"}

	_, err := svc.CreateConnector(connectorCtx("user-1"), connect.NewRequest(req))
	require.NoError(t, err)

	require.Len(t, store.created, 1)
	require.Equal(t, connectorgrant.ExecAllowlist, store.created[0].ExecMode)
	// Duplicates are collapsed rather than stored twice.
	require.ElementsMatch(t, []string{"git", "go"}, store.created[0].ExecAllowlist)
}

// TestCreateConnectorDropsAllowlistWhenUnused keeps stored grants honest: an
// allowlist that does not apply should not sit in the record implying it does.
func TestCreateConnectorDropsAllowlistWhenUnused(t *testing.T) {
	store := newStubStore()
	svc := NewConnectorService(store, "")

	req := validCreateRequest()
	req.ExecMode = reliantv1.ConnectorExecMode_CONNECTOR_EXEC_MODE_DENY
	req.ExecAllowlist = []string{"git"}

	_, err := svc.CreateConnector(connectorCtx("user-1"), connect.NewRequest(req))
	require.NoError(t, err)
	require.Empty(t, store.created[0].ExecAllowlist)
}

func TestUnspecifiedExecModeDeniesShell(t *testing.T) {
	store := newStubStore()
	svc := NewConnectorService(store, "")

	req := validCreateRequest()
	req.ExecMode = reliantv1.ConnectorExecMode_CONNECTOR_EXEC_MODE_UNSPECIFIED

	_, err := svc.CreateConnector(connectorCtx("user-1"), connect.NewRequest(req))
	require.NoError(t, err)
	require.Equal(t, connectorgrant.ExecDeny, store.created[0].ExecMode,
		"an unset exec mode must resolve to deny, not to unrestricted")
}

func TestRevokeConnectorIsScopedToCaller(t *testing.T) {
	store := newStubStore()
	store.revokeOK = true
	svc := NewConnectorService(store, "")

	res, err := svc.RevokeConnector(connectorCtx("user-1"),
		connect.NewRequest(&reliantv1.RevokeConnectorRequest{Id: "grant-9"}))
	require.NoError(t, err)
	require.True(t, res.Msg.GetRevoked())

	// The caller's id must reach the store, so one user cannot revoke
	// another's connector by guessing an id.
	require.Equal(t, "user-1", store.revokedUser)
	require.Equal(t, "grant-9", store.revokedID)
}

func TestRevokeConnectorRequiresID(t *testing.T) {
	svc := NewConnectorService(newStubStore(), "")

	_, err := svc.RevokeConnector(connectorCtx("user-1"),
		connect.NewRequest(&reliantv1.RevokeConnectorRequest{}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestListConnectorsExcludesCredentials(t *testing.T) {
	store := newStubStore()
	svc := NewConnectorService(store, "")

	_, err := svc.CreateConnector(connectorCtx("user-1"), connect.NewRequest(validCreateRequest()))
	require.NoError(t, err)

	res, err := svc.ListConnectors(connectorCtx("user-1"), connect.NewRequest(&reliantv1.ListConnectorsRequest{}))
	require.NoError(t, err)
	require.Len(t, res.Msg.GetConnectors(), 1)

	c := res.Msg.GetConnectors()[0]
	require.NotEmpty(t, c.GetTokenPrefix())
	require.Equal(t, "/workspace", c.GetPathRoot())
	require.ElementsMatch(t, []string{"read_file", "search"}, c.GetAllowedTools())

	// Another user sees nothing.
	other, err := svc.ListConnectors(connectorCtx("user-2"), connect.NewRequest(&reliantv1.ListConnectorsRequest{}))
	require.NoError(t, err)
	require.Empty(t, other.Msg.GetConnectors())
}

func TestListAvailableToolsDescribesCatalog(t *testing.T) {
	svc := NewConnectorService(newStubStore(), "")

	res, err := svc.ListAvailableTools(connectorCtx("user-1"),
		connect.NewRequest(&reliantv1.ListAvailableToolsRequest{}))
	require.NoError(t, err)
	require.NotEmpty(t, res.Msg.GetTools())

	byName := map[string]*reliantv1.ConnectorTool{}
	for _, tool := range res.Msg.GetTools() {
		require.NotEmpty(t, tool.GetDescription(), "tool %q needs a description for the consent UI", tool.GetName())
		byName[tool.GetName()] = tool
	}

	require.False(t, byName["read_file"].GetMutating())
	require.True(t, byName["write_file"].GetMutating())
	require.True(t, byName["run_command"].GetNeedsExec())
}

// TestMCPURLWithoutPublicURL: a guessed hostname pasted into ChatGPT fails in
// a way that is very hard to diagnose from a phone, so return the path alone.
func TestMCPURLWithoutPublicURL(t *testing.T) {
	svc := NewConnectorService(newStubStore(), "")

	res, err := svc.CreateConnector(connectorCtx("user-1"), connect.NewRequest(validCreateRequest()))
	require.NoError(t, err)
	require.Equal(t, "/mcp", res.Msg.GetMcpUrl())
}

func TestMCPURLTrimsTrailingSlash(t *testing.T) {
	svc := NewConnectorService(newStubStore(), "https://api.example.com/")

	res, err := svc.CreateConnector(connectorCtx("user-1"), connect.NewRequest(validCreateRequest()))
	require.NoError(t, err)
	require.Equal(t, "https://api.example.com/mcp", res.Msg.GetMcpUrl())
}
