package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/yaml"

	"github.com/reliant-labs/forge/internal/otelcoltest"
	"github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
	"github.com/reliant-labs/forge/pkg/telemetry"
	"github.com/reliant-labs/forge/pkg/telemetry/collector"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// The fixtures are shaped after real systems so the golden reads like the
// config it becomes. The vendor and metric names are illustrative only:
// forge knows none of them.

func telBool(v bool) *bool { return &v }

// telWorkload is an image-only Workload on the cluster: a named-port scrape,
// a JSON log parse and an OTLP trace push. Its collector rule joins forge's
// own NetworkPolicy.
func telWorkload() telemetry.Target {
	return telemetry.Target{
		ServiceName: "zitadel", Scope: telemetry.ScopeProject,
		Source: "workload:zitadel", Attachment: telemetry.AttachWorkload,
		Runtime: telemetry.RuntimeCluster, Cluster: "k3d-demo",
		Namespaces: []string{"demo-prod"},
		Selector:   map[string]string{LabelName: "zitadel"},
		Ports:      map[string]int{"http": 8080},
		Metrics:    []telemetry.MetricsScrape{{PortName: "http", Path: "/debug/metrics", Allow: `projection_events_processed_total`}},
		Logs: &telemetry.LogParse{
			Format:   "json",
			Severity: &telemetry.LogSeverity{Field: "level"},
			Body:     "msg",
		},
		OTLP: &telemetry.OTLPPush{Signals: []string{"traces"}},
	}
}

// telManifests is a raw-manifest system: every metrics knob, a pprof pull, a
// container-scoped JSON parse with rules, and a check.
func telManifests() telemetry.Target {
	return telemetry.Target{
		ServiceName: "temporal", Scope: telemetry.ScopeProject,
		Source: "manifests:temporal", Attachment: telemetry.AttachManifests,
		Runtime: telemetry.RuntimeCluster, Cluster: "k3d-demo",
		Namespaces: []string{"temporal"},
		Selector:   map[string]string{LabelName: "temporal", "app.kubernetes.io/component": "frontend"},
		Container:  "temporal",
		Metrics: []telemetry.MetricsScrape{{
			Port: 9090, Allow: `service_requests|service_latency_(bucket|sum|count)|num_goroutines`,
			Counters: `num_goroutines`, DropLabels: []string{"workflowType"}, SampleLimit: 20000,
		}},
		Profiles: []telemetry.PprofScrape{{Port: 7936, Types: []string{"cpu", "heap"}}},
		Logs: &telemetry.LogParse{
			Format:    "json",
			Timestamp: &telemetry.LogTimestamp{Field: "ts", Layout: "epoch_s"},
			Severity:  &telemetry.LogSeverity{Field: "level"},
			Body:      "msg",
			Rules:     []telemetry.LogSeverityRule{{BodyMatch: `(?i)workflow not found`, FromSeverity: "error", Severity: "warn"}},
		},
		Checks: []telemetry.HealthCheck{{Name: "frontend", Kind: "tcp", Port: 7233}},
	}
}

// telSecured is an https scrape with a bearer token and a CA, platform
// scoped, with an authenticated pprof pull and its own NetworkPolicy.
func telSecured() telemetry.Target {
	return telemetry.Target{
		ServiceName: "openbao", Scope: telemetry.ScopePlatform,
		Source: "observability", Attachment: telemetry.AttachObservability,
		Runtime: telemetry.RuntimeCluster, Cluster: "k3d-demo",
		Namespaces: []string{"openbao"},
		Selector:   map[string]string{LabelName: "openbao"},
		Metrics: []telemetry.MetricsScrape{{
			Port: 8200, Path: "/v1/sys/metrics", Scheme: "https",
			Allow:       `vault_core_(unsealed|active)`,
			BearerToken: &telemetry.SecretMount{Secret: "openbao-metrics", Key: "token"},
			TLS:         &telemetry.TLS{CA: &telemetry.SecretMount{Secret: "openbao-ca", Key: "ca.crt"}},
		}},
		Profiles: []telemetry.PprofScrape{{
			Port: 8200, PathPrefix: "/v1/sys/pprof", Types: []string{"heap"}, Scheme: "https",
			BearerToken: &telemetry.SecretMount{Secret: "openbao-metrics", Key: "token"},
			TLS:         &telemetry.TLS{InsecureSkipVerify: true},
		}},
		NetworkPolicy: telBool(true),
	}
}

