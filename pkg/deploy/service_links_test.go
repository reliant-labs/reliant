package deploy

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

var podTemplateKinds = map[string]bool{"Deployment": true, "StatefulSet": true, "DaemonSet": true, "Job": true, "CronJob": true}

// assertServiceLinks checks every pod template in objs carries
// enableServiceLinks explicitly (a missing field is Kubernetes' default,
// true) and returns how many it saw.
func assertServiceLinks(t *testing.T, label string, objs []*unstructured.Unstructured, want bool) int {
	t.Helper()
	n := 0
	for k, o := range objects(t, objs) {
		if !podTemplateKinds[o["kind"].(string)] {
			continue
		}
		n++
		if got := get(podOf(o), "enableServiceLinks"); got != want {
			t.Errorf("%s: %s pod enableServiceLinks = %v, want %v", label, k, got, want)
		}
	}
	return n
}

// TestEnableServiceLinksDefaultsOff: Kubernetes injects <SERVICE>_PORT=tcp://…
// for every Service in the namespace, which collided with typed config
// (TEMPORAL_PORT, ZITADEL_PORT) and CrashLooped prod. Every kind forge renders
// opts out by default.
func TestEnableServiceLinksDefaultsOff(t *testing.T) {
	ws := []v1alpha1.Workload{
		svc("api", httpPort(true)),
		wl("worker", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindWorker}),
		wl("seed", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindJob, Args: []string{"x"}}),
		wl("nightly", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindCron, Args: []string{"x"}, Schedule: "@daily"}),
		wl("mgr", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindOperator, Group: "example.com", Version: "v1", CRDs: []string{"Widget"}}),
	}
	if n := assertServiceLinks(t, "default", render(t, v1alpha1.ProfileFull, ws...), false); n != len(ws) {
		t.Errorf("saw %d pod templates, want %d", n, len(ws))
	}
	if n := assertServiceLinks(t, "restricted", render(t, v1alpha1.ProfileRestricted, ws[:3]...), false); n != 3 {
		t.Errorf("restricted: saw %d pod templates, want 3", n)
	}
}

// TestEnableServiceLinksOverride: enableServiceLinks opts a workload back in,
// and only that workload.
func TestEnableServiceLinksOverride(t *testing.T) {
	on := svc("legacy", httpPort(false))
	on.Spec.EnableServiceLinks = true
	for _, k := range []v1alpha1.WorkloadKind{v1alpha1.KindWorker, v1alpha1.KindJob, v1alpha1.KindCron} {
		w := wl("opt-"+string(k), v1alpha1.WorkloadSpec{Kind: k, Args: []string{"x"}, Schedule: map[bool]string{true: "@daily"}[k == v1alpha1.KindCron], EnableServiceLinks: true})
		if n := assertServiceLinks(t, string(k), render(t, v1alpha1.ProfileFull, w), true); n != 1 {
			t.Errorf("%s: saw %d pod templates, want 1", k, n)
		}
	}
	o := objects(t, render(t, v1alpha1.ProfileFull, on, svc("api", httpPort(false))))
	if got := get(podOf(o["Deployment/legacy"]), "enableServiceLinks"); got != true {
		t.Errorf("opted-in = %v, want true", got)
	}
	if got := get(podOf(o["Deployment/api"]), "enableServiceLinks"); got != false {
		t.Errorf("neighbour = %v, want false", got)
	}
}

// TestEveryRenderedPodTemplateSetsServiceLinks walks every golden render and
// the node agent: a new renderer path that builds a PodSpec without the
// field fails here.
func TestEveryRenderedPodTemplateSetsServiceLinks(t *testing.T) {
	total := 0
	for _, c := range goldenCases() {
		if c.name == "service_links" {
			continue // the opt-in case; TestEnableServiceLinksOverride covers it
		}
		rctx := goldenCtx
		rctx.Network = c.network
		rctx.ImagePullSecrets = c.pull
		objs, err := RenderWorkloads(c.ws, c.profile, rctx)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		total += assertServiceLinks(t, c.name, objs, false)
	}
	agent, err := RenderObservabilityAgent(agentSpec(), agentCtx)
	if err != nil {
		t.Fatal(err)
	}
	if n := assertServiceLinks(t, "observability agent", agent, false); n != 1 {
		t.Errorf("agent: saw %d pod templates, want 1", n)
	}
	if total == 0 {
		t.Fatal("no pod templates rendered: the walk is vacuous")
	}
}
