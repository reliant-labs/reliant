package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/reliant-labs/forge/pkg/release"
)

// The replicas handover and the metrics-server refusal, with kubectl behind
// the kubectlRun seam (replicas_handover.go). The behaviour against a real API
// server is replicas_handover_envtest_test.go's.

// autoscaledDeploy is a forge workload's Deployment as the renderer emits it
// for an autoscaled workload: no spec.replicas.
func autoscaledDeploy(name, ns string) string {
	nsLine := ""
	if ns != "" {
		nsLine = "\n  namespace: " + ns
	}
	return `apiVersion: apps/v1
kind: Deployment
metadata:
  name: ` + name + nsLine + `
  labels: {app.kubernetes.io/name: ` + name + `, forge.dev/kind: service}
spec:
  selector: {matchLabels: {app.kubernetes.io/name: ` + name + `}}
  template:
    metadata: {labels: {app.kubernetes.io/name: ` + name + `}}
    spec: {containers: [{name: app, image: example/app:v1}]}`
}

func hpaDoc(name, ns, target string, minR, maxR int) string {
	nsLine := ""
	if ns != "" {
		nsLine = "\n  namespace: " + ns
	}
	minLine := ""
	if minR > 0 {
		minLine = "\n  minReplicas: " + itoa(minR)
	}
	return `apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: ` + name + nsLine + `
spec:
  scaleTargetRef: {apiVersion: apps/v1, kind: Deployment, name: ` + target + `}` + minLine + `
  maxReplicas: ` + itoa(maxR) + `
  metrics: [{type: Resource, resource: {name: cpu, target: {type: Utilization, averageUtilization: 70}}}]`
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestAutoscaledDeployments(t *testing.T) {
	pinned := strings.Replace(autoscaledDeploy("web", "prod"), "spec:\n", "spec:\n  replicas: 3\n", 1)
	chartDeploy := `apiVersion: apps/v1
kind: Deployment
metadata: {name: podinfo, namespace: prod}
spec: {selector: {matchLabels: {app: podinfo}}, template: {metadata: {labels: {app: podinfo}}, spec: {containers: [{name: c, image: x}]}}}`
	nullReplicas := strings.Replace(autoscaledDeploy("nulled", "prod"), "spec:\n", "spec:\n  replicas: null\n", 1)
	s := stream(
		autoscaledDeploy("api", ""), // namespace from -n
		hpaDoc("api", "prod", "api", 2, 6),
		pinned, hpaDoc("web", "prod", "web", 2, 6), // states replicas: not autoscaled
		autoscaledDeploy("worker", "prod"), hpaDoc("other", "prod", "elsewhere", 1, 3), // HPA targets another name
		autoscaledDeploy("split", "prod"), hpaDoc("split", "staging", "split", 1, 3), // HPA in another namespace
		chartDeploy, hpaDoc("podinfo", "prod", "podinfo", 0, 4), // a chart's: no forge.dev/kind, default min
		nullReplicas, hpaDoc("nulled", "prod", "nulled", 1, 2),
		"apiVersion: v1\nkind: Service\nmetadata: {name: api, namespace: prod}\nspec: {ports: [{port: 80}]}",
	)
	got := autoscaledDeployments(s, "prod")
	want := []autoscaledDeployment{
		{Name: "api", Namespace: "", MinReplicas: 2, MaxReplicas: 6, ForgeWorkload: true},
		{Name: "podinfo", Namespace: "prod", MinReplicas: 1, MaxReplicas: 4, ForgeWorkload: false},
		{Name: "nulled", Namespace: "prod", MinReplicas: 1, MaxReplicas: 2, ForgeWorkload: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("autoscaledDeployments:\n got %+v\nwant %+v", got, want)
	}
	if got := autoscaledDeployments(stream(autoscaledDeploy("api", "prod")), "prod"); len(got) != 0 {
		t.Errorf("a Deployment without replicas and no HPA is not autoscaled: %+v", got)
	}
	// An HPA labelled as a forge workload's marks the pair even when the
	// Deployment's label is missing.
	labelledHPA := strings.Replace(hpaDoc("podinfo", "prod", "podinfo", 1, 4), "metadata:\n", "metadata:\n  labels: {forge.dev/kind: worker}\n", 1)
	if got := autoscaledDeployments(stream(chartDeploy, labelledHPA), "prod"); len(got) != 1 || !got[0].ForgeWorkload {
		t.Errorf("want the HPA's forge.dev/kind to mark the workload: %+v", got)
	}
}

func TestClampReplicas(t *testing.T) {
	for _, c := range []struct{ v, min, max, want int64 }{
		{4, 2, 10, 4},
		{1, 2, 10, 2},
		{12, 2, 10, 10},
		{10, 2, 10, 10},
		{0, 1, 3, 1},
		{5, 1, 0, 5}, // no usable max
	} {
		if got := clampReplicas(c.v, c.min, c.max); got != c.want {
			t.Errorf("clampReplicas(%d, %d, %d) = %d, want %d", c.v, c.min, c.max, got, c.want)
		}
	}
}

const (
	mfForgeOwnsReplicas = `[{"manager":"kubectl","operation":"Apply","apiVersion":"apps/v1","fieldsType":"FieldsV1",
  "fieldsV1":{"f:spec":{"f:replicas":{},"f:selector":{}}}}]`
	mfForgeOwnsOther = `[{"manager":"kubectl","operation":"Apply","apiVersion":"apps/v1","fieldsType":"FieldsV1",
  "fieldsV1":{"f:spec":{"f:selector":{}}}},
 {"manager":"kube-controller-manager","operation":"Update","apiVersion":"apps/v1","subresource":"scale","fieldsType":"FieldsV1",
  "fieldsV1":{"f:spec":{"f:replicas":{}}}}]`
	mfHandoverOwns = `[{"manager":"kubectl","operation":"Apply","apiVersion":"apps/v1","fieldsType":"FieldsV1",
  "fieldsV1":{"f:spec":{"f:selector":{}}}},
 {"manager":"forge-replicas-handover","operation":"Apply","apiVersion":"apps/v1","fieldsType":"FieldsV1",
  "fieldsV1":{"f:spec":{"f:replicas":{}}}}]`
	mfKubectlUpdateOwns = `[{"manager":"kubectl","operation":"Update","apiVersion":"apps/v1","fieldsType":"FieldsV1",
  "fieldsV1":{"f:spec":{"f:replicas":{}}}}]`
)

func TestForgeOwnsReplicas(t *testing.T) {
	for _, c := range []struct {
		name, mf string
		want     bool
	}{
		{"forge's apply manager owns it", mfForgeOwnsReplicas, true},
		{"the HPA controller owns it through /scale", mfForgeOwnsOther, false},
		{"the handover manager owns it", mfHandoverOwns, false},
		{"kubectl Update is not forge's apply", mfKubectlUpdateOwns, false},
		{"no managedFields", `[]`, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := forgeOwnsReplicas(liveFixture(t, c.mf, map[string]any{"replicas": 4}))
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("forgeOwnsReplicas = %v, want %v", got, c.want)
			}
		})
	}
}

