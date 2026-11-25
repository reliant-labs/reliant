package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/internal/templates"
)

// pgo-refresh.yml hands `forge pgo pull` an explicit token, FORGE_OBS_TOKEN,
// which is never elevated: it must carry every scope the pull's calls need on
// its own. Those calls are the cloud backend's org resolution and env lookup
// (DeployService, deploy:read) and the export (ExportPGOProfile, obs:read).
// The workflow documents templates.PGORefreshTokenScopes; this drives the real
// open-and-export path against a control plane that enforces each call's
// scope, with a token carrying exactly those scopes.
//
// An obs:read-only token (what the first version of the workflow asked for)
// is refused at the env lookup, before any profile is read.
func TestPGORefreshTokenScopesCoverPGOPull(t *testing.T) {
	documented := strings.Split(templates.PGORefreshTokenScopes, ",")
	tokens := map[string][]string{
		"rlat_documented": documented,
		"rlat_obs_only":   {"obs:read"},
	}
	need := func(proc string) string {
		switch {
		case proc == procExportPGOProfileCloud:
			return "obs:read"
		case strings.HasPrefix(proc, "controlplane.v1.DeployService/"):
			return "deploy:read"
		}
		return ""
	}
	var (
		mu     sync.Mutex
		denied []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proc := strings.TrimPrefix(r.URL.Path, "/")
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		w.Header().Set("Content-Type", "application/json")
		scope := need(proc)
		if scope == "" {
			t.Errorf("unexpected call %s", proc)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if !slices.Contains(tokens[bearer], scope) {
			mu.Lock()
			denied = append(denied, proc+" "+bearer)
			mu.Unlock()
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"code":"permission_denied","message":"this credential does not carry the ` + scope + ` scope"}`))
			return
		}
		switch proc {
		case cloud.ProcGetOrganization:
			_, _ = w.Write([]byte(orgReplyJSON()))
		case procEnvs:
			_, _ = w.Write([]byte(`{"environments":[{"id":"env-prod","name":"prod","project":"obstarget"}]}`))
		case procExportPGOProfileCloud:
			_, _ = w.Write([]byte(`{"edgeCount":0}`))
		default:
			t.Errorf("unexpected call %s", proc)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	obsTargetProject(t, srv.URL)
	withProjectName(t, "obstarget")

	export := func(token string) error {
		t.Setenv(cloud.DefaultTokenEnv, token)
		ctx := context.Background()
		c, err := openObsCloud(ctx, "prod")
		if err != nil {
			return err
		}
		now := time.Now()
		_, err = c.ExportPGOProfile(ctx, obsbackend.ExportPGORequest{
			Services: []string{"api"}, Namespace: "obstarget", Env: "prod",
			Range: obsbackend.Range{From: now.Add(-168 * time.Hour), To: now},
		})
		return err
	}

	if err := export("rlat_documented"); err != nil {
		t.Fatalf("a token with the documented scopes (%s) could not pull: %v (refused: %v)", templates.PGORefreshTokenScopes, err, denied)
	}
	if len(denied) != 0 {
		t.Errorf("the control plane refused %v for the documented token", denied)
	}
	if err := export("rlat_obs_only"); err == nil {
		t.Error("an obs:read-only token pulled; the fake no longer enforces the env lookup's deploy:read, so this test proves nothing")
	}
}

const procExportPGOProfileCloud = "controlplane.v1.ObservabilityService/ExportPGOProfile"
