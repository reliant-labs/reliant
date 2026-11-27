package deploy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/yaml"

	"github.com/reliant-labs/forge/pkg/telemetry"
)

func kubernetesInventorySpec() ObservabilityAgentSpec {
	s := agentSpec()
	s.HeadersSecret = nil
	s.TokenSecret = &ObservabilitySecretKey{Name: "forge-telemetry-token", Key: "token"}
	s.KubernetesInventory = &KubernetesInventory{Cluster: "k3d-gpu", NodeInterval: "60s", WorkloadInterval: "300s", Events: true}
	return s
}

// The agent with its cluster's Kubernetes inventory, goldened whole: the
// cluster collector's config (k8sobjects, the cluster stamp, the shared
// destination and token) and its read-only RBAC are a reviewed diff.
func TestRenderKubernetesInventoryGolden(t *testing.T) {
	got := agentYAML(t, kubernetesInventorySpec())
	path := filepath.Join("testdata", "observability_agent_kubernetes_inventory.golden.yaml")
	if *updateGolden {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (regenerate with -update)", err)
	}
	if !bytes.Equal(want, got) {
		t.Errorf("%s differs from the render (-update to accept):\n--- got\n%s", path, got)
	}
}

func TestClusterCollectorConfig(t *testing.T) {
	s := kubernetesInventorySpec()
	cfgYAML, err := s.ClusterCollectorConfig(*s.KubernetesInventory)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := yaml.Unmarshal([]byte(cfgYAML), &cfg); err != nil {
		t.Fatal(err)
	}
	var objs []string
	for _, o := range cfg["receivers"].(map[string]any)["k8sobjects"].(map[string]any)["objects"].([]any) {
		m := o.(map[string]any)
		objs = append(objs, m["name"].(string)+"/"+m["mode"].(string))
	}
	if got := strings.Join(objs, ","); got != "nodes/pull,daemonsets/pull,deployments/pull,events/watch" {
		t.Errorf("objects = %s", got)
	}
	attrs := cfg["processors"].(map[string]any)["resource"].(map[string]any)["attributes"].([]any)
	if len(attrs) != 1 || attrs[0].(map[string]any)["key"] != ClusterNameAttribute || attrs[0].(map[string]any)["value"] != "k3d-gpu" {
		t.Errorf("resource attributes = %v, want only %s = k3d-gpu", attrs, ClusterNameAttribute)
	}
	if strings.Contains(cfgYAML, "file_storage") {
		t.Errorf("a Deployment has no host directory for a persistent queue:\n%s", cfgYAML)
	}
	if !strings.Contains(cfgYAML, "authenticator: bearertokenauth") {
		t.Errorf("exporter does not send the agent's token:\n%s", cfgYAML)
	}

	c := *s.KubernetesInventory
	c.Events = false
	noEvents, err := s.ClusterCollectorConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(noEvents, "events") {
		t.Errorf("events = false still watches events:\n%s", noEvents)
	}
	c.NodeInterval = "sixty"
	if _, err := s.ClusterCollectorConfig(c); err == nil {
		t.Error("a bad interval was accepted")
	}
}

// The collector's ClusterRole grants exactly what its receiver reads, and
// only get/list/watch: no Secrets, no nodes/proxy, no write verb, and no
// Events when it does not watch them.
func TestClusterCollectorRBACIsReadOnly(t *testing.T) {
	for _, events := range []bool{true, false} {
		s := kubernetesInventorySpec()
		s.KubernetesInventory.Events = events
		objs, err := RenderObservabilityAgent(s, agentCtx)
		if err != nil {
			t.Fatal(err)
		}
		var grants []string
		found := false
		for _, o := range objs {
			if o.GetKind() != "ClusterRole" || o.GetName() != "forge-observability-"+ClusterCollectorName {
				continue
			}
			found = true
			var role rbacv1.ClusterRole
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(o.Object, &role); err != nil {
				t.Fatal(err)
			}
			for _, r := range role.Rules {
				if len(r.ResourceNames) > 0 || len(r.NonResourceURLs) > 0 {
					t.Errorf("rule %+v names resources or URLs", r)
				}
				if v := strings.Join(r.Verbs, ","); v != "get,list,watch" {
					t.Errorf("rule %+v grants verbs %s, want get,list,watch", r, v)
				}
				for _, g := range r.APIGroups {
					for _, res := range r.Resources {
						grants = append(grants, g+"/"+res)
					}
				}
			}
		}
		if !found {
			t.Fatal("no cluster collector ClusterRole rendered")
		}
		want := "/nodes,apps/daemonsets,apps/deployments"
		if events {
			want += ",events.k8s.io/events"
		}
		if got := strings.Join(grants, ","); got != want {
			t.Errorf("events=%v: ClusterRole grants %s, want exactly %s", events, got, want)
		}
	}
}

