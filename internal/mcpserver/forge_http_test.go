// Copyright (c) 2025 Reliant Labs

package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/connectorgrant"
)

// fakeForgeControlPlane knows project shop in org-1 and project other in
// org-2, and records the bearer each call carried.
type fakeForgeControlPlane struct {
	mu   sync.Mutex
	auth []string
}

func (f *fakeForgeControlPlane) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/controlplane.v1.DeployService/ResolveHostedProject":
		switch body["name"] {
		case "shop":
			_, _ = io.WriteString(w, `{"projectId":"p-1","orgId":"org-1"}`)
		case "other":
			_, _ = io.WriteString(w, `{"projectId":"p-2","orgId":"org-2"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"code":"not_found","message":"no such project"}`)
		}
	case "/controlplane.v1.DeployService/ListEnvironments":
		_, _ = fmt.Fprintf(w, `{"environments":[{"id":"env-1","name":"prod","project":%q}]}`, body["project"])
	default:
		w.WriteHeader(http.StatusNotImplemented)
		_, _ = io.WriteString(w, `{"code":"unimplemented","message":"not in this fake"}`)
	}
}

func (f *fakeForgeControlPlane) bearers() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.auth...)
}

func forgeGrant(id string, projects []string, tools []string) *connectorgrant.Grant {
	return &connectorgrant.Grant{
		ID: id, UserID: "user-1", Name: "forge", Kind: connectorgrant.KindForge,
		OrgID: "org-1", ProjectNames: projects, AllowedTools: tools, ExecMode: connectorgrant.ExecDeny,
	}
}

// newForgeTestServer serves the forge endpoint and its discovery document on
// one origin, as reliant-api-server mounts them.
func newForgeTestServer(t *testing.T, store *memStore, validator OAuthTokenValidator) (string, *fakeForgeControlPlane) {
	t.Helper()
	cp := &fakeForgeControlPlane{}
	cpSrv := httptest.NewServer(cp)
	t.Cleanup(cpSrv.Close)

	handler, err := NewForgeHTTPHandler(ForgeHTTPDeps{
		Store: store, TokenValidator: validator, Bindings: store, OAuth: testOAuthConfig(),
		ControlPlaneURL: cpSrv.URL, ConsoleURL: "https://console.example", ConsentBaseURL: "https://app.example.com",
	})
	require.NoError(t, err)
	mux := http.NewServeMux()
	mux.Handle(ForgeMountPath, handler)
	mux.Handle(ForgeMountPath+"/", handler)
	mux.Handle(ForgeSSEMountPath, handler)
	MountForgeOAuthRoutes(mux, testOAuthConfig())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL, cp
}

func connectForge(t *testing.T, url, token string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	sess, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   url + ForgeMountPath,
		HTTPClient: &http.Client{Transport: &bearerTransport{token: token}},
	}, nil)
	require.NoError(t, err, "MCP client could not connect to /forge/mcp")
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

func TestForgeHandshakeListsOnlyGrantedTools(t *testing.T) {
	store := newMemStore()
	require.NoError(t, store.CreateGrant(context.Background(),
		forgeGrant("g-forge", nil, []string{"obs_status", "envs_list", "obs_init", "skill_load"})))
	url, _ := newForgeTestServer(t, store, &stubTokenValidator{userID: "user-1"})

	sess := connectForge(t, url, "user-jwt")
	assert.Equal(t, "reliant-forge", sess.InitializeResult().ServerInfo.Name)
	assert.Contains(t, sess.InitializeResult().Instructions, "Reliant Forge")

	res, err := sess.ListTools(context.Background(), nil)
	require.NoError(t, err)
	got := map[string]*mcp.Tool{}
	for _, tool := range res.Tools {
		got[tool.Name] = tool
	}
	assert.Len(t, got, 4, "only the granted tools are offered")
	require.Contains(t, got, "obs_init")
	// obs_init changes things: it must not claim read-only, so clients ask.
	assert.False(t, got["obs_init"].Annotations.ReadOnlyHint)
	assert.True(t, got["obs_status"].Annotations.ReadOnlyHint)
}

