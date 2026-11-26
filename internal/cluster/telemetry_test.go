package cluster

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/reliant-labs/forge/internal/kclplugin"
	"github.com/reliant-labs/forge/internal/kclrender"
	"github.com/reliant-labs/forge/pkg/deploy"
	"github.com/reliant-labs/forge/pkg/release"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// telemetryDoc is a render's output with declared telemetry on cluster
// gke-prod: a Workload target, a Manifests target that asks for its own
// NetworkPolicy, and the node agent that collects both.
func telemetryDoc(own bool) map[string]any {
	group := map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "temporal", "namespace": "temporal", "labels": map[string]any{
			WorkloadLabel: "temporal", release.LabelDeliveryUnit: "infra", ClusterRoutingLabel: "gke-prod",
		}},
	}
	other := map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]any{"name": "unrelated", "namespace": "shop-prod", "labels": map[string]any{ClusterRoutingLabel: "gke-prod"}},
	}
	targets := []any{
		map[string]any{
			"service_name": "admin", "scope": "project", "source": "workload:admin", "attachment": "workload",
			"runtime": "cluster", "cluster": "gke-prod", "namespaces": []any{"shop-prod"},
			"selector": map[string]any{deploy.LabelName: "admin"}, "ports": map[string]any{"http": 8080},
			"metrics": []any{map[string]any{"port": 0, "port_name": "http", "path": "/metrics"}},
		},
		map[string]any{
			"service_name": "temporal", "scope": "project", "source": "manifests:temporal", "attachment": "manifests",
			"runtime": "cluster", "cluster": "gke-prod", "namespaces": []any{"temporal"},
			"selector": map[string]any{"app": "temporal"}, "network_policy": true,
			"metrics": []any{map[string]any{"port": 9090}},
		},
	}
	agent := map[string]any{
		"apiVersion": "forge.dev/v1alpha1", "kind": deploy.ObservabilityAgentKind,
		"metadata": map[string]any{"name": deploy.ObservabilityAgentName, "namespace": "forge-observability", "labels": map[string]any{
			deploy.LabelName: deploy.ObservabilityAgentName, deploy.LabelManagedBy: "forge", deploy.LabelPartOf: "shop", ClusterRoutingLabel: "gke-prod",
		}},
		"spec": map[string]any{
			"endpoint": "https://otlp.example.com", "protocol": "http/protobuf",
			"signals": map[string]any{"traces": true, "metrics": true, "logs": true, "profiles": false},
			"image":   "otel/opentelemetry-collector-contrib:0.162.0",
		},
	}
	if !own {
		agent["spec"].(map[string]any)["telemetry"] = targets
	}
	out := map[string]any{
		"env":            "prod",
		"manifests":      []any{workloadRecord("admin", ""), agent, group, other},
		"network_policy": map[string]any{"egress_ports": []any{443}, "telemetry_namespace": "forge-observability"},
		"telemetry":      targets,
	}
	if own {
		out["telemetry_collector"] = map[string]any{"namespace": "observability", "pod_labels": map[string]any{"app": "otel"}}
	}
	return map[string]any{"output": out}
}

func extractTelemetryDoc(t *testing.T, doc map[string]any) []map[string]any {
	t.Helper()
	b, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := ExtractManifests(b)
	if err != nil {
		t.Fatal(err)
	}
	var objs []map[string]any
	for _, part := range strings.Split(stream, "---\n") {
		var o map[string]any
		if err := yaml.Unmarshal([]byte(part), &o); err != nil {
			t.Fatal(err)
		}
		if o != nil {
			objs = append(objs, o)
		}
	}
	return objs
}

func findObject(objs []map[string]any, kind, name string) (int, map[string]any) {
	for i, o := range objs {
		if o["kind"] == kind && o["metadata"].(map[string]any)["name"] == name {
			return i, o
		}
	}
	return -1, nil
}

