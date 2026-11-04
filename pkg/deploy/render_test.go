package deploy

import (
	"encoding/json"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/reliant-labs/forge/pkg/dbgrants"
	"github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
	"github.com/reliant-labs/forge/pkg/release"
	"github.com/reliant-labs/forge/pkg/serverkit/drainbudget"
)

var ctx = Context{Namespace: "acme-prod", PartOf: "proj"}

const testImage = "ghcr.io/acme/app:v1"

func wl(name string, spec v1alpha1.WorkloadSpec) v1alpha1.Workload {
	if spec.Image == "" {
		spec.Image = testImage
	}
	w := v1alpha1.Workload{Spec: spec}
	w.Name = name
	return w
}

func svc(name string, ports ...v1alpha1.Port) v1alpha1.Workload {
	return wl(name, v1alpha1.WorkloadSpec{Kind: v1alpha1.KindService, Ports: ports})
}

func httpPort(expose bool) v1alpha1.Port {
	return v1alpha1.Port{Name: "http", Port: 8080, Expose: expose}
}

func render(t *testing.T, p v1alpha1.Profile, ws ...v1alpha1.Workload) []*unstructured.Unstructured {
	t.Helper()
	objs, err := RenderWorkloads(ws, p, ctx)
	if err != nil {
		t.Fatalf("RenderWorkloads: %v", err)
	}
	return objs
}

// objects keys every rendered object by kind/name, as decoded JSON.
func objects(t *testing.T, objs []*unstructured.Unstructured) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, o := range objs {
		b, err := json.Marshal(o.Object)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		out[o.GetKind()+"/"+o.GetName()] = m
	}
	return out
}

func keysOf(m map[string]map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func get(m any, path ...any) any {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			mm, _ := m.(map[string]any)
			m = mm[k]
		case int:
			s, _ := m.([]any)
			if k >= len(s) {
				return nil
			}
			m = s[k]
		}
	}
	return m
}

func podOf(o map[string]any) any {
	if o["kind"] == "CronJob" {
		return get(o, "spec", "jobTemplate", "spec", "template", "spec")
	}
	return get(o, "spec", "template", "spec")
}

func mustErr(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want it to contain %q", err, want)
	}
}

// TestPerKindObjects pins the object set each kind renders (the per-kind
// table in RenderWorkloads' doc, expand.k:496-801).
func TestPerKindObjects(t *testing.T) {
	cases := []struct {
		w    v1alpha1.Workload
		want []string
	}{
		// Full with no Context.Network: no per-workload NetworkPolicy
		// (TestIngressPolicyOptIn).
		{svc("api", httpPort(true)), []string{"Deployment/api", "Service/api", "ServiceAccount/api"}},
		{wl("w", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindWorker}), []string{"Deployment/w", "ServiceAccount/w"}},
		{wl("c", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindCron, Schedule: "@daily", Args: []string{"sweep"}}), []string{"CronJob/c", "ServiceAccount/c"}},
		// An operator: a namespaced Role (config read + lease) beside the
		// ClusterRole its CRDs derive.
		{wl("op", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindOperator, Group: "e.com", CRDs: []string{"Widget"}}), []string{
			"ClusterRole/op-acme-prod-clusterrole", "ClusterRoleBinding/op-acme-prod-clusterrolebinding", "Deployment/op",
			"Role/op-role", "RoleBinding/op-rolebinding", "ServiceAccount/op",
		}},
		// An operator that declares ports is dialled (webhook, metrics,
		// workspace-controller's :9191), so it gets a Service. A worker
		// with ports still does not: nothing resolves it by name.
		{wl("op2", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindOperator, Group: "e.com", CRDs: []string{"Widget"}, Ports: []v1alpha1.Port{{Name: "http", Port: 9191}}}), []string{
			"ClusterRole/op2-acme-prod-clusterrole", "ClusterRoleBinding/op2-acme-prod-clusterrolebinding", "Deployment/op2",
			"Role/op2-role", "RoleBinding/op2-rolebinding", "Service/op2", "ServiceAccount/op2",
		}},
		{wl("w2", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindWorker, Ports: []v1alpha1.Port{{Name: "http", Port: 8080}}}), []string{"Deployment/w2", "ServiceAccount/w2"}},
		{wl("cli", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindTool}), nil},
	}
	for _, c := range cases {
		t.Run(c.w.Name, func(t *testing.T) {
			got := keysOf(objects(t, render(t, v1alpha1.ProfileFull, c.w)))
			if len(got) == 0 {
				got = nil
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("objects = %v\nwant      %v", got, c.want)
			}
		})
	}
	// A standalone job's name carries its spec hash.
	objs := objects(t, render(t, v1alpha1.ProfileFull, wl("seed", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindJob, Args: []string{"seed"}})))
	var job map[string]any
	for k, o := range objs {
		if strings.HasPrefix(k, "Job/seed-") {
			job = o
		}
	}
	if job == nil || len(objs) != 2 {
		t.Fatalf("standalone job: want Job/seed-<hash> + SA, got %v", keysOf(objs))
	}
	if get(job, "metadata", "annotations", AnnotationDeployPhase) != "pre-rollout" || get(job, "metadata", "labels", LabelJobName) != "seed" {
		t.Errorf("job metadata = %v", get(job, "metadata"))
	}
	if get(job, "spec", "backoffLimit") != float64(6) || get(podOf(job), "restartPolicy") != "OnFailure" {
		t.Errorf("job spec = %v", get(job, "spec"))
	}
}

// TestServiceDefaultPort: a service that declares no ports serves `http`
// :8080 (expand.k:502), and is NOT made public by omission.
func TestServiceDefaultPort(t *testing.T) {
	// Restricted, so the per-workload NetworkPolicy is emitted.
	o := objects(t, render(t, v1alpha1.ProfileRestricted, svc("api")))
	want := []any{map[string]any{"name": "http", "port": float64(8080), "targetPort": float64(8080), "protocol": "TCP"}}
	if got := get(o["Service/api"], "spec", "ports"); !reflect.DeepEqual(got, want) {
		t.Fatalf("service ports = %v", got)
	}
	if get(o["NetworkPolicy/api-ingress"], "spec", "ingress", 0, "from", 0) == nil {
		t.Error("the default port must be namespace-private, not exposed")
	}
	// The TCP default probe (EffectiveProbes) lands on it.
	if get(podOf(o["Deployment/api"]), "containers", 0, "readinessProbe", "tcpSocket", "port") != float64(8080) {
		t.Errorf("default probe = %v", get(podOf(o["Deployment/api"]), "containers", 0, "readinessProbe"))
	}
}

// TestProbesSplit: readiness and liveness are separate objects; liveness
// timings are Probes.Liveness(); TCP is a connect; nil means none.
func TestProbesSplit(t *testing.T) {
	api := svc("api", httpPort(true))
	api.Spec.Probes = &v1alpha1.Probes{}
	c := get(podOf(objects(t, render(t, v1alpha1.ProfileFull, api))["Deployment/api"]), "containers", 0)
	ready, live := get(c, "readinessProbe"), get(c, "livenessProbe")
	if get(ready, "httpGet", "path") != "/readyz" || get(live, "httpGet", "path") != "/healthz" {
		t.Fatalf("paths: readiness %v liveness %v", ready, live)
	}
	want := v1alpha1.Probes{}.Liveness()
	if get(live, "initialDelaySeconds") != float64(want.InitialDelaySeconds) || get(live, "periodSeconds") != float64(want.PeriodSeconds) {
		t.Errorf("liveness timings %v, want %+v", live, want)
	}
	r := v1alpha1.Probes{}.Readiness()
	if get(ready, "timeoutSeconds") != float64(r.TimeoutSeconds) || get(ready, "periodSeconds") != float64(r.PeriodSeconds) {
		t.Errorf("readiness timings %v, want %+v", ready, r)
	}

	pg := wl("pg", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindService, Ports: []v1alpha1.Port{{Name: "pg", Port: 5432}}, Probes: &v1alpha1.Probes{TCP: true}})
	c = get(podOf(objects(t, render(t, v1alpha1.ProfileFull, pg))["Deployment/pg"]), "containers", 0)
	for _, probe := range []string{"readinessProbe", "livenessProbe"} {
		if get(c, probe, "tcpSocket", "port") != float64(5432) || get(c, probe, "httpGet") != nil {
			t.Errorf("%s: want a TCP connect on 5432, got %v", probe, get(c, probe))
		}
	}

	w := wl("w", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindWorker})
	c = get(podOf(objects(t, render(t, v1alpha1.ProfileFull, w))["Deployment/w"]), "containers", 0)
	if get(c, "readinessProbe") != nil || get(c, "livenessProbe") != nil || get(c, "ports") != nil {
		t.Errorf("a worker with no probes and no ports must get neither: %v", c)
	}
	// A worker probed on probes.port states the port it answers on
	// (expand.k:445-452).
	w.Spec.Probes = &v1alpha1.Probes{Port: 9090}
	c = get(podOf(objects(t, render(t, v1alpha1.ProfileFull, w))["Deployment/w"]), "containers", 0)
	if get(c, "ports", 0, "containerPort") != float64(9090) || get(c, "readinessProbe", "httpGet", "port") != float64(9090) {
		t.Errorf("probed worker: %v", c)
	}
	// An operator's manager serves /healthz + /readyz (serverkit on its
	// PORT, or controller-runtime's probe listener), so a declared probe is
	// rendered like a service's. An undeclared one stays absent: forge
	// cannot know an arbitrary manager's paths (the lowering writes the
	// default for a forge-built one).
	op := wl("op", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindOperator, CRDs: []string{"Widget"}, Ports: []v1alpha1.Port{{Name: "http", Port: 9191}}})
	c = get(podOf(objects(t, render(t, v1alpha1.ProfileFull, op))["Deployment/op"]), "containers", 0)
	if get(c, "readinessProbe") != nil || get(c, "livenessProbe") != nil {
		t.Errorf("an operator with no declared probes must get none: %v", c)
	}
	op.Spec.Probes = &v1alpha1.Probes{}
	c = get(podOf(objects(t, render(t, v1alpha1.ProfileFull, op))["Deployment/op"]), "containers", 0)
	if get(c, "readinessProbe", "httpGet", "path") != "/readyz" || get(c, "livenessProbe", "httpGet", "path") != "/healthz" ||
		get(c, "readinessProbe", "httpGet", "port") != float64(9191) {
		t.Errorf("probed operator: want /readyz + /healthz on 9191, got %v", c)
	}
}

// TestGracePeriod: PRE_STOP_DELAY + SHUTDOWN_TIMEOUT + a 5s margin, each read
// by serverkit's rule — zero is the default, negative is none — so the grace
// period is exactly what the server will spend. "0s" used to render 0 while
// the server spent its 30s default, and the kubelet SIGKILLed mid-drain.
func TestGracePeriod(t *testing.T) {
	for _, c := range []struct {
		env  []v1alpha1.EnvVar
		want float64
	}{
		{nil, 40},
		{[]v1alpha1.EnvVar{{Name: "PRE_STOP_DELAY", Value: "10s"}, {Name: "SHUTDOWN_TIMEOUT", Value: "1m"}}, 75},
		{[]v1alpha1.EnvVar{{Name: "SHUTDOWN_TIMEOUT", Value: "1h"}}, 3610},
		{[]v1alpha1.EnvVar{{Name: "SHUTDOWN_TIMEOUT", Value: "1m30s"}}, 100},
		{[]v1alpha1.EnvVar{{Name: "SHUTDOWN_TIMEOUT", Value: "1500ms"}}, 12}, // rounded up
		{[]v1alpha1.EnvVar{{Name: "PRE_STOP_DELAY", Value: "7s"}}, 42},
		{[]v1alpha1.EnvVar{{Name: "PRE_STOP_DELAY", Value: "0s"}, {Name: "SHUTDOWN_TIMEOUT", Value: "0s"}}, 40},   // zero: the defaults
		{[]v1alpha1.EnvVar{{Name: "PRE_STOP_DELAY", Value: "-1s"}}, 35},                                           // negative: no pause
		{[]v1alpha1.EnvVar{{Name: "SHUTDOWN_TIMEOUT", Value: "-1s"}}, 10},                                         // negative: no window
		{[]v1alpha1.EnvVar{{Name: "SHUTDOWN_TIMEOUT", ManagedSecret: &v1alpha1.ManagedSecretRef{Name: "X"}}}, 40}, // a reference has no literal
	} {
		w := svc("api")
		w.Spec.Env = c.env
		pod := podOf(objects(t, render(t, v1alpha1.ProfileFull, w))["Deployment/api"])
		if got := get(pod, "terminationGracePeriodSeconds"); got != c.want {
			t.Errorf("env %v: grace = %v, want %v", c.env, got, c.want)
		}
	}
}

// TestGracePeriodAgreesWithTheServersReader pins that the grace period is
// derived from the same reading of PRE_STOP_DELAY / SHUTDOWN_TIMEOUT a server
// main gets from drainbudget.FromEnv: for every value a server accepts, the
// pod's grace is the server's effective pre-stop + shutdown, each rounded up,
// plus the margin. A private copy of the rule drifted once (it read "-1s" as
// malformed); this is the check that the two readers cannot.
func TestGracePeriodAgreesWithTheServersReader(t *testing.T) {
	ceil := func(d time.Duration) float64 { return float64((d + time.Second - 1) / time.Second) }
	for _, pair := range [][2]string{
		{"", ""}, {"0", "0s"}, {"10s", "1m"}, {"7s", "45s"}, {"-1s", "30s"}, {"-1s", "-1s"},
		{"500ms", "1500ms"}, {"1m30s", "1h"},
	} {
		m := map[string]string{drainbudget.EnvPreStopDelay: pair[0], drainbudget.EnvShutdownTimeout: pair[1]}
		b, err := drainbudget.FromEnv(func(k string) string { return m[k] })
		if err != nil {
			t.Fatalf("FromEnv(%v): %v", m, err)
		}
		w := svc("api")
		for k, v := range m {
			if v != "" {
				w.Spec.Env = append(w.Spec.Env, v1alpha1.EnvVar{Name: k, Value: v})
			}
		}
		pod := podOf(objects(t, render(t, v1alpha1.ProfileFull, w))["Deployment/api"])
		want := ceil(b.PreStop()) + ceil(b.Shutdown()) + graceMarginSeconds
		if got := get(pod, "terminationGracePeriodSeconds"); got != want {
			t.Errorf("env %v: grace = %v, the server's budget implies %v", m, got, want)
		}
	}
}