// telIsolated monitors the telemetry pipeline itself: isolated, so its
// export never shares the destination's queue.
func telIsolated() telemetry.Target {
	return telemetry.Target{
		ServiceName: "clickhouse", Scope: telemetry.ScopePlatform,
		Source: "observability", Attachment: telemetry.AttachObservability,
		Runtime: telemetry.RuntimeCluster, Cluster: "k3d-demo",
		Namespaces: []string{"observability"},
		Selector:   map[string]string{"app": "clickhouse"},
		Isolated:   true,
		Metrics:    []telemetry.MetricsScrape{{Port: 9363, Allow: `ClickHouseProfileEvents_.*`}},
	}
}

func telemetryTargets() []telemetry.Target {
	return []telemetry.Target{telWorkload(), telManifests(), telSecured(), telIsolated()}
}

func telemetryAgentSpec() ObservabilityAgentSpec {
	s := agentSpec()
	s.Signals.Profiles = true
	targets := telemetryTargets()
	s.Telemetry = &targets
	return s
}

func agentYAML(t *testing.T, s ObservabilityAgentSpec) []byte {
	t.Helper()
	objs, err := RenderObservabilityAgent(s, agentCtx)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	for _, o := range objs {
		b, err := yaml.Marshal(o.Object)
		if err != nil {
			t.Fatal(err)
		}
		buf.WriteString("---\n")
		buf.Write(b)
	}
	return buf.Bytes()
}

