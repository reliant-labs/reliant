package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/activity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	mcp "github.com/reliant-labs/reliant/internal/handlers/mcp"
)

type installPrincipal struct{}

func (installPrincipal) Actor(context.Context) activity.Actor { return activity.User("test-user", "") }
func (installPrincipal) Scope(context.Context) string         { return "test-user" }

// Use the actual interceptor and proto declaration, not a substitute Skip flag.
func installActivityClient(t *testing.T, svc *mcp.Service) (reliantv1connect.MCPServiceClient, func() []activity.Event) {
	t.Helper()
	var mu sync.Mutex
	var events []activity.Event
	ic, err := activity.Interceptor(activity.Deps{
		Principal: installPrincipal{},
		Writer: activity.WriterFunc(func(_ context.Context, ev activity.Event) error {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, ev)
			return nil
		}),
	})
	require.NoError(t, err)
	authed := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			return next(context.WithValue(ctx, auth.UserIDContextKey, "test-user"), req)
		}
	})
	mux := http.NewServeMux()
	mux.Handle(reliantv1connect.NewMCPServiceHandler(svc, connect.WithInterceptors(authed, ic)))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return reliantv1connect.NewMCPServiceClient(srv.Client(), srv.URL), func() []activity.Event {
		mu.Lock()
		defer mu.Unlock()
		return append([]activity.Event(nil), events...)
	}
}