// podTemplateDigest renders the agent and returns the cluster collector's
// pod-template config digest and its ConfigMap's config.
func podTemplateDigest(t *testing.T, s ObservabilityAgentSpec) (digest, cfg string) {
	t.Helper()
	objs, err := RenderObservabilityAgent(s, agentCtx)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range objs {
		if o.GetName() != ClusterCollectorName {
			continue
		}
		switch o.GetKind() {
		case "Deployment":
			var d appsv1.Deployment
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(o.Object, &d); err != nil {
				t.Fatal(err)
			}
			digest = d.Spec.Template.Annotations[clusterCollectorConfigAnnotation]
		case "ConfigMap":
			var cm corev1.ConfigMap
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(o.Object, &cm); err != nil {
				t.Fatal(err)
			}
			cfg = cm.Data["config.yaml"]
		}
	}
	if digest == "" || cfg == "" {
		t.Fatalf("no cluster collector Deployment digest (%q) or ConfigMap config", digest)
	}
	return digest, cfg
}

// The collector reads its config only at startup, so the pod template
// carries the config's SHA-256: an unchanged render keeps the pod, and
// turning events off or changing an interval rolls it.
func TestClusterCollectorPodTemplateCarriesTheConfigDigest(t *testing.T) {
	s := kubernetesInventorySpec()
	digest, cfg := podTemplateDigest(t, s)
	sum := sha256.Sum256([]byte(cfg))
	if want := hex.EncodeToString(sum[:]); digest != want {
		t.Errorf("digest %s is not the ConfigMap config's SHA-256 %s", digest, want)
	}
	if again, _ := podTemplateDigest(t, kubernetesInventorySpec()); again != digest {
		t.Errorf("the same declaration rendered digests %s and %s", digest, again)
	}
	for name, edit := range map[string]func(*KubernetesInventory){
		"events off":        func(c *KubernetesInventory) { c.Events = false },
		"node interval":     func(c *KubernetesInventory) { c.NodeInterval = "120s" },
		"workload interval": func(c *KubernetesInventory) { c.WorkloadInterval = "600s" },
	} {
		changed := kubernetesInventorySpec()
		inv := *changed.KubernetesInventory
		edit(&inv)
		changed.KubernetesInventory = &inv
		if d, _ := podTemplateDigest(t, changed); d == digest {
			t.Errorf("%s: the pod template's digest did not change, so the running collector keeps its old config", name)
		}
	}
}

// inventorySecrets are planted in every place a workload object carries a
// credential; none may reach an exported body.
var inventorySecrets = []string{
	"ENV-VALUE-SECRET", "ARG-SECRET", "COMMAND-SECRET", "LAST-APPLIED-SECRET",
	"ANNOTATION-SECRET", "LABEL-SECRET", "CONDITION-MESSAGE-SECRET", "EVENT-NOTE-SECRET",
	"INIT-CONTAINER-SECRET", "STRING-BODY-SECRET", "NODE-ADDRESS-SECRET",
}

