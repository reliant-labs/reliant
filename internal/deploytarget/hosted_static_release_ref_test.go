package deploytarget

import (
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

// A hosted site's release is a REFERENCE plus a digest, and the spec must
// carry both.
//
// The defect these pin: forge pushed a site to `<declared image>/static.v1`
// and published only the digest, while the control plane's operator
// recomposed `<registryBase>/<org>/static.v1/<site>` from the CR's labels.
// Two independent derivations of one address, which could not agree — so the
// operator 404'd pulling an artifact that existed. Observed against an
// in-process registry: pushed `org-fixture/site/static.v1`, pulled
// `org-fixture/static.v1/site`.
//
// The fix is the rule a workload's image already follows (forge #322): the
// reference carries its registry, the ledger keys artifacts by full
// reference, and whoever consumes a release pulls exactly what was recorded.

func staticGroupWithRepository(digests map[string]string) ServiceGroup {
	return ServiceGroup{
		Env: "prod", ProviderID: HostedProviderID,
		Hosted: &HostedTarget{Endpoint: "https://cp.example", Release: "v2", Digests: digests, PushBase: "ghcr.io/acme"},
		Services: []ResolvedService{{Name: "web", Hosted: &HostedWorkload{
			Tier: HostedTierStatic, Artifact: staticSiteArtifact,
			Static: &v1alpha1.StaticSiteSpec{BasePath: "/app"},
		}}},
	}
}

// TestHostedStaticPublishesTheRecordedReleaseRepository: the published spec
// names the repository the release was actually pushed to, verbatim — not a
// site name, not a path a consumer would have to recompose.
//
// MUTATION VERIFIED RED: dropping `spec.ReleaseRepository = artifact` in
// planHostedWith → releaseRepository absent from the published spec, which is
// precisely the state in which the operator has to guess.
func TestHostedStaticPublishesTheRecordedReleaseRepository(t *testing.T) {
	recs, err := HostedRecords(staticGroupWithRepository(map[string]string{staticSiteArtifact: digestB}))
	if err != nil {
		t.Fatalf("records: %v", err)
	}
	if len(recs) != 1 || recs[0].Kind != "StaticSite" {
		t.Fatalf("records = %+v, want one StaticSite", recs)
	}
	var doc struct {
		Spec map[string]any `json:"spec"`
	}
	if err := yaml.Unmarshal(recs[0].YAML, &doc); err != nil {
		t.Fatal(err)
	}
	spec := doc.Spec
	if spec["releaseRepository"] != staticSiteArtifact {
		t.Errorf("spec.releaseRepository = %v, want the pushed repository %q — without it the puller must recompose a path",
			spec["releaseRepository"], staticSiteArtifact)
	}
	if spec["liveDigest"] != digestB {
		t.Errorf("spec.liveDigest = %v, want %s", spec["liveDigest"], digestB)
	}
}

// TestStaticSiteSpecValidateRefusesAnUnpullableReleaseRepository: the recorded
// reference must be an address, so a host-less path is refused — it names no
// registry to pull from. A tag or digest on it is refused too: the release is
// addressed as <releaseRepository>@<liveDigest>, so a pin here would compose a
// reference with two of them.
func TestStaticSiteSpecValidateRefusesAnUnpullableReleaseRepository(t *testing.T) {
	for _, tc := range []struct{ name, repository, want string }{
		{"host-less", "acme/web/static.v1", "registry host"},
		{"tagged", "ghcr.io/acme/web/static.v1:v2", "must not carry a tag"},
		{"digested", "ghcr.io/acme/web/static.v1@sha256:" + strings.Repeat("a", 64), "must not carry a digest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := v1alpha1.StaticSiteSpec{ReleaseRepository: tc.repository}.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate(%q) = %v, want a refusal naming %q", tc.repository, err, tc.want)
			}
		})
	}
	// A registry-bearing repository is accepted, and so is the empty value:
	// a site has no release until its first deploy.
	for _, ok := range []string{"", staticSiteArtifact, "127.0.0.1:5000/org/web/static.v1", "localhost:5000/org/web/static.v1"} {
		if err := (v1alpha1.StaticSiteSpec{ReleaseRepository: ok}).Validate(); err != nil {
			t.Errorf("Validate(%q) = %v, want accepted", ok, err)
		}
	}
}
