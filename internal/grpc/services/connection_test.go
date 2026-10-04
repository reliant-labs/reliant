// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/crypto"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/vault"
)

// responseFieldAllowList is every field any ConnectionService response (and
// everything reachable from one) may carry. It is an ALLOW-list: adding a field
// to a response fails this test until a human reads the name and adds it here,
// which is the moment to ask "could this carry a secret?". Nothing here can.
var responseFieldAllowList = map[string]bool{
	"reliant.v1.ListIntegrationsResponse.integrations": true,
	"reliant.v1.Integration.id":                        true,
	"reliant.v1.Integration.display_name":              true,
	"reliant.v1.Integration.auth_kind":                 true,
	"reliant.v1.Integration.available":                 true,
	"reliant.v1.Integration.unavailable_reason":        true,

	"reliant.v1.ListConnectionsResponse.connections":       true,
	"reliant.v1.GetConnectionResponse.connection":          true,
	"reliant.v1.CreateApiKeyConnectionResponse.connection": true,
	"reliant.v1.CompleteOAuthResponse.connection":          true,
	"reliant.v1.CompleteOAuthResponse.redirect_after":      true,
	"reliant.v1.RenameConnectionResponse.connection":       true,
	"reliant.v1.SetDefaultConnectionResponse.connection":   true,
	"reliant.v1.StartOAuthResponse.authorize_url":          true, // a provider URL carrying a one-time state; never a credential
	"reliant.v1.TestConnectionResponse.ok":                 true,
	"reliant.v1.TestConnectionResponse.probed":             true,
	"reliant.v1.TestConnectionResponse.account_label":      true,
	"reliant.v1.TestConnectionResponse.error_class":        true,
	"reliant.v1.ListConnectionEventsResponse.events":       true,
	"reliant.v1.ConnectionEvent.id":                        true,
	"reliant.v1.ConnectionEvent.connection_id":             true,
	"reliant.v1.ConnectionEvent.kind":                      true,
	"reliant.v1.ConnectionEvent.run_id":                    true,
	"reliant.v1.ConnectionEvent.node_id":                   true,
	"reliant.v1.ConnectionEvent.tool_call_id":              true,
	"reliant.v1.ConnectionEvent.actor":                     true,
	"reliant.v1.ConnectionEvent.at":                        true,
	"reliant.v1.Connection.id":                             true,
	"reliant.v1.Connection.integration_id":                 true,
	"reliant.v1.Connection.auth_kind":                      true,
	"reliant.v1.Connection.name":                           true,
	"reliant.v1.Connection.account_label":                  true,
	"reliant.v1.Connection.external_account_id":            true,
	"reliant.v1.Connection.scopes":                         true,
	"reliant.v1.Connection.status":                         true,
	"reliant.v1.Connection.status_reason":                  true,
	"reliant.v1.Connection.is_default":                     true,
	"reliant.v1.Connection.access_expires_at":              true,
	"reliant.v1.Connection.last_used_at":                   true,
	"reliant.v1.Connection.created_at":                     true,
	"reliant.v1.Connection.updated_at":                     true,
}

func walkMessage(md protoreflect.MessageDescriptor, seen map[protoreflect.FullName]bool, visit func(protoreflect.FieldDescriptor)) {
	if seen[md.FullName()] {
		return
	}
	seen[md.FullName()] = true
	for i := 0; i < md.Fields().Len(); i++ {
		fd := md.Fields().Get(i)
		visit(fd)
		if fd.Message() != nil && !fd.IsMap() {
			walkMessage(fd.Message(), seen, visit)
		}
		if fd.IsMap() && fd.MapValue().Message() != nil {
			walkMessage(fd.MapValue().Message(), seen, visit)
		}
	}
}

func TestConnectionServiceResponsesCarryNoSecretFields(t *testing.T) {
	sd := reliantv1.File_reliant_v1_connection_proto.Services().ByName("ConnectionService")
	require.NotNil(t, sd)
	require.Greater(t, sd.Methods().Len(), 0)

	seenFields := map[string]bool{}
	for i := 0; i < sd.Methods().Len(); i++ {
		m := sd.Methods().Get(i)
		walkMessage(m.Output(), map[protoreflect.FullName]bool{}, func(fd protoreflect.FieldDescriptor) {
			name := string(fd.FullName())
			seenFields[name] = true
			require.True(t, responseFieldAllowList[name],
				"%s is a response field not on the allow-list: a ConnectionService response must never be able to carry a secret value. "+
					"If this field is safe, add it to responseFieldAllowList; if not, remove it.", name)
		})
	}
	// The allow-list must not rot: every entry has to still be a real field.
	for name := range responseFieldAllowList {
		require.True(t, seenFields[name], "allow-list entry %s no longer exists in any response", name)
	}
}

