// Copyright (c) 2025 Reliant Labs
package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const serverEnvSentinel = "sentinel-operator-credential-7f3a"

// recordedRequest is what a remote MCP server saw of one request.
type recordedRequest struct {
	url     string
	headers http.Header
}

// recordingMCPServer is a remote "MCP server" that records every request and
// fails the handshake: the request the client sent is the evidence.
func recordingMCPServer(t *testing.T) (*httptest.Server, func() []recordedRequest) {
	t.Helper()
	var (
		mu   sync.Mutex
		reqs []recordedRequest
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reqs = append(reqs, recordedRequest{url: r.URL.String(), headers: r.Header.Clone()})
		mu.Unlock()
		http.Error(w, "not an MCP server", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []recordedRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]recordedRequest(nil), reqs...)
	}
}

func remoteConfigReferencingSentinel(url string) config.MCPServer {
	return config.MCPServer{
		Type:    config.MCPHTTP,
		URL:     url + "/mcp/${RELIANT_TEST_SERVER_ENV_SENTINEL}",
		Enabled: true,
		Headers: map[string]string{
			"Authorization": "Bearer ${RELIANT_TEST_SERVER_ENV_SENTINEL}",
			"X-Default":     "${RELIANT_TEST_SERVER_ENV_SENTINEL:-fallback}",
		},
	}
}

func addServer(t *testing.T, m *Manager, cfg config.MCPServer) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = m.AddServer(ctx, "remote", cfg) // the handshake fails; the request it sent is the evidence
	_ = m.Close()
}

// A server-role manager runs in Reliant's environment, not the user's: a
// tenant's `Bearer ${VAR}` must never pick up the operator's value of VAR.
// An unresolved reference follows expand.go's rule for a missing variable —
// its ${VAR:-default}, else the empty string.
func TestServerRoleManager_NeverResolvesReferencesFromProcessEnvironment(t *testing.T) {
	t.Setenv("RELIANT_TEST_SERVER_ENV_SENTINEL", serverEnvSentinel)
	srv, requests := recordingMCPServer(t)

	// The role is authoritative even if a caller accidentally configures a lookup.
	addServer(t, NewManager(RoleServer, WithEnvLookup(os.LookupEnv)), remoteConfigReferencingSentinel(srv.URL))

	reqs := requests()
	require.NotEmpty(t, reqs, "the client must have reached the remote server, or the assertions below prove nothing")
	for _, r := range reqs {
		assert.NotContains(t, r.url, serverEnvSentinel, "request URL carries the process env value")
		for name, values := range r.headers {
			for _, v := range values {
				assert.NotContains(t, v, serverEnvSentinel, "header %s carries the process env value", name)
			}
		}
		assert.Equal(t, "/mcp/", r.url, "unresolved ${VAR} becomes the empty string")
		assert.Equal(t, "Bearer", strings.TrimSpace(r.headers.Get("Authorization")))
		assert.Equal(t, "fallback", r.headers.Get("X-Default"), "${VAR:-default} takes its default")
	}
}

// The daemon passes its own environment explicitly (WithEnvLookup), and the
// same config resolves from it.
func TestDaemonRoleManager_ResolvesReferencesThroughExplicitEnvLookup(t *testing.T) {
	t.Setenv("RELIANT_TEST_SERVER_ENV_SENTINEL", serverEnvSentinel)
	srv, requests := recordingMCPServer(t)

	addServer(t, NewManager(RoleDaemon, WithEnvLookup(os.LookupEnv)), remoteConfigReferencingSentinel(srv.URL))

	reqs := requests()
	require.NotEmpty(t, reqs)
	assert.Equal(t, "/mcp/"+serverEnvSentinel, reqs[0].url)
	assert.Equal(t, "Bearer "+serverEnvSentinel, reqs[0].headers.Get("Authorization"))
	assert.Equal(t, serverEnvSentinel, reqs[0].headers.Get("X-Default"))
}

// The default client factory refuses a stdio client off the daemon on its own,
// whichever path reached it (AddServer, a session- or dir-scoped client): a
// stdio child inherits this process's environment.
func TestServerRoleClientFactoryRefusesStdio(t *testing.T) {
	m := NewManager(RoleServer)
	c, err := m.clientFactory("s", config.MCPServer{Type: config.MCPStdio, Command: "npx"})
	require.ErrorIs(t, err, ErrStdioOnServer)
	assert.Nil(t, c)
}
