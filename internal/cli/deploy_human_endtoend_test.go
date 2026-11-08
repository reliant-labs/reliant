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
	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/pkg/credentials"
)

// Run actual command routing, build/cut (fixture builder), file ledger promotion
// and follow-through through the late hosted binding check. No real publish.
func TestRealHumanDeployCompletesFirstOwnerBindingFollowThrough(t *testing.T) {
	if testing.Short() {
		t.Skip("executes deploy build/cut fixture orchestration; full scoped test")
	}
	for _, versioned := range []bool{false, true} {
		t.Run(map[bool]string{false: "no-version", true: "versioned"}[versioned], func(t *testing.T) {
			path := isolatedCredentials(t)
			t.Setenv(cloud.DefaultTokenEnv, "")
			t.Setenv("FORGE_CREDENTIAL_HELPER", "")
			creates := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/controlplane.v1.DeployService/ResolveHostedProject":
					creates++
					if body["orgId"] != "A" || body["create"] != true {
						t.Errorf("creation request %v", body)
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"projectId": "project-A", "orgId": "A", "name": body["name"]})
				default:
					t.Errorf("unexpected RPC %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer srv.Close()
			dir := deployAllProject(t)
			t.Chdir(dir)
			// Keep the real release ledger locally for this fixture while the workload
			// still traverses the real hosted identity bootstrap and publish dispatch.
			t.Setenv(ledgerSelectionEnv, string(ledgerSelectMachine))
			render := strings.ReplaceAll(deployTestEnvRender, `"type":"cluster","cluster":"k3d-dev","namespace":"dev"`, `"type":"hosted"`)
			render = strings.Replace(render, `"output":{`, `"output":{"control_plane":{"type":"control_plane","endpoint":"`+srv.URL+`"},`, 1)
			ledger := testLedger(t, dir)
			// Establish the private ledger BEFORE the actual command captures
			// its stores; testStore must not change the ledger home mid-build.
			_ = ledger
			stubDeployCheckout(t, deployTestProvenance(), render)
			cut := func(version string) error {
				r := ociRelease(version, map[string]string{deployTestImage: sha("1")})
				r.SetProvenance(deployTestProvenance())
				for key, artifact := range r.Artifacts {
					artifact.Platforms = []string{"linux/amd64"}
					r.Artifacts[key] = artifact
				}
				_, err := ledger.Releases.Cut(context.Background(), r)
				return err
			}
			oldBuild := runDeployBuild
			runDeployBuild = func(_ context.Context, opts buildOptions) error { return cut(opts.release) }
			t.Cleanup(func() { runDeployBuild = oldBuild })
			if err := credentials.Store(path, srv.URL, cloud.HumanClientID, credentials.Credential{Kind: "human", Token: "human"}); err != nil {
				t.Fatal(err)
			}
			f := deployCmdFlags{organization: "A", rolloutMode: string(cluster.RolloutSkip)}
			f.promote.run.None = true
			f.promote.yes = true
			if versioned {
				if err := cut("v1.2.3"); err != nil {
					t.Fatal(err)
				}
				f.promote.version = "v1.2.3"
			}
			calls := 0
			old := runPromoteClientDeploy
			runPromoteClientDeploy = func(ctx context.Context, env string, opts deployOptions) error {
				if opts.preflightOnly {
					return nil
				}
				calls++
				if opts.organization != "" {
					t.Fatalf("real follow-through retained creation selector %q", opts.organization)
				}
				opts.frontendsOnly = true
				handled, err := dispatchHostedDeploy(ctx, dir, env, opts)
				if !handled || err == nil || !strings.Contains(err.Error(), "--frontends-only is not supported") {
					t.Fatalf("late binding failed: %v %v", handled, err)
				}
				return nil
			}
			t.Cleanup(func() { runPromoteClientDeploy = old })
			// Capacity is a read only dependency; fixture is intentionally static.
			fake := &capacityFake{capacity: map[string]any{"allowed": true}}
			oldClient := hostedDeployClient
			hostedDeployClient = func(cloud.Endpoint, cloud.Credential) deploytarget.HostedCaller { return fake }
			t.Cleanup(func() { hostedDeployClient = oldClient })
			if err := dispatchDeployCmd(context.Background(), "prod", f); err != nil {
				t.Fatal(err)
			}
			if creates != 1 || calls != 1 {
				t.Fatalf("incomplete real followthrough creates=%d publish=%d", creates, calls)
			}
			if _, err := os.Stat(filepath.Join(dir, hostedProjectBindingFile)); err != nil {
				t.Fatal(err)
			}
			// A separate invocation begins bound: even the same owner flag is
			// no longer an unbound-creation instruction, and must be refused
			// before another promotion or late publish.
			if err := dispatchDeployCmd(context.Background(), "prod", f); err == nil || !strings.Contains(err.Error(), "only for unbound creation") {
				t.Fatalf("already-bound real deploy accepted --org: %v", err)
			}
			if creates != 1 || calls != 1 {
				t.Fatal("already-bound refusal crossed a publish effect")
			}
		})
	}
}
