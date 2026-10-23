package release

import (
	"strings"
	"testing"
)

func chartTestShape() Shape {
	return Shape{Kind: EnvSelfManaged, Clusters: []string{"prod"}, Objects: []ShapeObject{
		{Cluster: "prod", Kind: "Deployment", Namespace: "app", Name: "api", Hash: testDigest("a"), ConfigHash: testDigest("a")},
	}}
}

func testDigest(s string) string {
	d, _ := HashDocument(s)
	return d
}

// chartTestPlan builds the plan and drops the drift section's standing
// "not observable" warn, which every plan here carries and none is about.
func chartTestPlan(t *testing.T, charts ChartPlan) Plan {
	t.Helper()
	p := buildChartTestPlan(t, charts)
	var kept []PlanFinding
	for _, f := range p.Findings {
		if f.Section != SectionDrift {
			kept = append(kept, f)
		}
	}
	p.Findings = kept
	return p
}

func buildChartTestPlan(t *testing.T, charts ChartPlan) Plan {
	t.Helper()
	live := chartTestShape()
	p, err := BuildPlan(PlanInput{
		EnvironmentID: "prod", BundleID: "b2", Candidate: chartTestShape(), Live: &live,
		CandidateConfigDigest: "sha256:x", Basis: PlanBasis{AppliedConfigDigest: "sha256:x"},
		Charts: charts,
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

var (
	certManagerCM = ObjectKey{Cluster: "prod", Kind: "ConfigMap", Namespace: "cert-manager", Name: "cert-manager"}
	forgePriority = ObjectKey{Cluster: "prod", Kind: "PriorityClass", Name: "forge-delivery"}
)

func TestChartFindingsAppearAndMoveTheDigest(t *testing.T) {
	base := chartTestPlan(t, ChartPlan{})
	if !base.ConfigIdentical {
		t.Fatal("fixture: an unchanged bundle must read as config-identical")
	}

	changed := chartTestPlan(t, ChartPlan{Changes: []ChartChange{{
		Chart: "cert-manager", Key: certManagerCM, Op: ChartChanged,
		Fields: []string{"data.config.yaml"}, Fingerprint: "sha256:v1",
	}}})
	if len(changed.Findings) != 1 || changed.Findings[0].Code != FindingConfigChanged ||
		changed.Findings[0].Section != SectionConfig || changed.Findings[0].Subject != certManagerCM.String() {
		t.Fatalf("a chart value change must be a config_changed finding on the object: %+v", changed.Findings)
	}
	if d := changed.Findings[0].Detail; !strings.Contains(d, "[chart cert-manager]") || !strings.Contains(d, "data.config.yaml") {
		t.Errorf("detail must mark the chart and name the field path: %q", d)
	}
	if changed.ConfigIdentical {
		t.Error("a chart change falsifies \"config identical — images at most\"")
	}
	if changed.Digest == base.Digest {
		t.Error("the digest must cover the chart change: approving the empty plan would approve it")
	}

	pruned := chartTestPlan(t, ChartPlan{Changes: []ChartChange{{
		Chart: "forge-delivery", Key: forgePriority, Op: ChartRemoved, Fingerprint: "sha256:p",
	}}})
	if len(pruned.Findings) != 1 || pruned.Findings[0].Code != FindingObjectRemoved ||
		pruned.Findings[0].Subject != forgePriority.String() ||
		!strings.Contains(pruned.Findings[0].Detail, "[chart forge-delivery]") {
		t.Fatalf("a chart prune must be an object_removed finding marked with its chart: %+v", pruned.Findings)
	}
	if pruned.Digest == base.Digest || pruned.Digest == changed.Digest {
		t.Error("each chart change must give its own digest")
	}

	both := chartTestPlan(t, ChartPlan{Changes: []ChartChange{
		{Chart: "cert-manager", Key: certManagerCM, Op: ChartChanged, Fingerprint: "sha256:v1"},
		{Chart: "forge-delivery", Key: forgePriority, Op: ChartRemoved, Fingerprint: "sha256:p"},
	}})
	if len(both.Findings) != 2 {
		t.Fatalf("want both chart findings, got %+v", both.Findings)
	}
}

// Same object, same fields, different rendered content: an approval of one
// must not cover the other.
func TestChartFingerprintIsDigested(t *testing.T) {
	plan := func(fp string) Plan {
		return chartTestPlan(t, ChartPlan{Changes: []ChartChange{{Chart: "cert-manager", Key: certManagerCM, Op: ChartChanged, Fields: []string{"data.config.yaml"}, Fingerprint: fp}}})
	}
	if plan("sha256:v1").Digest == plan("sha256:v2").Digest {
		t.Error("two different desired documents for one object digested alike")
	}
}

func TestChartPrunedLoadBalancerIsAStop(t *testing.T) {
	svc := ObjectKey{Cluster: "prod", Kind: "Service", Namespace: "envoy", Name: "edge"}
	p := chartTestPlan(t, ChartPlan{Changes: []ChartChange{{Chart: "envoy", Key: svc, Op: ChartRemoved, LoadBalancer: true, Fingerprint: "sha256:s"}}})
	if got := p.StopCodes(); len(got) != 1 || got[0] != FindingLBIdentityChange {
		t.Fatalf("pruning a LoadBalancer Service must need acknowledgement, got stops %v", got)
	}
}

func TestUnplannedChartIsAWarnNeverSilence(t *testing.T) {
	p := chartTestPlan(t, ChartPlan{Unplanned: []ChartUnplanned{{Chart: "cert-manager", Reason: "helm is not on PATH"}}})
	if len(p.Findings) != 1 || p.Findings[0].Code != FindingUnknown || p.Findings[0].Class != ClassWarn ||
		!strings.Contains(p.Findings[0].Detail, "helm is not on PATH") {
		t.Fatalf("a chart that could not be planned must say so: %+v", p.Findings)
	}
}

// A server computes the plan without charts; WithCharts completes it and keeps
// the digest that server can recompute.
func TestWithChartsKeepsTheBaseDigest(t *testing.T) {
	base := chartTestPlan(t, ChartPlan{})
	folded, err := base.WithCharts(ChartPlan{Changes: []ChartChange{{Chart: "cert-manager", Key: certManagerCM, Op: ChartChanged, Fingerprint: "sha256:v1"}}})
	if err != nil {
		t.Fatal(err)
	}
	if folded.BaseDigest != base.Digest || folded.Digest == base.Digest {
		t.Errorf("base=%s folded=%s want BaseDigest=%s and a new Digest", folded.BaseDigest, folded.Digest, base.Digest)
	}
	if ok, _ := folded.VerifyDigest(); !ok {
		t.Error("the folded plan's digest must verify against its own findings")
	}
	same, _ := base.WithCharts(ChartPlan{})
	if same.Digest != base.Digest || same.BaseDigest != "" {
		t.Error("no chart changes must leave the plan and its digest untouched")
	}
	// Order must not matter.
	a := ChartChange{Chart: "c", Key: certManagerCM, Op: ChartChanged, Fingerprint: "1"}
	b := ChartChange{Chart: "f", Key: forgePriority, Op: ChartRemoved, Fingerprint: "2"}
	p1, _ := base.WithCharts(ChartPlan{Changes: []ChartChange{a, b}})
	p2, _ := base.WithCharts(ChartPlan{Changes: []ChartChange{b, a}})
	if p1.Digest != p2.Digest {
		t.Error("the digest depends on observation order")
	}
}