// inventoryFixtures are k8sobjects bodies, one per line: a pull Deployment
// and DaemonSet, a Node, a watch envelope around an Event, and a body that
// is not an object at all.
func inventoryFixtures() []string {
	lastApplied := `{\"spec\":{\"template\":{\"spec\":{\"containers\":[{\"env\":[{\"name\":\"DB_PASSWORD\",\"value\":\"LAST-APPLIED-SECRET\"}]}]}}}}`
	podSpec := `{"initContainers":[{"name":"init","image":"busybox","env":[{"name":"T","value":"INIT-CONTAINER-SECRET"}]}],` +
		`"containers":[{"name":"app","image":"ghcr.io/acme/app:1.2.3","command":["/app","COMMAND-SECRET"],"args":["--token=ARG-SECRET"],` +
		`"env":[{"name":"DB_PASSWORD","value":"ENV-VALUE-SECRET"}]},{"name":"sidecar","image":"ghcr.io/acme/proxy:4"}]}`
	meta := func(kind, name string) string {
		return `"kind":"` + kind + `","apiVersion":"apps/v1","metadata":{"name":"` + name + `","namespace":"prod","uid":"uid-` + name + `",` +
			`"labels":{"app.kubernetes.io/name":"` + name + `","acme.io/token":"LABEL-SECRET"},` +
			`"annotations":{"kubectl.kubernetes.io/last-applied-configuration":"` + lastApplied + `","acme.io/note":"ANNOTATION-SECRET"}}`
	}
	return []string{
		`{` + meta("Deployment", "api") + `,"spec":{"replicas":3,"template":{"metadata":{"annotations":{"a":"ANNOTATION-SECRET"}},"spec":` + podSpec + `}},` +
			`"status":{"replicas":3,"readyReplicas":2,"conditions":[{"type":"Available","status":"True","reason":"MinimumReplicasAvailable","message":"CONDITION-MESSAGE-SECRET"}]}}`,
		`{` + meta("DaemonSet", "agent") + `,"spec":{"template":{"spec":` + podSpec + `}},"status":{"desiredNumberScheduled":4,"numberReady":4}}`,
		`{"kind":"Node","apiVersion":"v1","metadata":{"name":"gpu-1","uid":"uid-gpu-1","annotations":{"a":"ANNOTATION-SECRET"}},` +
			`"status":{"capacity":{"cpu":"8","nvidia.com/gpu":"1"},"allocatable":{"cpu":"7"},"nodeInfo":{"kubeletVersion":"v1.33.1","bootID":"x"},` +
			`"addresses":[{"type":"InternalIP","address":"NODE-ADDRESS-SECRET"}],"conditions":[{"type":"Ready","status":"True","reason":"KubeletReady","message":"CONDITION-MESSAGE-SECRET"}]}}`,
		`{"type":"ADDED","object":{"kind":"Event","apiVersion":"events.k8s.io/v1","metadata":{"name":"api.1","namespace":"prod","annotations":{"a":"ANNOTATION-SECRET"}},` +
			`"type":"Warning","reason":"BackOff","note":"EVENT-NOTE-SECRET","regarding":{"kind":"Pod","namespace":"prod","name":"api-1","fieldPath":"spec.containers{app}"}}}`,
		`"STRING-BODY-SECRET"`,
	}
}

// The projection, as rendered, keeps only allowlisted fields: every
// statement after the wipe writes into the projection or the body from it.
func TestClusterCollectorProjectsBeforeExport(t *testing.T) {
	s := kubernetesInventorySpec()
	cfgYAML, err := s.ClusterCollectorConfig(*s.KubernetesInventory)
	if err != nil {
		t.Fatal(err)
	}
	doc := parseCollector(t, cfgYAML)
	if got := strings.Join(doc.Service.Pipelines["logs"].Processors, ","); got != "memory_limiter,"+inventoryProcessor+",resource,batch" {
		t.Errorf("logs processors = %s: the projection must run on every row before export", got)
	}
	st := inventoryStatements()
	if st[0] != `set(log.cache["raw"], log.body)` || st[1] != "set(log.body, {})" {
		t.Errorf("the projection must wipe the body before copying anything back, starts %v", st[:2])
	}
	for _, v := range st[2:] {
		stmt := v.(string)
		if strings.HasPrefix(stmt, "set(log.body") && !strings.Contains(stmt, `log.cache["out"]`) && !strings.HasPrefix(stmt, `set(log.body["type"], log.cache["raw"]["type"])`) {
			t.Errorf("statement writes the body from outside the projection: %s", stmt)
		}
		for _, bad := range []string{`["env"]`, `["args"]`, `["command"]`, `["annotations"]`, `["note"]`, `["message"]`, `["addresses"]`} {
			if strings.Contains(stmt, bad) {
				t.Errorf("statement copies %s: %s", bad, stmt)
			}
		}
	}
}