func TestReplicasHandoverManifest(t *testing.T) {
	got := string(replicasHandoverManifest("api", "prod", 3))
	want := `{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"api","namespace":"prod"},"spec":{"replicas":3}}`
	if got != want {
		t.Errorf("handover manifest:\n got %s\nwant %s", got, want)
	}
}

func TestReplicasHandoverArgs(t *testing.T) {
	got := strings.Join(replicasHandoverArgs(), " ")
	want := "apply --server-side --force-conflicts --field-manager=forge-replicas-handover --validate=false -f -"
	if got != want {
		t.Errorf("argv:\n got %s\nwant %s", got, want)
	}
	// The handover's manager must never be one convergeFieldOwnership takes
	// over, or the next deploy would hand the field straight back to forge.
	if takenOverManager(metav1.ManagedFieldsEntry{Manager: replicasHandoverManager, Operation: metav1.ManagedFieldsOperationApply}) {
		t.Errorf("convergeFieldOwnership would take over %s", replicasHandoverManager)
	}
}

// handoverKubectl answers the handover's reads and records its writes.
type handoverKubectl struct {
	live    []map[string]any
	getErr  string
	applyFn func(stdin []byte) ([]byte, error)
	calls   []string
	applied []string
}

func (f *handoverKubectl) run(_ context.Context, args []string, stdin []byte) ([]byte, []byte, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	switch args[0] {
	case "get":
		b, _ := json.Marshal(map[string]any{"kind": "List", "items": f.live})
		if f.getErr != "" {
			return b, []byte(f.getErr), errors.New("exit status 1")
		}
		return b, nil, nil
	case "apply":
		f.applied = append(f.applied, string(stdin))
		if f.applyFn != nil {
			stderr, err := f.applyFn(stdin)
			return nil, stderr, err
		}
		return nil, nil, nil
	}
	return nil, nil, errors.New("unexpected kubectl " + args[0])
}