// Belt and braces under the allow-list: whatever the name, no response field may
// be of a type or name that smells like a credential.
func TestConnectionServiceResponsesHaveNoCredentialShapedFields(t *testing.T) {
	sd := reliantv1.File_reliant_v1_connection_proto.Services().ByName("ConnectionService")
	for i := 0; i < sd.Methods().Len(); i++ {
		walkMessage(sd.Methods().Get(i).Output(), map[protoreflect.FullName]bool{}, func(fd protoreflect.FieldDescriptor) {
			n := strings.ToLower(string(fd.Name()))
			for _, bad := range []string{"secret", "password", "api_key", "apikey", "ciphertext", "refresh", "client_secret", "private"} {
				require.NotContains(t, n, bad, "%s", fd.FullName())
			}
			if strings.Contains(n, "token") {
				require.Contains(t, n, "none", "%s looks like a token field", fd.FullName())
			}
			require.NotEqual(t, protoreflect.BytesKind, fd.Kind(), "%s: a bytes field could hold ciphertext", fd.FullName())
		})
	}
}

// Of every request message, only CreateApiKeyConnectionRequest.fields and the
// OAuth completion's state/code carry a secret, and each is debug_redact so
// prototext and logging interceptors scrub it.
func TestConnectionServiceRequestSecretsAreDebugRedacted(t *testing.T) {
	want := map[string]bool{
		"reliant.v1.CreateApiKeyConnectionRequest.fields": false,
		"reliant.v1.CompleteOAuthRequest.state":           false,
		"reliant.v1.CompleteOAuthRequest.code":            false,
	}
	sd := reliantv1.File_reliant_v1_connection_proto.Services().ByName("ConnectionService")
	for i := 0; i < sd.Methods().Len(); i++ {
		walkMessage(sd.Methods().Get(i).Input(), map[protoreflect.FullName]bool{}, func(fd protoreflect.FieldDescriptor) {
			opts, _ := fd.Options().(*descriptorpb.FieldOptions)
			redacted := opts.GetDebugRedact()
			name := string(fd.FullName())
			if _, isSecret := want[name]; isSecret {
				want[name] = redacted
				return
			}
			require.False(t, redacted, "%s is redacted but not a known secret field; add it to this test deliberately", name)
		})
	}
	for name, redacted := range want {
		require.True(t, redacted, "%s must be debug_redact", name)
	}
}

// ---- handler behaviour over a real DB ----

type connectionFixture struct {
	svc *ConnectionService
}

func newConnectionFixture(t *testing.T) *connectionFixture {
	t.Helper()
	repo, raw, cleanup := db.SetupTestDBWithRawDB(t)
	t.Cleanup(cleanup)
	k := make([]byte, 32)
	_, err := rand.Read(k)
	require.NoError(t, err)
	ring, err := crypto.ParseKeyring("v1:" + base64.StdEncoding.EncodeToString(k))
	require.NoError(t, err)
	v := vault.New(raw, vault.NewEnvKeyWrapper(ring))

	providers, err := connections.ProvidersFromEnv(func(string) string { return "" })
	require.NoError(t, err)
	store := repo.Connections()
	tokens := connections.NewTokenSource(store, v, providers, nil)
	broker := connections.NewBroker(store, v, providers, nil, "https://reliant.example")
	return &connectionFixture{svc: NewConnectionService(connections.NewService(store, v, providers, tokens, broker, nil))}
}

func asConnUser(userID string) context.Context {
	return context.WithValue(context.Background(), auth.UserIDContextKey, userID)
}

func (f *connectionFixture) createKey(t *testing.T, user, integration, name string) *reliantv1.Connection {
	t.Helper()
	resp, err := f.svc.CreateApiKeyConnection(asConnUser(user), connect.NewRequest(&reliantv1.CreateApiKeyConnectionRequest{
		IntegrationId: integration, Name: name, Kind: reliantv1.ApiKeyConnectionKind_API_KEY_CONNECTION_KIND_API_KEY,
		Fields: map[string]string{"api_key": "sk-secret-value-123"},
	}))
	require.NoError(t, err)
	return resp.Msg.Connection
}

