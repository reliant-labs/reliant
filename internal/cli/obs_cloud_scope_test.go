package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/credentials"
)

// The bug (forge report f2c50fd8): every `forge obs` run against a hosted env
// sent its first read with the narrow stored login, was refused "this
// credential does not carry the obs:read scope", then exchanged for obs:read
// and retried. The elevated token lives in memory only, so each run cost one
// permission denial in the control plane (846 in a week in prod). The obs
// backend now declares obs:read for its reads, so the client exchanges first
// and the control plane refuses nothing.
func TestObsCloudElevatesBeforeTheFirstReadAndIsRefusedNothing(t *testing.T) {
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
		refuse := func(scope string) {
			denied = append(denied, proc)
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"code":"permission_denied","message":"this credential does not carry the ` + scope + ` scope"}`))
		}
		switch proc {
		case "controlplane.v1.AccessTokenService/ExchangeToken":
			var req struct {
				Scopes []string `json:"scopes"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			if bearer != "rlat_narrow" || len(req.Scopes) != 1 || req.Scopes[0] != "obs:read" {
				refuse("deploy:write")
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"secret": "rlat_obs_read",
				"token":  map[string]any{"expiresAt": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)},
			})
		case cloud.ProcGetOrganization:
			_, _ = w.Write([]byte(orgReplyJSON()))
		case procEnvs:
			// deploy:read, which the login carries.
			if bearer != "rlat_narrow" {
				refuse("deploy:read")
				return
			}
			_, _ = w.Write([]byte(`{"environments":[{"id":"env-prod","name":"prod","project":"obstarget"}]}`))
		case procListIssuesCloud:
			if bearer != "rlat_obs_read" {
				refuse("obs:read")
				return
			}
			_, _ = w.Write([]byte(`{"issues":[]}`))
		default:
			t.Errorf("unexpected call %s", proc)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	obsTargetProject(t, srv.URL)
	withProjectName(t, "obstarget")
	// A stored `forge login`, not a token handed to forge verbatim: the
	// credential elevation applies to.
	t.Setenv(cloud.DefaultTokenEnv, "")
	t.Setenv("FORGE_HOME", t.TempDir())
	path, err := cloud.CredentialsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := credentials.Store(path, srv.URL, cloud.ClientID, credentials.Credential{Token: "rlat_narrow", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	if _, err := runObsIssuesCloud(t); err != nil {
		t.Fatalf("obs issues: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(denied) != 0 {
		t.Errorf("the control plane refused %v; a declared scope is elevated for before the first call (calls: %v)", denied, calls)
	}
	var exchanges, reads int
	for _, c := range calls {
		switch {
		case strings.HasPrefix(c, "controlplane.v1.AccessTokenService/ExchangeToken "):
			exchanges++
		case c == procListIssuesCloud+" rlat_obs_read":
			reads++
		}
	}
	if exchanges != 1 || reads != 1 {
		t.Errorf("%d exchanges and %d elevated reads, want one of each (calls: %v)", exchanges, reads, calls)
	}
}

const procListIssuesCloud = "controlplane.v1.IssuesService/ListIssues"
