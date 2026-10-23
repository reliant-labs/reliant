package deploytarget

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

// preflightGroup is hounders' hosted shape as a group: a registry-less
// workload this project builds (it has no release yet, so no digest and no
// registry), a static site and a database, with references between them.
func preflightGroup(api v1alpha1.WorkloadSpec) ServiceGroup {
	return ServiceGroup{
		Env:        "prod",
		ProviderID: HostedProviderID,
		Services: []ResolvedService{
			{Name: "api", Hosted: &HostedWorkload{Tier: HostedTierWorkload, Artifact: "hounders", Workload: &api}},
			{Name: "web", Hosted: &HostedWorkload{Tier: HostedTierStatic, Artifact: staticSiteArtifact, Static: &v1alpha1.StaticSiteSpec{
				RuntimeConfig: map[string]v1alpha1.RuntimeConfigValue{"API_URL": {WorkloadURL: &v1alpha1.WorkloadURLRef{Name: "api"}}},
			}}},
			{Name: "hounders", Hosted: &HostedWorkload{Tier: HostedTierDatabase, Database: &v1alpha1.ManagedDatabaseSpec{StorageGiB: 1}}},
		},
	}
}

func publicBackend() v1alpha1.WorkloadSpec {
	return v1alpha1.WorkloadSpec{
		Kind: v1alpha1.KindService, Image: "hounders", Args: []string{"api"},
		Ports:  []v1alpha1.Port{{Name: "http", Port: 8080, Expose: true}},
		Probes: &v1alpha1.Probes{},
		Env: []v1alpha1.EnvVar{
			{Name: "CORS_ORIGINS", WorkloadURL: &v1alpha1.WorkloadURLRef{Name: "web"}},
			{Name: "DATABASE_URL", DatabaseRef: &v1alpha1.DatabaseRef{Name: "hounders"}},
		},
	}
}

// PreflightHosted runs the deploy's own plan with no release: the admitted
// set is every workload, with the backend pinned to the placeholder — never
// to anything a real deploy could mistake for a pin.
func TestPreflightHostedAdmitsWithoutARelease(t *testing.T) {
	items, err := PreflightHosted(preflightGroup(publicBackend()))
	if err != nil {
		t.Fatalf("PreflightHosted refused an admissible env: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("admitted %d workload(s), want 3", len(items))
	}
	for _, it := range items {
		if it.Workload != nil && !strings.HasPrefix(it.Workload.Image, preflightRegistry+"/hounders@"+preflightDigest) {
			t.Errorf("workload image = %q, want the placeholder pin", it.Workload.Image)
		}
	}
}

// Every rule the deploy's plan applies is applied here, and each refusal is
// the plan's own words. The reference checks are new: the platform would
// publish these specs and wait forever.
func TestPreflightHostedRefusesWhatTheDeployWould(t *testing.T) {
	cases := map[string]struct {
		mutate func(*ServiceGroup)
		want   string
	}{
		"off the shape band": {
			func(g *ServiceGroup) {
				// On the 125m grid, off the 4 GiB-per-vCPU band (250m needs 1 GiB).
				g.Services[0].Hosted.Workload.Resources = v1alpha1.Resources{CPURequestMillicores: 250, MemoryRequestBytes: 128 << 20, MemoryLimitBytes: 128 << 20}
			},
			"shape band",
		},
		"databaseRef to no database": {
			func(g *ServiceGroup) { g.Services = g.Services[:2] },
			`databaseRef "hounders"`,
		},
		"workloadURL to a workload that exposes nothing": {
			func(g *ServiceGroup) { g.Services[0].Hosted.Workload.Ports[0].Expose = false },
			"only a workload with an exposed port has a URL",
		},
		"workloadURL to an undeclared workload": {
			func(g *ServiceGroup) {
				g.Services[0].Hosted.Workload.Env[0].WorkloadURL.Name = "admin"
			},
			`references workload "admin", which this env does not publish`,
		},
		"a Full-only field (restricted profile)": {
			func(g *ServiceGroup) {
				g.Services[0].Hosted.Workload.NamespacedRBAC = []v1alpha1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get"}}}
			},
			"namespacedRBAC",
		},
		"a kind the platform does not run": {
			func(g *ServiceGroup) {
				g.Services[0].Hosted.Workload.Kind = v1alpha1.KindCron
				g.Services[0].Hosted.Workload.Schedule = "@hourly"
				g.Services[0].Hosted.Workload.Ports = nil
				g.Services[0].Hosted.Workload.Probes = nil
				g.Services = g.Services[:1]
				g.Services[0].Hosted.Workload.Env = nil
			},
			"cron",
		},
		"a workload name the CR cannot carry": {
			func(g *ServiceGroup) {
				g.Services[0].Name = "API_Server"
			},
			"RFC-1123",
		},
		"a gating job naming a workload the hosted set lacks": {
			func(g *ServiceGroup) {
				g.Services = append(g.Services, ResolvedService{Name: "migrate", Hosted: &HostedWorkload{Tier: HostedTierWorkload, Artifact: "hounders",
					Workload: &v1alpha1.WorkloadSpec{Kind: v1alpha1.KindJob, Image: "hounders", Args: []string{"db", "migrate", "up"}, Before: []string{"worker"}}}})
			},
			"worker",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			g := preflightGroup(publicBackend())
			tc.mutate(&g)
			_, err := PreflightHosted(g)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "refusing to publish anything") {
				t.Fatalf("err = %v, want the plan's refusal naming %q", err, tc.want)
			}
		})
	}
}