func TestForgeDiscoveryDocument(t *testing.T) {
	url, _ := newForgeTestServer(t, newMemStore(), &stubTokenValidator{userID: "user-1"})
	resp, err := http.Get(url + "/.well-known/oauth-protected-resource/forge/mcp")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var doc protectedResourceMetadata
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&doc))
	assert.Equal(t, "https://api.example.com/forge/mcp", doc.Resource)
	assert.Equal(t, "Reliant Forge", doc.ResourceName)
	assert.Equal(t, testOAuthConfig().AuthorizationServers, doc.AuthorizationServers)
}

func TestForgeRefusesWithoutTokenOrGrant(t *testing.T) {
	store := newMemStore()
	url, _ := newForgeTestServer(t, store, &stubTokenValidator{userID: "user-1"})
	post := func(token string) *http.Response {
		req, _ := http.NewRequest(http.MethodPost, url+ForgeMountPath,
			strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		t.Cleanup(func() { _ = resp.Body.Close() })
		return resp
	}

	// No token: 401 pointing at the forge resource's own document.
	resp := post("")
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("WWW-Authenticate"),
		`resource_metadata="https://api.example.com/.well-known/oauth-protected-resource/forge/mcp"`)
	assert.Contains(t, resp.Header.Get("WWW-Authenticate"), `realm="reliant-forge"`)

	// A connector credential cannot act as its person: refused.
	resp = post(store.credentialFor(t, "g-any", "user-1"))
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// Signed in, but a workspace grant is not a forge grant: consent, in
	// forge mode.
	require.NoError(t, store.CreateGrant(context.Background(), &connectorgrant.Grant{
		ID: "g-ws", UserID: "user-1", DaemonID: "daemon-1", AllowedTools: ReadOnlyToolNames(), PathRoot: "/",
	}))
	resp = post("user-jwt")
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	var body map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Contains(t, body["consent_url"], "kind=forge")
}

func TestForgeReadToolCallsControlPlaneAsTheCaller(t *testing.T) {
	store := newMemStore()
	require.NoError(t, store.CreateGrant(context.Background(), forgeGrant("g-forge", []string{"shop"}, []string{"envs_list"})))
	url, cp := newForgeTestServer(t, store, &stubTokenValidator{userID: "user-1"})
	sess := connectForge(t, url, "user-jwt")

	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "envs_list", Arguments: map[string]any{"project": "shop"}})
	require.NoError(t, err)
	require.False(t, res.IsError, "envs_list: %v", res.Content)
	doc, _ := res.StructuredContent.(map[string]any)
	assert.Equal(t, "https://console.example/forge/p/shop", doc["console_url"])
	for _, a := range cp.bearers() {
		assert.Equal(t, "Bearer user-jwt", a, "the control plane is called as the signed-in person")
	}

	// A project outside the grant, and one in another org, are refused.
	for _, project := range []string{"other"} {
		res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "envs_list", Arguments: map[string]any{"project": project}})
		require.NoError(t, err)
		assert.True(t, res.IsError, "%s must be refused", project)
	}

	// Every call is audited against the grant.
	store.waitForAudit(t)
	recs := store.records()
	require.NotEmpty(t, recs)
	assert.Equal(t, "g-forge", recs[0].GrantID)
	assert.Equal(t, "envs_list", recs[0].ToolName)
}

func TestForgeGrantNarrowedMidSessionAppliesAtOnce(t *testing.T) {
	store := newMemStore()
	g := forgeGrant("g-forge", nil, []string{"envs_list"})
	require.NoError(t, store.CreateGrant(context.Background(), g))
	url, _ := newForgeTestServer(t, store, &stubTokenValidator{userID: "user-1"})
	sess := connectForge(t, url, "user-jwt")

	store.mu.Lock()
	g.ProjectNames = []string{"not-shop"}
	store.mu.Unlock()
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "envs_list", Arguments: map[string]any{"project": "shop"}})
	require.NoError(t, err)
	assert.True(t, res.IsError, "a grant narrowed mid-session refuses the next call")
}

// The workspace endpoint never resolves a forge grant: it reaches no daemon.
func TestWorkspaceResolutionIgnoresForgeGrants(t *testing.T) {
	store := newMemStore()
	require.NoError(t, store.CreateGrant(context.Background(), forgeGrant("g-forge", nil, []string{"envs_list"})))
	_, err := resolveForUser(context.Background(), store, store, "user-1", "", "https://app.example.com")
	var consent *ConsentError
	require.ErrorAs(t, err, &consent, "a user whose only grant is a forge grant has no workspace connector")
}
