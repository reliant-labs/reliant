// Copyright (c) 2025 Reliant Labs
package interceptors

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
)

// reliantAPIOnly are the RunService and TriggerService rpcs no narrow scope
// reaches yet (forge/pkg/accesstoken, runs_triggers.go): reliant:api only.
var reliantAPIOnly = map[string]bool{
	"/reliant.v1.TriggerService/GetTrigger":            true,
	"/reliant.v1.TriggerService/ListTriggers":          true,
	"/reliant.v1.TriggerService/ListTriggerEvents":     true,
	"/reliant.v1.TriggerService/GetLaunchEvent":        true,
	"/reliant.v1.TriggerService/ResolveTriggerSenders": true,
	"/reliant.v1.TriggerService/ListReplayEvents":      true,
	"/reliant.v1.TriggerService/PreviewReplay":         true,
	"/reliant.v1.TriggerService/DraftAutomation":       true,
}

// TestRPCScopes_CoverRunAndTriggerServices holds rpcScopes to the protos:
// every RunService and TriggerService rpc is decided, either a narrow scope
// or reliant:api only, so an rpc added later cannot silently take a default
// nobody chose. And every entry names an rpc that exists.
func TestRPCScopes_CoverRunAndTriggerServices(t *testing.T) {
	services := []protoreflect.ServiceDescriptor{
		reliantv1.File_reliant_v1_run_proto.Services().ByName("RunService"),
		reliantv1.File_reliant_v1_trigger_proto.Services().ByName("TriggerService"),
	}
	exists := map[string]bool{}
	for _, svc := range services {
		require.NotNil(t, svc)
		methods := svc.Methods()
		for i := 0; i < methods.Len(); i++ {
			procedure := fmt.Sprintf("/%s/%s", svc.FullName(), methods.Get(i).Name())
			exists[procedure] = true
			_, scoped := rpcScopes[procedure]
			require.Truef(t, scoped != reliantAPIOnly[procedure],
				"%s must be in exactly one of rpcScopes and reliantAPIOnly", procedure)
		}
	}
	for procedure := range rpcScopes {
		require.Truef(t, exists[procedure], "rpcScopes names %s, which no proto declares", procedure)
	}
	for procedure := range reliantAPIOnly {
		require.Truef(t, exists[procedure], "reliantAPIOnly names %s, which no proto declares", procedure)
	}
}

// TestAuthInterceptorRunsAndTriggersScopes proves the enforcement at the
// authentication boundary: a narrow token reaches exactly its slice of the
// runs and triggers rpcs and nothing else, a reliant:api token reaches all of
// them, and a refusal of a live token is PermissionDenied naming the scope
// it lacks — not Unauthenticated, which would tell the caller to re-mint.
func TestAuthInterceptorRunsAndTriggersScopes(t *testing.T) {
	authority := tokenauthority.NewMemory()
	interceptor, err := NewAuthInterceptor(auth.Settings{Mode: auth.ModeAPIKey, APIKey: "legacy-api-key-secret"}, nil)
	require.NoError(t, err)
	interceptor.SetAccessTokenIntrospector(authority)
	ctx := context.Background()

	tokens := map[fat.Scope]string{}
	for _, s := range append([]fat.Scope{fat.ScopeReliantAPI}, fat.RunsAndTriggersScopes...) {
		tokens[s] = mintFor(t, authority, "user-sdk", s).Plaintext
	}

	// Each narrow scope against one rpc of every slice, plus an rpc outside
	// all of them.
	probes := map[string]fat.Scope{
		"/reliant.v1.RunService/StartRun":          fat.ScopeRunsStart,
		"/reliant.v1.RunService/GetRun":            fat.ScopeRunsRead,
		"/reliant.v1.RunService/CancelRun":         fat.ScopeRunsControl,
		"/reliant.v1.TriggerService/FireTrigger":   fat.ScopeTriggersFire,
		"/reliant.v1.TriggerService/UpdateTrigger": fat.ScopeTriggersManage,
		"/reliant.v1.TriggerService/ListTriggers":  fat.ScopeReliantAPI,
		"/reliant.v1.ChatService/GetChat":          fat.ScopeReliantAPI,
	}
	for held, token := range tokens {
		for procedure, need := range probes {
			t.Run(string(held)+" "+procedure, func(t *testing.T) {
				authedCtx, claims, err := interceptor.authenticateRequest(ctx, procedure, bearerHeader(token))
				if !fat.SetOf(held).Permits(need) {
					require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
					var cerr *connect.Error
					require.True(t, errors.As(err, &cerr))
					require.Equal(t, fat.ReasonScopeMissing, cerr.Meta().Get(svcerr.ReasonHeader))
					require.Equal(t, string(need), cerr.Meta().Get(fat.ScopesHeader))
					return
				}
				require.NoError(t, err)
				require.Equal(t, "user-sdk", claims.Sub)
				p := fat.PrincipalFrom(authedCtx)
				require.NotNil(t, p, "the handler can call fat.RequireScope")
				require.NoError(t, fat.RequireScope(authedCtx, need))
				require.Error(t, auth.RequirePerson(authedCtx, "decide an approval"),
					"a narrow token is still a machine credential")
			})
		}
	}

	t.Run("runs:start and runs:control imply runs:read", func(t *testing.T) {
		for _, held := range []fat.Scope{fat.ScopeRunsStart, fat.ScopeRunsControl} {
			_, _, err := interceptor.authenticateRequest(ctx, "/reliant.v1.RunService/ListRuns", bearerHeader(tokens[held]))
			require.NoError(t, err, held)
		}
	})

	t.Run("a non-API token is still Unauthenticated on a narrow rpc", func(t *testing.T) {
		daemon := mintFor(t, authority, "user-sdk", fat.ScopeDaemonConnect)
		_, _, err := interceptor.authenticateRequest(ctx, "/reliant.v1.RunService/GetRun", bearerHeader(daemon.Plaintext))
		require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	})

	t.Run("a narrow token cannot authenticate a plain HTTP route", func(t *testing.T) {
		_, _, err := interceptor.authenticateRequest(ctx, "http:/attachments/x", bearerHeader(tokens[fat.ScopeRunsRead]))
		require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	})

	t.Run("a revoked narrow token is refused within the authority's answer", func(t *testing.T) {
		m := mintFor(t, authority, "user-revoke", fat.ScopeRunsStart)
		_, _, err := interceptor.authenticateRequest(ctx, "/reliant.v1.RunService/StartRun", bearerHeader(m.Plaintext))
		require.NoError(t, err)
		require.NoError(t, authority.RevokeForUser(ctx, "user-revoke", m.TokenID))
		_, _, err = interceptor.authenticateRequest(ctx, "/reliant.v1.RunService/StartRun", bearerHeader(m.Plaintext))
		require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	})
}