// TestGracePeriodRefusesWhatTheServerRefuses: a drain value the server's
// config loader refuses at boot (a bare number, an interpolation, a typo)
// fails the render, naming the workload and the variable. It used to render
// the DEFAULT grace period for it — a pod whose grace period was derived from
// a budget its process would never run, because it would never start.
func TestGracePeriodRefusesWhatTheServerRefuses(t *testing.T) {
	for _, env := range [][]v1alpha1.EnvVar{
		{{Name: "SHUTDOWN_TIMEOUT", Value: "30"}},
		{{Name: "PRE_STOP_DELAY", Value: "-1"}},
		{{Name: "SHUTDOWN_TIMEOUT", Value: "${X}"}},
	} {
		w := svc("api")
		w.Spec.Env = env
		_, err := RenderWorkloads([]v1alpha1.Workload{w}, v1alpha1.ProfileFull, ctx)
		if err == nil {
			t.Errorf("env %v: rendered, want an error the server's loader would also give", env)
			continue
		}
		if !strings.Contains(err.Error(), `"api"`) || !strings.Contains(err.Error(), env[0].Name) {
			t.Errorf("env %v: error must name the workload and the variable; got: %v", env, err)
		}
	}
}

// TestReplicasSpreadAndPDB: replicas > 1 get soft spread and a PDB
// (expand.k:255-282); one replica gets neither.
func TestReplicasSpreadAndPDB(t *testing.T) {
	one := objects(t, render(t, v1alpha1.ProfileFull, svc("api")))
	if _, ok := one["PodDisruptionBudget/api-pdb"]; ok || get(podOf(one["Deployment/api"]), "topologySpreadConstraints") != nil {
		t.Error("one replica must get no PDB and no spread")
	}
	w := svc("api")
	w.Spec.Replicas = 3
	o := objects(t, render(t, v1alpha1.ProfileFull, w))
	pdb := o["PodDisruptionBudget/api-pdb"]
	if get(pdb, "spec", "maxUnavailable") != float64(1) || get(pdb, "spec", "selector", "matchLabels", LabelName) != "api" {
		t.Errorf("pdb = %v", pdb)
	}
	spread := get(podOf(o["Deployment/api"]), "topologySpreadConstraints", 0)
	if get(spread, "whenUnsatisfiable") != "ScheduleAnyway" || get(spread, "topologyKey") != "kubernetes.io/hostname" {
		t.Errorf("spread = %v", spread)
	}
}

// TestStorage: a PVC at /data, Recreate, fsGroup.
func TestStorage(t *testing.T) {
	w := svc("db")
	w.Spec.StorageGiB = 5
	o := objects(t, render(t, v1alpha1.ProfileFull, w))
	dep := o["Deployment/db"]
	pod := podOf(dep)
	if get(dep, "spec", "strategy", "type") != "Recreate" {
		t.Errorf("strategy = %v", get(dep, "spec", "strategy"))
	}
	if get(pod, "securityContext", "fsGroup") != float64(RunAsUser) {
		t.Error("storage needs fsGroup so the non-root container can write its volume")
	}
	if get(pod, "containers", 0, "volumeMounts", 1, "mountPath") != "/data" || get(pod, "volumes", 1, "persistentVolumeClaim", "claimName") != "db-data" {
		t.Errorf("mounts %v volumes %v", get(pod, "containers", 0, "volumeMounts"), get(pod, "volumes"))
	}
	pvc := o["PersistentVolumeClaim/db-data"]
	if get(pvc, "spec", "resources", "requests", "storage") != "5Gi" || get(pvc, "spec", "accessModes", 0) != "ReadWriteOnce" {
		t.Errorf("pvc = %v", pvc)
	}
	// Stateless: no fsGroup, no Recreate.
	o = objects(t, render(t, v1alpha1.ProfileFull, svc("api")))
	if get(podOf(o["Deployment/api"]), "securityContext", "fsGroup") != nil || get(o["Deployment/api"], "spec", "strategy", "type") != "RollingUpdate" {
		t.Error("a stateless workload gets neither fsGroup nor a Recreate strategy")
	}
}

// TestKeepsSemanticEmptyMaps guards the pruning trap: in Kubernetes an empty
// map is often the meaning.
func TestKeepsSemanticEmptyMaps(t *testing.T) {
	o := objects(t, render(t, v1alpha1.ProfileRestricted, svc("api", httpPort(false))))
	if peer := get(o["NetworkPolicy/api-ingress"], "spec", "ingress", 0, "from", 0); !reflect.DeepEqual(peer, map[string]any{"podSelector": map[string]any{}}) {
		t.Fatalf("private workload's same-namespace peer was lost: %#v", peer)
	}
	if ed := get(podOf(o["Deployment/api"]), "volumes", 0, "emptyDir"); !reflect.DeepEqual(ed, map[string]any{}) {
		t.Fatalf("/tmp emptyDir source was lost: %#v", ed)
	}
	for k, obj := range o {
		if get(obj, "status") != nil || get(obj, "metadata", "creationTimestamp") != nil || get(obj, "spec", "template", "metadata", "creationTimestamp") != nil {
			t.Errorf("%s carries server-owned fields", k)
		}
	}
}

// TestNetworkPolicy: namespace ingress on declared ports, any-source ingress
// on the exposed port, default-deny for a pod with no ports, none for an
// operator.
func TestNetworkPolicy(t *testing.T) {
	withNet := ctx
	withNet.Network = &EnvNetworkPolicy{}
	renderNet := func(w v1alpha1.Workload) map[string]map[string]any {
		t.Helper()
		objs, err := RenderWorkloads([]v1alpha1.Workload{w}, v1alpha1.ProfileFull, withNet)
		if err != nil {
			t.Fatal(err)
		}
		return objects(t, objs)
	}
	api := svc("api", httpPort(true), v1alpha1.Port{Name: "grpc", Port: 9090})
	np := renderNet(api)["NetworkPolicy/api-ingress"]
	if got := get(np, "spec", "policyTypes"); !reflect.DeepEqual(got, []any{"Ingress"}) {
		t.Errorf("policyTypes = %v; egress is the namespace's control", got)
	}
	private, public := get(np, "spec", "ingress", 0), get(np, "spec", "ingress", 1)
	if get(private, "ports", 0, "port") != float64(9090) || get(private, "from", 0) == nil || get(private, "ports", 1) != nil {
		t.Errorf("private rule = %v", private)
	}
	if get(public, "ports", 0, "port") != float64(8080) || get(public, "from") != nil {
		t.Errorf("public rule = %v (want from anywhere)", public)
	}
	np = renderNet(wl("w", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindWorker}))["NetworkPolicy/w-ingress"]
	if np == nil || get(np, "spec", "ingress") != nil {
		t.Errorf("a worker with no ports must get an ingress default-deny policy, got %v", np)
	}
}

// TestIngressPolicyOptIn: the per-workload ingress NetworkPolicy is the
// hosted boundary, so ProfileRestricted ALWAYS emits it. Under ProfileFull it
// is emitted only when the env opted into network policy (Context.Network),
// so a self-hosted cluster keeps its prior behaviour (no policy) unless the
// env declares one.
func TestIngressPolicyOptIn(t *testing.T) {
	count := func(p v1alpha1.Profile, c Context) int {
		t.Helper()
		objs, err := RenderWorkloads([]v1alpha1.Workload{svc("api", httpPort(true))}, p, c)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for k := range objects(t, objs) {
			if k == "NetworkPolicy/api-ingress" {
				n++
			}
		}
		return n
	}
	withNet := ctx
	withNet.Network = &EnvNetworkPolicy{}
	if n := count(v1alpha1.ProfileFull, ctx); n != 0 {
		t.Error("Full without Context.Network must emit no per-workload policy")
	}
	if n := count(v1alpha1.ProfileFull, withNet); n != 1 {
		t.Error("Full with Context.Network must emit the per-workload policy")
	}
	if n := count(v1alpha1.ProfileRestricted, ctx); n != 1 {
		t.Error("Restricted must ALWAYS emit the per-workload policy")
	}
}

// TestEnvNetworkBundle ports kcl/lib/netpol.k. It is ENV scope, so it rides
// Context.Network and is emitted once, not per workload.
func TestEnvNetworkBundle(t *testing.T) {
	c := ctx
	c.Network = &EnvNetworkPolicy{EgressPorts: []int32{443, 5432}, TelemetryNamespace: "observability", IngressNamespace: "gw"}
	objs, err := RenderWorkloads([]v1alpha1.Workload{svc("a"), svc("b")}, v1alpha1.ProfileFull, c)
	if err != nil {
		t.Fatal(err)
	}
	o := objects(t, objs)
	for _, name := range []string{"default-deny", "allow-dns", "allow-internal", "allow-egress-external", "allow-egress-telemetry", "allow-ingress-controller"} {
		if o["NetworkPolicy/"+name] == nil {
			t.Errorf("missing NetworkPolicy/%s", name)
		}
	}
	if got := get(o["NetworkPolicy/default-deny"], "spec"); !reflect.DeepEqual(got, map[string]any{"podSelector": map[string]any{}, "policyTypes": []any{"Ingress", "Egress"}}) {
		t.Errorf("default-deny spec = %v", got)
	}
	if get(o["NetworkPolicy/allow-egress-external"], "spec", "egress", 0, "ports", 1, "port") != float64(5432) {
		t.Errorf("egress ports = %v", get(o["NetworkPolicy/allow-egress-external"], "spec", "egress"))
	}
	c.Network = &EnvNetworkPolicy{}
	objs, _ = RenderWorkloads([]v1alpha1.Workload{svc("a")}, v1alpha1.ProfileFull, c)
	o = objects(t, objs)
	if o["NetworkPolicy/allow-egress-external"] != nil || o["NetworkPolicy/allow-egress-telemetry"] != nil || o["NetworkPolicy/allow-ingress-controller"] != nil {
		t.Error("undeclared egress/telemetry/ingress must OMIT the policy, not render one against nothing")
	}
}

// TestEnvChannels pins every channel onto the source whose name is a
// contract with another component.
func TestEnvChannels(t *testing.T) {
	w := svc("api")
	w.Spec.Env = []v1alpha1.EnvVar{
		{Name: "PLAIN", Value: "v"},
		{Name: "EMPTY"},
		{Name: "SREF", SecretRef: &v1alpha1.SecretKeyRef{Name: "s", Key: "k"}},
		{Name: "SREF_OPT", SecretRef: &v1alpha1.SecretKeyRef{Name: "s", Key: "maybe", Optional: true}},
		{Name: "MANAGED", ManagedSecret: &v1alpha1.ManagedSecretRef{Name: "STRIPE_KEY"}},
		{Name: "MANAGED_OPT", ManagedSecret: &v1alpha1.ManagedSecretRef{Name: "SENTRY_DSN", Optional: true}},
		{Name: "DB", DatabaseRef: &v1alpha1.DatabaseRef{Name: "orders"}},
		{Name: "DBPASS", DatabaseRef: &v1alpha1.DatabaseRef{Name: "orders", Key: v1alpha1.DatabaseKeyPassword}},
		{Name: "CM", ConfigMapRef: &v1alpha1.ConfigMapKeyRef{Name: "features", Key: "map"}},
		{Name: "POD_IP", FieldRef: &v1alpha1.FieldRef{FieldPath: "status.podIP"}},
	}
	env := get(podOf(objects(t, render(t, v1alpha1.ProfileFull, w))["Deployment/api"]), "containers", 0, "env").([]any)
	want := []any{
		map[string]any{"name": "PLAIN", "value": "v"},
		map[string]any{"name": "EMPTY"},
		map[string]any{"name": "SREF", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "s", "key": "k"}}},
		map[string]any{"name": "SREF_OPT", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "s", "key": "maybe", "optional": true}}},
		map[string]any{"name": "MANAGED", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": v1alpha1.ManagedSecretsSecretName, "key": "STRIPE_KEY"}}},
		map[string]any{"name": "MANAGED_OPT", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": v1alpha1.ManagedSecretsSecretName, "key": "SENTRY_DSN", "optional": true}}},
		map[string]any{"name": "DB", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "orders-app", "key": "uri"}}},
		map[string]any{"name": "DBPASS", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "orders-app", "key": "password"}}},
		map[string]any{"name": "CM", "valueFrom": map[string]any{"configMapKeyRef": map[string]any{"name": "features", "key": "map"}}},
		map[string]any{"name": "POD_IP", "valueFrom": map[string]any{"fieldRef": map[string]any{"fieldPath": "status.podIP"}}},
	}
	if !reflect.DeepEqual(env, want) {
		gb, _ := json.MarshalIndent(env, "", " ")
		t.Fatalf("env =\n%s", gb)
	}
}

// TestIdentity: one SA per workload; no token unless RBAC; the namespaced
// Role carries the default config-read rules; an operator gets that Role
// beside its ClusterRole; annotations ride the SA only; an override renders
// none.
func TestIdentity(t *testing.T) {
	o := objects(t, render(t, v1alpha1.ProfileFull, svc("api")))
	if get(o["ServiceAccount/api"], "automountServiceAccountToken") != false || get(podOf(o["Deployment/api"]), "automountServiceAccountToken") != false {
		t.Error("a workload with no RBAC must mount no token, on the SA and the pod")
	}
	if get(podOf(o["Deployment/api"]), "serviceAccountName") != "api" {
		t.Error("pod must bind its own SA, never the namespace default")
	}

	rule := v1alpha1.PolicyRule{APIGroups: []string{"example.com"}, Resources: []string{"widgets"}, Verbs: []string{"list"}}
	w := wl("reaper", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindWorker, NamespacedRBAC: []v1alpha1.PolicyRule{rule}})
	o = objects(t, render(t, v1alpha1.ProfileFull, w))
	role := o["Role/reaper-role"]
	if len(get(role, "rules").([]any)) != 2 || get(role, "rules", 0, "resources", 1) != "secrets" || get(role, "rules", 1, "apiGroups", 0) != "example.com" {
		t.Errorf("role rules = %v (want lib/rbac.k defaults + declared)", get(role, "rules"))
	}
	if get(o["RoleBinding/reaper-rolebinding"], "subjects", 0, "name") != "reaper" {
		t.Error("RoleBinding must bind the workload's SA")
	}
	if get(podOf(o["Deployment/reaper"]), "automountServiceAccountToken") != true || get(o["ServiceAccount/reaper"], "automountServiceAccountToken") != nil {
		t.Error("a workload WITH RBAC needs its token")
	}

	op := wl("mgr", v1alpha1.WorkloadSpec{
		Kind: v1alpha1.KindOperator, CRDs: []string{"Widget"}, ClusterRBAC: []v1alpha1.PolicyRule{rule},
		ServiceAccountAnnotations: map[string]string{"iam.gke.io/gcp-service-account": "m@p.iam.gserviceaccount.com"},
	})
	o = objects(t, render(t, v1alpha1.ProfileFull, op))
	if o["Role/mgr-role"] == nil || o["RoleBinding/mgr-rolebinding"] == nil {
		t.Errorf("an operator reads its config through a namespaced Role; objects = %v", keysOf(o))
	}
	if get(o["ClusterRoleBinding/mgr-acme-prod-clusterrolebinding"], "subjects", 0, "namespace") != "acme-prod" {
		t.Error("cluster binding subject must be the env's namespace")
	}
	if get(o["ServiceAccount/mgr"], "metadata", "annotations", "iam.gke.io/gcp-service-account") == nil {
		t.Error("SA annotations lost")
	}
	if get(o["Deployment/mgr"], "metadata", "annotations") != nil || get(o["Deployment/mgr"], "spec", "template", "metadata", "annotations") != nil {
		t.Error("SA annotations must ride the SA only")
	}
	if get(podOf(o["Deployment/mgr"]), "containers", 0, "env", 0) == nil {
		t.Error("operator must get LEADER_ELECTION")
	}
	off := false
	op.Spec.LeaderElection = &off
	if get(podOf(objects(t, render(t, v1alpha1.ProfileFull, op))["Deployment/mgr"]), "containers", 0, "env") != nil {
		t.Error("leaderElection=false must render no LEADER_ELECTION")
	}

	ov := svc("api")
	ov.Spec.ServiceAccount = "reliant-cloudsql"
	o = objects(t, render(t, v1alpha1.ProfileFull, ov))
	if o["ServiceAccount/api"] != nil {
		t.Error("an SA override must render no ServiceAccount: forge does not own it")
	}
	pod := podOf(o["Deployment/api"])
	if get(pod, "serviceAccountName") != "reliant-cloudsql" || get(pod, "automountServiceAccountToken") != nil {
		t.Errorf("override pod = sa %v automount %v (the SA's owner decides)", get(pod, "serviceAccountName"), get(pod, "automountServiceAccountToken"))
	}
}

