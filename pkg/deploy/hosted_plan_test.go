package deploy

import (
	"errors"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

const mib = int64(1) << 20

// One entry point judges a hosted workload's resources by the plan it deploys
// under. The two rules disagree by design, and must never be confused: the
// shape the anonymous plan admits is off the band, and the band's smallest
// shape is over the anonymous cap.
func TestCheckHostedResources(t *testing.T) {
	t.Parallel()
	r := func(cpu, mem, cpuLim, memLim int64) v1alpha1.Resources {
		return v1alpha1.Resources{CPURequestMillicores: cpu, MemoryRequestBytes: mem, CPULimitMillicores: cpuLim, MemoryLimitBytes: memLim}
	}
	cases := []struct {
		name    string
		plan    HostedPlan
		in      v1alpha1.Resources
		wantFix string // "" = admitted
	}{
		{"standard: the entry shape is on the band", HostedPlanStandard, v1alpha1.Resources{}, ""},
		{"standard: 125m / 512Mi is on the band", HostedPlanStandard, r(125, 512*mib, 0, 0), ""},
		{"standard: 125m / 256Mi is off the band", HostedPlanStandard, r(125, 256*mib, 0, 0),
			"resources = fw.Resources {cpuRequestMillicores = 125, memoryRequestBytes = forge.mib(512)}"},
		{"standard: 100m rounds up to 125m", HostedPlanStandard, r(100, 256*mib, 0, 0),
			"resources = fw.Resources {cpuRequestMillicores = 125, memoryRequestBytes = forge.mib(512)}"},
		{"standard: a limit under the new request is raised", HostedPlanStandard, r(100, 256*mib, 100, 256*mib),
			"resources = fw.Resources {cpuRequestMillicores = 125, cpuLimitMillicores = 125, memoryRequestBytes = forge.mib(512), memoryLimitBytes = forge.mib(512)}"},
		{"claimable: 125m / 128Mi is admitted (no band)", HostedPlanClaimable, r(125, 128*mib, 0, 0), ""},
		{"claimable: 100m / 64Mi is admitted (no granularity)", HostedPlanClaimable, r(100, 64*mib, 0, 0), ""},
		{"claimable: the cap itself, no burst", HostedPlanClaimable, ClaimableLimitsFallback().Resources(), ""},
		{"claimable: the entry shape is over the cap", HostedPlanClaimable, v1alpha1.Resources{},
			"resources = fw.Resources {cpuRequestMillicores = 125, cpuLimitMillicores = 125, memoryRequestBytes = forge.mib(128), memoryLimitBytes = forge.mib(128)}"},
		{"claimable: 125m / 256Mi is over the memory cap", HostedPlanClaimable, r(125, 256*mib, 0, 0),
			"resources = fw.Resources {cpuRequestMillicores = 125, cpuLimitMillicores = 125, memoryRequestBytes = forge.mib(128), memoryLimitBytes = forge.mib(128)}"},
		{"claimable: a burst limit is over the cap", HostedPlanClaimable, r(125, 128*mib, 500, 0),
			"resources = fw.Resources {cpuRequestMillicores = 125, cpuLimitMillicores = 125, memoryRequestBytes = forge.mib(128), memoryLimitBytes = forge.mib(128)}"},
	}
	for _, c := range cases {
		err := CheckHostedResourcesWithin(c.plan, ClaimableLimitsFallback(), c.in)
		if c.wantFix == "" {
			if err != nil {
				t.Errorf("%s: refused: %v", c.name, err)
			}
			continue
		}
		var rr *ResourcesRefusal
		if !errors.As(err, &rr) {
			t.Errorf("%s: err = %v, want a *ResourcesRefusal", c.name, err)
			continue
		}
		if rr.Fix() != c.wantFix {
			t.Errorf("%s: fix = %s\n                   want %s", c.name, rr.Fix(), c.wantFix)
		}
		if !strings.Contains(err.Error(), c.plan.String()+" plan") {
			t.Errorf("%s: %q does not name the plan", c.name, err)
		}
		// The suggested fix is itself admitted: following it converges.
		if again := CheckHostedResourcesWithin(c.plan, ClaimableLimitsFallback(), rr.Want); again != nil {
			t.Errorf("%s: the suggested fix is refused too: %v", c.name, again)
		}
	}
}

func TestRunsBeforeRollout(t *testing.T) {
	t.Parallel()
	job := func(mod func(*v1alpha1.WorkloadSpec)) v1alpha1.WorkloadSpec {
		s := v1alpha1.WorkloadSpec{Kind: v1alpha1.KindJob}
		mod(&s)
		return s
	}
	for name, c := range map[string]struct {
		spec v1alpha1.WorkloadSpec
		want bool
	}{
		"standalone, default phase": {job(func(*v1alpha1.WorkloadSpec) {}), true},
		"standalone, pre-rollout":   {job(func(s *v1alpha1.WorkloadSpec) { s.DeployPhase = v1alpha1.DeployPhasePreRollout }), true},
		"standalone, post-rollout":  {job(func(s *v1alpha1.WorkloadSpec) { s.DeployPhase = v1alpha1.DeployPhasePostRollout }), false},
		"gating every workload":     {job(func(s *v1alpha1.WorkloadSpec) { s.Before = []string{v1alpha1.BeforeAll} }), true},
		"gating one workload":       {job(func(s *v1alpha1.WorkloadSpec) { s.Before = []string{"api"} }), true},
		"a service":                 {v1alpha1.WorkloadSpec{Kind: v1alpha1.KindService}, false},
	} {
		if got := RunsBeforeRollout(c.spec); got != c.want {
			t.Errorf("%s: RunsBeforeRollout = %v, want %v", name, got, c.want)
		}
	}
}

// The caps are the control plane's, not forge's: a published answer is
// judged by, whatever the compiled-in copy says.
func TestClaimableLimitsArePublishedNotCompiled(t *testing.T) {
	t.Parallel()
	published := ClaimableLimitsFallback()
	published.WorkloadCPUMillicores, published.WorkloadMemoryBytes = 250, 256*mib
	in := v1alpha1.Resources{CPURequestMillicores: 250, CPULimitMillicores: 250, MemoryRequestBytes: 256 * mib, MemoryLimitBytes: 256 * mib}
	if err := CheckHostedResourcesWithin(HostedPlanClaimable, published, in); err != nil {
		t.Errorf("a shape at the published cap is refused: %v", err)
	}
	if err := CheckHostedResourcesWithin(HostedPlanClaimable, ClaimableLimitsFallback(), in); err == nil {
		t.Error("the compiled-in fallback admitted a shape over its own cap")
	}
	if (ClaimableLimits{}).Valid() || !ClaimableLimitsFallback().Valid() {
		t.Error("Valid must refuse an empty answer and accept the fallback")
	}
}
