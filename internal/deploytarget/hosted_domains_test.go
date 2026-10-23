package deploytarget

import (
	"context"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

// domainGroup is a hosted group whose specs CARRY custom domains — the shape
// the backstop exists to refuse. Nothing in forge builds this any more (the
// authoring schema has no frontend `domains`, and the render refuses a
// hosted port's), so it is constructed directly here: that is exactly the
// out-of-band path the backstop covers.
func domainGroup() ServiceGroup {
	keep := int32(10)
	return ServiceGroup{
		Env: "prod", ProviderID: HostedProviderID,
		Hosted: &HostedTarget{Endpoint: "https://cp.example", Release: "v1", PushBase: "ghcr.io/acme",
			Digests: map[string]string{staticSiteArtifact: digestA, "membership": digestB}},
		Services: []ResolvedService{
			{Name: "web", Hosted: &HostedWorkload{Tier: HostedTierStatic, Artifact: staticSiteArtifact, Static: &v1alpha1.StaticSiteSpec{
				KeepReleases: &keep, Domains: []string{"hounders.club", "www.hounders.club"},
			}}},
			{Name: "membership", Hosted: &HostedWorkload{Tier: HostedTierWorkload, Artifact: "membership", Workload: &v1alpha1.WorkloadSpec{
				Kind: v1alpha1.KindService, Image: "ghcr.io/acme/membership:v1", Args: []string{"serve"},
				Probes: &v1alpha1.Probes{},
				Ports: []v1alpha1.Port{{Name: "http", Port: 8080, Expose: true,
					Domains: []string{"api.hounders.club"}}},
			}}},
		},
	}
}

func domainProvider(cp HostedCaller) HostedProvider {
	return HostedProvider{Client: cp, Rollout: cluster.RolloutPolicy{Mode: cluster.RolloutSkip}}
}

// THE SAFETY PROPERTY. On hosted a custom hostname is a bound control-plane
// resource, so one carried in a published spec is a field nothing reads: the
// deploy goes green and the hostname never resolves, with nothing anywhere
// saying why. forge refuses BEFORE writing anything, names every domain, and
// points at the two commands that do work.
//
// The render refuses this first (kcl/render.k `_hosted_domain_violations`);
// this is the backstop for a plan that did not come from that lowering.
//
// MUTATIONS VERIFIED RED:
//   - dropping the checkCustomDomains call from Deploy → the deploy succeeds
//     and publishes;
//   - moving it after publish() → EnsureDeployment appears in the call log.
func TestHostedDeployRefusesDomainsInSpec(t *testing.T) {
	cp := &fakeCP{status: readyStatus(digestA)}
	err := domainProvider(cp).Deploy(context.Background(), domainGroup())
	if err == nil {
		t.Fatal("the deploy published a custom domain carried in spec")
	}
	for _, want := range []string{
		"hounders.club", "www.hounders.club", "api.hounders.club",
		"refusing to publish anything", "a domain is not spec",
		// The worked example uses the first claim, so the author can
		// copy a line that runs rather than a placeholder.
		"forge domain add api.hounders.club",
		"forge domain bind api.hounders.club --env prod --target membership",
		"forge.OnCluster keeps `Port.domains`",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not mention %q:\n%v", want, err)
		}
	}
	// Refused BEFORE any write.
	for _, proc := range cp.procs() {
		if proc == "EnsureDeployment" || proc == "PublishDeploymentConfig" {
			t.Fatalf("refused, but %s was called: %v", proc, cp.procs())
		}
	}
}

// The refusal is scoped to specs that actually carry a domain: an ordinary
// hosted env must keep deploying, which is every env now that the field is
// gone from the authoring schema.
func TestCheckCustomDomainsScope(t *testing.T) {
	noDomains, err := planHosted(hostedGroup("v1", map[string]string{"api": digestA}, v1alpha1.Resources{}))
	if err != nil {
		t.Fatalf("plan without domains: %v", err)
	}
	if err := checkCustomDomains("prod", noDomains); err != nil {
		t.Errorf("an env carrying no domains was refused: %v", err)
	}
}

// --dry-run must not be the one path that goes quiet about a declaration a
// real deploy refuses.
func TestHostedPlanPrintReportsDeclaredDomains(t *testing.T) {
	plan, err := planHosted(domainGroup())
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	claims := hostedDomainClaims(plan)
	if len(claims) != 2 {
		t.Fatalf("hostedDomainClaims = %+v, want one per workload", claims)
	}
	if claims[0].Workload != "membership" || claims[1].Workload != "web" {
		t.Errorf("claims are not sorted by workload: %+v", claims)
	}
	if got := strings.Join(claims[1].Domains, ","); got != "hounders.club,www.hounders.club" {
		t.Errorf("web's domains = %q", got)
	}
}

// ─── Status decoding ─────────────────────────────────────────────────────────

const domainStatusBody = `{"deployments":[{"deployment":{"id":"dep-web","name":"web","tier":"DEPLOY_TIER_STATIC",
 "observed":{"state":"DEPLOY_OBSERVED_STATE_READY","url":"https://web.example","domains":[
   {"domain":"hounders.club","state":"DEPLOY_CUSTOM_DOMAIN_STATE_PENDING_DNS","requiredRecords":[
      {"type":"A","name":"hounders.club","value":"34.63.203.181"},
      {"type":"TXT","name":"_forge-challenge.hounders.club","value":"tok-123"}]},
   {"domain":"www.hounders.club","state":"DEPLOY_CUSTOM_DOMAIN_STATE_LIVE","liveSince":"2026-02-01T10:00:00Z"},
   {"domain":"shop.hounders.club","state":"DEPLOY_CUSTOM_DOMAIN_STATE_FAILED","lastError":"CAA record forbids this issuer"},
   {"domain":"blog.hounders.club","state":"DEPLOY_CUSTOM_DOMAIN_STATE_SOMETHING_NEW"}]}},
 "verdict":"DEPLOY_VERDICT_CONVERGED"}]}`