func requireConnectCode(t *testing.T, err error, want connect.Code) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, want, connect.CodeOf(err), "%v", err)
}

func TestConnectionService_RequiresAuthentication(t *testing.T) {
	f := newConnectionFixture(t)
	_, err := f.svc.ListConnections(context.Background(), connect.NewRequest(&reliantv1.ListConnectionsRequest{}))
	requireConnectCode(t, err, connect.CodeUnauthenticated)
	_, err = f.svc.CreateApiKeyConnection(context.Background(), connect.NewRequest(&reliantv1.CreateApiKeyConnectionRequest{}))
	requireConnectCode(t, err, connect.CodeUnauthenticated)
}

func TestConnectionService_CreateListGetNeverReturnSecret(t *testing.T) {
	f := newConnectionFixture(t)
	c := f.createKey(t, "alice", "linear", "work")
	require.Equal(t, reliantv1.ConnectionAuthKind_CONNECTION_AUTH_KIND_API_KEY, c.AuthKind)
	require.Equal(t, reliantv1.ConnectionStatus_CONNECTION_STATUS_ACTIVE, c.Status)
	require.True(t, c.IsDefault)

	list, err := f.svc.ListConnections(asConnUser("alice"), connect.NewRequest(&reliantv1.ListConnectionsRequest{}))
	require.NoError(t, err)
	require.Len(t, list.Msg.Connections, 1)
	get, err := f.svc.GetConnection(asConnUser("alice"), connect.NewRequest(&reliantv1.GetConnectionRequest{Id: c.Id}))
	require.NoError(t, err)
	for _, m := range []fmtStringer{c, list.Msg, get.Msg} {
		require.NotContains(t, m.String(), "sk-secret-value-123")
	}
}

type fmtStringer interface{ String() string }

func TestConnectionService_OtherUserGetsNotFoundEverywhere(t *testing.T) {
	f := newConnectionFixture(t)
	c := f.createKey(t, "alice", "linear", "work")
	bob := asConnUser("bob")

	_, err := f.svc.GetConnection(bob, connect.NewRequest(&reliantv1.GetConnectionRequest{Id: c.Id}))
	requireConnectCode(t, err, connect.CodeNotFound)
	_, err = f.svc.RenameConnection(bob, connect.NewRequest(&reliantv1.RenameConnectionRequest{Id: c.Id, Name: "x"}))
	requireConnectCode(t, err, connect.CodeNotFound)
	_, err = f.svc.SetDefaultConnection(bob, connect.NewRequest(&reliantv1.SetDefaultConnectionRequest{Id: c.Id}))
	requireConnectCode(t, err, connect.CodeNotFound)
	_, err = f.svc.TestConnection(bob, connect.NewRequest(&reliantv1.TestConnectionRequest{Id: c.Id}))
	requireConnectCode(t, err, connect.CodeNotFound)
	_, err = f.svc.DeleteConnection(bob, connect.NewRequest(&reliantv1.DeleteConnectionRequest{Id: c.Id}))
	requireConnectCode(t, err, connect.CodeNotFound)
	_, err = f.svc.ListConnectionEvents(bob, connect.NewRequest(&reliantv1.ListConnectionEventsRequest{Id: c.Id}))
	requireConnectCode(t, err, connect.CodeNotFound)

	// A truly missing id answers identically.
	_, err = f.svc.GetConnection(bob, connect.NewRequest(&reliantv1.GetConnectionRequest{Id: "conn_missing"}))
	requireConnectCode(t, err, connect.CodeNotFound)

	list, err := f.svc.ListConnections(bob, connect.NewRequest(&reliantv1.ListConnectionsRequest{}))
	require.NoError(t, err)
	require.Empty(t, list.Msg.Connections)

	// Still there for alice.
	_, err = f.svc.GetConnection(asConnUser("alice"), connect.NewRequest(&reliantv1.GetConnectionRequest{Id: c.Id}))
	require.NoError(t, err)
}

