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

func TestRealDeployWritePathsBindBeforeTargetReads(t *testing.T) {
	for _, versioned := range []bool{false, true} {
		for _, multi := range []bool{false, true} {
			for _, owner := range []string{"", "A"} {
				name := map[bool]string{false: "build-cut", true: "versioned"}[versioned] + "/" + map[bool]string{false: "single", true: "multiple"}[multi] + "/owner=" + owner
				t.Run(name, func(t *testing.T) {
					path := isolatedCredentials(t)
					t.Setenv(cloud.DefaultTokenEnv, "")
					t.Setenv("FORGE_CREDENTIAL_HELPER", "")
					calls := []string{}
					created := false
					srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls = append(calls, r.URL.Path)
						w.Header().Set("Content-Type", "application/json")
						var body map[string]any
						_ = json.NewDecoder(r.Body).Decode(&body)
						if r.Header.Get("Authorization") != "Bearer human" {
							t.Error("human bearer lost")
						}
						if r.URL.Path == "/controlplane.v1.DeployService/ResolveHostedProject" {
							requestedOwner, _ := body["orgId"].(string)
							if len(calls) == 1 && (body["create"] != true || requestedOwner != owner) {
								t.Errorf("first request must create/resolve with explicitowner: %v", body)
							}
							if multi && owner == "" {
								w.WriteHeader(400)
								_, _ = w.Write([]byte(`{"code":"failed_precondition","message":"first deployment requires --org"}`))
								return
							}
							created = true
							_, _ = w.Write([]byte(`{"projectId":"project-A","orgId":"A","name":"pt"}`))
							return
						}
						if !created {
							t.Errorf("target read before bind: %s", r.URL.Path)
						}
						// Stop after proving the real entrypoint's first target-dependent read;
						// no build, registry or promotion is allowed in this unit fixture.
						w.WriteHeader(403)
						_, _ = w.Write([]byte(`{"code":"permission_denied","message":"stop after project bootstrap"}`))
					}))
					defer srv.Close()
					render := strings.ReplaceAll(hostedStaticPlanFixture, "http://127.0.0.1:1", srv.URL)
					root := planProject(t, render)
					if err := os.WriteFile(filepath.Join(root, "forge.yaml"), []byte("name: pt\nmodule_path: example.com/pt\n"), 0600); err != nil {
						t.Fatal(err)
					}
					markServiceProject(t, root)
					if err := credentials.Store(path, srv.URL, cloud.HumanClientID, credentials.Credential{Kind: "human", Token: "human"}); err != nil {
						t.Fatal(err)
					}
					f := deployCmdFlags{organization: owner}
					if versioned {
						f.promote.version = "v1.2.3"
					}
					err := dispatchDeployCmd(context.Background(), "prod", f)
					if err == nil {
						t.Fatal("fixture must stop before actual deployment")
					}
					if len(calls) == 0 || calls[0] != "/controlplane.v1.DeployService/ResolveHostedProject" {
						t.Fatalf("no first bind: %v", calls)
					}
					if multi && owner == "" {
						if created || len(calls) != 1 {
							t.Fatal("ambiguous owner crossed bootstrap boundary")
						}
					} else {
						if !created {
							t.Fatal("project never created")
						}
						ep, e := projectEndpoint("prod", &cloud.Declaration{Endpoint: srv.URL})
						if e != nil || ep.ProjectID != "project-A" {
							t.Fatalf("binding absent: %+v %v", ep, e)
						}
					}
				})
			}
		}
	}
}

func TestRealDeployPreviewsDoNotCreateProjectBinding(t *testing.T) {
	for _, mode := range []string{"dry-run", "explain", "version-plan"} {
		t.Run(mode, func(t *testing.T) {
			path := isolatedCredentials(t)
			t.Setenv(cloud.DefaultTokenEnv, "")
			t.Setenv("FORGE_CREDENTIAL_HELPER", "")
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				if body["create"] == true {
					t.Errorf("preview created a hosted project: %s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"code":"not_found","message":"unbound preview target"}`))
			}))
			defer srv.Close()
			root := planProject(t, strings.ReplaceAll(hostedStaticPlanFixture, "http://127.0.0.1:1", srv.URL))
			if err := credentials.Store(path, srv.URL, cloud.HumanClientID, credentials.Credential{Kind: "human", Token: "human"}); err != nil {
				t.Fatal(err)
			}
			f := deployCmdFlags{organization: "A"}
			switch mode {
			case "dry-run":
				f.dryRun = true
			case "explain":
				f.explain = true
			case "version-plan":
				f.promote.version = "v1.2.3"
				f.promote.plan = true
			}
			_ = dispatchDeployCmd(context.Background(), "prod", f)
			if _, err := os.Stat(filepath.Join(root, hostedProjectBindingFile)); !os.IsNotExist(err) {
				t.Fatal("real preview wrote target binding")
			}
		})
	}
}

func TestDeployPreviewDoesNotCreateProjectBinding(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	for _, f := range []deployCmdFlags{{dryRun: true}, {explain: true}, {promote: promoteCmdFlags{plan: true}}, {promote: promoteCmdFlags{planOnly: true}}} {
		if err := prepareHostedProjectForWrite(context.Background(), root, "prod", &f); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, hostedProjectBindingFile)); !os.IsNotExist(err) {
		t.Fatal("preview created a target binding")
	}
}