// TestBefore: broadcast gating, the gated-kinds rules, ordering, and the
// cross-workload refusals.
func TestBefore(t *testing.T) {
	migrate := wl("migrate", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindJob, Command: []string{"/app/x", "migrate"}, Before: []string{v1alpha1.BeforeAll}})
	migrate.Spec.Image = "ghcr.io/acme/migrate:v1"
	migrate.Spec.Env = []v1alpha1.EnvVar{{Name: "DATABASE_URL", DatabaseRef: &v1alpha1.DatabaseRef{Name: "orders"}}}
	seed := wl("seed", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindJob, Args: []string{"seed"}, Before: []string{"api"}})
	provision := wl("provision", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindJob, Args: []string{"provision"}})
	cron := wl("nightly", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindCron, Schedule: "@daily", Args: []string{"x"}})
	op := wl("mgr", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindOperator, CRDs: []string{"W"}})
	worker := wl("w", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindWorker})
	tool := wl("cli", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindTool})

	// seed declared BEFORE migrate: migrate still runs first on api,
	// because migrate gates seed.
	api := svc("api")
	api.Spec.Env = []v1alpha1.EnvVar{{Name: "LOG_LEVEL", Value: "info"}}
	o := objects(t, render(t, v1alpha1.ProfileFull, seed, api, migrate, provision, cron, op, worker, tool))
	inits := func(key string) []string {
		var names []string
		for _, c := range asSlice(get(podOf(o[key]), "initContainers")) {
			names = append(names, get(c, "name").(string))
		}
		return names
	}
	var provisionJob string
	for k := range o {
		if strings.HasPrefix(k, "Job/provision-") {
			provisionJob = k
		}
		if strings.HasPrefix(k, "Job/migrate") || strings.HasPrefix(k, "Job/seed") || k == "ServiceAccount/migrate" || k == "ServiceAccount/seed" {
			t.Errorf("a gating job must render nothing of its own, got %s", k)
		}
	}
	for key, want := range map[string][]string{
		"Deployment/api":  {"migrate", "seed"},
		"Deployment/w":    {"migrate"},
		"Deployment/mgr":  {"migrate"},
		provisionJob:      {"migrate"},
		"CronJob/nightly": nil,
	} {
		if got := inits(key); !reflect.DeepEqual(got, want) {
			t.Errorf("%s initContainers = %v, want %v", key, got, want)
		}
	}
	init := get(podOf(o["Deployment/api"]), "initContainers", 0)
	if get(init, "image") != "ghcr.io/acme/migrate:v1" || !reflect.DeepEqual(get(init, "command"), []any{"/app/x", "migrate"}) {
		t.Errorf("init runs the JOB's image and command: %v", init)
	}
	// The init carries the JOB's env, never the dependent's
	// (positive_workload_job.k).
	wantEnv := []any{map[string]any{"name": "DATABASE_URL", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "orders-app", "key": "uri"}}}}
	if got := get(init, "env"); !reflect.DeepEqual(got, wantEnv) {
		t.Errorf("init env = %v, want the job's own %v", got, wantEnv)
	}
	if get(init, "volumeMounts", 0, "mountPath") != "/tmp" || get(init, "securityContext", "readOnlyRootFilesystem") != true {
		t.Errorf("init hardening/tmp: %v", init)
	}

	for _, c := range []struct {
		name string
		ws   []v1alpha1.Workload
		want string
	}{
		{"dangling", []v1alpha1.Workload{svc("api"), wl("j", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindJob, Args: []string{"x"}, Before: []string{"apii"}})}, `names "apii", which does not exist`},
		{"cron target", []v1alpha1.Workload{cron, wl("j", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindJob, Args: []string{"x"}, Before: []string{"nightly"}})}, "cannot be gated"},
		{"cycle", []v1alpha1.Workload{
			wl("a", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindJob, Args: []string{"x"}, Before: []string{"b"}}),
			wl("b", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindJob, Args: []string{"x"}, Before: []string{"a"}}),
		}, "cycle"},
		{"gating job rbac", []v1alpha1.Workload{svc("api"), wl("j", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindJob, Args: []string{"x"}, Before: []string{"api"},
			NamespacedRBAC: []v1alpha1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get"}}}})}, "namespacedRBAC is not allowed on a job with before"},
		{"duplicate", []v1alpha1.Workload{svc("api"), svc("api")}, "declared twice"},
	} {
		t.Run(c.name, func(t *testing.T) {
			objs, err := RenderWorkloads(c.ws, v1alpha1.ProfileFull, ctx)
			mustErr(t, err, c.want)
			if objs != nil {
				t.Error("a refused set must emit nothing")
			}
		})
	}
}

// TestErrorsReportedOnce: RenderWorkloads admits through Workload.Validate
// and does not re-check what it already checks, so each violation appears in
// the error exactly once. A duplicated message reads as two problems, and an
// author fixing the first finds the second was the same one.
func TestErrorsReportedOnce(t *testing.T) {
	gating := wl("j", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindJob, Args: []string{"x"}, Before: []string{"api"},
		NamespacedRBAC: []v1alpha1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get"}}}})
	long := wl(strings.Repeat("s", 53), v1alpha1.WorkloadSpec{Kind: v1alpha1.KindJob, Args: []string{"x"}})
	bad := svc("Bad_Name")
	_, err := RenderWorkloads([]v1alpha1.Workload{svc("api"), gating, long, bad}, v1alpha1.ProfileFull, ctx)
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{
		"namespacedRBAC is not allowed on a job with before",
		"a standalone job's name is at most 52 characters",
		`name "Bad_Name" must be an RFC-1123 label`,
	} {
		if n := strings.Count(err.Error(), want); n != 1 {
			t.Errorf("%q appears %d times in the error, want exactly once:\n%v", want, n, err)
		}
	}
}

// TestNativeSidecar: a Native sidecar renders as an initContainer with
// restartPolicy Always and its startup probe, so the kubelet starts it before
// the main container and waits for it; a plain sidecar stays in containers.
func TestNativeSidecar(t *testing.T) {
	w := svc("api", httpPort(true))
	w.Spec.Sidecars = []v1alpha1.Container{
		{
			Name: "proxy", Image: "gcr.io/cloud-sql-connectors/cloud-sql-proxy:2.14.1", Native: true,
			Ports:  []v1alpha1.Port{{Name: "proxy-health", Port: 9801}},
			Probes: &v1alpha1.Probes{StartupPath: "/startup", ReadinessPath: "/readiness", LivenessPath: "/liveness"},
		},
		{Name: "plain", Image: "gcr.io/p/plain:1"},
	}
	pod := podOf(objects(t, render(t, v1alpha1.ProfileFull, w))["Deployment/api"])
	init := get(pod, "initContainers", 0)
	if get(init, "name") != "proxy" || get(init, "restartPolicy") != "Always" {
		t.Fatalf("native sidecar must be an Always initContainer, got %v", init)
	}
	if get(init, "startupProbe", "httpGet", "path") != "/startup" || get(init, "startupProbe", "failureThreshold") != float64(360) {
		t.Errorf("startupProbe = %v", get(init, "startupProbe"))
	}
	if get(pod, "containers", 1, "name") != "plain" || get(pod, "containers", 1, "restartPolicy") != nil {
		t.Errorf("plain sidecar must stay a regular container: %v", get(pod, "containers"))
	}
	if n := len(asSlice(get(pod, "containers"))); n != 2 {
		t.Errorf("containers = %d, want main + plain", n)
	}
}

func asSlice(v any) []any { s, _ := v.([]any); return s }

// TestPodEscapeHatches: sidecars get the same hardening, volumes mount on
// the main container, placement lands on the pod.
func TestPodEscapeHatches(t *testing.T) {
	w := svc("api", httpPort(true))
	w.Spec.Sidecars = []v1alpha1.Container{{Name: "proxy", Image: "gcr.io/cloud-sql-connectors/cloud-sql-proxy:2.14.1", Args: []string{"--port=5432"}}}
	w.Spec.Volumes = []v1alpha1.Volume{
		{Name: "certs", MountPath: "/etc/certs", ReadOnly: true, Source: v1alpha1.VolumeSource{Secret: &v1alpha1.SecretVolumeSource{Name: "tls"}}},
		{Name: "shared", MountPath: "/shared", Source: v1alpha1.VolumeSource{PVC: &v1alpha1.PVCVolumeSource{ClaimName: "shared"}}},
	}
	w.Spec.NodeSelector = map[string]string{"pool": "general"}
	w.Spec.Tolerations = []v1alpha1.Toleration{{Key: "dedicated", Operator: v1alpha1.TolerationOpEqual, Value: "api", Effect: v1alpha1.TaintNoSchedule}}
	w.Spec.PodAnnotations = map[string]string{"prometheus.io/scrape": "true"}
	w.Spec.Ports[0].AppProtocol = "h2c"
	o := objects(t, render(t, v1alpha1.ProfileFull, w))
	dep := o["Deployment/api"]
	pod := podOf(dep)
	side := get(pod, "containers", 1)
	if !reflect.DeepEqual(get(side, "securityContext"), get(pod, "containers", 0, "securityContext")) {
		t.Errorf("sidecar hardening differs from main: %v", get(side, "securityContext"))
	}
	if get(pod, "containers", 0, "volumeMounts", 1, "mountPath") != "/etc/certs" || get(pod, "containers", 0, "volumeMounts", 1, "readOnly") != true {
		t.Errorf("mounts = %v", get(pod, "containers", 0, "volumeMounts"))
	}
	if get(pod, "securityContext", "fsGroup") != float64(RunAsUser) {
		t.Error("a mounted PVC needs fsGroup")
	}
	if get(pod, "nodeSelector", "pool") != "general" || get(pod, "tolerations", 0, "effect") != "NoSchedule" {
		t.Errorf("placement = %v %v", get(pod, "nodeSelector"), get(pod, "tolerations"))
	}
	if get(dep, "spec", "template", "metadata", "annotations", "prometheus.io/scrape") != "true" {
		t.Error("pod annotations lost")
	}
	if get(o["Service/api"], "spec", "ports", 0, "appProtocol") != "h2c" {
		t.Error("appProtocol lost")
	}
	// Service port only: a containerPort has no appProtocol field
	// (positive_service_app_protocol.k).
	if get(pod, "containers", 0, "ports", 0, "appProtocol") != nil {
		t.Error("appProtocol leaked onto the containerPort")
	}
}

// TestRestrictedKindSet renders the widest set ProfileRestricted accepts and
// asserts the EXACT object kinds: only what reaches the hosted user's own pods.
// No RBAC, ever.
func TestRestrictedKindSet(t *testing.T) {
	objs := render(t, v1alpha1.ProfileRestricted, restrictedSet()...)
	got := map[string]bool{}
	for _, o := range objs {
		got[o.GetKind()] = true
	}
	if !reflect.DeepEqual(got, RestrictedKinds) {
		t.Fatalf("restricted kinds = %v\nwant exactly     %v", sortedKindNames(got), sortedKindNames(RestrictedKinds))
	}
	for k, o := range objects(t, objs) {
		if strings.HasPrefix(k, "ServiceAccount/") && get(o, "automountServiceAccountToken") != false {
			t.Errorf("%s mounts a token under restricted", k)
		}
		pod := podOf(o)
		if pod == nil {
			continue
		}
		// None of the Full-only pod features may appear.
		for _, f := range []string{"nodeSelector", "tolerations", "affinity", "hostNetwork", "hostPID", "hostIPC", "runtimeClassName", "priorityClassName"} {
			if get(pod, f) != nil {
				t.Errorf("%s: %s under restricted", k, f)
			}
		}
		if get(pod, "automountServiceAccountToken") != false {
			t.Errorf("%s pod mounts a token under restricted", k)
		}
		name := get(o, "metadata", "name").(string)
		if strings.HasPrefix(k, "Job/") {
			name = get(o, "metadata", "labels", LabelJobName).(string)
		}
		if get(pod, "serviceAccountName") != name {
			t.Errorf("%s runs as %v, want its own generated SA", k, get(pod, "serviceAccountName"))
		}
		if len(asSlice(get(pod, "containers"))) != 1 {
			t.Errorf("%s has sidecars under restricted", k)
		}
		for _, v := range asSlice(get(pod, "volumes")) {
			vn := get(v, "name")
			if vn != v1alpha1.TmpVolumeName && vn != v1alpha1.DataVolumeName {
				t.Errorf("%s: declared volume %v under restricted", k, vn)
			}
		}
		if get(o, "spec", "template", "metadata", "annotations") != nil {
			t.Errorf("%s: pod annotations under restricted", k)
		}
	}
}

func restrictedSet() []v1alpha1.Workload {
	img := "ghcr.io/acme/app:v1"
	web := wl("web", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindService, Image: img, Replicas: 3, Ports: []v1alpha1.Port{httpPort(true)},
		Probes: &v1alpha1.Probes{}, Env: []v1alpha1.EnvVar{{Name: "DB", DatabaseRef: &v1alpha1.DatabaseRef{Name: "orders"}}, {Name: "KEY", ManagedSecret: &v1alpha1.ManagedSecretRef{Name: "KEY"}}, {Name: "DSN", ManagedSecret: &v1alpha1.ManagedSecretRef{Name: "DSN", Optional: true}}}})
	store := wl("store", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindService, Image: img, StorageGiB: 10})
	worker := wl("jobs", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindWorker, Image: img, Args: []string{"work"}})
	migrate := wl("migrate", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindJob, Image: img, Args: []string{"db", "migrate", "up"}, Before: []string{v1alpha1.BeforeAll}})
	report := wl("report", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindJob, Image: img, Args: []string{"report"}, DeployPhase: v1alpha1.DeployPhasePostRollout})
	return []v1alpha1.Workload{migrate, web, store, worker, report}
}