func TestConnectionService_ErrorCodes(t *testing.T) {
	f := newConnectionFixture(t)
	alice := asConnUser("alice")
	f.createKey(t, "alice", "linear", "work")

	_, err := f.svc.CreateApiKeyConnection(alice, connect.NewRequest(&reliantv1.CreateApiKeyConnectionRequest{
		IntegrationId: "linear", Name: "work", Kind: reliantv1.ApiKeyConnectionKind_API_KEY_CONNECTION_KIND_API_KEY,
		Fields: map[string]string{"api_key": "k"},
	}))
	requireConnectCode(t, err, connect.CodeAlreadyExists)

	_, err = f.svc.CreateApiKeyConnection(alice, connect.NewRequest(&reliantv1.CreateApiKeyConnectionRequest{
		IntegrationId: "linear", Name: "other", Kind: reliantv1.ApiKeyConnectionKind_API_KEY_CONNECTION_KIND_API_KEY,
		Fields: map[string]string{},
	}))
	requireConnectCode(t, err, connect.CodeInvalidArgument)

	// github is an OAuth integration: pasting a key for it is refused.
	_, err = f.svc.CreateApiKeyConnection(alice, connect.NewRequest(&reliantv1.CreateApiKeyConnectionRequest{
		IntegrationId: "github", Name: "n", Kind: reliantv1.ApiKeyConnectionKind_API_KEY_CONNECTION_KIND_API_KEY,
		Fields: map[string]string{"api_key": "k"},
	}))
	requireConnectCode(t, err, connect.CodeInvalidArgument)

	_, err = f.svc.StartOAuth(alice, connect.NewRequest(&reliantv1.StartOAuthRequest{IntegrationId: "nope"}))
	requireConnectCode(t, err, connect.CodeNotFound)
	// The fixture has no GitHub credentials configured: unavailable, not a crash.
	_, err = f.svc.StartOAuth(alice, connect.NewRequest(&reliantv1.StartOAuthRequest{IntegrationId: "github"}))
	requireConnectCode(t, err, connect.CodeFailedPrecondition)
	_, err = f.svc.CompleteOAuth(alice, connect.NewRequest(&reliantv1.CompleteOAuthRequest{State: "never", Code: "c"}))
	requireConnectCode(t, err, connect.CodeFailedPrecondition)
}

func TestConnectionService_ListIntegrationsShowsUnavailableWithReason(t *testing.T) {
	f := newConnectionFixture(t)
	resp, err := f.svc.ListIntegrations(asConnUser("alice"), connect.NewRequest(&reliantv1.ListIntegrationsRequest{}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.Integrations, 1)
	gh := resp.Msg.Integrations[0]
	require.Equal(t, "github", gh.Id)
	require.False(t, gh.Available)
	require.Contains(t, gh.UnavailableReason, "RELIANT_GITHUB_APP_CLIENT_ID")
	require.Equal(t, reliantv1.ConnectionAuthKind_CONNECTION_AUTH_KIND_GITHUB_APP_USER, gh.AuthKind)
}

func TestConnectionService_DefaultRenameDeleteEvents(t *testing.T) {
	f := newConnectionFixture(t)
	alice := asConnUser("alice")
	a := f.createKey(t, "alice", "linear", "a")
	b := f.createKey(t, "alice", "linear", "b")
	require.True(t, a.IsDefault)
	require.False(t, b.IsDefault)

	set, err := f.svc.SetDefaultConnection(alice, connect.NewRequest(&reliantv1.SetDefaultConnectionRequest{Id: b.Id}))
	require.NoError(t, err)
	require.True(t, set.Msg.Connection.IsDefault)
	got, _ := f.svc.GetConnection(alice, connect.NewRequest(&reliantv1.GetConnectionRequest{Id: a.Id}))
	require.False(t, got.Msg.Connection.IsDefault)

	ren, err := f.svc.RenameConnection(alice, connect.NewRequest(&reliantv1.RenameConnectionRequest{Id: b.Id, Name: "renamed"}))
	require.NoError(t, err)
	require.Equal(t, "renamed", ren.Msg.Connection.Name)

	evs, err := f.svc.ListConnectionEvents(alice, connect.NewRequest(&reliantv1.ListConnectionEventsRequest{Id: b.Id}))
	require.NoError(t, err)
	var kinds []string
	for _, e := range evs.Msg.Events {
		kinds = append(kinds, e.Kind)
	}
	require.Equal(t, []string{"renamed", "default_changed", "created"}, kinds, "newest first")
	require.Equal(t, "user:alice", evs.Msg.Events[0].Actor)

	_, err = f.svc.DeleteConnection(alice, connect.NewRequest(&reliantv1.DeleteConnectionRequest{Id: b.Id}))
	require.NoError(t, err)
	_, err = f.svc.GetConnection(alice, connect.NewRequest(&reliantv1.GetConnectionRequest{Id: b.Id}))
	requireConnectCode(t, err, connect.CodeNotFound)
}
