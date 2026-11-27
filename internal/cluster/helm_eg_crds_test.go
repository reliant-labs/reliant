package cluster

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/templates"
)

// pinnedEnvoyGatewayChartVersionForTest reads the envoy_gateway pin from
// internal/templates/ingress/envoy/VERSION — the SAME file the deploy path
// reads (ingressPinnedVersions). Read rather than hardcoded on purpose: a
// hardcoded version is a second pin, and the point of the version-tie test is
// that there is only one. Bump VERSION and this test exercises the new chart.
func pinnedEnvoyGatewayChartVersionForTest(t *testing.T) string {
	t.Helper()

	b, err := templates.IngressTemplates().Get("envoy/VERSION")
	if err != nil {
		t.Fatalf("read pinned ingress VERSION: %v", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if k, v, ok := strings.Cut(line, "="); ok && strings.TrimSpace(k) == "envoy_gateway" {
			return strings.TrimSpace(v)
		}
	}
	t.Fatal("ingress VERSION declares no envoy_gateway= pin")
	return ""
}

// Envoy Gateway's gateway-helm chart ships its CRDs in a dependency subchart
// under a helm `crds/` directory, split across two sources that forge must
// treat OPPOSITELY:
//
//	charts/crds/crds/generated/gateway.envoyproxy.io_*.yaml   EG's OWN — forge needs these
//	charts/crds/crds/gatewayapi-crds.yaml                     an EXPERIMENTAL-channel
//	                                                          Gateway API copy — forge must
//	                                                          never apply it
//
// forge renders the chart twice: `--skip-crds` for the controller, and
// `--include-crds` (chartOwnCRDs) to harvest the chart's own CRDs, because the
// controller starts informers on them and dies on cache-sync without them.
// The harvest therefore has to separate those two sources precisely.
//
// These tests pin that separation. They are the regression tests for a filter
// that keyed off ONE group name while the file it meant to exclude carries TWO.

// The experimental Gateway API bundle must be excluded WHOLE.
//
// THE BUG: the filter dropped `gateway.networking.k8s.io` by name, but
// gatewayapi-crds.yaml also declares three `gateway.networking.x-k8s.io` CRDs
// (xbackends, xbackendtrafficpolicies, xmeshes). Every CRD in that file is
// annotated `gateway.networking.k8s.io/channel: experimental`, and the
// x-k8s.io three sailed through a group-name filter — so forge applied
// experimental-channel CRDs out of the very file its docstring says it
// excludes, widening the cluster's API surface with kinds nothing asked for.
//
// They are not even watched: the chart grants the controller
// `multicluster.x-k8s.io/serviceimports` and `gateway.networking.k8s.io`, and
// never `gateway.networking.x-k8s.io`. So the leak buys nothing and cannot be
// justified as a controller prerequisite.
//
// The CHANNEL ANNOTATION is the discriminator, not the group: it is what
// upstream stamps to say which bundle a CRD came from, it is what the
// safe-upgrades ValidatingAdmissionPolicy reads, and EG's own generated CRDs
// carry none. Keying on it excludes the whole experimental bundle however
// upstream regroups it.
func TestChartOwnCRDs_ExcludesTheExperimentalGatewayAPIBundleWhole(t *testing.T) {
	// One doc per source the real --include-crds render produces.
	included := strings.Join([]string{
		// From gatewayapi-crds.yaml — the standard group. Already excluded.
		`apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: gateways.gateway.networking.k8s.io
  annotations:
    gateway.networking.k8s.io/channel: experimental
spec:
  group: gateway.networking.k8s.io`,
		// From the SAME file — the x-k8s.io group that leaked.
		`apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: xbackends.gateway.networking.x-k8s.io
  annotations:
    gateway.networking.k8s.io/channel: experimental
spec:
  group: gateway.networking.x-k8s.io`,
		// EG's OWN generated CRD: no channel annotation. Must survive.
		`apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: backends.gateway.envoyproxy.io
spec:
  group: gateway.envoyproxy.io`,
	}, docDelimiter)

	kept := keepChartOwnCRDs(included)

	if !strings.Contains(kept, "backends.gateway.envoyproxy.io") {
		t.Error("EG's own CRDs must survive — the controller starts informers on them " +
			"and fails cache-sync without them")
	}
	for _, leaked := range []string{
		"gateways.gateway.networking.k8s.io",
		"xbackends.gateway.networking.x-k8s.io",
	} {
		if strings.Contains(kept, leaked) {
			t.Errorf("%s came from the chart's experimental Gateway API bundle and must be "+
				"excluded whole; forge owns the standard channel at its pinned version", leaked)
		}
	}
}

// The real chart, really rendered. The unit test above cannot catch upstream
// regrouping or re-annotating the bundle, which is exactly the drift a chart
// bump introduces — and the filter's whole job is to survive that.
//
// This is ALSO the version tie. It does not compare the CRD set against a
// second pinned list, because a second list is a thing that can disagree: the
// CRDs are harvested from the SAME `helm template` invocation, at the same
// resolved version, as the controller manifests forge applies beside them. So
// a chart bump moves both or neither, and the test asserts the tie STRUCTURALLY
// — every surviving CRD is EG's own, from the version under test.
func TestChartOwnCRDs_RealChartYieldsOnlyEnvoyGatewaysOwnCRDs(t *testing.T) {
	if testing.Short() {
		t.Skip("pulls the gateway-helm chart over the network")
	}
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm not installed")
	}

	spec := HelmChartSpec{
		Name:      "envoy-gateway",
		OCI:       "oci://docker.io/envoyproxy/gateway-helm",
		Version:   pinnedEnvoyGatewayChartVersionForTest(t),
		Namespace: "envoy-gateway-system",
		CRDBundle: "gateway-api",
	}

	own, err := chartOwnCRDs(context.Background(), spec)
	if err != nil {
		t.Fatalf("chartOwnCRDs: %v", err)
	}

	// Every surviving doc must be a CRD in EG's own group, and nothing else.
	var got []string
	for _, doc := range splitDocs(own) {
		m, ok := parseDoc(doc)
		if !ok || strings.TrimSpace(doc) == "" {
			continue
		}
		if m.Kind != "CustomResourceDefinition" {
			t.Errorf("chartOwnCRDs returned a non-CRD %s/%s; the CRD pass applies "+
				"Established-gated and must carry CRDs only", m.Kind, m.Metadata.Name)
			continue
		}
		if g := crdGroup(doc); g != envoyGatewayOwnGroup {
			t.Errorf("CRD %s is in group %q; only %q is EG's own — everything else "+
				"belongs to a bundle forge pins itself", m.Metadata.Name, g, envoyGatewayOwnGroup)
		}
		if strings.Contains(doc, experimentalChannelAnnotation) {
			t.Errorf("CRD %s is annotated experimental-channel and must not be applied",
				m.Metadata.Name)
		}
		got = append(got, m.Metadata.Name)
	}

	// The eight the controller informs on. Named explicitly: a chart bump that
	// DROPS one is as much a break as one that adds a bundle, and a test that
	// only counted would pass on a silent substitution.
	for _, want := range []string{
		"backends.gateway.envoyproxy.io",
		"backendtrafficpolicies.gateway.envoyproxy.io",
		"clienttrafficpolicies.gateway.envoyproxy.io",
		"envoyextensionpolicies.gateway.envoyproxy.io",
		"envoypatchpolicies.gateway.envoyproxy.io",
		"envoyproxies.gateway.envoyproxy.io",
		"httproutefilters.gateway.envoyproxy.io",
		"securitypolicies.gateway.envoyproxy.io",
	} {
		if !slicesContains(got, want) {
			t.Errorf("chart %s no longer yields %s; forge renders resources in this group "+
				"(Backend, BackendTrafficPolicy) and they cannot apply without it",
				spec.Version, want)
		}
	}
}

func slicesContains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}