// Targets of every kind, goldened as the whole agent (config, mounts, args)
// so every change to how the node agent collects declared telemetry is a
// reviewed diff.
func TestRenderObservabilityAgentWithTelemetryGolden(t *testing.T) {
	got := agentYAML(t, telemetryAgentSpec())
	path := filepath.Join("testdata", "observability_agent_telemetry.golden.yaml")
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

// No declared telemetry renders the agent byte for byte as before: an empty
// list is the same as none, and both are TestRenderObservabilityAgentGolden's
// unchanged golden.
func TestObservabilityAgentWithoutTelemetryIsUnchanged(t *testing.T) {
	for name, spec := range map[string]ObservabilityAgentSpec{
		"observability_agent": agentSpec(),
		"observability_agent_grpc": func() ObservabilityAgentSpec {
			s := agentSpec()
			s.Protocol, s.Endpoint = "grpc", "https://otlp.example.com:443"
			s.Signals.Profiles = true
			s.HeadersSecret = nil
			return s
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			want, err := os.ReadFile(filepath.Join("testdata", name+".golden.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			spec.Telemetry = &[]telemetry.Target{}
			if got := agentYAML(t, spec); !bytes.Equal(want, got) {
				t.Errorf("an empty Telemetry list changed the agent:\n%s", got)
			}
		})
	}
}

// ObservabilityAgentSpec is part of pkg/deploy's API and stays comparable (a
// map key needs it): Telemetry is a pointer for that reason. On the wire it
// is still the KCL record's plain "telemetry" list, both ways.
var _ = map[ObservabilityAgentSpec]bool{}

func TestObservabilityAgentSpecTelemetryIsAJSONList(t *testing.T) {
	targets := telemetryTargets()
	list, err := json.Marshal(targets)
	if err != nil {
		t.Fatal(err)
	}
	var spec ObservabilityAgentSpec
	if err := json.Unmarshal([]byte(`{"telemetry":`+string(list)+`}`), &spec); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(spec.declaredTelemetry(), targets) {
		t.Errorf("decoded telemetry = %+v, want %+v", spec.declaredTelemetry(), targets)
	}
	out, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]json.RawMessage
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(back["telemetry"], list) {
		t.Errorf("encoded telemetry = %s, want the list %s", back["telemetry"], list)
	}
	none, err := json.Marshal(agentSpec())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(none, []byte(`"telemetry"`)) {
		t.Errorf("a spec with no telemetry encodes a telemetry key: %s", none)
	}
}

func pipelineProcessors(t *testing.T, cfg collectorDoc, name string) []string {
	t.Helper()
	p, ok := cfg.Service.Pipelines[name]
	if !ok {
		t.Fatalf("no pipeline %q in %v", name, cfg.Service.Pipelines)
	}
	return p.Processors
}

// The fragment's splice points, each where its comment says it must be.
func TestAgentSplicesTheTelemetryFragment(t *testing.T) {
	raw, err := telemetryAgentSpec().AgentConfigFor(agentCtx)
	if err != nil {
		t.Fatal(err)
	}
	cfg := parseCollector(t, raw)

	if got, want := pipelineProcessors(t, cfg, "logs"), []string{"memory_limiter", "k8sattributes", collector.IdentityProcessor, collector.LogsProcessor, "transform/line_resource", "batch"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("logs processors = %v, want %v", got, want)
	}
	if got, want := pipelineProcessors(t, cfg, "metrics/node"), []string{"memory_limiter", "k8sattributes", collector.IdentityProcessor, "batch"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("metrics/node processors = %v, want %v", got, want)
	}
	// Pushed pipelines share k8sattributes, so they run the identity
	// processor too: it is what deletes the extracted selector labels.
	for _, p := range []string{"traces", "metrics", "profiles"} {
		got := pipelineProcessors(t, cfg, p)
		if i, j := indexOf(got, "k8sattributes"), indexOf(got, collector.IdentityProcessor); i < 0 || j != i+1 {
			t.Errorf("%s processors = %v, want %s right after k8sattributes", p, got, collector.IdentityProcessor)
		}
	}

	ops, _ := cfg.Receivers["filelog"]["operators"].([]any)
	if len(ops) < 2 || ops[0].(map[string]any)["type"] != "container" || ops[1].(map[string]any)["type"] != "copy" {
		t.Fatalf("filelog operators must be container, then the Preserve copy, then logLines: %v", ops)
	}
	if to := ops[1].(map[string]any)["to"]; !strings.Contains(fmt.Sprint(to), collector.OriginalAttribute) {
		t.Errorf("Preserve copies to %v, want %s", to, collector.OriginalAttribute)
	}

	labels := cfg.Processors["k8sattributes"]["extract"].(map[string]any)["labels"].([]any)
	var extracted []string
	for _, l := range labels {
		extracted = append(extracted, fmt.Sprint(l.(map[string]any)["tag_name"]))
	}
	for _, want := range []string{"service.name", collector.LabelAttributePrefix + LabelName, collector.LabelAttributePrefix + "app"} {
		if !contains(extracted, want) {
			t.Errorf("k8sattributes extracts %v, want %s", extracted, want)
		}
	}

	for _, p := range []string{"metrics/telemetry_zitadel", "metrics/telemetry_temporal", "profiles/telemetry_temporal", "metrics/telemetry_openbao", "profiles/telemetry_openbao", "metrics/telemetry_clickhouse"} {
		if _, ok := cfg.Service.Pipelines[p]; !ok {
			t.Errorf("no pipeline %s", p)
		}
	}
}

// An isolated target exports through the destination with a bounded
// in-memory queue that drops on overflow, never the shared persistent one.
func TestAgentIsolatedTelemetryHasABoundedQueue(t *testing.T) {
	raw, err := telemetryAgentSpec().AgentConfigFor(agentCtx)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Exporters map[string]map[string]any `json:"exporters"`
		Service   struct {
			Pipelines map[string]struct {
				Exporters []string `json:"exporters"`
			} `json:"pipelines"`
		} `json:"service"`
	}
	if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatal(err)
	}
	name := "otlphttp/destination_telemetry_isolated"
	if got := doc.Service.Pipelines["metrics/telemetry_clickhouse"].Exporters; strings.Join(got, ",") != name {
		t.Fatalf("isolated target exports to %v, want %s", got, name)
	}
	exp := doc.Exporters[name]
	if exp["endpoint"] != "https://otlp.example.com" || exp["headers"] != "${env:"+agentHeadersEnv+"}" {
		t.Errorf("isolated exporter is not the destination: %v", exp)
	}
	q := exp["sending_queue"].(map[string]any)
	if q["block_on_overflow"] != false || q["queue_size"] != float64(collector.DefaultIsolatedQueueSize) || q["storage"] != nil {
		t.Errorf("isolated queue = %v, want an in-memory %d that drops on overflow", q, collector.DefaultIsolatedQueueSize)
	}
	if got := doc.Service.Pipelines["metrics/telemetry_temporal"].Exporters; strings.Join(got, ",") != "otlphttp/destination" {
		t.Errorf("a shared target exports to %v, want the destination", got)
	}
}

