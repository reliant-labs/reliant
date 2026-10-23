package cli

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/doctor"
)

// hostedValidateBundle is hounders' prod shape (deploy/kcl/prod/main.k at
// houndersclub PR #1) on the workload model: a workload this project builds,
// a frontend on forge.OnHosted and a ManagedDatabase, all hosted, with cross-workload
// references in both directions and a managed secret. `api` is the
// fw.Workload's body, spliced in so a test can break exactly one thing.
func hostedValidateBundle(api string) string {
	return `    project = "hounders"
    env = "prod"
    control_plane = forge.ControlPlane { endpoint = "http://127.0.0.1:8090"}
    secret_provider = forge.HostedSecrets {}
    workloads = [fw.Workload {
        name = "api"
        image = "ghcr.io/acme/hounders"
        build = forge.GoBuild {cmd = "./cmd/hounders", output_name = "hounders"}
        args = ["api"]
        runtime = forge.OnHosted {}
` + api + `
    }]
    databases = [forge.ManagedDatabase {
        name = "hounders"
        runtime = forge.OnHosted {}
        spec = tiers.ManagedDatabase {storageGiB = 1}
    }]
    frontends = [forge.Frontend {
        name = "web"
        image = "ghcr.io/acme/web"
        path = "frontends/web"
        public_dir = "out"
        runtime = forge.OnHosted {}
        runtime_config = {
            API_URL = forge.WorkloadURL {workload = "api"}
            SITE_URL = forge.WorkloadURL {workload = "web"}
        }
    }]`
}

const hostedValidateAPI = `        ports = [fw.Port {name = "http", port = 8080, expose = True}]
        env = {
            AUTO_MIGRATE = "true"
            CORS_ORIGINS = forge.WorkloadURL {workload = "web"}
            DATABASE_URL = forge.DatabaseRef {name = "hounders"}
            STRIPE_SECRET_KEY = forge.ManagedSecret {name = "STRIPE_SECRET_KEY"}
        }`

// TestValidateKCL_HostedEnvIsJudgedByItsDeployPath is the houndersclub
// Deployability failure: `forge ci validate-kcl` reported
//
//	prod: render produced no k8s objects
//
// for a hosted env, whose empty manifest stream is CORRECT — the control
// plane runs it. It must pass, and say what it validated instead.
func TestValidateKCL_HostedEnvIsJudgedByItsDeployPath(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL; skipped in -short")
	}
	dir := workloadURLProject(t, "prod", hostedValidateBundle(hostedValidateAPI))
	t.Chdir(dir)

	out, err := runForge(t, "ci", "validate-kcl")
	if err != nil {
		t.Fatalf("validate-kcl refused a hosted env the deploy path admits: %v\n%s", err, out)
	}
	if !strings.Contains(out, "1 env(s) with hosted workloads, 3 hosted item(s) the control plane would admit") {
		t.Errorf("validate-kcl passed without saying what it validated for the hosted env:\n%s", out)
	}
}

// TestValidateKCL_BrokenHostedEnvIsStillCaught: judging a hosted env by its
// deploy path must not turn into skipping it. Each case is something
// `forge env deploy` would refuse, and each must fail validate-kcl with the
// deploy path's own words.
func TestValidateKCL_BrokenHostedEnvIsStillCaught(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL; skipped in -short")
	}
	cases := []struct {
		name, api, want string
	}{
		{
			// Off the hosted shape band: planHosted's CheckShapeBand.
			name: "off-band resources",
			api: `        ports = [fw.Port {name = "http", port = 8080, expose = True}]
        resources = fw.Resources {cpuRequestMillicores = 250, memoryRequestBytes = 134217728}`,
			want: "shape band",
		},
		{
			// A databaseRef nothing publishes: the pod would never start.
			name: "dangling databaseRef",
			api: `        ports = [fw.Port {name = "http", port = 8080, expose = True}]
        env = {DATABASE_URL = forge.DatabaseRef {name = "orders"}}`,
			want: `databaseRef "orders"`,
		},
		{
			// The site's API_URL names a workload with no exposed port: the
			// control plane's resolver refuses it permanently.
			name: "workloadURL to an unexposed workload",
			api:  `        ports = [fw.Port {name = "http", port = 8080}]`,
			want: "only a workload with an exposed port has a URL",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := workloadURLProject(t, "prod", hostedValidateBundle(tc.api))
			t.Chdir(dir)
			res := doctor.CheckDeployManifests(context.Background(), &doctor.Environment{ProjectDir: dir, DeployShaper: deployShapeOf})
			if res.Status != doctor.StatusFail {
				t.Fatalf("status = %s (%s), want fail: a hosted env the deploy path refuses passed validation", res.Status, res.Message)
			}
			if !strings.Contains(res.Evidence, "would refuse its hosted workloads before publishing anything") || !strings.Contains(res.Evidence, tc.want) {
				t.Errorf("evidence does not carry the deploy path's refusal (want %q):\n%s", tc.want, res.Evidence)
			}
			if _, err := runForge(t, "ci", "validate-kcl"); err == nil {
				t.Error("`forge ci validate-kcl` exited 0 on a hosted env `forge env deploy` would refuse")
			}
		})
	}
}