// TestRestrictedRefusesFullOnly: Validate(Restricted) runs first, so a set
// carrying any Full-only feature renders NOTHING.
func TestRestrictedRefusesFullOnly(t *testing.T) {
	rule := []v1alpha1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get"}}}
	for name, mutate := range map[string]func(*v1alpha1.WorkloadSpec){
		"namespacedRBAC": func(s *v1alpha1.WorkloadSpec) { s.NamespacedRBAC = rule },
		"sidecars": func(s *v1alpha1.WorkloadSpec) {
			s.Sidecars = []v1alpha1.Container{{Name: "x", Image: "ghcr.io/a/b:v1"}}
		},
		"volumes": func(s *v1alpha1.WorkloadSpec) {
			s.Volumes = []v1alpha1.Volume{{Name: "v", MountPath: "/v", Source: v1alpha1.VolumeSource{EmptyDir: &v1alpha1.EmptyDirVolumeSource{}}}}
		},
		"serviceAccount": func(s *v1alpha1.WorkloadSpec) { s.ServiceAccount = "x" },
		"nodeSelector":   func(s *v1alpha1.WorkloadSpec) { s.NodeSelector = map[string]string{"a": "b"} },
		"tolerations": func(s *v1alpha1.WorkloadSpec) {
			s.Tolerations = []v1alpha1.Toleration{{Operator: v1alpha1.TolerationOpExists}}
		},
		"podAnnotations": func(s *v1alpha1.WorkloadSpec) { s.PodAnnotations = map[string]string{"a": "b"} },
		"secretRef": func(s *v1alpha1.WorkloadSpec) {
			s.Env = []v1alpha1.EnvVar{{Name: "X", SecretRef: &v1alpha1.SecretKeyRef{Name: "a", Key: "b"}}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			ws := restrictedSet()
			mutate(&ws[1].Spec)
			objs, err := RenderWorkloads(ws, v1alpha1.ProfileRestricted, ctx)
			mustErr(t, err, name)
			if objs != nil {
				t.Error("a refused set must emit nothing")
			}
		})
	}
	op := wl("mgr", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindOperator, CRDs: []string{"W"}})
	_, err := RenderWorkloads([]v1alpha1.Workload{op}, v1alpha1.ProfileRestricted, ctx)
	mustErr(t, err, `kind "operator" is not allowed`)
}

// TestEveryPodIsPSARestricted checks every pod template RenderWorkloads
// emits, under both profiles, against the Pod Security `restricted`
// standard the control plane enforces on hosted namespaces (and forge's own
// namespaces label): runAsNonRoot, seccomp RuntimeDefault, and for every
// container (main, sidecar, init) no privilege escalation and ALL
// capabilities dropped; no hostPath and no host namespaces.
func TestEveryPodIsPSARestricted(t *testing.T) {
	full := append(restrictedSet(),
		wl("nightly", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindCron, Schedule: "@daily", Args: []string{"x"}}),
		wl("mgr", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindOperator, CRDs: []string{"W"}}),
	)
	full[1].Spec.Sidecars = []v1alpha1.Container{{Name: "proxy", Image: "gcr.io/p/proxy:1"}}
	// N3: the securityContext override (uid 1000, writable rootfs) must
	// stay Pod Security restricted-compliant.
	full = append(full, wl("console", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindService, SecurityContext: &v1alpha1.PodSecurity{
		RunAsUser: new(int64(1000)), RunAsGroup: new(int64(1000)), FSGroup: new(int64(1000)), ReadOnlyRootFilesystem: new(false),
	}}))
	for profile, ws := range map[v1alpha1.Profile][]v1alpha1.Workload{v1alpha1.ProfileFull: full, v1alpha1.ProfileRestricted: restrictedSet()} {
		pods := 0
		for k, o := range objects(t, render(t, profile, ws...)) {
			pod := podOf(o)
			if pod == nil {
				continue
			}
			pods++
			if msg := psaRestrictedViolation(pod); msg != "" {
				t.Errorf("%s %s: %s", profile, k, msg)
			}
		}
		if pods == 0 {
			t.Fatalf("%s: no pod templates checked", profile)
		}
	}
}

func psaRestrictedViolation(pod any) string {
	if get(pod, "securityContext", "runAsNonRoot") != true {
		return "pod runAsNonRoot is not true"
	}
	if get(pod, "securityContext", "seccompProfile", "type") != "RuntimeDefault" {
		return "pod seccomp is not RuntimeDefault"
	}
	for _, f := range []string{"hostNetwork", "hostPID", "hostIPC"} {
		if get(pod, f) == true {
			return f
		}
	}
	for _, v := range asSlice(get(pod, "volumes")) {
		if get(v, "hostPath") != nil {
			return "hostPath volume"
		}
	}
	containers := append(asSlice(get(pod, "containers")), asSlice(get(pod, "initContainers"))...)
	for _, c := range containers {
		sc := get(c, "securityContext")
		switch {
		case sc == nil:
			return "container " + get(c, "name").(string) + " has no securityContext"
		case get(sc, "allowPrivilegeEscalation") != false:
			return "container " + get(c, "name").(string) + " allows privilege escalation"
		case !slices.Equal(anyStrings(get(sc, "capabilities", "drop")), []string{"ALL"}):
			return "container " + get(c, "name").(string) + " does not drop ALL"
		case get(sc, "capabilities", "add") != nil:
			return "container " + get(c, "name").(string) + " adds capabilities"
		case get(sc, "privileged") == true:
			return "container " + get(c, "name").(string) + " is privileged"
		case get(sc, "runAsNonRoot") == false:
			return "container " + get(c, "name").(string) + " overrides runAsNonRoot"
		}
	}
	return ""
}

func anyStrings(v any) []string {
	var out []string
	for _, s := range asSlice(v) {
		out = append(out, s.(string))
	}
	return out
}

// TestPullPolicy ports expand.k:73-79.
func TestPullPolicy(t *testing.T) {
	for img, want := range map[string]string{
		"ghcr.io/a/b:v1": "IfNotPresent", "ghcr.io/a/b:latest": "Always", "ghcr.io/a/b": "Always",
		"ghcr.io/a/b:abc123-dirty": "Always", "localhost:5000/b": "Always", "localhost:5000/b:v2": "IfNotPresent",
		// "dirty" mid-tag is a name, not the dev-build suffix
		// (positive_image_pull_policy.k).
		"ghcr.io/a/b:dirty-feature-v1": "IfNotPresent",
	} {
		if got := string(pullPolicy(img)); got != want {
			t.Errorf("pullPolicy(%s) = %s, want %s", img, got, want)
		}
	}
}

// TestEnvStamp ports kcl/lib/labels.k: forge.dev/env on every object and
// every pod template, never on a selector.
func TestEnvStamp(t *testing.T) {
	c := ctx
	c.Env = "prod"
	c.Network = &EnvNetworkPolicy{}
	ws := append(restrictedSet(), wl("nightly", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindCron, Schedule: "@daily", Args: []string{"x"}}))
	objs, err := RenderWorkloads(ws, v1alpha1.ProfileFull, c)
	if err != nil {
		t.Fatal(err)
	}
	for k, o := range objects(t, objs) {
		if get(o, "metadata", "labels", LabelEnv) != "prod" {
			t.Errorf("%s: no env stamp", k)
		}
		if pod := podOf(o); pod != nil {
			tmpl := get(o, "spec", "template", "metadata", "labels", LabelEnv)
			if o["kind"] == "CronJob" {
				tmpl = get(o, "spec", "jobTemplate", "spec", "template", "metadata", "labels", LabelEnv)
			}
			if tmpl != "prod" {
				t.Errorf("%s: pod template not stamped", k)
			}
		}
		if get(o, "spec", "selector", "matchLabels", LabelEnv) != nil || get(o, "spec", "selector", LabelEnv) != nil {
			t.Errorf("%s: env stamp on a selector", k)
		}
	}
}

// TestRenderIsDeterministic: same input, byte-identical output. Two
// executors apply this against the same objects.
func TestRenderIsDeterministic(t *testing.T) {
	first := ""
	for i := 0; i < 30; i++ {
		objs, err := RenderWorkloads(goldenCases()[len(goldenCases())-1].ws, v1alpha1.ProfileFull, ctx)
		if err != nil {
			t.Fatal(err)
		}
		db, _ := RenderManagedDatabase("orders", v1alpha1.ManagedDatabaseSpec{}, ctx)
		b, _ := json.Marshal(append(objs, db...))
		if i == 0 {
			first = string(b)
		} else if string(b) != first {
			t.Fatal("RenderWorkloads produced different bytes for the same input")
		}
	}
}

// TestRenderRefusesInvalid: validation runs before anything is emitted.
func TestRenderRefusesInvalid(t *testing.T) {
	_, err := RenderWorkloads([]v1alpha1.Workload{svc("api")}, v1alpha1.ProfileFull, Context{})
	mustErr(t, err, "namespace")
	_, err = RenderWorkloads([]v1alpha1.Workload{svc("Bad_Name")}, v1alpha1.ProfileFull, ctx)
	mustErr(t, err, "RFC-1123")
	bare := svc("api")
	bare.Spec.Image = "api:latest"
	if _, err := RenderWorkloads([]v1alpha1.Workload{bare}, v1alpha1.ProfileFull, ctx); err != nil {
		t.Errorf("a cluster the author operates may run a bare imported image: %v", err)
	}
	_, err = RenderWorkloads([]v1alpha1.Workload{bare}, v1alpha1.ProfileRestricted, ctx)
	mustErr(t, err, "registry host")
	long := wl(strings.Repeat("j", 55), v1alpha1.WorkloadSpec{Kind: v1alpha1.KindJob, Args: []string{"x"}})
	_, err = RenderWorkloads([]v1alpha1.Workload{long}, v1alpha1.ProfileFull, ctx)
	mustErr(t, err, "at most 52 characters")
	if _, err := RenderManagedDatabase("orders", v1alpha1.ManagedDatabaseSpec{Instances: 9}, ctx); err == nil {
		t.Error("out-of-range instances must be refused, not clamped")
	}
}

// TestJobHashTracksSpec: an unchanged spec keeps its Job name (a re-apply is
// a no-op); a changed one gets a new name.
func TestJobHashTracksSpec(t *testing.T) {
	job := func(spec v1alpha1.WorkloadSpec) map[string]any {
		t.Helper()
		spec.Kind = v1alpha1.KindJob
		for k, o := range objects(t, render(t, v1alpha1.ProfileFull, wl("seed", spec))) {
			if strings.HasPrefix(k, "Job/") {
				return o
			}
		}
		t.Fatal("no Job rendered")
		return nil
	}
	name := func(o map[string]any) string { return get(o, "metadata", "name").(string) }
	a := job(v1alpha1.WorkloadSpec{Args: []string{"seed"}})
	b := job(v1alpha1.WorkloadSpec{Args: []string{"seed"}})
	c := job(v1alpha1.WorkloadSpec{Args: []string{"seed", "--all"}})
	if name(a) != name(b) || name(a) == name(c) || len(name(a)) != len("seed-")+v1alpha1.StandaloneJobHashLength {
		t.Errorf("job names %q %q %q", name(a), name(b), name(c))
	}
	// The name IS <name>-<spec-hash label>, and the stable handle and the
	// ServiceAccount are NOT hashed (positive_job_spec_hash.k).
	for _, o := range []map[string]any{a, c} {
		if want := "seed-" + get(o, "metadata", "labels", LabelSpecHash).(string); name(o) != want {
			t.Errorf("job name %q != seed-<spec-hash label> %q", name(o), want)
		}
		if get(o, "metadata", "labels", LabelJobName) != "seed" || get(podOf(o), "serviceAccountName") != "seed" {
			t.Errorf("job-name label / pod SA must be the stable unhashed name: %v / %v", get(o, "metadata", "labels", LabelJobName), get(podOf(o), "serviceAccountName"))
		}
	}
	// deployPhase is metadata: it neither moves the hash nor appears on the
	// pod template (positive_job_deploy_phase.k).
	post := job(v1alpha1.WorkloadSpec{Args: []string{"seed"}, DeployPhase: v1alpha1.DeployPhasePostRollout})
	if name(post) != name(a) {
		t.Errorf("deployPhase moved the hash: %q vs %q", name(post), name(a))
	}
	if get(post, "metadata", "annotations", AnnotationDeployPhase) != "post-rollout" || get(a, "metadata", "annotations", AnnotationDeployPhase) != "pre-rollout" {
		t.Errorf("phase annotations: post %v, default %v", get(post, "metadata", "annotations"), get(a, "metadata", "annotations"))
	}
	for _, o := range []map[string]any{a, post} {
		if get(o, "spec", "template", "metadata", "annotations") != nil {
			t.Errorf("the pod template must carry no annotation: %v", get(o, "spec", "template", "metadata", "annotations"))
		}
	}
}

// TestJobHasNoTTL: a finished Job must persist. It is the record that this
// spec already ran; if the TTL controller deletes it, the next apply (a GitOps
// reconciler, the hosted operator, a redeploy) re-creates it and the
// migration runs again. Superseded Jobs go away by spec-hash name + prune.
func TestJobHasNoTTL(t *testing.T) {
	for _, o := range render(t, v1alpha1.ProfileFull, wl("migrate", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindJob, Args: []string{"migrate"}})) {
		if o.GetKind() != "Job" {
			continue
		}
		if ttl := get(objects(t, []*unstructured.Unstructured{o})["Job/"+o.GetName()], "spec", "ttlSecondsAfterFinished"); ttl != nil {
			t.Errorf("Job %s sets ttlSecondsAfterFinished=%v: a garbage-collected Job is re-created by the next apply and RUNS AGAIN", o.GetName(), ttl)
		}
		return
	}
	t.Fatal("no Job rendered for a standalone job workload")
}