// telemetryPolicy finds the standalone telemetry NetworkPolicy of a service
// by its forge.dev/telemetry-service label: the name carries a digest of the
// service (collector.NetworkPolicies), so it is not spelled out here.
func telemetryPolicy(objs []map[string]any, svc string) (int, map[string]any) {
	for i, o := range objs {
		if o["kind"] != "NetworkPolicy" {
			continue
		}
		labels, _ := o["metadata"].(map[string]any)["labels"].(map[string]any)
		if labels["forge.dev/telemetry-service"] == svc {
			return i, o
		}
	}
	return -1, nil
}

func admits(rule map[string]any) (ns, pod string) {
	from := rule["from"].([]any)[0].(map[string]any)
	ns = from["namespaceSelector"].(map[string]any)["matchLabels"].(map[string]any)["kubernetes.io/metadata.name"].(string)
	for _, v := range from["podSelector"].(map[string]any)["matchLabels"].(map[string]any) {
		pod = v.(string)
	}
	return ns, pod
}

// The agent's collector admits itself everywhere it pulls: into the
// Workload's own policy, and, for the Manifests target that asked, through a
// policy placed right after its group's objects with the group's owner and
// delivery unit.
func TestExtractManifestsAdmitsTheNodeAgent(t *testing.T) {
	objs := extractTelemetryDoc(t, telemetryDoc(false))

	_, cm := findObject(objs, "ConfigMap", deploy.ObservabilityAgentName)
	if cm == nil || !strings.Contains(cm["data"].(map[string]any)["config.yaml"].(string), "prometheus/telemetry_admin") {
		t.Fatalf("the agent config does not collect the declared telemetry: %v", cm)
	}

	_, np := findObject(objs, "NetworkPolicy", "admin-ingress")
	if np == nil {
		t.Fatal("no admin-ingress policy")
	}
	rules := np["spec"].(map[string]any)["ingress"].([]any)
	if ns, pod := admits(rules[len(rules)-1].(map[string]any)); ns != "forge-observability" || pod != deploy.ObservabilityAgentName {
		t.Errorf("admin's policy admits %s/%s, want the agent", ns, pod)
	}

	at, own := telemetryPolicy(objs, "temporal")
	group, _ := findObject(objs, "Deployment", "temporal")
	if own == nil || at != group+1 {
		t.Fatalf("telemetry-temporal at %d, want right after its group's Deployment at %d", at, group)
	}
	labels := own["metadata"].(map[string]any)["labels"].(map[string]any)
	for k, want := range map[string]string{WorkloadLabel: "temporal", release.LabelDeliveryUnit: "infra", ClusterRoutingLabel: "gke-prod", deploy.LabelEnv: "prod"} {
		if labels[k] != want {
			t.Errorf("telemetry-temporal label %s = %v, want %s", k, labels[k], want)
		}
	}
	if ns, pod := admits(own["spec"].(map[string]any)["ingress"].([]any)[0].(map[string]any)); ns != "forge-observability" || pod != deploy.ObservabilityAgentName {
		t.Errorf("telemetry-temporal admits %s/%s, want the agent", ns, pod)
	}
}

// With its own collector declared, the env's targets are that collector's:
// the agent collects none of them, and every policy admits it instead.
func TestExtractManifestsAdmitsTheEnvsOwnCollector(t *testing.T) {
	objs := extractTelemetryDoc(t, telemetryDoc(true))
	_, cm := findObject(objs, "ConfigMap", deploy.ObservabilityAgentName)
	if strings.Contains(cm["data"].(map[string]any)["config.yaml"].(string), "telemetry_") {
		t.Error("the agent collects targets the env's own collector owns")
	}
	_, np := findObject(objs, "NetworkPolicy", "admin-ingress")
	rules := np["spec"].(map[string]any)["ingress"].([]any)
	if ns, pod := admits(rules[len(rules)-1].(map[string]any)); ns != "observability" || pod != "otel" {
		t.Errorf("admin's policy admits %s/%s, want the env's collector", ns, pod)
	}
	_, own := telemetryPolicy(objs, "temporal")
	if ns, pod := admits(own["spec"].(map[string]any)["ingress"].([]any)[0].(map[string]any)); ns != "observability" || pod != "otel" {
		t.Errorf("telemetry-temporal admits %s/%s, want the env's collector", ns, pod)
	}
}