// TestValidateKCL_HostedEnvRenderingManifestsFails: a hosted deploy applies
// NOTHING to a cluster of forge's own, so a raw k8s object in an all-hosted
// env's output.manifests is one no deploy will ever create — the silent-drop
// shape CheckDeployManifests exists for.
func TestValidateKCL_HostedEnvRenderingManifestsFails(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL; skipped in -short")
	}
	dir := workloadURLProject(t, "prod", hostedValidateBundle(hostedValidateAPI)+
		"\n    additional_manifests = [{apiVersion = \"v1\", kind = \"ConfigMap\", metadata = {name = \"stray\"}}]")
	t.Chdir(dir)
	res := doctor.CheckDeployManifests(context.Background(), &doctor.Environment{ProjectDir: dir, DeployShaper: deployShapeOf})
	if res.Status != doctor.StatusFail || !strings.Contains(res.Evidence, "stray") {
		t.Fatalf("status = %s, evidence:\n%s\nwant a failure naming the object no hosted deploy applies", res.Status, res.Evidence)
	}
}

// TestDoctorDeploy_HostedEnvContentChecksReadThePlatformObjects: the content
// checks judge a hosted env by what the control plane RUNS
// (pkg/deploy.RenderWorkloads(Restricted) of each hosted workload), never by
// its empty stream.
//
// The Hounders defect was a hosted `api` that shipped with NO probes: the
// hosted path had no default. ADR 0002 §5 puts the default in one place, so a
// forge-built hosted service that declares nothing is probed (HTTP
// /readyz + /healthz), and so is a declared one — both pass Deploy Probes.
func TestDoctorDeploy_HostedEnvContentChecksReadThePlatformObjects(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL; skipped in -short")
	}
	declared := hostedValidateAPI + `
        probes = fw.Probes {readinessPath = "/healthz", livenessPath = "/healthz"}`
	for _, tc := range []struct{ name, api string }{
		{"defaulted probes pass", hostedValidateAPI},
		{"declared /healthz for both passes", declared},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := workloadURLProject(t, "prod", hostedValidateBundle(tc.api))
			// A real migration, so Deploy Migrations has to FIND the hosted
			// workload's AUTO_MIGRATE rather than skip for want of SQL.
			writeTestFile(t, filepath.Join(dir, "db", "migrations", "00001_init.up.sql"), "CREATE TABLE t (id TEXT PRIMARY KEY);\n")
			t.Chdir(dir)
			env := &doctor.Environment{ProjectDir: dir, DeployShaper: deployShapeOf}
			if probes := doctor.CheckDeployProbes(context.Background(), env); probes.Status != doctor.StatusPass {
				t.Fatalf("Deploy Probes = %s (%s)\n%s — a forge-built hosted service must ship probes", probes.Status, probes.Message, probes.Evidence)
			}
			// Everything else the platform runs for this workload is on the
			// happy path: forge's renderer sets requests/limits, sources
			// every credential from a Secret and binds its own SA.
			for name, check := range map[string]doctor.CheckFunc{
				"resources":       doctor.CheckDeployResources,
				"secrets":         doctor.CheckDeploySecrets,
				"service account": doctor.CheckDeployServiceAccount,
				"migrations":      doctor.CheckDeployMigrations,
			} {
				res := check(context.Background(), env)
				if res.Status == doctor.StatusFail || res.Status == doctor.StatusUnknown {
					t.Errorf("%s = %s on a well-formed hosted env: %s\n%s", name, res.Status, res.Message, res.Evidence)
				}
			}
			if res := doctor.CheckDeploySecrets(context.Background(), env); !strings.Contains(res.Message, "all sourced from a Secret") {
				t.Errorf("Deploy Secrets did not read the hosted workload's credentials (managedSecret, databaseRef): %s", res.Message)
			}
			if res := doctor.CheckDeployMigrations(context.Background(), env); res.Status != doctor.StatusPass {
				t.Errorf("Deploy Migrations = %s: the hosted workload's AUTO_MIGRATE was not read: %s", res.Status, res.Message)
			}
		})
	}
}
