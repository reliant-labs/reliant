package cli

// The plan a reviewer approves must cover what the deploy applies, charts
// included. 2026-10-16: a cert-manager chart value changed its ConfigMap and a
// chart prune deleted a PriorityClass, and neither was in the plan or its digest.

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

func planThroughPlanForDeploy(t *testing.T, charts release.ChartPlan) *release.Plan {
	t.Helper()
	applied := ledgerOnlyBundle("bnd_1", "v1", ledgerOnlyShape())
	candidate := ledgerOnlyBundle("bnd_2", "v2", ledgerOnlyShape())
	ledger := ledgerOnlyFixture(t, ledgerOnlyControlPlane(t, applied, candidate),
		[]release.Promotion{appliedPromotion("p-1", "v1", applied)},
		map[string]bool{"db-credentials": true, "cloudflare-api-token": true})

	noteRecordedBundles("v2", []bundleWriteOutcome{{Env: "prod", Digest: candidate.Digest, Recorded: true}})
	prev := observeDeployCharts
	observeDeployCharts = func(context.Context, string, string, []string) release.ChartPlan { return charts }
	t.Cleanup(func() { observeDeployCharts = prev })

	plan := planForDeploy(context.Background(), t.TempDir(), "prod", "v2", nil, ledger, io.Discard)
	if plan == nil {
		t.Fatal("no plan computed")
	}
	return plan
}

func TestDeployPlan_ChartChangesAreInThePlanAndItsDigest(t *testing.T) {
	baseline := planThroughPlanForDeploy(t, release.ChartPlan{})
	if got := findingsByCode(baseline); len(got[release.FindingConfigChanged])+len(got[release.FindingObjectRemoved]) != 0 {
		t.Fatalf("an unchanged deploy must plan no chart change: %+v", baseline.Findings)
	}

	cm := release.ObjectKey{Cluster: "prod-ctx", Kind: "ConfigMap", Namespace: "cert-manager", Name: "cert-manager"}
	pc := release.ObjectKey{Cluster: "prod-ctx", Kind: "PriorityClass", Name: "forge-delivery"}

	valueChange := planThroughPlanForDeploy(t, release.ChartPlan{Changes: []release.ChartChange{
		{Chart: "cert-manager", Key: cm, Op: release.ChartChanged, Fields: []string{"data.config.yaml"}, Fingerprint: "sha256:v1"},
	}})
	got := findingsByCode(valueChange)[release.FindingConfigChanged]
	if len(got) != 1 || got[0].Subject != cm.String() || got[0].Section != release.SectionConfig ||
		!strings.Contains(got[0].Detail, "[chart cert-manager]") || !strings.Contains(got[0].Detail, "data.config.yaml") {
		t.Fatalf("chart value change missing from the plan: %+v", valueChange.Findings)
	}
	if valueChange.Digest == baseline.Digest {
		t.Error("the digest an operator approves does not cover the chart value change")
	}

	prune := planThroughPlanForDeploy(t, release.ChartPlan{Changes: []release.ChartChange{
		{Chart: "forge-delivery", Key: pc, Op: release.ChartRemoved, Fingerprint: "sha256:p"},
	}})
	removed := findingsByCode(prune)[release.FindingObjectRemoved]
	if len(removed) != 1 || removed[0].Subject != pc.String() || !strings.Contains(removed[0].Detail, "[chart forge-delivery]") {
		t.Fatalf("chart prune missing from the plan: %+v", prune.Findings)
	}
	if prune.Digest == baseline.Digest || prune.Digest == valueChange.Digest {
		t.Error("the digest does not cover the chart prune")
	}

	var out strings.Builder
	renderDeployPlan(&out, "prod", *prune)
	if !strings.Contains(out.String(), "per-object diff") || !strings.Contains(out.String(), "[chart forge-delivery]") {
		t.Errorf("rendered plan must show the pruned chart object under the per-object diff:\n%s", out.String())
	}
}

// A control plane's plan cannot see charts: the write carries the digest it can
// recompute, while the approval stays bound to the full one.
func TestServerPlanDigest_CarriesTheBaseDigestForAChartPlan(t *testing.T) {
	base := buildTestPlan(t)
	folded, err := base.WithCharts(release.ChartPlan{Changes: []release.ChartChange{{
		Chart: "c", Key: release.ObjectKey{Cluster: "x", Kind: "ConfigMap", Name: "n"}, Op: release.ChartChanged, Fingerprint: "sha256:1",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := serverPlanDigest(&folded, envLedger{Hosted: true}); got != base.Digest {
		t.Errorf("server digest = %q, want the pre-chart digest %q", got, base.Digest)
	}
	if deployPlanDigest(&folded) == base.Digest {
		t.Error("the approvable digest must differ from the server's")
	}
}