func liveDeployment(t *testing.T, name, mf string, replicas int) map[string]any {
	t.Helper()
	o := liveFixture(t, mf, map[string]any{"replicas": replicas}).raw
	o["metadata"].(map[string]any)["name"] = name
	o["metadata"].(map[string]any)["namespace"] = "prod"
	return o
}

func TestHandoverReplicasAfterPreRolloutGate(t *testing.T) {
	deployment := autoscaledDeploy("api", "prod")
	hpa := hpaDoc("api", "prod", "api", 2, 6)
	job := "apiVersion: batch/v1\nkind: Job\nmetadata: {name: migrate, namespace: prod}"
	support := "apiVersion: v1\nkind: ServiceAccount\nmetadata: {name: migrate, namespace: prod}"
	phases, err := partitionRolloutPhases(stream(deployment, hpa, job, support))
	if err != nil {
		t.Fatal(err)
	}
	if !phases.gated() || phases.support != support || phases.preJobs != job {
		t.Fatalf("migration support and Job must still precede workloads: %+v", phases)
	}
	if phases.workloads != stream(deployment, hpa) {
		t.Fatalf("Deployment and HPA must share the held-back apply: %s", phases.workloads)
	}
	// Exercise the handover with the actual stream sent after the gate, not
	// the original unsplit render. Previously this stream lacked the HPA and
	// handed over nothing, so SSA would default the live count of 4 to 1.
	f := &handoverKubectl{live: []map[string]any{liveDeployment(t, "api", mfForgeOwnsReplicas, 4)}}
	if err := handoverReplicas(context.Background(), f.run, "prod", phases.workloads, nil); err != nil {
		t.Fatal(err)
	}
	want := string(replicasHandoverManifest("api", "prod", 4))
	if len(f.applied) != 1 || f.applied[0] != want {
		t.Fatalf("want replica ownership handed over at 4, got %v", f.applied)
	}
}

func TestAutoscaleAfterDeliveryUnitSplit(t *testing.T) {
	deployment := strings.Replace(autoscaledDeploy("api", ""), "labels: {", "labels: {forge.dev/delivery-unit: app, ", 1)
	base := strings.Replace(autoscaledDeploy("base", "prod"), "labels: {", "labels: {forge.dev/delivery-unit: base, ", 1)
	// The HPA precedes its target and has no unit label. Its target's
	// resolved namespace and unit, not document order, determine the wave.
	hpa := hpaDoc("api", "prod", "api", 2, 6)
	support := "apiVersion: v1\nkind: ServiceAccount\nmetadata: {name: app, namespace: prod}"
	s := stream(hpa, base, support, deployment)
	order, err := planUnitWaves([]release.DeliveryUnit{{Name: "base"}, {Name: "app", After: []string{"base"}}}, s)
	if err != nil || len(order) < 2 {
		t.Fatalf("expected staged delivery units, got %v: %v", order, err)
	}
	head, stages := splitUnitStages(s, order, "prod")
	if head != support || stages.final != "" {
		t.Fatalf("only support should go ahead: head=%s final=%s", head, stages.final)
	}
	var appStream string
	for _, wave := range stages.waves {
		if strings.Contains(wave.stream, "kind: HorizontalPodAutoscaler") {
			appStream = wave.stream
			if len(wave.workloads) != 1 || wave.workloads[0].Kind != "Deployment" || wave.workloads[0].Name != "api" {
				t.Fatalf("await only the target Deployment, got %v", wave.workloads)
			}
		}
	}
	if appStream != stream(hpa, deployment) {
		t.Fatalf("HPA must share its target's wave: %s", appStream)
	}
	f := &handoverKubectl{live: []map[string]any{liveDeployment(t, "api", mfForgeOwnsReplicas, 4)}}
	if err := handoverReplicas(context.Background(), f.run, "prod", appStream, nil); err != nil {
		t.Fatal(err)
	}
	if want := string(replicasHandoverManifest("api", "prod", 4)); len(f.applied) != 1 || f.applied[0] != want {
		t.Fatalf("want replica ownership handed over at 4, got %v", f.applied)
	}
	metrics := &metricsKubectl{stderr: "Error from server (NotFound)", err: errors.New("exit status 1")}
	if err := refuseAutoscaleWithoutMetrics(context.Background(), metrics.run, "", "prod", appStream, nil); err == nil || len(metrics.calls) != 1 {
		t.Fatalf("staged autoscale must refuse missing metrics, got %v, calls %v", err, metrics.calls)
	}
}

