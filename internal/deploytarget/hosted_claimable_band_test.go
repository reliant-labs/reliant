package deploytarget

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/deploy"
	"github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

// The claimable plan's shape (125m / 128 MiB in forge's compiled-in copy) is
// off the 4 GiB-per-vCPU band by construction. The band is a billing rule and that plan is never billed,
// so a group judged under the claimable plan skips it and is held to the
// plan's caps instead; every other hosted group is still banded. The plan is
// the group's (ServiceGroup.HostedPlan), not a property of the credential, so
// a preview with no credential is judged exactly as the deploy will be.
func TestHostedPlanJudgesResourcesByThePlan(t *testing.T) {
	claimableShape := v1alpha1.Resources{
		CPURequestMillicores: 125, CPULimitMillicores: 125,
		MemoryRequestBytes: 128 << 20, MemoryLimitBytes: 128 << 20,
	}
	group := func(plan deploy.HostedPlan, r v1alpha1.Resources) ServiceGroup {
		g := hostedGroup("v1", map[string]string{"api": digestA}, r)
		g.HostedPlan = plan
		return g
	}

	_, err := planHosted(group(deploy.HostedPlanStandard, claimableShape))
	if err == nil || !strings.Contains(err.Error(), "shape band") {
		t.Fatalf("a normal hosted deploy of 125m/128Mi: err = %v, want the shape-band refusal", err)
	}
	// The refusal names the exact edit, on the band.
	if !strings.Contains(err.Error(), "fix: in deploy/kcl/") || !strings.Contains(err.Error(), "memoryRequestBytes = forge.mib(512)") {
		t.Errorf("the band refusal lacks the exact KCL edit:\n%v", err)
	}

	plan, err := planHosted(group(deploy.HostedPlanClaimable, claimableShape))
	if err != nil {
		t.Fatalf("a claimable deploy of 125m/128Mi must pass the plan: %v", err)
	}
	if len(plan) == 0 {
		t.Fatal("the claimable plan published nothing")
	}

	// The entry shape (250m / 1 GiB) is on the band and over the cap.
	_, err = planHosted(group(deploy.HostedPlanClaimable, v1alpha1.Resources{}))
	if err == nil || !strings.Contains(err.Error(), "1024 MiB of memory (at most 128 MiB, no burst)") ||
		!strings.Contains(err.Error(), "memoryLimitBytes = forge.mib(128)") {
		t.Fatalf("the claimable plan must refuse 1 GiB with the exact edit: err = %v", err)
	}

	// The caps the control plane published win over the compiled-in copy.
	g := group(deploy.HostedPlanClaimable, v1alpha1.Resources{CPURequestMillicores: 250, CPULimitMillicores: 250,
		MemoryRequestBytes: 256 << 20, MemoryLimitBytes: 256 << 20})
	if _, err := planHosted(g); err == nil {
		t.Fatal("250m/256Mi passed the compiled-in 125m/128Mi caps")
	}
	g.ClaimableLimits = deploy.ClaimableLimitsFallback()
	g.ClaimableLimits.WorkloadCPUMillicores, g.ClaimableLimits.WorkloadMemoryBytes = 250, 256<<20
	if _, err := planHosted(g); err != nil {
		t.Fatalf("250m/256Mi under published 250m/256Mi caps: %v", err)
	}
}