// No declared telemetry: the same stream as before, object for object.
func TestExtractManifestsWithoutTelemetryIsUnchanged(t *testing.T) {
	doc := telemetryDoc(false)
	out := doc["output"].(map[string]any)
	delete(out, "telemetry")
	delete(out["manifests"].([]any)[1].(map[string]any)["spec"].(map[string]any), "telemetry")
	objs := extractTelemetryDoc(t, doc)
	if _, np := telemetryPolicy(objs, "temporal"); np != nil {
		t.Error("a telemetry policy rendered with no telemetry declared")
	}
	_, np := findObject(objs, "NetworkPolicy", "admin-ingress")
	for _, r := range np["spec"].(map[string]any)["ingress"].([]any) {
		if r.(map[string]any)["from"] != nil {
			if from := r.(map[string]any)["from"].([]any)[0].(map[string]any); from["namespaceSelector"] != nil {
				t.Errorf("admin's policy admits another namespace with no telemetry: %v", r)
			}
		}
	}
}

const telemetryRenderMain = `
import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    env = "staging"
    network_policy = forge.NetworkPolicy {egress_ports = [443]}
    required_secrets = [
        forge.ExternalSecret {name = "otlp-auth", namespace = "forge-observability", keys = ["headers"]}
        forge.ExternalSecret {name = "openbao-metrics", namespace = "forge-observability", keys = ["token"]}
    ]
    observability = forge.Observability {
        destination = forge.ObservabilityDestination {
            endpoint = "https://otlp.example.com"
            headers_secret = forge.ObservabilitySecretRef {name = "otlp-auth", key = "headers"}
        }
        telemetry = [forge.Telemetry {
            service_name = "openbao", cluster = "k3d-demo", namespaces = ["openbao"], selector = {"app.kubernetes.io/name" = "openbao"}
            metrics = [forge.MetricsScrape {port = 8200, scheme = "https", bearer_token = forge.SecretRef {name = "openbao-metrics", key = "token"}}]
        }]
    }
    workloads = [fw.Workload {
        name = "zitadel", image = "ghcr.io/zitadel/zitadel:v4", ports = [fw.Port {name = "http", port = 8080}]
        runtime = forge.OnCluster {target = _k3d}
        telemetry = forge.Telemetry {metrics = [forge.MetricsScrape {port = "http", path = "/debug/metrics"}]}
    }]
    manifests = [forge.Manifests {name = "temporal", namespace = "temporal", objects = [
        {apiVersion = "v1", kind = "Service", metadata = {name = "temporal"}, spec = {ports = [{port = 7233}]}}
    ], telemetry = [forge.Telemetry {selector = {app = "temporal"}, network_policy = True, metrics = [forge.MetricsScrape {port = 9090}]}]}]
})
`