// TestRenderManagedDatabase pins control-plane's live-proven CNPG Cluster
// shape (manageddatabase.BuildClusterPlan) and the names DatabaseRef depends
// on.
func TestRenderManagedDatabase(t *testing.T) {
	objs, err := RenderManagedDatabase("order-db", v1alpha1.ManagedDatabaseSpec{Instances: 2, StorageGiB: 50}, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 1 || objs[0].GetKind() != "Cluster" || objs[0].GetAPIVersion() != "postgresql.cnpg.io/v1" {
		t.Fatalf("want exactly one postgresql.cnpg.io/v1 Cluster, got %v", objs)
	}
	c := objects(t, objs)["Cluster/order-db"]
	want := map[string]any{
		"instances":             float64(2),
		"enableSuperuserAccess": false,
		"bootstrap":             map[string]any{"initdb": map[string]any{"database": "order_db", "owner": "order_db_app"}},
		"storage":               map[string]any{"size": "50Gi"},
		"managed":               map[string]any{"roles": []any{wantReadOnlyRole("order-db"), wantOffDashboardRole()}},
	}
	if !reflect.DeepEqual(c["spec"], want) {
		t.Errorf("Cluster spec = %#v\nwant %#v", c["spec"], want)
	}
	if get(c, "metadata", "annotations", AnnotationDeletionPolicy) != "retain" {
		t.Errorf("deletion policy annotation = %v, want retain", get(c, "metadata", "annotations", AnnotationDeletionPolicy))
	}
	if DatabaseCredentialSecretName("order-db") != "order-db-app" {
		t.Error("CNPG publishes <cluster>-app; DatabaseRef reads it")
	}
}

// backupCtx is an environment that HAS declared an object store.
var backupCtx = Context{
	Namespace: "acme-prod",
	PartOf:    "proj",
	DatabaseBackup: &DatabaseBackup{
		Bucket:          "s3://forge-db-backups/org-acme/prod",
		RetentionPolicy: "30d",
	},
}

// TestRenderManagedDatabaseBackup pins the backup stanza: the CNPG-I plugin
// (not the deprecated in-tree barmanObjectStore), an ObjectStore under this
// database's OWN prefix, and Workload Identity when no secret is named.
func TestRenderManagedDatabaseBackup(t *testing.T) {
	objs, err := RenderManagedDatabase("order-db", v1alpha1.ManagedDatabaseSpec{}, backupCtx)
	if err != nil {
		t.Fatal(err)
	}
	all := objects(t, objs)
	store, ok := all["ObjectStore/order-db"]
	if !ok {
		t.Fatalf("no ObjectStore rendered; got %v", objs)
	}
	if got := get(store, "spec", "configuration", "destinationPath"); got != "s3://forge-db-backups/org-acme/prod/order-db" {
		t.Errorf("destinationPath = %v; each database must sit under its OWN prefix", got)
	}
	if got := get(store, "spec", "retentionPolicy"); got != "30d" {
		t.Errorf("retentionPolicy = %v, want 30d", got)
	}
	// No CredentialSecret => Workload Identity, and NO static key anywhere.
	if got := get(store, "spec", "configuration", "googleCredentials", "gkeEnvironment"); got != true {
		t.Errorf("gkeEnvironment = %v; an unset credential secret must mean Workload Identity, not an absent credential", got)
	}
	if get(store, "spec", "configuration", "s3Credentials") != nil {
		t.Error("s3Credentials rendered with no credential secret declared")
	}

	c := all["Cluster/order-db"]
	plugin := get(c, "spec", "plugins", 0)
	if got := get(plugin, "name"); got != barmanPluginName {
		t.Errorf("plugin = %v, want the CNPG-I barman plugin (the in-tree stanza is deprecated from 1.26)", got)
	}
	if got := get(plugin, "isWALArchiver"); got != true {
		t.Errorf("isWALArchiver = %v: without it there is no continuous archiving and no PITR", got)
	}
	if got := get(plugin, "parameters", "serverName"); got != "order-db" {
		t.Errorf("serverName = %v, want order-db", got)
	}
	if get(c, "spec", "backup") != nil {
		t.Error("spec.backup rendered: the deprecated in-tree stanza must not be used")
	}
}

// TestRenderManagedDatabaseNoBackupWithoutStore keeps the original gate: an
// environment with no object store renders a Cluster that does not CLAIM to
// be backed up.
func TestRenderManagedDatabaseNoBackupWithoutStore(t *testing.T) {
	objs, err := RenderManagedDatabase("order-db", v1alpha1.ManagedDatabaseSpec{}, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 1 {
		t.Fatalf("want only the Cluster, got %d objects", len(objs))
	}
	c := objects(t, objs)["Cluster/order-db"]
	if get(c, "spec", "plugins") != nil || get(c, "spec", "backup") != nil {
		t.Error("a Cluster with no object store must render no backup configuration at all")
	}
}

// TestRenderManagedDatabaseRestore pins the declarative restore, and above all
// the serverName rule: a restored database archives to its OWN stream.
//
// Reusing the source's serverName is not a style preference. Verified against
// CNPG 1.30.0 + plugin v0.15.0: barman-cloud-check-wal-archive fails with
// "Expected empty archive", the recovery job crash-loops, and the restore
// never completes.
func TestRenderManagedDatabaseRestore(t *testing.T) {
	spec := v1alpha1.ManagedDatabaseSpec{Restore: &v1alpha1.DatabaseRestore{
		SourceDatabase: "order-db",
		PointInTime:    "2026-01-02T15:04:05Z",
	}}
	objs, err := RenderManagedDatabase("order-db-restored", spec, backupCtx)
	if err != nil {
		t.Fatal(err)
	}
	c := objects(t, objs)["Cluster/order-db-restored"]

	if got := get(c, "spec", "bootstrap", "initdb"); got != nil {
		t.Error("a restoring Cluster must not also declare initdb: it would bootstrap empty and report success")
	}
	if got := get(c, "spec", "bootstrap", "recovery", "recoveryTarget", "targetTime"); got != "2026-01-02T15:04:05Z" {
		t.Errorf("targetTime = %v", got)
	}
	ext := get(c, "spec", "externalClusters", 0)
	if got := get(ext, "plugin", "parameters", "serverName"); got != "order-db" {
		t.Errorf("recovery reads serverName = %v, want the SOURCE's stream order-db", got)
	}
	archive := get(c, "spec", "plugins", 0, "parameters", "serverName")
	if archive == "order-db" {
		t.Fatal("restored cluster archives into its SOURCE's stream: barman refuses a non-empty archive and the recovery job crash-loops")
	}
	if archive != "order-db-restored-restored" {
		t.Errorf("archive serverName = %v", archive)
	}
}

// TestRenderManagedDatabaseRestoreUnderANewName pins WHERE a restore reads.
// The source's backups were written to <bucket>/<source>/<stream>/; a
// database restored under a different name archives to <bucket>/<new>/, so
// reading through its own store would look in <bucket>/<new>/<source>/ —
// an empty prefix — and the recovery job would find no base backup.
func TestRenderManagedDatabaseRestoreUnderANewName(t *testing.T) {
	spec := v1alpha1.ManagedDatabaseSpec{Restore: &v1alpha1.DatabaseRestore{
		SourceDatabase: "order-db",
		PointInTime:    "2026-01-02T15:04:05Z",
	}}
	objs, err := RenderManagedDatabase("order-db-pitr", spec, backupCtx)
	if err != nil {
		t.Fatal(err)
	}
	all := objects(t, objs)
	c := all["Cluster/order-db-pitr"]

	// The archive side is untouched: the new database writes under its own
	// prefix, with retention, exactly as a database that was never restored.
	if got := get(c, "spec", "plugins", 0, "parameters", "barmanObjectName"); got != "order-db-pitr" {
		t.Errorf("archive store = %v, want the database's own order-db-pitr", got)
	}
	own := all["ObjectStore/order-db-pitr"]
	if got := get(own, "spec", "configuration", "destinationPath"); got != "s3://forge-db-backups/org-acme/prod/order-db-pitr" {
		t.Errorf("archive destinationPath = %v", got)
	}

	// The read side resolves to the SOURCE's location.
	ext := get(c, "spec", "externalClusters", 0, "plugin", "parameters")
	storeName, _ := get(ext, "barmanObjectName").(string)
	if storeName == "order-db-pitr" {
		t.Fatal("recovery reads through the NEW database's store: that is <bucket>/order-db-pitr/order-db/, which is empty")
	}
	read, ok := all["ObjectStore/"+storeName]
	if !ok {
		t.Fatalf("recovery names ObjectStore %q, which was not rendered; got %v", storeName, objs)
	}
	if got := get(read, "spec", "configuration", "destinationPath"); got != "s3://forge-db-backups/org-acme/prod/order-db" {
		t.Errorf("recovery destinationPath = %v, want the SOURCE's prefix s3://forge-db-backups/org-acme/prod/order-db", got)
	}
	if got := get(ext, "serverName"); got != "order-db" {
		t.Errorf("recovery serverName = %v, want the source's stream order-db", got)
	}
	// Retention belongs to whoever archives to a store, never to a reader.
	if got := get(read, "spec", "retentionPolicy"); got != nil {
		t.Errorf("restore-source store carries retentionPolicy %v; only the archiving store may", got)
	}
	// It is the restoring database's object: pruned with it, not the source.
	if got := get(read, "metadata", "labels", LabelName); got != "order-db-pitr" {
		t.Errorf("restore-source store labelled %s=%v, want the restoring database order-db-pitr", LabelName, got)
	}
	// No database can own this name, so the read store never overwrites
	// another database's archive store.
	if err := v1alpha1.ValidateDatabaseName(storeName); err == nil {
		t.Errorf("restore-source store %q is a valid database name, so a database of that name would share it", storeName)
	}
}

// TestRenderManagedDatabaseRestoreInPlace: restoring a database onto its own
// name reads through its own store, which already points at the source's
// prefix — no second store.
func TestRenderManagedDatabaseRestoreInPlace(t *testing.T) {
	spec := v1alpha1.ManagedDatabaseSpec{Restore: &v1alpha1.DatabaseRestore{SourceDatabase: "order-db"}}
	objs, err := RenderManagedDatabase("order-db", spec, backupCtx)
	if err != nil {
		t.Fatal(err)
	}
	all := objects(t, objs)
	if len(objs) != 2 {
		t.Errorf("want the archive ObjectStore and the Cluster, got %d objects", len(objs))
	}
	c := all["Cluster/order-db"]
	if got := get(c, "spec", "externalClusters", 0, "plugin", "parameters", "barmanObjectName"); got != "order-db" {
		t.Errorf("recovery store = %v, want order-db", got)
	}
	if got := get(all["ObjectStore/order-db"], "spec", "configuration", "destinationPath"); got != "s3://forge-db-backups/org-acme/prod/order-db" {
		t.Errorf("destinationPath = %v", got)
	}
}

// TestRenderManagedDatabaseServerNameOverride: a platform that can restore the
// same database twice allocates the stream itself, because the derived name
// would collide on the second restore and fail it mid-incident.
func TestRenderManagedDatabaseServerNameOverride(t *testing.T) {
	octx := backupCtx
	store := *backupCtx.DatabaseBackup
	store.ServerName = "order-db-r2"
	octx.DatabaseBackup = &store

	spec := v1alpha1.ManagedDatabaseSpec{Restore: &v1alpha1.DatabaseRestore{SourceDatabase: "order-db"}}
	objs, err := RenderManagedDatabase("order-db", spec, octx)
	if err != nil {
		t.Fatal(err)
	}
	c := objects(t, objs)["Cluster/order-db"]
	if got := get(c, "spec", "plugins", 0, "parameters", "serverName"); got != "order-db-r2" {
		t.Errorf("archive serverName = %v, want the platform-allocated order-db-r2", got)
	}
	// The source is still read from its own stream.
	if got := get(c, "spec", "externalClusters", 0, "plugin", "parameters", "serverName"); got != "order-db" {
		t.Errorf("recovery serverName = %v, want order-db", got)
	}
}

// TestRenderManagedDatabaseRestoreNeedsStore: a restore with nowhere to read
// from is refused at render, not discovered in a crash-looping job.
func TestRenderManagedDatabaseRestoreNeedsStore(t *testing.T) {
	spec := v1alpha1.ManagedDatabaseSpec{Restore: &v1alpha1.DatabaseRestore{SourceDatabase: "order-db"}}
	if _, err := RenderManagedDatabase("copy", spec, ctx); err == nil {
		t.Fatal("want an error restoring in an environment with no object store")
	}
}

// TestRenderManagedDatabasePlacement: the data tier lands where the platform
// says it may, which decides where the volumes physically live.
func TestRenderManagedDatabasePlacement(t *testing.T) {
	pctx := ctx
	pctx.DatabasePlacement = &DatabasePlacement{
		NodeSelector: map[string]string{"reliant.dev/pool": "data"},
		Tolerations: []v1alpha1.Toleration{{
			Key: "reliant.dev/runtime", Operator: v1alpha1.TolerationOpEqual,
			Value: "kata", Effect: v1alpha1.TaintNoSchedule,
		}},
	}
	objs, err := RenderManagedDatabase("order-db", v1alpha1.ManagedDatabaseSpec{}, pctx)
	if err != nil {
		t.Fatal(err)
	}
	c := objects(t, objs)["Cluster/order-db"]
	if got := get(c, "spec", "nodeSelector", "reliant.dev/pool"); got != "data" {
		t.Errorf("nodeSelector = %v", get(c, "spec", "nodeSelector"))
	}
	if got := get(c, "spec", "tolerations", 0, "effect"); got != "NoSchedule" {
		t.Errorf("tolerations = %v", get(c, "spec", "tolerations"))
	}
}

// TestManagedDatabaseRestoreValidation: a malformed PITR timestamp is refused
// at validation. CNPG would accept the Cluster and fail inside the recovery
// job, which is the worst possible moment to learn it.
func TestManagedDatabaseRestoreValidation(t *testing.T) {
	bad := v1alpha1.ManagedDatabaseSpec{Restore: &v1alpha1.DatabaseRestore{
		SourceDatabase: "order-db", PointInTime: "yesterday",
	}}
	if err := bad.Validate(); err == nil {
		t.Error("want an error for a non-RFC3339 pointInTime")
	}
	missing := v1alpha1.ManagedDatabaseSpec{Restore: &v1alpha1.DatabaseRestore{}}
	if err := missing.Validate(); err == nil {
		t.Error("want an error for a restore with no sourceDatabase")
	}
}

// TestRenderDispatch: Render handles the per-object tiers and REFUSES a
// Workload, which must be rendered with its set.
func TestRenderDispatch(t *testing.T) {
	md := &v1alpha1.ManagedDatabase{}
	md.Name = "orders"
	if objs, err := Render(md, ctx); err != nil || len(objs) != 1 {
		t.Fatalf("ManagedDatabase: %v %d", err, len(objs))
	}
	ss := &v1alpha1.StaticSite{}
	ss.Name = "web"
	if objs, err := Render(ss, ctx); err != nil || len(objs) != 0 {
		t.Fatalf("StaticSite: want no objects and no error, got %v %d", err, len(objs))
	}
	w := svc("api")
	_, err := Render(&w, ctx)
	mustErr(t, err, "RenderWorkloads")
	if _, err := Render(&v1alpha1.WorkloadList{}, ctx); err == nil {
		t.Error("a non-tier object must be refused")
	}
}

// TestImagePullSecrets: Context.ImagePullSecrets land on EVERY ServiceAccount
// forge generates, and NOT on the pod spec (lib/rbac.k, pinned by
// positive_service_sa_pullsecrets_rbac.k). A workload with a serviceAccount
// override gets them on the POD instead, since forge does not own that SA
// (lib/services.k:487). Unset means no imagePullSecrets key anywhere.
func TestImagePullSecrets(t *testing.T) {
	rule := []v1alpha1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get"}}}
	ov := svc("proxy")
	ov.Spec.ServiceAccount = "reliant-cloudsql"
	ws := []v1alpha1.Workload{
		svc("api"),
		wl("reaper", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindWorker, NamespacedRBAC: rule}),
		wl("mgr", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindOperator, CRDs: []string{"Widget"}, Group: "example.com"}),
		wl("nightly", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindCron, Schedule: "@daily", Args: []string{"x"}}),
		wl("seed", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindJob, Args: []string{"seed"}}),
		wl("migrate", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindJob, Args: []string{"migrate"}, Before: []string{v1alpha1.BeforeAll}}),
		ov,
	}
	c := ctx
	c.ImagePullSecrets = []string{"ghcr-creds", "extra-creds"}
	objs, err := RenderWorkloads(ws, v1alpha1.ProfileFull, c)
	if err != nil {
		t.Fatal(err)
	}
	want := []any{map[string]any{"name": "ghcr-creds"}, map[string]any{"name": "extra-creds"}}
	sas := 0
	for k, o := range objects(t, objs) {
		if strings.HasPrefix(k, "ServiceAccount/") {
			sas++
			if got := get(o, "imagePullSecrets"); !reflect.DeepEqual(got, want) {
				t.Errorf("%s imagePullSecrets = %v, want %v", k, got, want)
			}
		}
		pod := podOf(o)
		if pod == nil {
			continue
		}
		if k == "Deployment/proxy" {
			if got := get(pod, "imagePullSecrets"); !reflect.DeepEqual(got, want) {
				t.Errorf("override pod imagePullSecrets = %v, want %v (forge does not own its SA)", got, want)
			}
		} else if get(pod, "imagePullSecrets") != nil {
			t.Errorf("%s: pull secrets on the pod; they belong on its generated SA", k)
		}
	}
	// api, reaper, mgr, nightly, seed: the gating migrate and the override
	// render none.
	if sas != 5 {
		t.Errorf("checked %d ServiceAccounts, want 5", sas)
	}

	for k, o := range objects(t, render(t, v1alpha1.ProfileFull, ws...)) {
		if get(o, "imagePullSecrets") != nil || get(podOf(o), "imagePullSecrets") != nil {
			t.Errorf("%s: imagePullSecrets rendered with none configured", k)
		}
	}
}

