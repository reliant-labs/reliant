package cli

import (
	"strings"
	"testing"
)

// The contract: `forge env up` runs an env on THIS machine, and refuses one
// that runs anywhere else. These tests pin both halves — the refusal for a
// hosted env and for a remote cluster, and ACCEPTANCE for the local shapes,
// because a gate that rejects the dev env is worse than no gate at all.

// wantRefusal is the ADR's exact first line. Written out here rather than
// built from the helper under test, so a reworded message fails instead of
// agreeing with itself.
func wantRefusal(env string) string {
	return env + " is not a local environment — use 'forge env deploy " + env + "'"
}

func TestEnvUpRefusesHostedEnv(t *testing.T) {
	e := &KCLEntities{Workloads: []WorkloadEntity{
		{Name: "api", Runtime: RuntimeEntity{Type: RuntimeHosted}},
	}}
	v := classifyEnvLocality(e)
	if v.local() {
		t.Fatal("a hosted workload must make the env non-local")
	}
	err := refuseNonLocalEnvUp("prod", v)
	if err == nil {
		t.Fatal("want a refusal for a hosted env")
	}
	if !strings.HasPrefix(err.Error(), wantRefusal("prod")) {
		t.Errorf("refusal must OPEN with the exact contract line.\ngot:  %q\nwant prefix: %q", err.Error(), wantRefusal("prod"))
	}
	// Actionable: the message names which binding is not local.
	if !strings.Contains(err.Error(), "api") {
		t.Errorf("refusal should name the offending workload:\n%s", err.Error())
	}
}

func TestEnvUpRefusesRemoteCluster(t *testing.T) {
	e := &KCLEntities{Workloads: []WorkloadEntity{
		{Name: "api", Runtime: RuntimeEntity{
			Type:    RuntimeCluster,
			Cluster: &ClusterRuntime{Cluster: "gke_acme-prod_us-central1_prod", Namespace: "acme-prod"},
		}},
	}}
	v := classifyEnvLocality(e)
	if v.local() {
		t.Fatal("a workload on a remote kubectl context must make the env non-local")
	}
	err := refuseNonLocalEnvUp("staging", v)
	if err == nil {
		t.Fatal("want a refusal for a remote-cluster env")
	}
	if !strings.HasPrefix(err.Error(), wantRefusal("staging")) {
		t.Errorf("refusal must OPEN with the exact contract line.\ngot:  %q\nwant prefix: %q", err.Error(), wantRefusal("staging"))
	}
	if !strings.Contains(err.Error(), "gke_acme-prod_us-central1_prod") {
		t.Errorf("refusal should name the remote cluster:\n%s", err.Error())
	}
}

