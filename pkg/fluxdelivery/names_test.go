package fluxdelivery_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/fluxdelivery"
	"github.com/reliant-labs/forge/pkg/release"
)

// TestNamesMatchFluxtenant pins every name to the spelling control-plane's
// fluxtenant composes today (its bundle goldens and its label-bound test), so
// an environment's binding ("") adopts the live objects by name. The bounded
// spellings were computed by fluxtenant's boundLabelValue for the UUID pair
// that broke prod.
func TestNamesMatchFluxtenant(t *testing.T) {
	t.Parallel()
	const (
		org = "55949942-3ee9-4cfc-bb7c-817bdba75186"
		env = "6a7e973c-42cc-4c39-ba00-1810510cc7bb"
	)
	for got, want := range map[string]string{
		fluxdelivery.SourceName("acme", "env-prod", ""):                                                                                                    "acme.env.env-prod",
		fluxdelivery.KustomizationName("acme", "env-prod", "", "gke_reliant-labs_us-central1_prod"):                                                        "acme.env.env-prod-gke-reliant-labs-us-central1-prod",
		fluxdelivery.KustomizationName("acme", "env-prod", "", "vke-staging"):                                                                              "acme.env.env-prod-vke-staging",
		fluxdelivery.KustomizationName("acme", "env-prod", "", release.BundleHostedCluster):                                                                "acme.env.env-prod-hosted",
		fluxdelivery.TreeKustomizationName("acme", "env-prod", "", "daemon", "daemon-substrate", release.PhasePre):                                         "acme.env.env-prod-daemon-daemon-substrate-pre",
		fluxdelivery.TreeKustomizationName("acme", "env-prod", "", "hub", "base", release.PhaseRollout):                                                    "acme.env.env-prod-hub-base-rollout",
		fluxdelivery.KustomizationName(org, env, "", "hosted"):                                                                                             org + ".env.6a7e973c.f54ebe8df87c",
		fluxdelivery.KustomizationName(org, env, "", "gke_reliant-labs-475814_us-central1_prod"):                                                           org + ".env.6a7e973c.afba485e570b",
		fluxdelivery.TreeKustomizationName(org, env, "", "gke_reliant-labs-475814_us-central1-a_prod-daemon-v2", "daemon-substrate", release.PhaseRollout): org + ".env.6a7e973c.db6e6d565f48",
		fluxdelivery.LabelValue("gke_reliant-labs_us-central1_prod"):                                                                                       "gke-reliant-labs-us-central1-prod",
		fluxdelivery.ResourceSetName("env-prod", ""):                                                                                                       "delivery.env-prod",
		fluxdelivery.ResourceSetName("env-prod", "web"):                                                                                                    "delivery.env-prod.web",
		fluxdelivery.SourceName("acme", "env-prod", "web"):                                                                                                 "acme.env.env-prod.b.web",
		fluxdelivery.KustomizationName("acme", "env-prod", "web", "vke-staging"):                                                                           "acme.env.env-prod.b.web-vke-staging",
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

// TestNamesAreLegalLabelValues: every Kustomization and ResourceSet name is
// also a label value (kustomize-controller and flux-operator stamp them on
// what they apply), however long its inputs.
func TestNamesAreLegalLabelValues(t *testing.T) {
	t.Parallel()
	labelValue := regexp.MustCompile(`^[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$`)
	subdomain := regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)
	long := strings.Repeat("a", 63)
	for _, name := range []string{
		fluxdelivery.KustomizationName(long, long, "", "gke_project_region_"+long),
		fluxdelivery.TreeKustomizationName(long, long, long, "PROD-East", long, release.PhasePost),
		fluxdelivery.KustomizationName("acme", "prod", "", "vke-608665d9.example"),
		fluxdelivery.ResourceSetName(long, long),
	} {
		if len(name) > 63 || !labelValue.MatchString(name) || !subdomain.MatchString(name) {
			t.Errorf("%q (%d chars) is not both a subdomain and a label value", name, len(name))
		}
	}
	a := fluxdelivery.KustomizationName(long, long, "", "c1")
	b := fluxdelivery.KustomizationName(long, long, "", "c2")
	if a == b {
		t.Errorf("bounding merged two clusters into %q", a)
	}
}

// TestBindingNamesNeverCollide, in the spirit of fluxtenant's
// TestBundleNamesNeverCollideWithAConfigArtifactName: a binding's names can
// never equal another environment's or another binding's. The `.b.` marker
// cannot occur in an env id, because an env id is an RFC-1123 label and a
// label admits no dot; the adversarial envs below are the ones that would
// imitate it if it could.
func TestBindingNamesNeverCollide(t *testing.T) {
	t.Parallel()
	seen := map[string]string{}
	record := func(name, what string) {
		if prev, ok := seen[name]; ok {
			t.Errorf("%s and %s share the name %q", prev, what, name)
		}
		seen[name] = what
	}
	for _, c := range []struct{ env, binding string }{
		{"prod", ""}, {"prod", "web"}, {"prod", "api"},
		{"prod-b-web", ""}, {"prod-web", ""}, {"prodb", "web"}, {"b", "web"}, {"prod", "b-web"},
	} {
		what := "env " + c.env + " binding " + c.binding
		record(fluxdelivery.SourceName("acme", c.env, c.binding), what+" source")
		record(fluxdelivery.KustomizationName("acme", c.env, c.binding, "hub"), what+" kustomization")
		record(fluxdelivery.ResourceSetName(c.env, c.binding), what+" resourceset")
		if c.binding != "" && !strings.Contains(fluxdelivery.SourceName("acme", c.env, c.binding), ".b."+c.binding) {
			t.Errorf("binding %q is not marked in its source name", c.binding)
		}
	}
}
