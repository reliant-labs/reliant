package cluster

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
	"github.com/reliant-labs/forge/pkg/stsresize"
)

func resizeSTS(name, namespace, cluster, unit string, templates ...string) map[string]any {
	labels := map[string]any{"app.kubernetes.io/name": name, "app.kubernetes.io/part-of": "obs", "forge.dev/env": "prod"}
	if cluster != "" {
		labels[ClusterRoutingLabel] = cluster
	}
	if unit != "" {
		labels[release.LabelDeliveryUnit] = unit
	}
	meta := map[string]any{"name": name, "labels": labels}
	if namespace != "" {
		meta["namespace"] = namespace
	}
	var vcts []any
	for _, t := range templates {
		vcts = append(vcts, map[string]any{
			"metadata": map[string]any{"name": "data"},
			"spec": map[string]any{
				"accessModes":      []any{"ReadWriteOnce"},
				"storageClassName": "standard-rwo",
				"resources":        map[string]any{"requests": map[string]any{"storage": t}},
			},
		})
	}
	spec := map[string]any{
		"serviceName": name,
		"selector":    map[string]any{"matchLabels": map[string]any{"app.kubernetes.io/name": name}},
	}
	if vcts != nil {
		spec["volumeClaimTemplates"] = vcts
	}
	return map[string]any{"apiVersion": "apps/v1", "kind": "StatefulSet", "metadata": meta, "spec": spec}
}

func resizeKinds(items []any) []string {
	var out []string
	for _, it := range items {
		m := it.(map[string]any)
		md := m["metadata"].(map[string]any)
		out = append(out, m["kind"].(string)+"/"+md["name"].(string))
	}
	return out
}

func resizeFind(t *testing.T, items []any, kind string) map[string]any {
	t.Helper()
	for _, it := range items {
		if m := it.(map[string]any); m["kind"] == kind {
			return m
		}
	}
	t.Fatalf("no %s in %v", kind, resizeKinds(items))
	return nil
}