func TestHandoverReplicas(t *testing.T) {
	s := stream(autoscaledDeploy("api", ""), hpaDoc("api", "prod", "api", 2, 3))

	t.Run("forge owns replicas: hands the clamped count to the handover manager", func(t *testing.T) {
		f := &handoverKubectl{live: []map[string]any{liveDeployment(t, "api", mfForgeOwnsReplicas, 4)}}
		var out bytes.Buffer
		if err := handoverReplicas(context.Background(), f.run, "prod", s, &out); err != nil {
			t.Fatal(err)
		}
		if len(f.calls) != 2 || f.calls[0] != "get -f - -o json --ignore-not-found --show-managed-fields -n prod" ||
			f.calls[1] != strings.Join(replicasHandoverArgs(), " ") {
			t.Fatalf("calls:\n%s", strings.Join(f.calls, "\n"))
		}
		if want := `{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"api","namespace":"prod"},"spec":{"replicas":3}}`; f.applied[0] != want {
			t.Errorf("handover applied %s, want %s", f.applied[0], want)
		}
		if !strings.Contains(out.String(), "keeping spec.replicas at 3, clamped from 4 into the autoscaler's [2, 3]") {
			t.Errorf("report: %q", out.String())
		}
	})

	t.Run("forge no longer owns replicas: reads and writes nothing", func(t *testing.T) {
		for _, mf := range []string{mfForgeOwnsOther, mfHandoverOwns} {
			f := &handoverKubectl{live: []map[string]any{liveDeployment(t, "api", mf, 4)}}
			var out bytes.Buffer
			if err := handoverReplicas(context.Background(), f.run, "prod", s, &out); err != nil {
				t.Fatal(err)
			}
			if len(f.applied) != 0 || out.Len() != 0 {
				t.Errorf("a Deployment forge does not own replicas of was written: %v %q", f.applied, out.String())
			}
		}
	})

	t.Run("not live yet: nothing to hand over", func(t *testing.T) {
		f := &handoverKubectl{}
		if err := handoverReplicas(context.Background(), f.run, "prod", s, nil); err != nil {
			t.Fatal(err)
		}
		if len(f.calls) != 1 || len(f.applied) != 0 {
			t.Errorf("calls %v, applied %v", f.calls, f.applied)
		}
	})

	t.Run("no autoscaled Deployment: kubectl is never run", func(t *testing.T) {
		f := &handoverKubectl{}
		pinned := strings.Replace(autoscaledDeploy("api", "prod"), "spec:\n", "spec:\n  replicas: 4\n", 1)
		if err := handoverReplicas(context.Background(), f.run, "prod", stream(pinned, hpaDoc("api", "prod", "api", 2, 3)), nil); err != nil {
			t.Fatal(err)
		}
		if len(f.calls) != 0 {
			t.Errorf("calls %v", f.calls)
		}
	})

	t.Run("a failed read or handover fails the apply", func(t *testing.T) {
		f := &handoverKubectl{getErr: "Unable to connect to the server: dial tcp: i/o timeout"}
		if err := handoverReplicas(context.Background(), f.run, "prod", s, nil); err == nil {
			t.Error("a failed read must fail the apply rather than reset replicas to 1")
		}
		f = &handoverKubectl{live: []map[string]any{liveDeployment(t, "api", mfForgeOwnsReplicas, 4)},
			applyFn: func([]byte) ([]byte, error) { return []byte("denied"), errors.New("exit status 1") }}
		if err := handoverReplicas(context.Background(), f.run, "prod", s, nil); err == nil || !strings.Contains(err.Error(), "denied") {
			t.Errorf("a failed handover must fail the apply, naming why: %v", err)
		}
	})
}

