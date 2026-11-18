package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/credentials"
)

func TestHostedProjectBindingIdentityAcrossOrganizations(t *testing.T) {
	path := isolatedCredentials(t)
	t.Setenv("FORGE_CREDENTIAL_HELPER", "")
	t.Setenv(cloud.DefaultTokenEnv, "")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "forge.yaml"), []byte("name: shop\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	withProjectName(t, "shop")
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer same-human" {
			t.Error("org changed human credential")
		}
		if r.URL.Path != "/controlplane.v1.DeployService/ResolveHostedProject" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		if req["projectId"] == "" && req["orgId"] == nil {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"code":"failed_precondition","message":"first deployment requires --org"}`))
			return
		}
		org, _ := req["orgId"].(string)
		if org == "" {
			org = "A"
		}
		if req["projectId"] == "project-A" && org != "A" {
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"code":"permission_denied","message":"owner immutable"}`))
			return
		}
		_, _ = w.Write([]byte(`{"projectId":"project-` + org + `","name":"shop","orgId":"` + org + `"}`))
	}))
	defer srv.Close()
	if err := credentials.Store(path, srv.URL, cloud.HumanClientID, credentials.Credential{Kind: "human", Token: "same-human"}); err != nil {
		t.Fatal(err)
	}
	decl := &cloud.Declaration{Endpoint: srv.URL}
	if _, err := bindHostedProject(context.Background(), "prod", decl, ""); err == nil || !strings.Contains(err.Error(), "--org") {
		t.Fatalf("ambiguous unbound deploy must not prompt/guess: %v", err)
	}
	ep, err := bindHostedProject(context.Background(), "prod", decl, "A")
	if err != nil || ep.ProjectID != "project-A" {
		t.Fatalf("bind: %+v %v", ep, err)
	}
	if _, err := bindHostedProject(context.Background(), "prod", decl, "B"); err == nil {
		t.Fatal("org switch changed project owner")
	}
	ep, err = projectEndpoint("prod", decl)
	if err != nil || ep.ProjectID != "project-A" {
		t.Fatalf("owner binding lost: %+v %v", ep, err)
	}
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "forge.yaml"), []byte("name: shop\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(other)
	ep, err = bindHostedProject(context.Background(), "prod", decl, "B")
	if err != nil || ep.ProjectID != "project-B" {
		t.Fatalf("same identity could not bind B in another project: %+v %v", ep, err)
	}
	c, err := cloud.ResolveCredential("", ep)
	if err != nil || c.Token != "same-human" || c.Kind != "human" {
		t.Fatalf("per-org human credential introduced: %+v %v", c, err)
	}
	if calls != 3 {
		t.Fatalf("unexpected wire sequence: %d", calls)
	}
}

// A delegated capability (a CI/automation token) is scoped server-side and
// keeps the GetTenant contract: binding must neither call the control plane
// nor write a project binding, and --org still cannot retarget it.
func TestHostedProjectBindingSkipsDelegatedCapability(t *testing.T) {
	path := isolatedCredentials(t)
	t.Setenv("FORGE_CREDENTIAL_HELPER", "")
	t.Setenv(cloud.DefaultTokenEnv, "")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "forge.yaml"), []byte("name: shop\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	withProjectName(t, "shop")
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		t.Errorf("delegated binding called the control plane: %s", r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	if err := credentials.Store(path, srv.URL, cloud.ClientID, credentials.Credential{Token: "rlat_delegated"}); err != nil {
		t.Fatal(err)
	}
	decl := &cloud.Declaration{Endpoint: srv.URL}
	ep, err := bindHostedProject(context.Background(), "prod", decl, "")
	if err != nil || ep.ProjectID != "" {
		t.Fatalf("delegated bind = %+v, %v; want no project identity and no error", ep, err)
	}
	if _, err := bindHostedProject(context.Background(), "prod", decl, "A"); err == nil || !strings.Contains(err.Error(), "delegated capability") {
		t.Fatalf("--org with a delegated capability must be refused: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, hostedProjectBindingFile)); !os.IsNotExist(err) {
		t.Fatalf("delegated bind wrote %s: %v", hostedProjectBindingFile, err)
	}
	if calls != 0 {
		t.Fatalf("control plane calls = %d, want 0", calls)
	}
}