// TestEnvUpAcceptsLocalEnvs is the half that keeps the gate honest. Each of
// these is a shape `forge env up` has always run, and every one of them must
// stay runnable.
func TestEnvUpAcceptsLocalEnvs(t *testing.T) {
	cases := []struct {
		name string
		e    *KCLEntities
	}{
		{
			"host processes",
			&KCLEntities{Workloads: []WorkloadEntity{
				{Name: "api", Runtime: RuntimeEntity{Type: RuntimeHost, Host: &HostRuntime{Runner: "air"}}},
			}},
		},
		{
			"docker-compose",
			&KCLEntities{Workloads: []WorkloadEntity{
				{Name: "pg", Runtime: RuntimeEntity{Type: RuntimeCompose, Compose: &ComposeRuntime{Service: "pg"}}},
			}},
		},
		{
			"a k3d cluster",
			&KCLEntities{Workloads: []WorkloadEntity{
				{Name: "api", Runtime: RuntimeEntity{Type: RuntimeCluster, Cluster: &ClusterRuntime{Cluster: "k3d-acme", Namespace: "acme-dev"}}},
			}},
		},
		{
			"a kind cluster",
			&KCLEntities{Workloads: []WorkloadEntity{
				{Name: "api", Runtime: RuntimeEntity{Type: RuntimeCluster, Cluster: &ClusterRuntime{Cluster: "kind-acme"}}},
			}},
		},
		{
			"docker-desktop",
			&KCLEntities{Workloads: []WorkloadEntity{
				{Name: "api", Runtime: RuntimeEntity{Type: RuntimeCluster, Cluster: &ClusterRuntime{Cluster: "docker-desktop"}}},
			}},
		},
		{
			"a build-only workload",
			&KCLEntities{Workloads: []WorkloadEntity{
				{Name: "cli", Runtime: RuntimeEntity{Type: RuntimeBuildOnly, BuildOnly: &BuildOnlyDeploy{}}},
			}},
		},
		{
			"a frontend dev server",
			&KCLEntities{Frontends: []FrontendEntity{
				{Name: "web", Path: "frontends/web", Runtime: FrontendRuntime{Type: FrontendRuntimeHost}},
			}},
		},
		{
			// The scaffolded dev env: host service + dev server + a local
			// cluster target. The shape `forge env up dev` runs every day.
			"the scaffolded dev env",
			&KCLEntities{
				Workloads: []WorkloadEntity{
					{Name: "api", Runtime: RuntimeEntity{Type: RuntimeHost, Host: &HostRuntime{Runner: "air"}}},
					{Name: "migrate", Runtime: RuntimeEntity{Type: RuntimeCluster, Cluster: &ClusterRuntime{Cluster: "k3d-acme"}}},
				},
				Frontends:     []FrontendEntity{{Name: "web", Runtime: FrontendRuntime{Type: FrontendRuntimeHost}}},
				ClusterTarget: &ClusterTargetEntity{Cluster: "k3d-acme", Namespace: "acme-dev"},
			},
		},
		{
			// An env that declares nothing deployable still runs here —
			// `entitiesEmpty` is what reports that, with its own message.
			"an empty env",
			&KCLEntities{},
		},
		{
			"a nil render",
			nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if v := classifyEnvLocality(c.e); !v.local() {
				t.Errorf("`forge env up` must accept %s, got refusal reasons %v", c.name, v.reasons)
			}
		})
	}
}

// TestEnvUpRefusesRemoteClusterTarget: an env can bind no workload to a
// cluster and still place its support resources (Namespace, gateways) on a
// remote one via the env-wide target.
func TestEnvUpRefusesRemoteClusterTarget(t *testing.T) {
	e := &KCLEntities{
		Workloads:     []WorkloadEntity{{Name: "api", Runtime: RuntimeEntity{Type: RuntimeHost, Host: &HostRuntime{}}}},
		ClusterTarget: &ClusterTargetEntity{Cluster: "vke-608665d9-remote", Namespace: "acme-staging"},
	}
	if v := classifyEnvLocality(e); v.local() {
		t.Error("a remote env-wide cluster_target must make the env non-local")
	}
}

// TestEnvUpRefusesHostedDatabaseAndShippedFrontend covers the two non-workload
// bindings that also mean "runs somewhere else".
func TestEnvUpRefusesHostedDatabaseAndShippedFrontend(t *testing.T) {
	hostedDB := &KCLEntities{Databases: []DatabaseEntity{{Name: "main", Runtime: RuntimeHosted}}}
	if v := classifyEnvLocality(hostedDB); v.local() {
		t.Error("a hosted database must make the env non-local")
	}
	shipped := &KCLEntities{Frontends: []FrontendEntity{
		{Name: "web", Runtime: FrontendRuntime{Type: FrontendRuntimeFirebase}},
	}}
	if v := classifyEnvLocality(shipped); v.local() {
		t.Error("a frontend that ships to Firebase must make the env non-local")
	}
}

// TestEnvUpRefusalIsTotalWithDeployRefusal pins the pair: the two verbs name
// each other, so whichever one a user reaches for first tells them the other.
// A reworded pointer on either side strands them.
func TestEnvUpRefusalIsTotalWithDeployRefusal(t *testing.T) {
	up := refuseNonLocalEnvUp("prod", classifyEnvLocality(&KCLEntities{
		Workloads: []WorkloadEntity{{Name: "api", Runtime: RuntimeEntity{Type: RuntimeHosted}}},
	})).Error()
	if !strings.Contains(up, "forge env deploy prod") {
		t.Errorf("`env up`'s refusal must point at `forge env deploy prod`:\n%s", up)
	}
	down := refuseLocalEnvDeploy("dev").Error()
	if !strings.Contains(down, "forge env up dev") {
		t.Errorf("`env deploy`'s refusal must point at `forge env up dev`:\n%s", down)
	}
}