// Every Secret a target reads is mounted, required, at the root the config
// reads it from, projecting only the referenced keys.
func TestAgentMountsTelemetrySecretsRequired(t *testing.T) {
	ds := renderedDaemonSet(t, telemetryAgentSpec())
	mounts := map[string]string{}
	for _, m := range ds.Spec.Template.Spec.Containers[0].VolumeMounts {
		mounts[m.Name] = m.MountPath
	}
	found := map[string][]string{}
	for _, v := range ds.Spec.Template.Spec.Volumes {
		if v.Secret == nil || !strings.HasPrefix(v.Name, "telemetry-secret-") {
			continue
		}
		if v.Secret.Optional != nil && *v.Secret.Optional {
			t.Errorf("volume %s mounts %s optionally: a missing credential file stops the agent", v.Name, v.Secret.SecretName)
		}
		if want := TelemetryMountRoot + "/" + v.Secret.SecretName; mounts[v.Name] != want {
			t.Errorf("secret %s mounted at %q, want %q", v.Secret.SecretName, mounts[v.Name], want)
		}
		for _, it := range v.Secret.Items {
			found[v.Secret.SecretName] = append(found[v.Secret.SecretName], it.Key+"="+it.Path)
		}
	}
	want := map[string][]string{"openbao-ca": {"ca.crt=ca.crt"}, "openbao-metrics": {"token=token"}}
	if fmt.Sprint(found) != fmt.Sprint(want) {
		t.Errorf("telemetry secret volumes = %v, want %v", found, want)
	}
	if !contains(ds.Spec.Template.Spec.Containers[0].Args, "--feature-gates=service.profilesSupport") {
		t.Errorf("profiles need the feature gate: %v", ds.Spec.Template.Spec.Containers[0].Args)
	}
}

// What the node agent cannot collect as declared is a render error naming
// the fix, never a target silently not collected.
func TestAgentRefusesTelemetryItCannotCollect(t *testing.T) {
	address := telemetry.Target{
		ServiceName: "alertmanager", Source: "observability", Attachment: telemetry.AttachObservability,
		Runtime: telemetry.RuntimeCluster, Cluster: "k3d-demo", Address: "alertmanager.observability.svc",
		Metrics: []telemetry.MetricsScrape{{Port: 9093}},
	}
	optional := telSecured()
	optional.Metrics[0].BearerToken.Optional = true
	for name, c := range map[string]struct {
		spec func() ObservabilityAgentSpec
		want string
	}{
		"address pull": {func() ObservabilityAgentSpec {
			s := telemetryAgentSpec()
			s.Telemetry = &[]telemetry.Target{address}
			return s
		}, "no singleton"},
		"profiles off": {func() ObservabilityAgentSpec {
			s := telemetryAgentSpec()
			s.Signals.Profiles = false
			return s
		}, "signals.profiles = True"},
		"metrics off": {func() ObservabilityAgentSpec {
			s := telemetryAgentSpec()
			s.Signals.Metrics = false
			return s
		}, "signals.metrics = False"},
		"logs off": {func() ObservabilityAgentSpec {
			s := telemetryAgentSpec()
			s.Signals.Logs = false
			return s
		}, "signals.logs = False"},
		"optional secret": {func() ObservabilityAgentSpec {
			s := telemetryAgentSpec()
			s.Telemetry = &[]telemetry.Target{optional}
			return s
		}, "Make the reference required"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := RenderObservabilityAgent(c.spec(), agentCtx)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want one naming %q", err, c.want)
			}
		})
	}
}

