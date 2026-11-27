package deploytarget

import (
	"context"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

// The release artifact key each hosted tier is bound under. The key is what
// the plan looks a digest up by, so the exact value per tier is a contract.

// TestHostedArtifactOf_PerTier is the unit half: the artifact key each tier
// is bound under.
//
// THE DATABASE ROW IS THE POINT. hostedArtifactOf used to fall through to
// `return name` for every non-Workload tier, so a ManagedDatabase got the
// deployment's own NAME as its artifact. That was latent while nothing sent
// the value; the moment F1 sends it, the control plane refuses it twice over
// (InvalidArgument in EnsureDeployment, and the schema's
// ck_cp_deployments_artifact_release_bound_tier behind that), so every env
// with a hosted database would fail to deploy.
func TestHostedArtifactOf_PerTier(t *testing.T) {
	t.Parallel()

	// A MANAGED DATABASE IS NEVER RELEASE-BOUND: a promotion moves a
	// Workload's digest or a StaticSite's release and nothing else.
	db := &HostedWorkload{Tier: HostedTierDatabase, Database: &v1alpha1.ManagedDatabaseSpec{}}
	if got := hostedArtifactOf("orders", db); got != "" {
		t.Errorf("a database's artifact = %q, want empty (it is never release-bound)", got)
	}
	// Even when a declaration NAMES one. Honouring it would only move the
	// platform's refusal to the far side of the wire, where the message is
	// worse.
	declared := &HostedWorkload{Tier: HostedTierDatabase, Artifact: "orders-db",
		Database: &v1alpha1.ManagedDatabaseSpec{}}
	if got := hostedArtifactOf("orders", declared); got != "" {
		t.Errorf("a database with a declared artifact = %q, want empty", got)
	}

	// A WORKLOAD is bound under its image repository.
	workload := &HostedWorkload{Tier: HostedTierWorkload,
		Workload: &v1alpha1.WorkloadSpec{Image: "ghcr.io/acme/api:v1"}}
	if got, want := hostedArtifactOf("api", workload), HostedArtifactName("ghcr.io/acme/api:v1"); got != want {
		t.Errorf("a workload's artifact = %q, want the image repo %q", got, want)
	}
	// A declared artifact wins for a release-bound tier: the ledger may
	// key the bytes under a name the image does not derive.
	pinned := &HostedWorkload{Tier: HostedTierWorkload, Artifact: "hounders",
		Workload: &v1alpha1.WorkloadSpec{Image: "ghcr.io/acme/api:v1"}}
	if got := hostedArtifactOf("api", pinned); got != "hounders" {
		t.Errorf("a declared workload artifact = %q, want %q", got, "hounders")
	}

	// A STATIC SITE is bound under its declared artifact — the repository
	// `forge build` recorded the site release under.
	static := &HostedWorkload{Tier: HostedTierStatic, Artifact: "ghcr.io/acme/web/site",
		Static: &v1alpha1.StaticSiteSpec{}}
	if got := hostedArtifactOf("web", static); got != "ghcr.io/acme/web/site" {
		t.Errorf("a static site's artifact = %q", got)
	}
	// With none declared it still falls back to the frontend NAME — but a
	// bare name is no longer a usable key for a site, because a site's
	// artifact key IS the repository the release was pushed to, and the
	// published spec hands it to the platform as the address to pull. So
	// the PLAN refuses it rather than publishing an unpullable spec; see
	// TestHostedStaticPlanRefusesAnUnaddressableArtifact.
	bare := &HostedWorkload{Tier: HostedTierStatic, Static: &v1alpha1.StaticSiteSpec{}}
	if got := hostedArtifactOf("web", bare); got != "web" {
		t.Errorf("a static site with no declared artifact = %q, want the name %q", got, "web")
	}
}

// TestHostedStaticPlanRefusesAnAnaddressableArtifact: a site whose artifact
// key names no registry host has no pullable address, so the plan refuses it
// before any RPC rather than publishing a digest the platform cannot locate.
//
// This is the defect's own failure mode moved one layer earlier: an
// unlocatable release used to be discovered by the operator, as a 404 on an
// artifact that existed.
//
// MUTATION VERIFIED RED: dropping the hostedImageNamesRegistry guard in
// planHostedWith → the bare key publishes and the refusal disappears.
func TestHostedStaticPlanRefusesAnUnaddressableArtifact(t *testing.T) {
	cp := &fakeCP{status: staticReadyStatus(digestB)}
	group := ServiceGroup{
		Env: "prod", ProviderID: HostedProviderID,
		Hosted:   &HostedTarget{Endpoint: "https://cp.example", Release: "v2", Digests: map[string]string{"web": digestB}, PushBase: "ghcr.io/acme"},
		Services: []ResolvedService{{Name: "web", Hosted: &HostedWorkload{Tier: HostedTierStatic, Static: &v1alpha1.StaticSiteSpec{}}}},
	}
	err := HostedProvider{Client: cp}.Deploy(context.Background(), group)
	if err == nil || !strings.Contains(err.Error(), "names no registry host") {
		t.Fatalf("err = %v, want a refusal naming the unaddressable artifact", err)
	}
	if n := len(cp.procs()); n != 0 {
		t.Fatalf("%d RPCs before the refusal", n)
	}
}