// The projection run by the pinned collector image over credential-bearing
// objects: none of the planted secrets reaches an exported body, and the
// inventory fields do.
func TestClusterCollectorDropsCredentialsInTheCollector(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the pinned collector image in docker; runs in task test")
	}
	s := kubernetesInventorySpec()
	cfgYAML, err := s.ClusterCollectorConfig(*s.KubernetesInventory)
	if err != nil {
		t.Fatal(err)
	}
	doc := parseCollector(t, cfgYAML)
	dir := t.TempDir()
	in := filepath.Join(dir, "in")
	if err := os.Mkdir(in, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(in, "objects.log"), []byte(strings.Join(inventoryFixtures(), "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// filelog stands in for k8sobjects: it parses each line into a map
	// body, the shape k8sobjects emits.
	cfg := collectorDoc{Receivers: map[string]map[string]any{"filelog": {
		"include":   []string{"/in/*.log"},
		"operators": []any{map[string]any{"type": "json_parser", "parse_to": "body", "on_error": "send_quiet"}},
	}}}
	raw, logs := runCollectorFile(t, cfg, map[string]any{inventoryProcessor: doc.Processors[inventoryProcessor]}, []string{inventoryProcessor}, in, "/in", "BackOff")
	if n, want := strings.Count(raw, `"observedTimeUnixNano"`), len(inventoryFixtures()); n != want {
		t.Fatalf("collector exported %d records, want %d:\n%s\ncollector logs:\n%s", n, want, raw, logs)
	}
	for _, secret := range inventorySecrets {
		if strings.Contains(raw, secret) {
			t.Errorf("exported %s:\n%s", secret, raw)
		}
	}
	for _, want := range []string{"ghcr.io/acme/app:1.2.3", "ghcr.io/acme/proxy:4", `"sidecar"`, "uid-api", "MinimumReplicasAvailable",
		"desiredNumberScheduled", "nvidia.com/gpu", "v1.33.1", "KubeletReady", `"ADDED"`, `"Warning"`, "api-1", `"app.kubernetes.io/name"`} {
		if !strings.Contains(raw, want) {
			t.Errorf("projection dropped %s:\n%s", want, raw)
		}
	}
}

// Without KubernetesInventory the agent renders exactly what it did: no
// cluster collector objects, and the same agent config.
func TestNoClusterCollectorWithoutKubernetesInventory(t *testing.T) {
	objs, err := RenderObservabilityAgent(agentSpec(), agentCtx)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range objs {
		if strings.Contains(o.GetName(), ClusterCollectorName) {
			t.Errorf("rendered %s %s", o.GetKind(), o.GetName())
		}
	}
}

func scrapedAgentSpec() ObservabilityAgentSpec {
	s := agentSpec()
	targets := []telemetry.Target{{
		ServiceName: "dcgm-exporter", Scope: telemetry.ScopePlatform, Source: "workload:gpu",
		Attachment: telemetry.AttachObservability, Runtime: telemetry.RuntimeCluster, Cluster: "k3d-gpu",
		Selector: map[string]string{"app": "nvidia-dcgm-exporter"}, Namespaces: []string{"gpu-operator"},
		Metrics: []telemetry.MetricsScrape{{Port: 9400, Path: "/metrics", Interval: "30s"}},
	}}
	s.Telemetry = &targets
	return s
}

// With KubernetesInventory, every scrape pipeline of the node agent
// carries the cluster and the node, before batch.
func TestScrapeCarriesClusterAndNodeWithKubernetesInventory(t *testing.T) {
	s := scrapedAgentSpec()
	s.KubernetesInventory = &KubernetesInventory{Cluster: "k3d-gpu", NodeInterval: "60s", WorkloadInterval: "300s", Events: true}
	cfgYAML, err := s.AgentConfigFor(agentCtx)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := yaml.Unmarshal([]byte(cfgYAML), &cfg); err != nil {
		t.Fatal(err)
	}
	p := cfg["service"].(map[string]any)["pipelines"].(map[string]any)["metrics/telemetry_dcgm-exporter"].(map[string]any)
	procs := p["processors"].([]any)
	if procs[len(procs)-1] != "batch" || procs[len(procs)-2] != scrapeClusterProcessor {
		t.Errorf("processors = %v", procs)
	}
	attrs := cfg["processors"].(map[string]any)[scrapeClusterProcessor].(map[string]any)["attributes"].([]any)
	got := map[string]any{}
	for _, a := range attrs {
		got[a.(map[string]any)["key"].(string)] = a.(map[string]any)["value"]
	}
	if got[ClusterNameAttribute] != "k3d-gpu" || got[NodeNameAttribute] != "${env:K8S_NODE_NAME}" || len(got) != 2 {
		t.Errorf("%s attributes = %v", scrapeClusterProcessor, got)
	}
}

// Without it, the node agent's scrapes are left as they were.
func TestScrapeUnstampedWithoutKubernetesInventory(t *testing.T) {
	cfgYAML, err := scrapedAgentSpec().AgentConfigFor(agentCtx)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(cfgYAML, scrapeClusterProcessor) || strings.Contains(cfgYAML, "key: "+ClusterNameAttribute) {
		t.Errorf("scrape stamped without KubernetesInventory:\n%s", cfgYAML)
	}
}

// The agent record's `kubernetesInventory` key, exactly as kcl/render.k
// writes it (kcl/tests/positive_observability_kubernetes_inventory.k pins
// the KCL side of the same literal), decodes whole.
func TestKubernetesInventoryDecodesTheRenderedRecord(t *testing.T) {
	var s ObservabilityAgentSpec
	rec := `{"kubernetesInventory": {"cluster": "gpu", "nodeInterval": "60s", "workloadInterval": "300s", "events": true}}`
	if err := yaml.Unmarshal([]byte(rec), &s); err != nil {
		t.Fatal(err)
	}
	want := KubernetesInventory{Cluster: "gpu", NodeInterval: "60s", WorkloadInterval: "300s", Events: true}
	if s.KubernetesInventory == nil || *s.KubernetesInventory != want {
		t.Errorf("decoded %+v, want %+v", s.KubernetesInventory, want)
	}
}