// A Workload target's collector rule joins the workload's own policy, and
// admits only the collector, on only the pulled port.
func TestWorkloadPolicyAdmitsTheTelemetryCollector(t *testing.T) {
	ws := []v1alpha1.Workload{
		gw("zitadel", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindService, Ports: []v1alpha1.Port{{Name: "http", Port: 8080}}}),
		scaffoldAPI(),
	}
	base := goldenCtx
	base.Network = &EnvNetworkPolicy{TelemetryNamespace: "forge-observability"}
	with := base
	with.Telemetry = &WorkloadTelemetry{Collector: AgentCollectorRef("forge-observability"), Targets: telemetryTargets()}

	policy := func(rctx Context, name string) *networkingv1.NetworkPolicy {
		t.Helper()
		objs, err := RenderWorkloads(ws, v1alpha1.ProfileFull, rctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range objs {
			if o.GetKind() == "NetworkPolicy" && o.GetName() == name+"-ingress" {
				var np networkingv1.NetworkPolicy
				if err := runtime.DefaultUnstructuredConverter.FromUnstructured(o.Object, &np); err != nil {
					t.Fatal(err)
				}
				return &np
			}
		}
		t.Fatalf("no %s-ingress policy", name)
		return nil
	}

	before, after := policy(base, "zitadel"), policy(with, "zitadel")
	if len(after.Spec.Ingress) != len(before.Spec.Ingress)+1 {
		t.Fatalf("ingress rules %d -> %d, want one more", len(before.Spec.Ingress), len(after.Spec.Ingress))
	}
	rule := after.Spec.Ingress[len(after.Spec.Ingress)-1]
	if len(rule.From) != 1 || rule.From[0].NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != "forge-observability" ||
		rule.From[0].PodSelector.MatchLabels[LabelName] != ObservabilityAgentName {
		t.Errorf("rule admits %+v, want only the agent's pods", rule.From)
	}
	if len(rule.Ports) != 1 || rule.Ports[0].Port.IntValue() != 8080 {
		t.Errorf("rule ports = %+v, want only the scrape port 8080", rule.Ports)
	}
	// A workload with no target of its own renders exactly as before.
	if a, b := policy(base, "api"), policy(with, "api"); !reflect.DeepEqual(a.Spec, b.Spec) {
		t.Errorf("api's policy changed:\n%v\n%v", a.Spec, b.Spec)
	}
	// network_policy = False keeps the policy closed to the collector.
	closed := with
	off := telWorkload()
	off.NetworkPolicy = telBool(false)
	closed.Telemetry = &WorkloadTelemetry{Collector: with.Telemetry.Collector, Targets: []telemetry.Target{off}}
	if got := policy(closed, "zitadel"); len(got.Spec.Ingress) != len(before.Spec.Ingress) {
		t.Errorf("network_policy = False still added a rule: %+v", got.Spec.Ingress)
	}
}

// The merged node-agent config, telemetry of every kind included, loads in
// the pinned collector: every component exists in contrib 0.162.0, every
// key is one it reads, every OTTL statement compiles, and every
// credentials file it names is read from where the DaemonSet mounts it.
func TestAgentConfigWithTelemetryValidatesInThePinnedCollector(t *testing.T) {
	testkit.Slow(t, "runs the pinned otelcol-contrib validate (docker or a local binary)")
	col := otelcoltest.Find(t)
	for name, spec := range map[string]ObservabilityAgentSpec{
		"http": telemetryAgentSpec(),
		"grpc-token": func() ObservabilityAgentSpec {
			s := telemetryAgentSpec()
			s.Protocol, s.Endpoint = "grpc", "https://otlp.example.com:443"
			s.HeadersSecret, s.TokenSecret = nil, &ObservabilitySecretKey{Name: "otlp-token", Key: "token"}
			return s
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := spec.AgentConfigFor(agentCtx)
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			// The DaemonSet mounts each Secret under TelemetryMountRoot;
			// here they are files under dir, at the same relative paths.
			root := filepath.Join(dir, "secrets")
			for _, m := range []string{"openbao-metrics/token", "openbao-ca/ca.crt"} {
				p := filepath.Join(root, m)
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			// validate builds every component, and kubeletstats reads the
			// pod's service-account CA when it is built; there is no pod
			// here, so it alone authenticates with none. Nothing it renders
			// depends on the declared telemetry.
			var doc map[string]any
			if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
				t.Fatal(err)
			}
			doc["receivers"].(map[string]any)["kubeletstats"].(map[string]any)["auth_type"] = "none"
			b, err := yaml.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			cfg := strings.ReplaceAll(string(b), TelemetryMountRoot, root)
			path := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(path, []byte(cfg), 0o644); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			args := append([]string{"validate"}, spec.CollectorArgs(path)...)
			// What the DaemonSet's downward API and Secret env supply.
			env := []string{"K8S_NODE_NAME=node-a", "K8S_NODE_IP=10.0.0.1", agentHeadersEnv + `={"authorization":"Bearer x"}`}
			out, err := otelcoltest.Command(ctx, col, dir, fmt.Sprintf("forge-agent-validate-%d", time.Now().UnixNano()), env, args...).CombinedOutput()
			if err != nil {
				t.Fatalf("otelcol validate: %v\n%s\nconfig:\n%s", err, out, cfg)
			}
		})
	}
}
