package cluster

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// Envoy Gateway 1.9 moved the Gateway API safe-upgrades
// ValidatingAdmissionPolicy out of the CRD bundle and into the chart's
// templates. `--skip-crds` does not skip a template, so from 1.9 the chart
// renders its own copy of a resource that is ALSO inside the standard-install
// bundle forge pins and applies itself.
//
// Two writers of one cluster-scoped policy is not a cosmetic duplicate: the
// policy decides which CRD writes are admitted, so whichever copy landed last
// governs whether forge's next pinned bump is allowed at all. These tests pin
// the rule that forge renders exactly one source for the Gateway API group.

// The value must be set for a chart that delegates its Gateway API CRDs to
// forge, and for no other chart — keyed off the DECLARED bundle rather than
// the chart's name, so the rule follows the ownership decision instead of one
// vendor.
func TestGatewayAPIChartValues_SetOnlyForTheGatewayAPIBundle(t *testing.T) {
	const want = "crds.gatewayAPI.safeUpgradePolicy.enabled=false"

	got := gatewayAPIChartValues(HelmChartSpec{Name: "envoy-gateway", CRDBundle: "gateway-api"})
	if strings.Join(got, " ") != "--set "+want {
		t.Errorf("gateway-api bundle must disable the chart's safe-upgrades policy; got %q", got)
	}

	for _, bundle := range []string{"", "cert-manager"} {
		if got := gatewayAPIChartValues(HelmChartSpec{Name: "cert-manager", CRDBundle: bundle}); got != nil {
			t.Errorf("bundle %q must not set Gateway API values; got %q", bundle, got)
		}
	}
}

// The real chart, really rendered. The argv test above cannot catch upstream
// renaming the value — the flag would still be passed and silently ignored,
// and the duplicate policy would come back. Network-gated, since it pulls the
// chart.
func TestHelmTemplate_EnvoyGatewayRendersNoSafeUpgradesPolicy(t *testing.T) {
	if testing.Short() {
		t.Skip("pulls the gateway-helm chart over the network")
	}
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm not installed")
	}

	spec := HelmChartSpec{
		Name:      "envoy-gateway",
		OCI:       "oci://docker.io/envoyproxy/gateway-helm",
		Version:   "v1.9.2",
		Namespace: "envoy-gateway-system",
		CRDBundle: "gateway-api",
	}

	rendered, err := helmTemplate(context.Background(), spec)
	if err != nil {
		t.Fatalf("helm template: %v", err)
	}
	if strings.Contains(rendered, "ValidatingAdmissionPolicy") {
		t.Error("the chart rendered a safe-upgrades ValidatingAdmissionPolicy; " +
			"forge's pinned Gateway API bundle already supplies it, and two writers " +
			"of the policy that gates CRD upgrades can deny forge's next bump")
	}

	// The same value must not cost the chart its OWN CRDs: those are not the
	// Gateway API group and forge does not supply them, so dropping them
	// crashloops the controller on cache-sync.
	own, err := chartOwnCRDs(context.Background(), spec)
	if err != nil {
		t.Fatalf("chartOwnCRDs: %v", err)
	}
	if !strings.Contains(own, "gateway.envoyproxy.io") {
		t.Error("Envoy Gateway's own CRDs must survive; the controller starts informers on them")
	}
	if strings.Contains(own, "group: "+standardGatewayAPIGroup) {
		t.Error("the standard Gateway API group must come from forge's pinned bundle, not the chart")
	}
}
