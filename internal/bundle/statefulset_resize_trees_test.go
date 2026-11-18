package bundle

// THE RESIZE JOB, THROUGH A REAL BUNDLE BUILD.
//
// internal/cluster renders the pre-rollout Job that converges a StatefulSet
// whose declared volume grew; these tests hand its objects to the real bundle
// build and read the result back the way a reconciler does. They pin the
// three claims the design rests on: the Job is accepted by a build that
// REFUSES unsafe Jobs (Reconciled), it lands in a pre tree that the StatefulSet's
// rollout tree waits on, and the StatefulSet itself is prune-guarded.

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/reliant-labs/forge/pkg/release"
	"github.com/reliant-labs/forge/pkg/stsresize"
)

func resizeStream(t *testing.T, cluster, unit string) string {
	t.Helper()
	sts := &appsv1.StatefulSet{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "StatefulSet"},
		ObjectMeta: metav1.ObjectMeta{Name: "clickhouse", Namespace: "observability-data", Labels: map[string]string{"forge.dev/workload": "clickhouse"}},
		Spec: appsv1.StatefulSetSpec{
			ServiceName: "clickhouse",
			Selector:    &metav1.LabelSelector{MatchLabels: map[string]string{"app": "clickhouse"}},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{
				ObjectMeta: metav1.ObjectMeta{Name: "data"},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("100Gi")}},
				},
			}},
		},
	}
	objs, err := stsresize.Build(stsresize.JobSpec{
		Namespace: "observability-data", Unit: unit, StatefulSets: []*appsv1.StatefulSet{sts},
		Labels: map[string]string{"forge.dev/workload": "clickhouse"},
	})
	if err != nil {
		t.Fatal(err)
	}
	docs, err := objs.Manifests()
	if err != nil {
		t.Fatal(err)
	}
	stsDoc, err := yaml.Marshal(sts)
	if err != nil {
		t.Fatal(err)
	}
	var parts []string
	for _, d := range docs {
		meta := d["metadata"].(map[string]any)
		labels := meta["labels"].(map[string]any)
		if unit != "" {
			labels["forge.dev/delivery-unit"] = unit
		}
		b, err := yaml.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		parts = append(parts, "# cluster: "+cluster+"\n"+string(b))
	}
	unitLabel := ""
	if unit != "" {
		unitLabel = "\n    forge.dev/delivery-unit: " + unit
	}
	stsText := strings.Replace(string(stsDoc), "  labels:\n    forge.dev/workload: clickhouse", "  labels:\n    forge.dev/workload: clickhouse"+unitLabel, 1)
	parts = append(parts, "# cluster: "+cluster+"\n"+stsText)
	return strings.Join(parts, "---\n")
}

func TestResizeJob_BuildsIntoAPreTreeThatGatesTheStatefulSetsRollout(t *testing.T) {
	in := buildFixture()
	in.Shape = ShapeInput{Kind: release.EnvSelfManaged, Clusters: []string{"gke-prod"}, Manifests: resizeStream(t, "gke-prod", "infra")}
	in.Units = []release.DeliveryUnit{{Name: "infra", After: []string{}}}
	// Reconciled: the build REFUSES a Job with a TTL or an unhashed name, so a
	// green build here is the proof the resize Job is one a reconciler accepts.
	in.Reconciled = true
	b := mustBuild(t, in)

	by := map[string]release.BundleTree{}
	for _, tr := range b.Doc.Trees {
		by[tr.Path] = tr
	}
	pre, ok := by["manifests/gke-prod/infra/pre"]
	if !ok {
		t.Fatalf("no infra/pre tree: %v", b.Doc.Trees)
	}
	if len(pre.Health) != 1 || pre.Health[0].Kind != "Job" || !strings.HasPrefix(pre.Health[0].Name, "sts-volume-resize-infra-") {
		t.Fatalf("infra/pre is judged on %v, want the resize Job", pre.Health)
	}
	rollout := by["manifests/gke-prod/infra/rollout"]
	if !reflect.DeepEqual(rollout.DependsOn, []string{"manifests/gke-prod/infra/pre"}) {
		t.Fatalf("the StatefulSet's rollout depends on %v, want the pre tree", rollout.DependsOn)
	}
	if len(rollout.Health) != 1 || rollout.Health[0].Kind != "StatefulSet" {
		t.Fatalf("rollout health = %v", rollout.Health)
	}

	// The identity rides the pre tree, so it exists before the Job runs; the
	// StatefulSet is alone in rollout.
	layer, _ := b.Layer(release.BundleManifestsLayer)
	summary, err := release.ValidateManifestsLayer(bytes.NewReader(layer))
	if err != nil {
		t.Fatalf("the ingest gate refuses the layer: %v", err)
	}
	placed := map[string]string{}
	for _, o := range summary.Objects {
		placed[o.Kind] = treeOf(o.Path)
	}
	for _, kind := range []string{"ServiceAccount", "Role", "RoleBinding", "NetworkPolicy", "Job"} {
		if placed[kind] != "manifests/gke-prod/infra/pre" {
			t.Errorf("%s is in %q, want infra/pre", kind, placed[kind])
		}
	}
	if placed["StatefulSet"] != "manifests/gke-prod/infra/rollout" {
		t.Errorf("StatefulSet is in %q", placed["StatefulSet"])
	}
}
