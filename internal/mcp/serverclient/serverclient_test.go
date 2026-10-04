package serverclient

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The server role must be unable to spawn a subprocess. The strongest form of
// that is that nothing it links can: os/exec absent from the dependency graph.
func TestPackageDoesNotDependOnOSExec(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to go list")
	}
	out, err := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", ".").Output()
	require.NoError(t, err)
	for _, dep := range strings.Fields(string(out)) {
		require.NotEqual(t, "os/exec", dep, "serverclient must not (transitively) import os/exec")
	}
}

func TestNewServerClientRejectsUnsafeEntries(t *testing.T) {
	for name, entry := range map[string]Entry{
		"http":        {Name: "a", URL: "http://example.com/mcp"},
		"loopback":    {Name: "b", URL: "https://127.0.0.1/mcp"},
		"localhost":   {Name: "c", URL: "https://localhost/mcp"},
		"private":     {Name: "d", URL: "https://10.1.2.3/mcp"},
		"empty":       {Name: "e"},
		"not allowed": {Name: "f", URL: "https://example.com/mcp", AllowedHosts: []string{"other.com"}},
	} {
		_, err := NewServerClient(entry, Creds{})
		assert.Error(t, err, name)
	}
	_, err := NewServerClient(Entry{Name: "ok", URL: "https://example.com/mcp"}, Creds{})
	assert.NoError(t, err)
}

func TestClientSpeaksStreamableHTTP(t *testing.T) {
	var sawAuth string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		var req struct {
			ID     *int64 `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Mcp-Session-Id", "s1")
		var result string
		switch req.Method {
		case "initialize":
			result = `{"protocolVersion":"2025-06-18"}`
		case "tools/list":
			result = `{"tools":[{"name":"send","description":"d","inputSchema":{"type":"object"}}]}`
		default:
			result = `{"content":[{"type":"text","text":"ok"}]}`
		}
		msg := `{"jsonrpc":"2.0","id":` + itoa(*req.ID) + `,"result":` + result + `}`
		if req.Method == "tools/call" {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: message\ndata: " + msg + "\n\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(msg))
	}))
	defer srv.Close()

	c, err := NewServerClient(Entry{Name: "x", URL: srv.URL, AllowedHosts: []string{"127.0.0.1"}}, Creds{BearerToken: "tok"})
	require.Error(t, err, "loopback is refused even when allowlisted")
	require.Nil(t, c)

	// httptest binds loopback, so bypass host validation to exercise the wire.
	c = &Client{entry: Entry{Name: "x", URL: srv.URL}, creds: Creds{BearerToken: "tok"}, http: srv.Client()}
	tools, err := c.ListTools(context.Background())
	require.NoError(t, err)
	require.Len(t, tools, 1)
	assert.Equal(t, "send", tools[0].Name)
	assert.Equal(t, "Bearer tok", sawAuth)

	res, err := c.CallTool(context.Background(), "send", map[string]any{"to": "a"})
	require.NoError(t, err)
	assert.True(t, bytes.Contains(res, []byte(`"ok"`)))
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }
