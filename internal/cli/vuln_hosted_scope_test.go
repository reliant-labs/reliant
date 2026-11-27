package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
)

// The bug: a hosted `forge vuln list prod` with a session credential (a
// Reliant session through the credential helper, or a `forge login`) exited 4
// for an org owner who holds vuln:read. The session's token carries
// LoginScopes, which lack vuln:read, and the control plane's refusal
// ("missing vuln:read in target organization") names no "<scope> scope", so
// the reactive elevation never fired. The findings reads now declare
// vuln:read, so the client exchanges for it before the first read.
func TestHostedVulnReadsElevateForVulnReadBeforeTheFirstRead(t *testing.T) {
	var (
		mu     sync.Mutex
		calls  []string // "<procedure> <bearer>"
		denied []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proc := strings.TrimPrefix(r.URL.Path, "/")
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, proc+" "+bearer)
		w.Header().Set("Content-Type", "application/json")
		switch proc {
		case "controlplane.v1.AccessTokenService/ExchangeToken":
			var req struct {
				Scopes []string `json:"scopes"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			if bearer != "rlat_session" || len(req.Scopes) != 1 || req.Scopes[0] != "vuln:read" {
				t.Errorf("exchange by %q for %v, want the session's token for [vuln:read]", bearer, req.Scopes)
				w.WriteHeader(http.StatusForbidden)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"secret": "rlat_vuln_read",
				"token":  map[string]any{"expiresAt": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)},
			})
		case procGetVulnerabilitySummary, procGetReleaseVulnerabilities, procGetVulnerability:
			if bearer != "rlat_vuln_read" {
				// The control plane's own wording, which names no
				// "<scope> scope" for a reactive elevation to read.
				denied = append(denied, proc)
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"code":"permission_denied","message":"missing vuln:read in target organization"}`))
				return
			}
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected call %s", proc)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	client := cloud.NewClient(cloud.Endpoint{URL: srv.URL}, cloud.Credential{Token: "rlat_session", Source: cloud.SourceLogin})
	declareVulnReadScope(client)
	for _, proc := range []string{procGetVulnerabilitySummary, procGetReleaseVulnerabilities, procGetVulnerability} {
		var out map[string]any
		if err := client.Call(context.Background(), proc, map[string]any{"project": "p"}, &out); err != nil {
			t.Fatalf("%s: %v", proc, err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(denied) != 0 {
		t.Errorf("the control plane refused %v; the reads declare vuln:read and elevate first (calls: %v)", denied, calls)
	}
	var exchanges int
	for _, c := range calls {
		if strings.HasPrefix(c, "controlplane.v1.AccessTokenService/ExchangeToken ") {
			exchanges++
		}
	}
	if exchanges != 1 {
		t.Errorf("%d exchanges, want one for the three reads (calls: %v)", exchanges, calls)
	}
}

// A caller that cannot declare scopes (a test's fake) is left untouched.
func TestDeclareVulnReadScopeIgnoresACallerThatCannotDeclare(t *testing.T) {
	declareVulnReadScope(fakeCloudCallerNoDeclare{})
}

type fakeCloudCallerNoDeclare struct{}

func (fakeCloudCallerNoDeclare) Call(context.Context, string, any, any) error { return nil }