// A hosted JOB is published, not dropped: it is admitted as a Workload CR of
// kind job, pinned like any other workload, and rendered with the set, so
// its `before` gate resolves against the hosted workloads it names.
func TestPreflightHostedAdmitsAHostedJob(t *testing.T) {
	g := preflightGroup(publicBackend())
	g.Services = append(g.Services, ResolvedService{Name: "migrate", Hosted: &HostedWorkload{Tier: HostedTierWorkload, Artifact: "hounders",
		Workload: &v1alpha1.WorkloadSpec{Kind: v1alpha1.KindJob, Image: "hounders", Args: []string{"db", "migrate", "up"}, Before: []string{"api"}}}})
	items, err := PreflightHosted(g)
	if err != nil {
		t.Fatalf("PreflightHosted refused a hosted gating job: %v", err)
	}
	var job *HostedPreflightItem
	for i := range items {
		if items[i].Name == "migrate" {
			job = &items[i]
		}
	}
	if job == nil || job.Tier != HostedTierWorkload || job.Workload == nil || job.Workload.Kind != v1alpha1.KindJob {
		t.Fatalf("migrate not admitted as a workload-tier job: %+v", items)
	}
	if !strings.HasSuffix(job.Workload.Image, "@"+preflightDigest) {
		t.Errorf("job image %q is not digest-pinned", job.Workload.Image)
	}
}

// A bare hosted image resolves against the platform's push base, and the
// release ledger keys its artifact by that ADDRESS — host included — with
// releaseRegistries mapping the key to itself. Composing `registry + "/" +
// artifact` from that pair doubled the repository
// (`<base>/hounders/<base>/hounders@…`), which the control plane refused as
// ImageRejected for every hosted workload in prod. The key IS the repository.
func TestHostedPinsAnAddressKeyedArtifactOnce(t *testing.T) {
	const repo = "registry.example.com/55949942-3ee9-4cfc-bb7c-817bdba75186/hounders/hounders"
	api := publicBackend()
	g := preflightGroup(api)
	g.Services[0].Hosted.Artifact = repo
	g.Hosted = &HostedTarget{Release: "v1", PushBase: "registry.example.com/55949942-3ee9-4cfc-bb7c-817bdba75186/hounders",
		Digests:    map[string]string{repo: digestA, staticSiteArtifact: digestB},
		Registries: map[string]string{repo: repo}}
	plan, err := planHosted(g)
	if err != nil {
		t.Fatalf("planHosted: %v", err)
	}
	for _, item := range plan {
		spec, ok := item.Spec.(v1alpha1.WorkloadSpec)
		if !ok {
			continue
		}
		if want := repo + "@" + digestA; spec.Image != want {
			t.Fatalf("workload image = %q, want %q", spec.Image, want)
		}
		return
	}
	t.Fatal("plan carried no workload")
}

// The reference checks run at DEPLOY time too — the preflight is the plan,
// so a release-bound deploy of a dangling reference is refused before any
// RPC, not published to wait forever.
func TestHostedDeployRefusesADanglingReference(t *testing.T) {
	g := preflightGroup(publicBackend())
	g.Services = g.Services[:2] // no database for DATABASE_URL's databaseRef
	g.Hosted = &HostedTarget{Release: "v1", PushBase: "ghcr.io/acme",
		Digests:    map[string]string{"hounders": digestA, "web": digestB},
		Registries: map[string]string{"hounders": "ghcr.io/acme"}}
	if _, err := planHosted(g); err == nil || !strings.Contains(err.Error(), `databaseRef "hounders"`) {
		t.Fatalf("planHosted = %v, want the dangling databaseRef refused", err)
	}
}