// metricsKubectl answers the discovery read with stderr and err.
type metricsKubectl struct {
	stderr string
	err    error
	calls  []string
}

func (f *metricsKubectl) run(_ context.Context, args []string, _ []byte) ([]byte, []byte, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	return nil, []byte(f.stderr), f.err
}

func TestRefuseAutoscaleWithoutMetrics(t *testing.T) {
	s := stream(autoscaledDeploy("api", "prod"), hpaDoc("api", "prod", "api", 2, 6),
		autoscaledDeploy("worker", "prod"), hpaDoc("worker", "prod", "worker", 1, 4))
	exit1 := errors.New("exit status 1")

	t.Run("not served: refused with the fix", func(t *testing.T) {
		f := &metricsKubectl{stderr: "Error from server (NotFound): the server could not find the requested resource", err: exit1}
		err := refuseAutoscaleWithoutMetrics(context.Background(), f.run, "k3d-dev", "prod", s, nil)
		want := "workload(s) api, worker declare autoscale, but cluster k3d-dev does not serve the metrics.k8s.io API (metrics-server); " +
			"a HorizontalPodAutoscaler there could never scale. Install metrics-server (k3d ships it unless created with " +
			"--disable=metrics-server), or remove autoscale"
		if err == nil || err.Error() != want {
			t.Errorf("refusal:\n got %v\nwant %s", err, want)
		}
		if len(f.calls) != 1 || f.calls[0] != "get --raw /apis/metrics.k8s.io/v1beta1" {
			t.Errorf("calls %v", f.calls)
		}
	})

	t.Run("served: proceeds", func(t *testing.T) {
		f := &metricsKubectl{}
		if err := refuseAutoscaleWithoutMetrics(context.Background(), f.run, "k3d-dev", "prod", s, nil); err != nil {
			t.Error(err)
		}
	})

	t.Run("unconfirmed: warns and proceeds", func(t *testing.T) {
		f := &metricsKubectl{stderr: "Error from server (ServiceUnavailable): the server is currently unable to handle the request", err: exit1}
		var out bytes.Buffer
		if err := refuseAutoscaleWithoutMetrics(context.Background(), f.run, "k3d-dev", "prod", s, &out); err != nil {
			t.Errorf("a registered but unready metrics API must not refuse: %v", err)
		}
		if !strings.Contains(out.String(), "warning: could not confirm that cluster k3d-dev serves the metrics.k8s.io API") {
			t.Errorf("want a warning, got %q", out.String())
		}
	})

	t.Run("no forge autoscaled workload: no read", func(t *testing.T) {
		f := &metricsKubectl{stderr: "Error from server (NotFound)", err: exit1}
		chart := `apiVersion: apps/v1
kind: Deployment
metadata: {name: podinfo, namespace: prod}
spec: {selector: {matchLabels: {app: podinfo}}, template: {metadata: {labels: {app: podinfo}}, spec: {containers: [{name: c, image: x}]}}}`
		if err := refuseAutoscaleWithoutMetrics(context.Background(), f.run, "k3d-dev", "prod", stream(chart, hpaDoc("podinfo", "prod", "podinfo", 1, 4)), nil); err != nil {
			t.Errorf("a chart's HPA is not forge's to judge: %v", err)
		}
		if len(f.calls) != 0 {
			t.Errorf("calls %v", f.calls)
		}
	})
}