func TestExpandStatefulSetResizeJobs_EmitsTheJobAheadOfTheStatefulSet(t *testing.T) {
	ns := map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": "observability-data"}}
	svc := map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": "clickhouse"}}
	sts := resizeSTS("clickhouse", "observability-data", "gke_prod", "infra", "100Gi")
	got, err := expandStatefulSetResizeJobs([]any{ns, svc, sts}, resizeJobOpts{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Namespace/observability-data", "Service/clickhouse"}
	if gotKinds := resizeKinds(got); strings.Join(gotKinds[:2], ",") != strings.Join(want, ",") {
		t.Fatalf("existing objects reordered: %v", gotKinds)
	}
	order := resizeKinds(got)
	if last := order[len(order)-1]; last != "StatefulSet/clickhouse" {
		t.Fatalf("the StatefulSet must stay last, got %v", order)
	}
	for _, kind := range []string{"ServiceAccount", "Role", "RoleBinding", "NetworkPolicy", "Job"} {
		obj := resizeFind(t, got, kind)
		md := obj["metadata"].(map[string]any)
		if md["namespace"] != "observability-data" {
			t.Fatalf("%s namespace = %v", kind, md["namespace"])
		}
		labels := md["labels"].(map[string]any)
		// The objects deploy to the StatefulSet's cluster, in its env and unit.
		for k, v := range map[string]string{ClusterRoutingLabel: "gke_prod", release.LabelDeliveryUnit: "infra", "forge.dev/env": "prod", "app.kubernetes.io/part-of": "obs"} {
			if labels[k] != v {
				t.Fatalf("%s label %s = %v, want %s", kind, k, labels[k], v)
			}
		}
	}
	job := resizeFind(t, got, "Job")
	md := job["metadata"].(map[string]any)
	if !strings.HasPrefix(md["name"].(string), "sts-volume-resize-infra-") {
		t.Fatalf("a unit's Job is named for it: %v", md["name"])
	}
	// The Job gates the rollout, and is a no-TTL hash-named one-shot, the only
	// shape a reconciler accepts (release.CheckFluxSafety).
	phase, err := release.ClassifyPhase("Job", md["name"].(string), map[string]string{release.AnnotationDeployPhase: md["annotations"].(map[string]any)[release.AnnotationDeployPhase].(string)})
	if err != nil || phase != release.PhasePre {
		t.Fatalf("phase = %q, %v; want pre", phase, err)
	}
	if _, ttl := job["spec"].(map[string]any)["ttlSecondsAfterFinished"]; ttl {
		t.Fatal("a TTL re-runs the Job every period under a reconciler")
	}
	if f := release.CheckFluxSafety([]any{job}); len(f) != 0 {
		t.Fatalf("the Job is not flux-safe: %v", f)
	}
}

func TestExpandStatefulSetResizeJobs_OneJobPerClusterNamespaceUnit(t *testing.T) {
	items := []any{
		resizeSTS("a", "ns1", "hub", "", "10Gi"),
		resizeSTS("b", "ns1", "hub", "", "10Gi"), // joins a's Job
		resizeSTS("c", "ns2", "hub", "", "10Gi"), // another namespace
		resizeSTS("d", "ns1", "daemon", "", "10Gi"),
		resizeSTS("e", "ns1", "hub", "infra", "10Gi"), // another unit
	}
	got, err := expandStatefulSetResizeJobs(items, resizeJobOpts{})
	if err != nil {
		t.Fatal(err)
	}
	jobs := 0
	for _, it := range got {
		if it.(map[string]any)["kind"] == "Job" {
			jobs++
		}
	}
	if jobs != 4 {
		t.Fatalf("jobs = %d, want 4 (hub/ns1, hub/ns2, daemon/ns1, hub/ns1/infra): %v", jobs, resizeKinds(got))
	}
	// a and b share one Job whose input names both.
	for _, it := range got {
		m := it.(map[string]any)
		if m["kind"] != "Job" {
			continue
		}
		md := m["metadata"].(map[string]any)
		if md["labels"].(map[string]any)[ClusterRoutingLabel] == "hub" && md["namespace"] == "ns1" && md["labels"].(map[string]any)[release.LabelDeliveryUnit] == nil {
			env := m["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["env"].([]any)
			var declared string
			for _, e := range env {
				if e.(map[string]any)["name"] == "RESIZE_DECLARED" {
					declared = e.(map[string]any)["value"].(string)
				}
			}
			var d []stsresize.Declared
			if err := json.Unmarshal([]byte(declared), &d); err != nil || len(d) != 2 || d[0].Name != "a" || d[1].Name != "b" {
				t.Fatalf("declared = %s (%v)", declared, err)
			}
		}
	}
}

func TestExpandStatefulSetResizeJobs_NothingToDo(t *testing.T) {
	cases := map[string]struct {
		items []any
		opts  resizeJobOpts
	}{
		"no StatefulSet":                 {items: []any{map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": "x"}}}},
		"no volumeClaimTemplates":        {items: []any{resizeSTS("x", "ns", "", "")}},
		"lifecycle local":                {items: []any{resizeSTS("x", "ns", "", "", "1Gi")}, opts: resizeJobOpts{Lifecycle: "local"}},
		"lifecycle ephemeral":            {items: []any{resizeSTS("x", "ns", "", "", "1Gi")}, opts: resizeJobOpts{Lifecycle: "ephemeral"}},
		"disabled":                       {items: []any{resizeSTS("x", "ns", "", "", "1Gi")}, opts: resizeJobOpts{Disabled: true}},
		"no namespace anywhere":          {items: []any{resizeSTS("x", "", "", "", "1Gi")}},
		"a Deployment named like one":    {items: []any{map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "x"}, "spec": map[string]any{"volumeClaimTemplates": []any{}}}}},
		"a StatefulSet of another group": {items: []any{map[string]any{"apiVersion": "apps.kruise.io/v1", "kind": "StatefulSet", "metadata": map[string]any{"name": "x"}}}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := expandStatefulSetResizeJobs(tc.items, tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.items) {
				t.Fatalf("emitted objects for nothing to converge: %v", resizeKinds(got))
			}
		})
	}
}

func TestExpandStatefulSetResizeJobs_DefaultNamespaceAndImage(t *testing.T) {
	got, err := expandStatefulSetResizeJobs([]any{resizeSTS("x", "", "", "", "1Gi")}, resizeJobOpts{Namespace: "from-target", Image: "mirror/k8s:1"})
	if err != nil {
		t.Fatal(err)
	}
	job := resizeFind(t, got, "Job")
	if ns := job["metadata"].(map[string]any)["namespace"]; ns != "from-target" {
		t.Fatalf("namespace = %v", ns)
	}
	img := job["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["image"]
	if img != "mirror/k8s:1" {
		t.Fatalf("image = %v", img)
	}
}

func TestExpandStatefulSetResizeJobs_DeterministicAndHashTracksTheDeclaredVolume(t *testing.T) {
	jobName := func(size string) string {
		got, err := expandStatefulSetResizeJobs([]any{resizeSTS("x", "ns", "", "", size)}, resizeJobOpts{})
		if err != nil {
			t.Fatal(err)
		}
		return resizeFind(t, got, "Job")["metadata"].(map[string]any)["name"].(string)
	}
	if a, b := jobName("100Gi"), jobName("100Gi"); a != b {
		t.Fatalf("the same render named the Job %s and %s", a, b)
	}
	if jobName("100Gi") == jobName("200Gi") {
		t.Fatal("a changed declared size must be a new Job (a Job's spec is immutable, and it must run again)")
	}
}

func TestExpandStatefulSetResizeJobs_PullSecretsRideTheServiceAccount(t *testing.T) {
	got, err := expandStatefulSetResizeJobs([]any{resizeSTS("x", "ns", "hub", "", "1Gi")},
		resizeJobOpts{ImagePullSecrets: map[workloadGroupKey][]string{{cluster: "hub", namespace: "ns"}: {"ghcr-creds"}}})
	if err != nil {
		t.Fatal(err)
	}
	sa := resizeFind(t, got, "ServiceAccount")
	secrets, _ := sa["imagePullSecrets"].([]any)
	if len(secrets) != 1 || secrets[0].(map[string]any)["name"] != "ghcr-creds" {
		t.Fatalf("imagePullSecrets = %v", sa["imagePullSecrets"])
	}
}

func TestDecodeStatefulSetResize(t *testing.T) {
	if d, img, _, err := decodeStatefulSetResize(nil); d || img != "" || err != nil {
		t.Fatalf("absent: %v %q %v", d, img, err)
	}
	if d, img, _, err := decodeStatefulSetResize(map[string]any{"image": "i", "disabled": true}); !d || img != "i" || err != nil {
		t.Fatalf("declared: %v %q %v", d, img, err)
	}
	if _, _, _, err := decodeStatefulSetResize(map[string]any{"imag": "i"}); err == nil {
		t.Fatal("a misspelt key must be refused, not ignored")
	}
}
