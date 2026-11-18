package templates_test

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/templates"
)

func TestIngressTemplatesEmbedded(t *testing.T) {
	cat := templates.IngressTemplates()
	// VERSION pins the gateway-helm chart + Gateway API CRD versions. It is
	// the SINGLE source of truth the declarative platform path reads:
	// ingressPinnedVersions → fetchGatewayAPICRDs supplies the pinned
	// standard-channel CRDs for a forge.HelmChart with crds="gateway-api"
	// (deploy_helm.go::fetchHelmChartCRDs). The Envoy Gateway controller and
	// the `eg` GatewayClass are NO LONGER vendored/installed imperatively —
	// they come from the env's declared helm_charts (the controller chart +
	// the GatewayClass riding its `manifests`). See
	// internal/cli/dev_cluster_ingress.go.
	for _, name := range []string{
		"envoy/VERSION",
	} {
		b, err := cat.Get(name)
		if err != nil {
			t.Errorf("Get(%q): %v", name, err)
			continue
		}
		if len(b) == 0 {
			t.Errorf("Get(%q) returned empty bytes", name)
		}
	}
	// VERSION carries both pin lines for the declarative CRD fetch to read.
	b, _ := cat.Get("envoy/VERSION")
	for _, want := range []string{"envoy_gateway=", "gateway_api="} {
		if !strings.Contains(string(b), want) {
			t.Errorf("VERSION missing %q in:\n%s", want, b)
		}
	}
}
