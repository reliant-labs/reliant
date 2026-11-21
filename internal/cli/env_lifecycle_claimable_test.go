package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
)

// TestEnvLifecycle_UsesTheStoredClaimableCredential pins obs 81e743cf: an env
// this machine deployed with --claimable could not be deleted ("not signed in
// to Reliant on this machine") although its anonymous token was stored beside
// the claim link. Not signed in, the lifecycle verbs present that token.
func TestEnvLifecycle_UsesTheStoredClaimableCredential(t *testing.T) {
	var auth atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth.Store(r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"environments":[{"id":"env_9","name":"prod-web","project":"shop"}]}`))
	}))
	t.Cleanup(srv.Close)
	undeclaredDeleteProject(t, srv.URL)

	ep, err := cloud.ResolveEndpoint("prod-web", &cloud.Declaration{Endpoint: srv.URL})
	if err != nil {
		t.Fatal(err)
	}

	// Not signed in and no claimable deploy: the original refusal stands.
	if _, err := resolveDeclaredLifecycleTarget(t.Context(), "prod-web", "delete", "", ""); err == nil {
		t.Fatal("want the not-signed-in refusal with no stored claimable deploy")
	}

	now := time.Now()
	if _, err := cloud.StoreClaimable(ep, "shop", "prod-web", cloud.ClaimableDeploy{
		AccessToken: "rlat_anonymous0123456789", ClaimURL: "https://app.example/claim#rcl_x",
		ExpiresAt: now.Add(72 * time.Hour), PurgeAfter: now.Add(10 * 24 * time.Hour),
	}, now); err != nil {
		t.Fatal(err)
	}
	got, err := resolveDeclaredLifecycleTarget(t.Context(), "prod-web", "delete", "", "")
	if err != nil || got.EnvironmentID != "env_9" {
		t.Fatalf("target = %+v, %v; want env_9 via the stored claimable token", got, err)
	}
	if a, _ := auth.Load().(string); !strings.Contains(a, "rlat_anonymous0123456789") {
		t.Fatalf("Authorization = %q; want the stored claimable token", a)
	}

	// A token the caller names still wins over the stored one.
	if _, err := resolveDeclaredLifecycleTarget(t.Context(), "prod-web", "delete", "tok-explicit", ""); err != nil {
		t.Fatal(err)
	}
	if a, _ := auth.Load().(string); !strings.Contains(a, "tok-explicit") {
		t.Fatalf("Authorization = %q; want the --token credential", a)
	}
}