// A real render: the KCL agent record carries the targets, Go decodes it
// strictly into the spec and renders them into the agent; the workload's
// policy and the Manifests group's own policy admit the agent; the Secret is
// mounted.
func TestRenderedTelemetryReachesTheNodeAgent(t *testing.T) {
	testkit.Slow(t, "renders the KCL module (its files are invisible to the test cache)")
	dir := t.TempDir()
	for name, body := range map[string]string{
		"kcl.mod": "[package]\nname = \"telemetryagent\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n",
		"main.k":  telemetryRenderMain,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	kclplugin.Register()
	out, err := kclrender.Run(dir, dir, nil)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	stream, err := ExtractManifests(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"prometheus/telemetry_zitadel", "prometheus/telemetry_openbao", "prometheus/telemetry_temporal",
		deploy.TelemetryMountRoot + "/openbao-metrics",
		"name: telemetry-temporal",
	} {
		if !strings.Contains(stream, want) {
			t.Errorf("rendered stream lacks %q", want)
		}
	}
	var objs []map[string]any
	for _, part := range strings.Split(stream, "---\n") {
		var o map[string]any
		if err := yaml.Unmarshal([]byte(part), &o); err != nil {
			t.Fatal(err)
		}
		if o != nil {
			objs = append(objs, o)
		}
	}
	_, np := findObject(objs, "NetworkPolicy", "zitadel-ingress")
	if np == nil {
		t.Fatal("no zitadel-ingress policy")
	}
	rules := np["spec"].(map[string]any)["ingress"].([]any)
	if ns, pod := admits(rules[len(rules)-1].(map[string]any)); ns != "forge-observability" || pod != deploy.ObservabilityAgentName {
		t.Errorf("zitadel's policy admits %s/%s, want the agent", ns, pod)
	}
	at, _ := telemetryPolicy(objs, "temporal")
	svc, _ := findObject(objs, "Service", "temporal")
	if at != svc+1 {
		t.Errorf("telemetry-temporal at %d, want right after the group's Service at %d", at, svc)
	}
}

// A ManagedDatabase target that asks for a NetworkPolicy gets one of its
// own, like a Manifests target: right after the objects its record expanded
// into (grouped by the database's name), with their delivery unit. Unset
// renders none: a policy selecting the instances would deny every other
// Postgres caller.
func TestExtractManifestsManagedDatabasePolicy(t *testing.T) {
	for _, ask := range []bool{true, false} {
		doc := telemetryDoc(false)
		out := doc["output"].(map[string]any)
		db := map[string]any{
			"apiVersion": "postgresql.cnpg.io/v1", "kind": "Cluster",
			"metadata": map[string]any{"name": "db", "namespace": "shop-prod", "labels": map[string]any{
				AppNameLabel: "db", release.LabelDeliveryUnit: "data", ClusterRoutingLabel: "gke-prod",
			}},
		}
		out["manifests"] = append(out["manifests"].([]any), db)
		target := map[string]any{
			"service_name": "db-postgres", "scope": "project", "source": "manageddatabase:db", "attachment": "manageddatabase",
			"runtime": "cluster", "cluster": "gke-prod", "namespaces": []any{"shop-prod"},
			"selector": map[string]any{"cnpg.io/cluster": "db"}, "ports": map[string]any{"metrics": 9187},
			"metrics": []any{map[string]any{"port": 0, "port_name": "metrics", "allow": "cnpg_.*"}},
		}
		if ask {
			target["network_policy"] = true
		}
		out["telemetry"] = append(out["telemetry"].([]any), target)
		objs := extractTelemetryDoc(t, doc)
		at, np := telemetryPolicy(objs, "db-postgres")
		if !ask {
			if np != nil {
				t.Error("network_policy unset rendered a policy for the database's instances")
			}
			continue
		}
		group, _ := findObject(objs, "Cluster", "db")
		if np == nil || at != group+1 {
			t.Fatalf("db-postgres policy at %d, want right after the database's Cluster at %d", at, group)
		}
		labels := np["metadata"].(map[string]any)["labels"].(map[string]any)
		if labels[release.LabelDeliveryUnit] != "data" || labels[ClusterRoutingLabel] != "gke-prod" {
			t.Errorf("db-postgres policy labels = %v, want the database's delivery unit and cluster", labels)
		}
		if ns, pod := admits(np["spec"].(map[string]any)["ingress"].([]any)[0].(map[string]any)); ns != "forge-observability" || pod != deploy.ObservabilityAgentName {
			t.Errorf("db-postgres policy admits %s/%s, want the agent", ns, pod)
		}
	}
}