// TestOperatorClusterRBACDisjointAcrossNamespaces: the SAME operator rendered
// into two namespaces of one cluster must share NO cluster-scoped object name,
// or the second deploy silently repoints the first's binding at its own
// namespace (lib/rbac.k:198-222; positive_operator_cluster_rbac_per_env.k).
// Each binding's roleRef resolves to its own ClusterRole and its subject names
// its own namespace; the ServiceAccount name stays bare.
func TestOperatorClusterRBACDisjointAcrossNamespaces(t *testing.T) {
	op := wl("mgr", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindOperator, CRDs: []string{"Widget"}, Group: "example.com"})
	clusterScoped := func(ns string) map[string]map[string]any {
		c := ctx
		c.Namespace = ns
		objs, err := RenderWorkloads([]v1alpha1.Workload{op}, v1alpha1.ProfileFull, c)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]map[string]any{}
		for k, o := range objects(t, objs) {
			if o["kind"] == "ServiceAccount" && get(o, "metadata", "name") != "mgr" {
				t.Errorf("%s: SA name must be bare, got %v", ns, get(o, "metadata", "name"))
			}
			if o["kind"] == "ClusterRole" || o["kind"] == "ClusterRoleBinding" {
				out[k] = o
			}
		}
		if len(out) != 2 {
			t.Fatalf("%s: want one ClusterRole + one binding, got %v", ns, keysOf(out))
		}
		for k, o := range out {
			if o["kind"] != "ClusterRoleBinding" {
				continue
			}
			role := "ClusterRole/" + get(o, "roleRef", "name").(string)
			if out[role] == nil {
				t.Errorf("%s: %s roleRef does not resolve to this render's ClusterRole", ns, k)
			}
			if get(o, "subjects", 0, "namespace") != ns || get(o, "subjects", 0, "name") != "mgr" {
				t.Errorf("%s: %s subject = %v", ns, k, get(o, "subjects", 0))
			}
		}
		return out
	}
	a, b := clusterScoped("team-a"), clusterScoped("team-b")
	for k := range a {
		if b[k] != nil {
			t.Errorf("cluster-scoped %s is rendered by both namespaces: the later deploy would take it over", k)
		}
	}
}

// TestOperatorClusterRoleDerivesCRDRules: an operator's ClusterRole grants
// what its manager needs on the CRDs it declares, the same rules the
// scaffolded controller's kubebuilder markers state
// (internal/templates/crd/controller.go.tmpl:56-58), then its declared
// clusterRBAC — and NOTHING namespace-local. The config-read defaults and the
// leader-election leases live in the namespaced Role: a ClusterRole carrying
// them read every Secret in the cluster and could take any controller's
// lease. expand.k derived no CRD rules, so an operator declaring crds without
// restating them in cluster_rbac could not watch its own resources.
func TestOperatorClusterRoleDerivesCRDRules(t *testing.T) {
	extra := v1alpha1.PolicyRule{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"list"}}
	op := wl("mgr", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindOperator, Group: "reliant.dev", CRDs: []string{"Workspace", "Policy"}, ClusterRBAC: []v1alpha1.PolicyRule{extra}})
	o := objects(t, render(t, v1alpha1.ProfileFull, op))
	defaults := map[string]any{"apiGroups": []any{""}, "resources": []any{"configmaps", "secrets"}, "verbs": []any{"get", "list", "watch"}}
	leases := map[string]any{"apiGroups": []any{"coordination.k8s.io"}, "resources": []any{"leases"}, "verbs": []any{"get", "list", "watch", "create", "update", "patch", "delete"}}
	want := []any{
		map[string]any{"apiGroups": []any{"reliant.dev"}, "resources": []any{"workspaces", "policies"}, "verbs": []any{"get", "list", "watch", "create", "update", "patch", "delete"}},
		map[string]any{"apiGroups": []any{"reliant.dev"}, "resources": []any{"workspaces/status", "policies/status"}, "verbs": []any{"get", "update", "patch"}},
		map[string]any{"apiGroups": []any{"reliant.dev"}, "resources": []any{"workspaces/finalizers", "policies/finalizers"}, "verbs": []any{"update"}},
		map[string]any{"apiGroups": []any{""}, "resources": []any{"pods"}, "verbs": []any{"list"}},
	}
	if got := get(o["ClusterRole/mgr-acme-prod-clusterrole"], "rules"); !reflect.DeepEqual(got, want) {
		gb, _ := json.MarshalIndent(got, "", " ")
		t.Fatalf("ClusterRole rules =\n%s", gb)
	}
	if got := get(o["Role/mgr-role"], "rules"); !reflect.DeepEqual(got, []any{defaults, leases}) {
		gb, _ := json.MarshalIndent(got, "", " ")
		t.Fatalf("operator Role rules =\n%s\nwant the config-read defaults + leases", gb)
	}

	// No leader election: no leases. No group: nothing to derive (a CRD
	// needs its group to be addressable), so the ClusterRole is only the
	// declared rules and the Role only the defaults.
	off := false
	op.Spec.LeaderElection = &off
	op.Spec.Group = ""
	o = objects(t, render(t, v1alpha1.ProfileFull, op))
	if got := get(o["ClusterRole/mgr-acme-prod-clusterrole"], "rules"); !reflect.DeepEqual(got, []any{want[3]}) {
		t.Errorf("without group or leader election, ClusterRole rules = %v", got)
	}
	if got := get(o["Role/mgr-role"], "rules"); !reflect.DeepEqual(got, []any{defaults}) {
		t.Errorf("without leader election, Role rules = %v", got)
	}

	// Nothing cluster-scoped at all: no ClusterRole. An empty one is a
	// binding that grants nothing, and one more object to audit.
	op.Spec.ClusterRBAC = nil
	o = objects(t, render(t, v1alpha1.ProfileFull, op))
	if o["ClusterRole/mgr-acme-prod-clusterrole"] != nil || o["ClusterRoleBinding/mgr-acme-prod-clusterrolebinding"] != nil {
		t.Errorf("an operator with no cluster-scoped rules rendered a ClusterRole: %v", keysOf(o))
	}
	if o["Role/mgr-role"] == nil {
		t.Error("the operator still needs its namespaced config-read Role")
	}
}

// TestNoClusterRoleGrantsSecrets is the security invariant, swept across every
// kind and every tier combination: NO ClusterRole forge renders grants
// ConfigMaps or Secrets it was not explicitly asked for. The config-read
// defaults are namespace-local by construction; a ClusterRole carrying them
// was cluster-wide read on every Secret for any workload with clusterRBAC and
// for every operator.
func TestNoClusterRoleGrantsSecrets(t *testing.T) {
	pods := []v1alpha1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"list"}}}
	for _, w := range []v1alpha1.Workload{
		wl("svc", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindService, ClusterRBAC: pods}),
		wl("both", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindWorker, ClusterRBAC: pods, NamespacedRBAC: pods}),
		wl("cron", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindCron, Args: []string{"x"}, Schedule: "@daily", ClusterRBAC: pods}),
		wl("mgr", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindOperator, Group: "e.com", CRDs: []string{"W"}}),
		wl("mgr2", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindOperator, Group: "e.com", CRDs: []string{"W"}, ClusterRBAC: pods}),
	} {
		for k, obj := range objects(t, render(t, v1alpha1.ProfileFull, w)) {
			if !strings.HasPrefix(k, "ClusterRole/") {
				continue
			}
			for _, r := range asSlice(get(obj, "rules")) {
				for _, res := range asSlice(get(r, "resources")) {
					if res == "secrets" || res == "configmaps" {
						t.Errorf("%s: %s grants %s cluster-wide: %v", w.Name, k, res, r)
					}
				}
			}
		}
	}
}

// TestBothRBACTiersCoexist: namespacedRBAC and clusterRBAC on one workload
// render BOTH a Role (defaults + namespaced rules) and a ClusterRole (the
// cluster rules only), each bound to the SAME ServiceAccount, with the token
// mounted. The permissions in force are the union, and each object states
// exactly the part of it that is at its scope.
func TestBothRBACTiersCoexist(t *testing.T) {
	nsRule := v1alpha1.PolicyRule{APIGroups: []string{""}, Resources: []string{"configmaps"}, Verbs: []string{"create"}}
	clRule := v1alpha1.PolicyRule{APIGroups: []string{"reliant.dev"}, Resources: []string{"workspaces"}, Verbs: []string{"list"}}
	w := wl("sweeper", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindWorker, NamespacedRBAC: []v1alpha1.PolicyRule{nsRule}, ClusterRBAC: []v1alpha1.PolicyRule{clRule}})
	o := objects(t, render(t, v1alpha1.ProfileFull, w))
	want := []string{
		"ClusterRole/sweeper-acme-prod-clusterrole", "ClusterRoleBinding/sweeper-acme-prod-clusterrolebinding",
		"Deployment/sweeper", "Role/sweeper-role", "RoleBinding/sweeper-rolebinding", "ServiceAccount/sweeper",
	}
	if got := keysOf(o); !reflect.DeepEqual(got, want) {
		t.Fatalf("objects = %v\nwant      %v", got, want)
	}
	defaults := map[string]any{"apiGroups": []any{""}, "resources": []any{"configmaps", "secrets"}, "verbs": []any{"get", "list", "watch"}}
	if got := get(o["Role/sweeper-role"], "rules"); !reflect.DeepEqual(got, []any{defaults, map[string]any{"apiGroups": []any{""}, "resources": []any{"configmaps"}, "verbs": []any{"create"}}}) {
		t.Errorf("Role rules = %v, want defaults + namespacedRBAC", got)
	}
	if got := get(o["ClusterRole/sweeper-acme-prod-clusterrole"], "rules"); !reflect.DeepEqual(got, []any{map[string]any{"apiGroups": []any{"reliant.dev"}, "resources": []any{"workspaces"}, "verbs": []any{"list"}}}) {
		t.Errorf("ClusterRole rules = %v, want clusterRBAC only", got)
	}
	for _, b := range []string{"RoleBinding/sweeper-rolebinding", "ClusterRoleBinding/sweeper-acme-prod-clusterrolebinding"} {
		if get(o[b], "subjects", 0, "name") != "sweeper" || get(o[b], "subjects", 0, "namespace") != "acme-prod" || len(asSlice(get(o[b], "subjects"))) != 1 {
			t.Errorf("%s subjects = %v, want the workload's one SA", b, get(o[b], "subjects"))
		}
	}
	if get(podOf(o["Deployment/sweeper"]), "automountServiceAccountToken") != true {
		t.Error("a workload with RBAC needs its token mounted")
	}
}

// TestIdentityJobAndAnnotationsNoLeak covers the job-kind RBAC tier
// (positive_job_namespaced_rbac.k) and sweeps EVERY rendered object for
// ServiceAccount annotations leaking off the SA
// (positive_service_account_annotations.k).
func TestIdentityJobAndAnnotationsNoLeak(t *testing.T) {
	rule := v1alpha1.PolicyRule{APIGroups: []string{""}, Resources: []string{"configmaps"}, Verbs: []string{"create"}}
	plain := objects(t, render(t, v1alpha1.ProfileFull, wl("seed", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindJob, Args: []string{"x"}})))
	for k := range plain {
		if strings.HasPrefix(k, "Role/") || strings.HasPrefix(k, "RoleBinding/") {
			t.Errorf("a plain job must get no %s", k)
		}
	}
	const key = "iam.gke.io/gcp-service-account"
	ann := map[string]string{key: "x@p.iam.gserviceaccount.com"}
	for _, w := range []v1alpha1.Workload{
		wl("idp", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindJob, Args: []string{"x"}, NamespacedRBAC: []v1alpha1.PolicyRule{rule}, ServiceAccountAnnotations: ann}),
		wl("api", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindService, ServiceAccountAnnotations: ann}),
		wl("mgr", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindOperator, CRDs: []string{"W"}, Group: "e.com", ServiceAccountAnnotations: ann}),
	} {
		o := objects(t, render(t, v1alpha1.ProfileFull, w))
		for k, obj := range o {
			onSA := strings.HasPrefix(k, "ServiceAccount/")
			has := get(obj, "metadata", "annotations", key) != nil
			if onSA != has {
				t.Errorf("%s: SA annotation present=%v (want only on the ServiceAccount)", k, has)
			}
			for _, path := range [][]any{{"spec", "template", "metadata", "annotations", key}, {"spec", "jobTemplate", "spec", "template", "metadata", "annotations", key}} {
				if get(obj, path...) != nil {
					t.Errorf("%s: SA annotation leaked onto the pod template", k)
				}
			}
		}
		if w.Name == "idp" {
			role := o["Role/idp-role"]
			if len(asSlice(get(role, "rules"))) != 2 || get(o["RoleBinding/idp-rolebinding"], "subjects", 0, "name") != "idp" {
				t.Errorf("job Role/binding: %v / %v", get(role, "rules"), get(o["RoleBinding/idp-rolebinding"], "subjects"))
			}
			for k, obj := range o {
				if strings.HasPrefix(k, "Job/") && get(podOf(obj), "serviceAccountName") != "idp" {
					t.Error("the job pod must bind its SA")
				}
			}
		}
	}
}