func TestInstallServer_ActivityMatchesPersistedOutcome(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name                string
		daemonRollbackFails bool
		storeRollbackFails  bool
		previous            bool
		noBundle            bool
		preflight           bool
		startupFails        bool
		bundleTransportFail bool
	}{
		{name: "both rollbacks confirmed"},
		{name: "previous config restored", previous: true},
		{name: "daemon rollback failed", daemonRollbackFails: true},
		{name: "stored rollback failed", storeRollbackFails: true},
		{name: "both rollbacks failed", daemonRollbackFails: true, storeRollbackFails: true},
		{name: "replacement rollback failed", previous: true, daemonRollbackFails: true},
		{name: "bundle transport error stays private", daemonRollbackFails: true, bundleTransportFail: true},
		{name: "no bundled skills", noBundle: true},
		{name: "preflight refused", preflight: true},
		{name: "saved but startup failed", noBundle: true, startupFails: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			const secret = "private-install-token"
			name := "github" // Embedded recommendation has a bundled skill.
			if tt.noBundle {
				name = "custom-server"
			}
			initial := mcp.MCPConfigFile{MCPServers: map[string]mcp.MCPServerConfig{
				"unrelated": {Command: "keep-me"},
			}}
			if tt.previous {
				initial.MCPServers[name] = mcp.MCPServerConfig{Command: "old-command"}
			}
			initialJSON, err := json.Marshal(initial)
			require.NoError(t, err)
			storedJSON, err := json.Marshal(map[string]string{"project": string(initialJSON)})
			require.NoError(t, err)

			// State is copied on writes: an attempted failed compensation must not
			// accidentally restore the fixture via an aliased record or config map.
			var stateMu sync.Mutex
			daemonJSON := string(initialJSON)
			stored := string(storedJSON)
			writes, upserts, bundles, starts := 0, 0, 0, 0
			store := ownedProjectRepo(t, stored)
			store.GetProjectConfigRecordFunc = func(_ context.Context, id string) (*db.ProjectConfigRecord, error) {
				stateMu.Lock()
				defer stateMu.Unlock()
				copy := stored
				return &db.ProjectConfigRecord{ProjectID: id, MCPConfigs: &copy}, nil
			}
			store.UpsertProjectConfigRecordFunc = func(_ context.Context, record *db.ProjectConfigRecord) error {
				stateMu.Lock()
				defer stateMu.Unlock()
				upserts++
				if upserts == 2 && tt.storeRollbackFails {
					return errors.New("snapshot transport failed: " + secret)
				}
				stored = *record.MCPConfigs
				return nil
			}
			store.EmitUserRefetchFunc = func(context.Context, string, db.RefetchType, db.RefetchOpts) error { return nil }
			router := daemonFunc(func(command string, payload []byte) ([]byte, error) {
				stateMu.Lock()
				defer stateMu.Unlock()
				switch command {
				case "mcp.write_config":
					writes++
					if writes == 2 && tt.daemonRollbackFails {
						return nil, errors.New("daemon transport failed: " + secret)
					}
					var req struct {
						Content string `json:"content"`
					}
					if err := json.Unmarshal(payload, &req); err != nil {
						return nil, err
					}
					daemonJSON = req.Content
					return []byte(`{"success":true}`), nil
				case "mcp.install_bundle":
					bundles++
					if tt.bundleTransportFail {
						return nil, errors.New("bundle transport failed: " + secret)
					}
					return []byte(`{"error":"skill bundle rejected"}`), nil
				case "mcp.manage_server":
					starts++
					if tt.startupFails {
						return []byte(`{"success":false,"error":"startup rejected"}`), nil
					}
					return []byte(`{"success":true}`), nil
				default:
					return nil, errors.New("unexpected command: " + command)
				}
			})
			client, events := installActivityClient(t, newService(t, store, router))
			command := "new-command"
			if tt.preflight {
				command = ""
			}
			resp, err := client.InstallServer(context.Background(), connect.NewRequest(&reliantv1.InstallServerRequest{
				ProjectId: "p-1", Name: name, Scope: reliantv1.ConfigScope_CONFIG_SCOPE_PROJECT,
				Config: &reliantv1.MCPServerConfig{Command: command,
					Env: []string{"GITHUB_PERSONAL_ACCESS_TOKEN=" + secret}},
			}))
			require.NoError(t, err, "partial failures remain recoverable responses, not RPC errors that suppress activity")
			assert.Equal(t, tt.noBundle && !tt.startupFails, resp.Msg.Success)
			assert.NotContains(t, resp.Msg.Message, secret)

			stateMu.Lock()
			defer stateMu.Unlock()
			var daemonCfg mcp.MCPConfigFile
			require.NoError(t, json.Unmarshal([]byte(daemonJSON), &daemonCfg))
			var storedScopes map[string]string
			require.NoError(t, json.Unmarshal([]byte(stored), &storedScopes))
			var storedCfg mcp.MCPConfigFile
			require.NoError(t, json.Unmarshal([]byte(storedScopes["project"]), &storedCfg))
			assert.Equal(t, initial.MCPServers["unrelated"], daemonCfg.MCPServers["unrelated"])
			assert.Equal(t, initial.MCPServers["unrelated"], storedCfg.MCPServers["unrelated"])

			if tt.preflight {
				assert.Zero(t, writes)
				assert.Zero(t, upserts)
				assert.Zero(t, bundles)
			} else if tt.noBundle {
				assert.Equal(t, 1, writes)
				assert.Equal(t, 1, upserts)
				assert.Zero(t, bundles)
				assert.Equal(t, 1, starts)
			} else {
				assert.Equal(t, 2, writes)
				assert.Equal(t, 2, upserts, "attempt stored rollback even when daemon rollback failed")
				assert.Equal(t, 1, bundles)
				assert.Zero(t, starts)
				if tt.daemonRollbackFails || tt.storeRollbackFails {
					assert.Contains(t, resp.Msg.Message, "rollback could not be confirmed")
					assert.NotContains(t, resp.Msg.Message, "MCP install rolled back.")
				} else {
					assert.Contains(t, resp.Msg.Message, "MCP install rolled back.")
				}
				if !tt.bundleTransportFail {
					assert.Contains(t, resp.Msg.Message, "skill bundle rejected", "preserve the original daemon-reported failure")
				}
			}
			wantDaemonChanged := !tt.preflight && (tt.noBundle || tt.daemonRollbackFails)
			wantStoreChanged := !tt.preflight && (tt.noBundle || tt.storeRollbackFails)
			if wantDaemonChanged {
				assert.Equal(t, "new-command", daemonCfg.MCPServers[name].Command)
			} else {
				assert.Equal(t, initial.MCPServers[name], daemonCfg.MCPServers[name])
			}
			if wantStoreChanged {
				assert.Equal(t, "new-command", storedCfg.MCPServers[name].Command)
			} else {
				assert.Equal(t, initial.MCPServers[name], storedCfg.MCPServers[name])
			}

			got := events()
			if !wantDaemonChanged && !wantStoreChanged {
				assert.Empty(t, got, "only a confirmed no-op should suppress activity")
				return
			}
			require.Len(t, got, 1)
			assert.Equal(t, "test-user", got[0].Scope)
			assert.Equal(t, activity.Actor{ID: "test-user", Name: "test-user", Kind: activity.ActorUser}, got[0].Actor)
			assert.Equal(t, "install", got[0].Verb)
			assert.Equal(t, activity.Target{Kind: "mcp_server", ID: name, Label: name}, got[0].Target)
			assert.Equal(t, reliantv1connect.MCPServiceInstallServerProcedure, got[0].Procedure)
			wantSuccess := "false"
			if tt.noBundle && !tt.startupFails {
				wantSuccess = "true"
			}
			assert.Equal(t, map[string]string{"project_id": "p-1", "scope": "CONFIG_SCOPE_PROJECT", "success": wantSuccess}, got[0].Detail)
			encoded, err := json.Marshal(got)
			require.NoError(t, err)
			assert.NotContains(t, string(encoded), secret)
			assert.NotContains(t, string(encoded), "new-command")
			assert.NotContains(t, string(encoded), "skill bundle rejected")
		})
	}
}
