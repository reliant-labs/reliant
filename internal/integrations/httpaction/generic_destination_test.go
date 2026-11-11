package httpaction

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/reliant-labs/reliant/internal/netguard/netguardtest"
	"github.com/stretchr/testify/require"
)

// Evidence for the documented generic HTTP trust boundary: the caller chooses
// the initial destination. The credential's host pin protects subsequent hops,
// not the initial request. Use only a synthetic credential and local TLS server.
func TestGenericHTTPInitialDestinationIsCallerSelected(t *testing.T) {
	var mu sync.Mutex
	var hosts []string
	srv := netguardtest.NewTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Key") != "synthetic-secret" {
			t.Error("initial destination did not receive synthetic credential")
		}
		mu.Lock()
		hosts = append(hosts, r.Host)
		mu.Unlock()
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "https://other.example.com/target", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	m, action := builtinHTTPRequest(t)
	runner := tenantRunner(t, srv)
	src := &fixedSource{cred: fakeCred{id: "generic", secret: "synthetic-secret"}}
	for _, host := range []string{"trusted.example.com", "caller-selected.example.com"} {
		res, err := runner.RunAuthenticated(context.Background(), m, action,
			map[string]any{"url": "https://" + host + "/", "connection": "generic"}, src, CallSite{RunID: "run"})
		require.NoError(t, err)
		require.False(t, res.IsError, "%+v", res)
	}
	_, err := runner.RunAuthenticated(context.Background(), m, action,
		map[string]any{"url": "https://trusted.example.com/redirect", "connection": "generic"}, src, CallSite{RunID: "run"})
	require.ErrorContains(t, err, "redirect refused")
	require.ErrorContains(t, err, "a credential is never sent to a second host")
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{"trusted.example.com", "caller-selected.example.com", "trusted.example.com"}, hosts)
}