// TestBatchResources: a CronJob's and a standalone Job's container always
// carry requests AND limits (never BestEffort), and a declared override lands
// exactly (positive_cronjob_resources.k).
func TestBatchResources(t *testing.T) {
	override := v1alpha1.Resources{CPURequestMillicores: 500, CPULimitMillicores: 2000, MemoryRequestBytes: 512 << 20, MemoryLimitBytes: 2 << 30}
	for _, kind := range []v1alpha1.WorkloadKind{v1alpha1.KindCron, v1alpha1.KindJob} {
		for _, r := range []v1alpha1.Resources{{}, override} {
			spec := v1alpha1.WorkloadSpec{Kind: kind, Args: []string{"x"}, Resources: r}
			if kind == v1alpha1.KindCron {
				spec.Schedule = "@daily"
			}
			for k, o := range objects(t, render(t, v1alpha1.ProfileFull, wl("b", spec))) {
				pod := podOf(o)
				if pod == nil {
					continue
				}
				res := get(pod, "containers", 0, "resources")
				want := map[string]any{"requests": map[string]any{"cpu": "250m", "memory": "1Gi"}, "limits": map[string]any{"cpu": "250m", "memory": "1Gi"}}
				if r != (v1alpha1.Resources{}) {
					want = map[string]any{"requests": map[string]any{"cpu": "500m", "memory": "512Mi"}, "limits": map[string]any{"cpu": "2", "memory": "2Gi"}}
				}
				if !reflect.DeepEqual(res, want) {
					t.Errorf("%s resources = %v, want %v", k, res, want)
				}
			}
		}
	}
}

// TestClusterRBACOnService: a non-operator with clusterRBAC gets a
// ClusterRole carrying ONLY the declared rules, the config-read defaults in a
// namespaced Role beside it (both bound to its one SA), its token mounted,
// and none of an operator's derivations (no CRD rules, no leases, no
// LEADER_ELECTION).
func TestClusterRBACOnService(t *testing.T) {
	rule := v1alpha1.PolicyRule{APIGroups: []string{"reliant.dev"}, Resources: []string{"workspaces"}, Verbs: []string{"get", "list", "watch"}}
	w := wl("workspace-proxy", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindService, ClusterRBAC: []v1alpha1.PolicyRule{rule}})
	o := objects(t, render(t, v1alpha1.ProfileFull, w))
	want := []string{
		"ClusterRole/workspace-proxy-acme-prod-clusterrole", "ClusterRoleBinding/workspace-proxy-acme-prod-clusterrolebinding",
		"Deployment/workspace-proxy", "Role/workspace-proxy-role", "RoleBinding/workspace-proxy-rolebinding",
		"Service/workspace-proxy", "ServiceAccount/workspace-proxy",
	}
	if got := keysOf(o); !reflect.DeepEqual(got, want) {
		t.Fatalf("objects = %v\nwant      %v", got, want)
	}
	rules := get(o["ClusterRole/workspace-proxy-acme-prod-clusterrole"], "rules")
	wantRules := []any{
		map[string]any{"apiGroups": []any{"reliant.dev"}, "resources": []any{"workspaces"}, "verbs": []any{"get", "list", "watch"}},
	}
	if !reflect.DeepEqual(rules, wantRules) {
		t.Errorf("ClusterRole rules = %v, want the declared clusterRBAC only (no config-read defaults)", rules)
	}
	defaults := []any{map[string]any{"apiGroups": []any{""}, "resources": []any{"configmaps", "secrets"}, "verbs": []any{"get", "list", "watch"}}}
	if got := get(o["Role/workspace-proxy-role"], "rules"); !reflect.DeepEqual(got, defaults) {
		t.Errorf("Role rules = %v, want the config-read defaults", got)
	}
	if get(o["RoleBinding/workspace-proxy-rolebinding"], "subjects", 0, "name") != "workspace-proxy" ||
		get(o["ClusterRoleBinding/workspace-proxy-acme-prod-clusterrolebinding"], "subjects", 0, "name") != "workspace-proxy" {
		t.Error("both bindings must name the workload's one ServiceAccount")
	}
	pod := podOf(o["Deployment/workspace-proxy"])
	if get(pod, "automountServiceAccountToken") != true || get(o["ServiceAccount/workspace-proxy"], "automountServiceAccountToken") != nil {
		t.Error("clusterRBAC needs the token mounted")
	}
	if get(pod, "containers", 0, "env") != nil {
		t.Error("a non-operator must not get LEADER_ELECTION")
	}
}

// TestTerminationGraceOverride (N1): an explicit value wins over the
// drain-derived one, including 0.
func TestTerminationGraceOverride(t *testing.T) {
	w := wl("temporal", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindWorker, TerminationGracePeriodSeconds: new(int32(60)),
		Env: []v1alpha1.EnvVar{{Name: "SHUTDOWN_TIMEOUT", Value: "10s"}}})
	if got := get(podOf(objects(t, render(t, v1alpha1.ProfileFull, w))["Deployment/temporal"]), "terminationGracePeriodSeconds"); got != float64(60) {
		t.Errorf("grace = %v, want the explicit 60", got)
	}
	w.Spec.TerminationGracePeriodSeconds = new(int32(0))
	if got := get(podOf(objects(t, render(t, v1alpha1.ProfileFull, w))["Deployment/temporal"]), "terminationGracePeriodSeconds"); got != float64(0) {
		t.Errorf("grace = %v, want an explicit 0 honoured", got)
	}
	// A batch pod gets it too when declared (it has none by default).
	j := wl("seed", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindJob, Args: []string{"x"}, TerminationGracePeriodSeconds: new(int32(90))})
	for k, o := range objects(t, render(t, v1alpha1.ProfileFull, j)) {
		if strings.HasPrefix(k, "Job/") && get(podOf(o), "terminationGracePeriodSeconds") != float64(90) {
			t.Errorf("job grace = %v", get(podOf(o), "terminationGracePeriodSeconds"))
		}
	}
}

// TestDeploymentStrategy: an explicit strategy wins, Recreate carries no
// rollingUpdate parameters, and unset renders Kubernetes' default STATED (the
// storage rule still overrides it). A reconciler must not see fields the API
// server filled in under another manager.
func TestDeploymentStrategy(t *testing.T) {
	recreate := wl("workers", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindWorker, Strategy: v1alpha1.StrategyRecreate})
	dep := objects(t, render(t, v1alpha1.ProfileFull, recreate))["Deployment/workers"]
	if got := get(dep, "spec", "strategy", "type"); got != "Recreate" {
		t.Errorf("strategy type = %v, want Recreate", got)
	}
	if got := get(dep, "spec", "strategy", "rollingUpdate"); got != nil {
		t.Errorf("Recreate carries rollingUpdate %v; Kubernetes refuses the combination", got)
	}
	rolling := wl("api", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindService, Strategy: v1alpha1.StrategyRollingUpdate})
	if got := get(objects(t, render(t, v1alpha1.ProfileFull, rolling))["Deployment/api"], "spec", "strategy", "type"); got != "RollingUpdate" {
		t.Errorf("strategy type = %v, want RollingUpdate", got)
	}
	bare := objects(t, render(t, v1alpha1.ProfileFull, svc("plain")))["Deployment/plain"]
	if got := get(bare, "spec", "strategy", "type"); got != "RollingUpdate" {
		t.Errorf("unset strategy rendered %v, want the default RollingUpdate stated", got)
	}
	for _, field := range []string{"maxSurge", "maxUnavailable"} {
		if got := get(bare, "spec", "strategy", "rollingUpdate", field); got != "25%" {
			t.Errorf("rollingUpdate.%s = %v, want Kubernetes' 25%% stated", field, got)
		}
	}
	for name, dep := range map[string]any{"plain": bare, "workers": dep, "store": objects(t, render(t, v1alpha1.ProfileFull, wl("store", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindService, StorageGiB: 10})))["Deployment/store"]} {
		if got := get(dep, "spec", "progressDeadlineSeconds"); got != float64(DefaultProgressDeadlineSeconds) {
			t.Errorf("%s: progressDeadlineSeconds = %v, want %d stated", name, got, DefaultProgressDeadlineSeconds)
		}
	}
	// The storage rule still forces Recreate on its own.
	store := wl("store", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindService, StorageGiB: 10})
	if got := get(objects(t, render(t, v1alpha1.ProfileFull, store))["Deployment/store"], "spec", "strategy", "type"); got != "Recreate" {
		t.Errorf("storage strategy = %v, want Recreate", got)
	}
}

// TestPriorityClassName: a declared class lands on the pod spec, for a
// long-running kind and for a batch pod; unset renders nothing, so the
// cluster's own default applies.
func TestPriorityClassName(t *testing.T) {
	w := wl("workspace-proxy", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindService, PriorityClassName: "reliant-platform"})
	pod := podOf(objects(t, render(t, v1alpha1.ProfileFull, w))["Deployment/workspace-proxy"])
	if got := get(pod, "priorityClassName"); got != "reliant-platform" {
		t.Errorf("priorityClassName = %v, want reliant-platform", got)
	}
	bare := podOf(objects(t, render(t, v1alpha1.ProfileFull, svc("api")))["Deployment/api"])
	if got := get(bare, "priorityClassName"); got != nil {
		t.Errorf("unset priorityClassName rendered %v; the cluster default must apply", got)
	}
	j := wl("seed", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindJob, Args: []string{"x"}, PriorityClassName: "reliant-platform"})
	for k, o := range objects(t, render(t, v1alpha1.ProfileFull, j)) {
		if strings.HasPrefix(k, "Job/") && get(podOf(o), "priorityClassName") != "reliant-platform" {
			t.Errorf("job priorityClassName = %v", get(podOf(o), "priorityClassName"))
		}
	}
}

// TestActiveDeadline (N2): Job.spec.activeDeadlineSeconds on a standalone
// Job, and on a CronJob's jobTemplate.
func TestActiveDeadline(t *testing.T) {
	j := wl("seed", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindJob, Args: []string{"x"}, ActiveDeadlineSeconds: new(int64(60))})
	for k, o := range objects(t, render(t, v1alpha1.ProfileRestricted, j)) {
		if strings.HasPrefix(k, "Job/") && get(o, "spec", "activeDeadlineSeconds") != float64(60) {
			t.Errorf("job activeDeadlineSeconds = %v", get(o, "spec", "activeDeadlineSeconds"))
		}
	}
	c := wl("nightly", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindCron, Schedule: "@daily", Args: []string{"x"}, ActiveDeadlineSeconds: new(int64(600))})
	if got := get(objects(t, render(t, v1alpha1.ProfileFull, c))["CronJob/nightly"], "spec", "jobTemplate", "spec", "activeDeadlineSeconds"); got != float64(600) {
		t.Errorf("cron activeDeadlineSeconds = %v", got)
	}
	// Unset: no key (no deadline).
	for k, o := range objects(t, render(t, v1alpha1.ProfileFull, wl("seed", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindJob, Args: []string{"x"}}))) {
		if strings.HasPrefix(k, "Job/") && get(o, "spec", "activeDeadlineSeconds") != nil {
			t.Error("an unset deadline must render none")
		}
	}
}

// TestPodSecurityOverrideRender (N3): uid/gid/fsGroup/readOnlyRootFilesystem
// land on the pod and on EVERY container (main, sidecar, init), and
// runAsNonRoot stays true.
func TestPodSecurityOverrideRender(t *testing.T) {
	sc := &v1alpha1.PodSecurity{RunAsUser: new(int64(1000)), RunAsGroup: new(int64(1001)), FSGroup: new(int64(1002)), ReadOnlyRootFilesystem: new(false)}
	w := wl("console", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindService, SecurityContext: sc,
		Sidecars: []v1alpha1.Container{{Name: "proxy", Image: "gcr.io/p/proxy:1"}}})
	migrate := wl("migrate", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindJob, Args: []string{"m"}, Before: []string{v1alpha1.BeforeAll}})
	pod := podOf(objects(t, render(t, v1alpha1.ProfileFull, w, migrate))["Deployment/console"])
	if get(pod, "securityContext", "runAsUser") != float64(1000) || get(pod, "securityContext", "runAsGroup") != float64(1001) ||
		get(pod, "securityContext", "fsGroup") != float64(1002) || get(pod, "securityContext", "runAsNonRoot") != true {
		t.Errorf("pod securityContext = %v", get(pod, "securityContext"))
	}
	for i, c := range append(asSlice(get(pod, "containers")), asSlice(get(pod, "initContainers"))...) {
		sc := get(c, "securityContext")
		if get(sc, "runAsUser") != float64(1000) || get(sc, "runAsGroup") != float64(1001) || get(sc, "readOnlyRootFilesystem") != false || get(sc, "runAsNonRoot") != true {
			t.Errorf("container %d (%v) securityContext = %v", i, get(c, "name"), sc)
		}
	}
	if len(asSlice(get(pod, "initContainers"))) != 1 || len(asSlice(get(pod, "containers"))) != 2 {
		t.Fatal("expected one init and two containers to check")
	}
	// Unset: forge's defaults unchanged.
	pod = podOf(objects(t, render(t, v1alpha1.ProfileFull, svc("api")))["Deployment/api"])
	if get(pod, "securityContext", "runAsUser") != float64(RunAsUser) || get(pod, "containers", 0, "securityContext", "readOnlyRootFilesystem") != true || get(pod, "securityContext", "fsGroup") != nil {
		t.Errorf("default securityContext changed: %v", get(pod, "securityContext"))
	}
}

// TestSecretVolumeOptional: a Secret volume marked optional renders
// `optional: true` (the pod starts before the control plane has delivered the
// Secret, and the kubelet fills the volume in once it appears), and one that is
// not renders byte-identically to before the field existed.
func TestSecretVolumeOptional(t *testing.T) {
	w := svc("api", httpPort(true))
	w.Spec.Volumes = []v1alpha1.Volume{
		{Name: "otlp", MountPath: "/var/run/secrets/forge/otlp", ReadOnly: true, Source: v1alpha1.VolumeSource{Secret: &v1alpha1.SecretVolumeSource{
			Name: "reliant-otel-auth", Optional: true, Items: []v1alpha1.KeyToPath{{Key: "OTEL_EXPORTER_OTLP_HEADERS", Path: "headers"}},
		}}},
		{Name: "certs", MountPath: "/etc/certs", ReadOnly: true, Source: v1alpha1.VolumeSource{Secret: &v1alpha1.SecretVolumeSource{Name: "tls"}}},
	}
	pod := podOf(objects(t, render(t, v1alpha1.ProfileFull, w))["Deployment/api"])
	byName := map[string]any{}
	for _, v := range asSlice(get(pod, "volumes")) {
		m, _ := v.(map[string]any)
		byName[m["name"].(string)] = m["secret"]
	}
	otlp, _ := byName["otlp"].(map[string]any)
	if otlp["optional"] != true {
		t.Errorf("optional volume rendered optional = %v", otlp["optional"])
	}
	if items := asSlice(otlp["items"]); len(items) != 1 || items[0].(map[string]any)["path"] != "headers" {
		t.Errorf("the projected file is %v, want headers", otlp["items"])
	}
	certs, _ := byName["certs"].(map[string]any)
	if _, set := certs["optional"]; set {
		t.Errorf("a volume that did not ask for optional rendered optional = %v", certs["optional"])
	}
}

