package deploytarget

import (
	"context"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

// Every hosted refusal that means "this release cannot ship" must point at
// `forge env deploy <env>` — and must NOT point at
// `forge env build <env> --release <v> --no-build`.
//
// WHY THE NEGATIVE ASSERTION CARRIES THE WEIGHT. --no-build cuts a release
// over digests an earlier push already left in .forge/state. Every refusal
// below fires precisely when there are no such digests: no promoted release
// at all, a release pinning no artifact for this workload, an image nobody
// pushed. So the old advice asked the user to produce a release with no
// images in it, which either fails its own completeness gate or succeeds and
// re-ships the same hole. That exact text is what the owner hit on hounders
// prod from the UI (O-15). A test that only checked for the new string would
// still pass if the old one were left beside it.
func TestHostedRefusalsNameTheDeployFixAndNeverNoBuild(t *testing.T) {
	cases := map[string]func() error{
		// No promoted release: a backend workload with nothing to pin.
		"no promoted release": func() error {
			cp := &fakeCP{}
			return HostedProvider{Client: cp}.Deploy(context.Background(),
				hostedGroup("", nil, v1alpha1.Resources{}))
		},
		// A release that exists but pins no digest for this artifact.
		"release pins no artifact": func() error {
			cp := &fakeCP{}
			return HostedProvider{Client: cp}.Deploy(context.Background(),
				hostedGroup("v1", map[string]string{"other": digestA}, v1alpha1.Resources{}))
		},
		// The static-site twin of both of the above.
		"static site unbound": func() error {
			cp := &fakeCP{status: staticReadyStatus(digestB)}
			return HostedProvider{Client: cp}.Deploy(context.Background(), staticGroup("", nil))
		},
		"static site pins no artifact": func() error {
			cp := &fakeCP{status: staticReadyStatus(digestB)}
			return HostedProvider{Client: cp}.Deploy(context.Background(),
				staticGroup("v1", map[string]string{"other": digestB}))
		},
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			err := run()
			if err == nil {
				t.Fatal("expected a refusal")
			}
			got := err.Error()
			if strings.Contains(got, "--no-build") {
				t.Errorf("refusal still recommends --no-build, which cuts a release with no images in it:\n%s", got)
			}
			if !strings.Contains(got, "forge env deploy") {
				t.Errorf("refusal does not name `forge env deploy <env>` as the fix:\n%s", got)
			}
			if !strings.Contains(got, "<existing-version>") {
				t.Errorf("refusal does not offer the existing-release form:\n%s", got)
			}
		})
	}
}

// An image outside the org's push base is the one refusal whose primary fix is
// not "re-deploy": the declaration itself is wrong. It must say so — drop the
// host and let forge resolve it (ADR-0003 F1) — and still never say --no-build.
func TestHostedOffBaseImageRefusalNamesDroppingTheHost(t *testing.T) {
	err := checkImagePushBase("prod", "registry.reliant.dev/org-7", []hostedPlanItem{{
		Name: "api",
		Tier: HostedTierWorkload,
		Spec: v1alpha1.WorkloadSpec{Image: "ghcr.io/acme/api@" + digestA},
	}})
	if err == nil {
		t.Fatal("an image outside the push base must be refused")
	}
	got := err.Error()
	if strings.Contains(got, "--no-build") {
		t.Errorf("off-base refusal still recommends --no-build:\n%s", got)
	}
	for _, want := range []string{
		"drop the registry host",
		"registry.reliant.dev/org-7/api",
		"forge env deploy prod",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("off-base refusal is missing %q:\n%s", want, got)
		}
	}
}