// ReadHostedStatus carries each declared domain's state, the records still
// required, and the last error — and decodes a state name this binary has
// never heard of as "unknown" rather than guessing at it.
func TestReadHostedStatusDecodesCustomDomains(t *testing.T) {
	cp := &fakeCP{
		envs:   `{"environments":[{"id":"env-1","name":"prod"}]}`,
		status: func(int) string { return domainStatusBody },
	}
	st, err := ReadHostedStatus(context.Background(), cp, "", "prod")
	if err != nil {
		t.Fatalf("ReadHostedStatus: %v", err)
	}
	if len(st.Workloads) != 1 {
		t.Fatalf("workloads = %+v", st.Workloads)
	}
	got := st.Workloads[0].Domains
	if len(got) != 4 {
		t.Fatalf("domains = %+v, want 4", got)
	}
	want := []struct {
		domain, state, lastError, liveSince string
		records                             int
	}{
		{"hounders.club", DomainStatePendingDNS, "", "", 2},
		{"www.hounders.club", DomainStateLive, "", "2026-02-01T10:00:00Z", 0},
		{"shop.hounders.club", DomainStateFailed, "CAA record forbids this issuer", "", 0},
		// A state from a newer control plane must NOT read as live.
		{"blog.hounders.club", DomainStateUnknown, "", "", 0},
	}
	for i, w := range want {
		g := got[i]
		if g.Domain != w.domain || g.State != w.state || g.LastError != w.lastError || g.LiveSince != w.liveSince || len(g.RequiredRecords) != w.records {
			t.Errorf("domain[%d] = %+v, want %+v", i, g, w)
		}
	}
	if r := got[0].RequiredRecords[0]; r.Type != "A" || r.Name != "hounders.club" || r.Value != "34.63.203.181" {
		t.Errorf("first required record = %+v", r)
	}
}

// The printed block names each domain's state and the exact record to set —
// the author's next action is to type it into a registrar's form.
func TestFormatCustomDomains(t *testing.T) {
	cp := &fakeCP{
		envs:   `{"environments":[{"id":"env-1","name":"prod"}]}`,
		status: func(int) string { return domainStatusBody },
	}
	st, err := ReadHostedStatus(context.Background(), cp, "", "prod")
	if err != nil {
		t.Fatalf("ReadHostedStatus: %v", err)
	}
	out := FormatCustomDomains("  ", st.Workloads[0].Domains)
	for _, want := range []string{
		"hounders.club: pending_dns",
		"DNS record to set: A hounders.club → 34.63.203.181",
		"DNS record to set: TXT _forge-challenge.hounders.club → tok-123",
		"www.hounders.club: live since 2026-02-01T10:00:00Z",
		"shop.hounders.club: failed",
		"error: CAA record forbids this issuer",
		"blog.hounders.club: unknown",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("formatted block is missing %q:\n%s", want, out)
		}
	}
	if FormatCustomDomains("  ", nil) != "" {
		t.Error("a workload with no domains must format to nothing, so callers can print unconditionally")
	}
}

// A StaticSite spec's domains go through the same validation a port's do:
// DNS form, no wildcard, no duplicate, at most MaxDomains.
func TestStaticSiteSpecRefusesBadDomains(t *testing.T) {
	for _, tc := range []struct {
		name    string
		domains []string
		want    string
	}{
		{"wildcard", []string{"*.hounders.club"}, "wildcard"},
		{"uppercase", []string{"Hounders.club"}, "not a lowercase DNS hostname"},
		{"not a hostname", []string{"http://hounders.club"}, "not a lowercase DNS hostname"},
		{"duplicate", []string{"hounders.club", "hounders.club"}, "listed twice"},
		{"too many", []string{"a.club", "b.club", "c.club", "d.club", "e.club", "f.club", "g.club", "h.club", "i.club"}, "at most 8"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := v1alpha1.StaticSiteSpec{Domains: tc.domains}.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one naming %q", err, tc.want)
			}
		})
	}
	if err := (v1alpha1.StaticSiteSpec{Domains: []string{"hounders.club", "www.hounders.club"}}).Validate(); err != nil {
		t.Errorf("a valid pair of domains was refused: %v", err)
	}
}

// A hosted workload's URL is allocated by the platform, so the author never
// wrote it down: deploy and status must both print it, one line per
// workload, and say nothing for a workload that has none yet.
func TestFormatHostedURLs(t *testing.T) {
	cp := &fakeCP{
		envs:   `{"environments":[{"id":"env-1","name":"prod"}]}`,
		status: func(int) string { return domainStatusBody },
	}
	st, err := ReadHostedStatus(context.Background(), cp, "", "prod")
	if err != nil {
		t.Fatalf("ReadHostedStatus: %v", err)
	}
	ws := append(st.Workloads, HostedWorkloadStatus{Name: "worker"})
	if got, want := FormatHostedURLs("  ", ws), "  url web: https://web.example\n"; got != want {
		t.Errorf("FormatHostedURLs = %q, want %q", got, want)
	}
	if FormatHostedURLs("  ", []HostedWorkloadStatus{{Name: "worker"}}) != "" {
		t.Error("workloads with no URL must format to nothing, so callers can print unconditionally")
	}
}