// TestRolloutDefaultsAgree: pkg/deploy renders a Deployment's rollout settings
// and pkg/release writes the same ones into a hand-written Deployment, and they
// cannot import each other. This is the one place that says they are equal.
func TestRolloutDefaultsAgree(t *testing.T) {
	if DefaultProgressDeadlineSeconds != release.DefaultProgressDeadlineSeconds {
		t.Errorf("progressDeadlineSeconds: render %d, release %d", DefaultProgressDeadlineSeconds, release.DefaultProgressDeadlineSeconds)
	}
	if defaultRolloutPercent != release.DefaultRolloutPercent {
		t.Errorf("rollout percent: render %q, release %q", defaultRolloutPercent, release.DefaultRolloutPercent)
	}
}

// wantReadOnlyRole is the CNPG managed-role entry for the read-only role,
// written out in full so a change to any attribute is a visible diff.
func wantReadOnlyRole(database string) map[string]any {
	return map[string]any{
		"name":            "forge_ro",
		"ensure":          "present",
		"comment":         "forge read-only role: column grants only, no forge:secret column, no write privilege",
		"login":           true,
		"superuser":       false,
		"createdb":        false,
		"createrole":      false,
		"replication":     false,
		"bypassrls":       false,
		"inherit":         false,
		"connectionLimit": float64(10),
		"passwordSecret":  map[string]any{"name": database + "-forge-ro"},
	}
}

// TestRenderManagedDatabaseDeclaresTheReadOnlyRole: the Cluster carries the
// forge_ro managed role, so CNPG converges it and nobody runs a setup step.
// The role can log in and can do nothing else the platform could mistake for
// power: no superuser, role or database creation, replication or RLS bypass,
// and no membership (pg_read_all_data would make the column grants moot).
func TestRenderManagedDatabaseDeclaresTheReadOnlyRole(t *testing.T) {
	for _, name := range []string{"order-db", "x"} {
		objs, err := RenderManagedDatabase(name, v1alpha1.ManagedDatabaseSpec{}, ctx)
		if err != nil {
			t.Fatal(err)
		}
		c := objects(t, objs)["Cluster/"+name]
		roles, _ := get(c, "spec", "managed", "roles").([]any)
		if len(roles) != 2 {
			t.Fatalf("%s: managed.roles = %v, want the read-only role and the absent dashboard role", name, roles)
		}
		if !reflect.DeepEqual(roles[0], wantReadOnlyRole(name)) {
			t.Errorf("%s: managed role = %#v\nwant %#v", name, roles[0], wantReadOnlyRole(name))
		}
		role := roles[0].(map[string]any)
		if _, ok := role["inRoles"]; ok {
			t.Errorf("%s: the read-only role is a member of %v: it must hold no role", name, role["inRoles"])
		}
		if role["passwordSecret"].(map[string]any)["name"] != dbgrants.SecretName(name) {
			t.Errorf("%s: passwordSecret does not name the contract Secret", name)
		}
	}
}

// TestRenderManagedDatabaseRestoreKeepsTheReadOnlyRole: a database bootstrapped
// from a backup still declares the role. Managed roles are the Cluster's, not
// the bootstrap's, and a restored database that lost its read-only role would
// lose `forge db query` exactly when someone is investigating an incident.
func TestRenderManagedDatabaseRestoreKeepsTheReadOnlyRole(t *testing.T) {
	spec := v1alpha1.ManagedDatabaseSpec{Restore: &v1alpha1.DatabaseRestore{SourceDatabase: "order-db"}}
	objs, err := RenderManagedDatabase("order-db-restored", spec, backupCtx)
	if err != nil {
		t.Fatal(err)
	}
	c := objects(t, objs)["Cluster/order-db-restored"]
	roles, _ := get(c, "spec", "managed", "roles").([]any)
	if len(roles) != 2 || !reflect.DeepEqual(roles[0], wantReadOnlyRole("order-db-restored")) {
		t.Errorf("restored Cluster managed.roles = %v, want the read-only role first", roles)
	}
}

// wantOffDashboardRole is what a database that declares no dashboards carries:
// the role present but unable to log in and without a password. It is NOT
// `ensure: absent`: CNPG drops an absent role with a plain DROP ROLE, which
// Postgres refuses once the role holds a grant, so the role would keep its
// login and password.
func wantOffDashboardRole() map[string]any {
	return map[string]any{
		"name":            "forge_dash",
		"ensure":          "present",
		"comment":         "forge dashboard role: OFF (cannot log in, no password); dashboards are not enabled on this database",
		"login":           false,
		"disablePassword": true,
		"superuser":       false,
		"createdb":        false,
		"createrole":      false,
		"replication":     false,
		"bypassrls":       false,
		"inherit":         false,
	}
}

// wantDashboardRole is the enabled role, written out in full like the
// read-only one so a change to any attribute is a visible diff.
func wantDashboardRole(database string, connectionLimit float64) map[string]any {
	return map[string]any{
		"name":            "forge_dash",
		"ensure":          "present",
		"comment":         "forge dashboard role: reads only what migrations grant it, no write privilege",
		"login":           true,
		"superuser":       false,
		"createdb":        false,
		"createrole":      false,
		"replication":     false,
		"bypassrls":       false,
		"inherit":         false,
		"connectionLimit": connectionLimit,
		"passwordSecret":  map[string]any{"name": database + "-forge-dash"},
	}
}

func dashboardRolesOf(t *testing.T, name string, spec v1alpha1.ManagedDatabaseSpec) []any {
	t.Helper()
	objs, err := RenderManagedDatabase(name, spec, ctx)
	if err != nil {
		t.Fatal(err)
	}
	roles, _ := get(objects(t, objs)["Cluster/"+name], "spec", "managed", "roles").([]any)
	return roles
}

// TestRenderManagedDatabaseDashboardRole: disabled (nil, or declared but not
// enabled) renders the role NOLOGIN with no password, never absent; enabled renders a login role with no
// membership, a connection limit of max_concurrency+1 and a password Secret
// named like forge_ro's.
func TestRenderManagedDatabaseDashboardRole(t *testing.T) {
	for name, spec := range map[string]v1alpha1.ManagedDatabaseSpec{
		"undeclared":      {},
		"declared-off":    {Dashboards: &v1alpha1.DatabaseDashboards{Enabled: false, MaxConcurrency: 4}},
		"declared-no-use": {Dashboards: &v1alpha1.DatabaseDashboards{}},
	} {
		roles := dashboardRolesOf(t, "order-db", spec)
		if len(roles) != 2 || !reflect.DeepEqual(roles[1], wantOffDashboardRole()) {
			t.Errorf("%s: managed.roles = %#v, want forge_ro then forge_dash off", name, roles)
		}
	}

	if r := wantOffDashboardRole(); r["ensure"] == "absent" || r["login"] != false || r["disablePassword"] != true {
		t.Error("the off role must be present, NOLOGIN and passwordless")
	} else if _, ok := r["passwordSecret"]; ok {
		t.Error("CNPG refuses a role that both sets and disables a password")
	}

	for name, tc := range map[string]struct {
		d     v1alpha1.DatabaseDashboards
		limit float64
	}{
		"defaults":    {v1alpha1.DatabaseDashboards{Enabled: true}, 3},
		"one":         {v1alpha1.DatabaseDashboards{Enabled: true, MaxConcurrency: 1}, 2},
		"max":         {v1alpha1.DatabaseDashboards{Enabled: true, MaxConcurrency: 4}, 5},
		"limits-only": {v1alpha1.DatabaseDashboards{Enabled: true, RowCap: 10, TimeoutMs: 100}, 3},
	} {
		roles := dashboardRolesOf(t, "order-db", v1alpha1.ManagedDatabaseSpec{Dashboards: &tc.d})
		if len(roles) != 2 || !reflect.DeepEqual(roles[0], wantReadOnlyRole("order-db")) {
			t.Fatalf("%s: managed.roles = %#v, want forge_ro first and unchanged", name, roles)
		}
		if !reflect.DeepEqual(roles[1], wantDashboardRole("order-db", tc.limit)) {
			t.Errorf("%s: dashboard role = %#v\nwant %#v", name, roles[1], wantDashboardRole("order-db", tc.limit))
		}
		if _, ok := roles[1].(map[string]any)["inRoles"]; ok {
			t.Errorf("%s: forge_dash is a member of a role: it must hold none", name)
		}
	}
}

// TestRenderManagedDatabaseRejectsOutOfRangeDashboards: a limit outside its
// range is refused, never clamped.
func TestRenderManagedDatabaseRejectsOutOfRangeDashboards(t *testing.T) {
	for name, d := range map[string]v1alpha1.DatabaseDashboards{
		"row cap too big":     {Enabled: true, RowCap: 10001},
		"timeout too long":    {Enabled: true, TimeoutMs: 10001},
		"timeout too short":   {Enabled: true, TimeoutMs: 99},
		"concurrency too big": {Enabled: true, MaxConcurrency: 5},
		"negative row cap":    {Enabled: true, RowCap: -1},
	} {
		if _, err := RenderManagedDatabase("order-db", v1alpha1.ManagedDatabaseSpec{Dashboards: &d}, ctx); err == nil {
			t.Errorf("%s: rendered, want a refusal", name)
		}
	}
}

// TestDashboardRoleSecret: the Secret the dashboard role's passwordSecret
// names, minted like forge_ro's.
func TestDashboardRoleSecret(t *testing.T) {
	sec, err := DashboardRoleSecret("order-db", "s3cret", ctx)
	if err != nil {
		t.Fatal(err)
	}
	m := objects(t, []*unstructured.Unstructured{sec})["Secret/order-db-forge-dash"]
	if m == nil {
		t.Fatalf("no Secret/order-db-forge-dash in %v", sec.Object)
	}
	if m["type"] != "kubernetes.io/basic-auth" {
		t.Errorf("type = %v", m["type"])
	}
	sd, _ := m["stringData"].(map[string]any)
	if sd["username"] != "forge_dash" || sd["password"] != "s3cret" {
		t.Errorf("stringData = %v", sd)
	}
	if get(m, "metadata", "labels", "cnpg.io/reload") != "true" {
		t.Errorf("labels = %v, want cnpg.io/reload", get(m, "metadata", "labels"))
	}
	if _, err := DashboardRoleSecret("order-db", "", ctx); err == nil {
		t.Error("a role with no password rendered")
	}
}

// TestReadOnlyRoleSecret pins the credentials Secret CNPG reads as the role's
// passwordSecret and a client reads to connect: basic-auth, the role's name,
// labelled so a password change applies immediately.
func TestReadOnlyRoleSecret(t *testing.T) {
	envCtx := ctx
	envCtx.Env = "prod"
	sec, err := ReadOnlyRoleSecret("order-db", "s3cret", envCtx)
	if err != nil {
		t.Fatal(err)
	}
	m := objects(t, []*unstructured.Unstructured{sec})["Secret/order-db-forge-ro"]
	if m == nil {
		t.Fatalf("Secret name = %q", sec.GetName())
	}
	if m["type"] != "kubernetes.io/basic-auth" {
		t.Errorf("type = %v, want kubernetes.io/basic-auth (CNPG refuses anything else)", m["type"])
	}
	sd, _ := m["stringData"].(map[string]any)
	if sd["username"] != "forge_ro" || sd["password"] != "s3cret" {
		t.Errorf("stringData = %v", sd)
	}
	if get(m, "metadata", "labels", "cnpg.io/reload") != "true" {
		t.Error("no cnpg.io/reload label: a changed password would wait for CNPG's next reconcile")
	}
	if get(m, "metadata", "labels", LabelEnv) != "prod" || get(m, "metadata", "namespace") != ctx.Namespace {
		t.Errorf("metadata = %v", m["metadata"])
	}
	if _, err := ReadOnlyRoleSecret("order-db", "", ctx); err == nil {
		t.Error("an empty password was accepted: the role would log in with none")
	}
	if _, err := ReadOnlyRoleSecret("Not_A_Name", "x", ctx); err == nil {
		t.Error("an invalid database name was accepted")
	}
}

// TestVersionStamp: app.kubernetes.io/version is the release, on the pod
// template of every kind that rolls with it, and never on a selector or a
// standalone Job (whose template is immutable and whose name is a spec hash).
func TestVersionStamp(t *testing.T) {
	ws := append(restrictedSet(), wl("nightly", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindCron, Schedule: "@daily", Args: []string{"x"}}),
		wl("seed", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindJob, Args: []string{"x"}}))
	renderAt := func(version string) map[string]map[string]any {
		c := ctx
		c.Version = version
		objs, err := RenderWorkloads(ws, v1alpha1.ProfileFull, c)
		if err != nil {
			t.Fatal(err)
		}
		return objects(t, objs)
	}

	v1 := renderAt("20261025")
	seen := 0
	for k, o := range v1 {
		got := get(o, "spec", "template", "metadata", "labels", LabelVersion)
		switch o["kind"] {
		case "CronJob":
			got = get(o, "spec", "jobTemplate", "spec", "template", "metadata", "labels", LabelVersion)
		case "Job":
			if got != nil {
				t.Errorf("%s: standalone Job pod template stamped with a version", k)
			}
			continue
		}
		if podOf(o) == nil {
			continue
		}
		seen++
		if got != "20261025" {
			t.Errorf("%s: pod template version label = %v, want 20261025", k, got)
		}
		if get(o, "spec", "selector", "matchLabels", LabelVersion) != nil || get(o, "spec", "selector", LabelVersion) != nil {
			t.Errorf("%s: version label on a selector (immutable)", k)
		}
		if get(o, "metadata", "labels", LabelVersion) != nil {
			t.Errorf("%s: version label on the object itself", k)
		}
	}
	if seen == 0 {
		t.Fatal("rendered no pod templates")
	}

	// Stable for a release, so a re-render does not roll; moves with it.
	if !reflect.DeepEqual(v1, renderAt("20261025")) {
		t.Error("same release rendered different bytes")
	}
	if reflect.DeepEqual(v1, renderAt("20261026")) {
		t.Error("a new release did not change the pod template")
	}

	// No release, no label: output is byte-identical to a render that never
	// heard of versions.
	if !reflect.DeepEqual(renderAt(""), objects(t, render(t, v1alpha1.ProfileFull, ws...))) {
		t.Error("an empty version changed the render")
	}
	for k, o := range renderAt("") {
		if get(o, "spec", "template", "metadata", "labels", LabelVersion) != nil {
			t.Errorf("%s: label emitted for an empty version", k)
		}
	}
}

func TestLabelValue(t *testing.T) {
	for in, want := range map[string]string{
		"20261025":                        "20261025",
		"v1.2.3":                          "v1.2.3",
		"1.2.3+build.5":                   "1.2.3-build.5",
		"-weird-":                         "weird",
		"":                                "",
		"///":                             "",
		strings.Repeat("a", 70):           strings.Repeat("a", 63),
		strings.Repeat("a", 62) + "-zzzz": strings.Repeat("a", 62),
	} {
		if got := labelValue(in); got != want {
			t.Errorf("labelValue(%q) = %q, want %q", in, got, want)
		}
	}
}
