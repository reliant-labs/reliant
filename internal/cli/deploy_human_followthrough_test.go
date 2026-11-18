package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/credentials"
)

// Continue past bootstrap and target reads through the real follow-through and
// late hosted dispatch. Only the final provider publish is stopped, after its
// project binding check, so this catches reapplying the unbound-only selector.
func TestHumanFirstDeploySelectorConsumedBeforeLatePublish(t *testing.T) {
	for _, versioned := range []bool{false, true} {
		t.Run(map[bool]string{false: "build-cut", true: "versioned"}[versioned], func(t *testing.T) {
			path := isolatedCredentials(t)
			t.Setenv(cloud.DefaultTokenEnv, "")
			t.Setenv("FORGE_CREDENTIAL_HELPER", "")
			creates, reads, lateResolves := 0, 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/controlplane.v1.DeployService/ResolveHostedProject":
					if body["create"] == true {
						creates++
						if body["orgId"] != "A" {
							t.Error("creation owner lost")
						}
					} else {
						lateResolves++
						if body["orgId"] != nil {
							t.Error("creation selector repeated on bound project")
						}
					}
					_, _ = w.Write([]byte(`{"projectId":"project-A","orgId":"A","name":"pt"}`))
				case "/controlplane.v1.DeployService/CheckDeployCapacity":
					reads++
					if creates != 1 || r.Header.Get("Forge-Project-ID") != "project-A" {
						t.Error("target read did not follow binding")
					}
					_, _ = w.Write([]byte(`{"allowed":true}`))
				default:
					t.Errorf("unexpected operation %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer srv.Close()
			root := planProject(t, strings.ReplaceAll(hostedStaticPlanFixture, "http://127.0.0.1:1", srv.URL))
			if err := credentials.Store(path, srv.URL, cloud.HumanClientID, credentials.Credential{Kind: "human", Token: "human"}); err != nil {
				t.Fatal(err)
			}
			f := deployCmdFlags{organization: "A"}
			if versioned {
				f.promote.version = "v1.2.3"
			}
			if err := prepareHostedProjectForWrite(context.Background(), root, "prod", &f); err != nil {
				t.Fatal(err)
			}
			if f.organization != "" {
				t.Fatal("successful bootstrap did not consume creation-only selector")
			}
			ep, err := projectEndpoint("prod", &cloud.Declaration{Endpoint: srv.URL})
			if err != nil {
				t.Fatal(err)
			}
			if err := cloud.NewClient(ep, cloud.Credential{Kind: "human", Token: "human"}).Call(context.Background(), "controlplane.v1.DeployService/CheckDeployCapacity", map[string]any{}, nil); err != nil {
				t.Fatal(err)
			}
			old := runPromoteClientDeploy
			runPromoteClientDeploy = func(ctx context.Context, env string, opts deployOptions) error {
				// This flag stops immediately AFTER the real late dispatch's binding
				// validation, before any provider/deployment effect.
				opts.frontendsOnly = true
				handled, err := dispatchHostedDeploy(ctx, root, env, opts)
				if !handled || err == nil || !strings.Contains(err.Error(), "--frontends-only is not supported") {
					t.Fatalf("late publish failed before bound target validation: handled=%v err=%v", handled, err)
				}
				return nil
			}
			t.Cleanup(func() { runPromoteClientDeploy = old })
			if err := applyHostedPublish(context.Background(), "prod", promoteFollowOptions{clientDeploy: deployOptions{organization: f.organization}}); err != nil {
				t.Fatal(err)
			}
			if creates != 1 || reads != 1 || lateResolves != 0 {
				t.Fatalf("incomplete follow-through: creates=%d reads=%d late=%d", creates, reads, lateResolves)
			}
			// On a later invocation the same flag must still be refused before reads.
			before := reads
			again := deployCmdFlags{organization: "A"}
			if err := prepareHostedProjectForWrite(context.Background(), root, "prod", &again); err == nil || !strings.Contains(err.Error(), "only for unbound creation") {
				t.Fatalf("preexisting binding accepted --org: %v", err)
			}
			if reads != before {
				t.Fatal("preexisting-bound refusal performed target reads")
			}
		})
	}
}
