package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/credentials"
)

func TestCloudIdentityWireUsesHumanProjectOwnershipAcrossOrgs(t *testing.T) {
	path := isolatedCredentials(t)
	t.Setenv("FORGE_CREDENTIAL_HELPER", "")
	t.Setenv("IDENTITY_TEST_TOKEN", "")
	t.Setenv(cloud.DefaultTokenEnv, "")
	calls := []string{}
	project, org := "", ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer human-identity" {
			t.Error("org switch changed login")
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/controlplane.v1.DeployService/ResolveHostedProject":
			if body["projectId"] != project || body["name"] != "shop" {
				t.Errorf("project resolution: %v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"projectId": project, "orgId": org, "name": "shop"})
		case "/controlplane.v1.DeployService/ListEnvironments":
			if r.Header.Get("Forge-Project-ID") != project {
				t.Error("environment read not project-bound")
			}
			if !reflect.DeepEqual(body, map[string]any{"project": "shop", "search": "prod"}) {
				t.Errorf("environment request: %v", body)
			}
			_, _ = w.Write([]byte(`{"environments":[{"id":"env","name":"prod","project":"shop"}]}`))
		case "/" + procIdentityStatus:
			if r.Header.Get("Forge-Project-ID") != project || !reflect.DeepEqual(body, map[string]any{"orgId": org, "environmentId": "env"}) {
				t.Errorf("identity request wrong ownership: %v", body)
			}
			_, _ = w.Write([]byte(`{"hasInstance":true,"state":"IDENTITY_STATE_READY"}`))
		default:
			t.Errorf("unexpected endpoint: %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	if err := credentials.Store(path, srv.URL, cloud.HumanClientID, credentials.Credential{Kind: "human", Token: "human-identity"}); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"A", "B"} {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "forge.yaml"), []byte("name: shop\n"), 0600); err != nil {
			t.Fatal(err)
		}
		t.Chdir(root)
		if err := os.MkdirAll(filepath.Join(root, ".forge"), 0700); err != nil {
			t.Fatal(err)
		}
		org, project = target, "project-"+target
		raw, _ := json.Marshal(map[string]string{srv.URL: project})
		if err := os.WriteFile(filepath.Join(root, hostedProjectBindingFile), raw, 0600); err != nil {
			t.Fatal(err)
		}
		identityTestTarget(t, srv.URL)
		calls = nil
		if out, err := executeIdentity("status", "prod", "--json"); err != nil {
			t.Fatalf("%s: %v %s", org, err, out)
		}
		want := []string{"/controlplane.v1.DeployService/ResolveHostedProject", "/controlplane.v1.DeployService/ListEnvironments", "/" + procIdentityStatus}
		if !reflect.DeepEqual(calls, want) {
			t.Fatalf("identity wire: %v want %v", calls, want)
		}
	}
}
