package deploy

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

func agentSpec() ObservabilityAgentSpec {
	return ObservabilityAgentSpec{
		Endpoint:       "https://otlp.example.com",
		Protocol:       "http/protobuf",
		HeadersSecret:  &ObservabilitySecretKey{Name: "otlp-auth", Key: "headers"},
		Signals:        ObservabilitySignals{Traces: true, Metrics: true, Logs: true},
		Image:          "otel/opentelemetry-collector-contrib:0.162.0@sha256:39923a8e431bd1f57be82411999d389fcfe40857492e4365456d97a4c1f74be6",
		MemoryLimitMiB: 256,
	}
}

var agentCtx = ObservabilityContext{Namespace: "forge-observability", PartOf: "demo", Env: "staging"}

// The node agent renders with no cluster: this is the k3d-free test of the
// DaemonSet, goldened so every change to it is a reviewed diff.
func TestRenderObservabilityAgentGolden(t *testing.T) {
	cases := map[string]ObservabilityAgentSpec{
		"observability_agent": agentSpec(),
		"observability_agent_grpc": func() ObservabilityAgentSpec {
			s := agentSpec()
			s.Protocol, s.Endpoint = "grpc", "https://otlp.example.com:443"
			s.Signals.Profiles = true
			s.HeadersSecret = nil
			return s
		}(),
	}
	for name, spec := range cases {
		t.Run(name, func(t *testing.T) {
			objs, err := RenderObservabilityAgent(spec, agentCtx)
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
			path := filepath.Join("testdata", name+".golden.yaml")
			if *updateGolden {
				if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (regenerate with -update)", err)
			}
			if !bytes.Equal(want, buf.Bytes()) {
				t.Errorf("%s differs from the render (-update to accept):\n--- got\n%s", path, buf.String())
			}
		})
	}
}

func renderedDaemonSet(t *testing.T, s ObservabilityAgentSpec) *appsv1.DaemonSet {
	t.Helper()
	objs, err := RenderObservabilityAgent(s, agentCtx)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range objs {
		if o.GetKind() == "DaemonSet" {
			var ds appsv1.DaemonSet
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(o.Object, &ds); err != nil {
				t.Fatal(err)
			}
			return &ds
		}
	}
	t.Fatal("no DaemonSet rendered")
	return nil
}

// What the agent needs, and no more: hostPath on /var/log/pods READ-ONLY, no
// capabilities, a read-only root, and the headers from a Secret rather than
// the manifest.
func TestObservabilityAgentSecurityPosture(t *testing.T) {
	ds := renderedDaemonSet(t, agentSpec())
	pod := ds.Spec.Template.Spec
	c := pod.Containers[0]

	var podLogs *corev1.VolumeMount
	for i := range c.VolumeMounts {
		if c.VolumeMounts[i].MountPath == "/var/log/pods" {
			podLogs = &c.VolumeMounts[i]
		}
	}
	if podLogs == nil || !podLogs.ReadOnly {
		t.Errorf("/var/log/pods must be mounted read-only: %+v", podLogs)
	}
	paths := map[string]bool{}
	for _, v := range pod.Volumes {
		if v.HostPath != nil {
			paths[v.HostPath.Path] = true
		}
	}
	if len(paths) != 2 || !paths["/var/log/pods"] || !paths[agentStorageDir] {
		t.Errorf("hostPath volumes = %v, want exactly /var/log/pods and %s", paths, agentStorageDir)
	}
	sc := c.SecurityContext
	if sc == nil || sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem ||
		sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation ||
		sc.Capabilities == nil || len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" {
		t.Errorf("agent container must drop ALL capabilities, forbid escalation and keep a read-only root: %+v", sc)
	}
	var headers *corev1.EnvVar
	for i := range c.Env {
		if c.Env[i].Name == agentHeadersEnv {
			headers = &c.Env[i]
		}
	}
	if headers == nil || headers.Value != "" || headers.ValueFrom == nil || headers.ValueFrom.SecretKeyRef == nil ||
		headers.ValueFrom.SecretKeyRef.Name != "otlp-auth" || headers.ValueFrom.SecretKeyRef.Key != "headers" {
		t.Errorf("destination headers must come from a secretKeyRef, never inline: %+v", headers)
	}
	if !strings.Contains(c.Image, "0.162.0@sha256:") {
		t.Errorf("the collector image must be pinned >= 0.162 by digest: %s", c.Image)
	}
}

// The agent is NOT a hosted workload: its kinds are outside the Restricted
// allowlist on purpose, and RenderWorkloads under that profile still refuses
// anything beyond it. The platform renders its own agent.
func TestObservabilityAgentIsOutsideTheRestrictedProfile(t *testing.T) {
	objs, err := RenderObservabilityAgent(agentSpec(), agentCtx)
	if err != nil {
		t.Fatal(err)
	}
	outside := map[string]bool{}
	for _, o := range objs {
		if !RestrictedKinds[o.GetKind()] {
			outside[o.GetKind()] = true
		}
	}
	for _, k := range []string{"DaemonSet", "ClusterRole", "ClusterRoleBinding", "Namespace", "ConfigMap"} {
		if !outside[k] {
			t.Errorf("%s is expected outside RestrictedKinds (the agent is never hosted-rendered)", k)
		}
	}
	for _, k := range []string{"DaemonSet", "ClusterRole", "ClusterRoleBinding"} {
		if RestrictedKinds[k] {
			t.Errorf("RestrictedKinds must never allow %s", k)
		}
	}
	// And the Restricted workload path still validates a plain service whose
	// env carries the OTEL contract, with no agent anywhere in the set.
	w := v1alpha1.Workload{}
	w.Name = "api"
	w.Spec = v1alpha1.WorkloadSpec{
		Kind: v1alpha1.KindService, Image: "ghcr.io/acme/api@sha256:" + strings.Repeat("a", 64),
		Ports: []v1alpha1.Port{{Name: "http", Port: 8080, Expose: true}},
		Env: []v1alpha1.EnvVar{
			{Name: "OTEL_SERVICE_NAME", Value: "api"},
			{Name: "OTEL_EXPORTER_OTLP_PROTOCOL", Value: "http/protobuf"},
			{Name: "OTEL_RESOURCE_ATTRIBUTES", Value: "deployment.environment.name=prod,service.namespace=demo"},
		},
	}
	got, err := RenderWorkloads([]v1alpha1.Workload{w}, v1alpha1.ProfileRestricted, Context{Namespace: "env-1"})
	if err != nil {
		t.Fatalf("a hosted service carrying the OTEL_* contract must still render under Restricted: %v", err)
	}
	for _, o := range got {
		if !RestrictedKinds[o.GetKind()] {
			t.Errorf("restricted render produced %s", o.GetKind())
		}
	}
}

// The service is node-local and the DaemonSet reaches every node.
func TestObservabilityAgentServiceIsNodeLocal(t *testing.T) {
	objs, err := RenderObservabilityAgent(agentSpec(), agentCtx)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range objs {
		if o.GetKind() != "Service" {
			continue
		}
		spec := o.Object["spec"].(map[string]any)
		if spec["internalTrafficPolicy"] != "Local" {
			t.Errorf("internalTrafficPolicy = %v, want Local", spec["internalTrafficPolicy"])
		}
		return
	}
	t.Fatal("no Service")
}

// Collection model: apps PUSH. The agent pulls only stdout (filelog) and
// kubelet stats; it scrapes nothing, and k8sattributes only enriches.
func TestAgentConfigCollectsByPushAndEnrichesOnly(t *testing.T) {
	cfg, err := agentSpec().AgentConfig("forge-observability")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Receivers  map[string]any `json:"receivers"`
		Processors map[string]any `json:"processors"`
		Service    struct {
			Pipelines map[string]struct {
				Receivers  []string `json:"receivers"`
				Processors []string `json:"processors"`
				Exporters  []string `json:"exporters"`
			} `json:"pipelines"`
		} `json:"service"`
	}
	if err := yaml.Unmarshal([]byte(cfg), &doc); err != nil {
		t.Fatal(err)
	}
	for r := range doc.Receivers {
		switch r {
		case "otlp", "filelog", "kubeletstats":
		default:
			t.Errorf("receiver %q: the agent pulls only stdout and kubelet stats (never scrape application endpoints by default)", r)
		}
	}
	for _, banned := range []string{"org", "token", "attributes/", "resource/"} {
		for p := range doc.Processors {
			if strings.HasPrefix(p, banned) {
				t.Errorf("processor %q looks like an identity-stamping rule; forge's agent only enriches", p)
			}
		}
	}
	if strings.Contains(cfg, "prometheus") {
		t.Error("the agent config scrapes Prometheus; scraping is a per-namespace opt-in, not a default")
	}
	logs := doc.Service.Pipelines["logs"]
	if len(logs.Receivers) != 1 || logs.Receivers[0] != "filelog" {
		t.Errorf("logs pipeline receivers = %v, want only filelog (one log path)", logs.Receivers)
	}
	if m := doc.Service.Pipelines["metrics/node"]; len(m.Receivers) != 1 || m.Receivers[0] != "kubeletstats" {
		t.Errorf("kubelet stats get their own pipeline, got %v", m.Receivers)
	}
	if !contains(doc.Service.Pipelines["traces"].Receivers, "otlp") || contains(doc.Service.Pipelines["logs"].Receivers, "otlp") {
		t.Error("traces come over OTLP; logs must NOT also come over OTLP (one shipping path)")
	}
	if !strings.Contains(cfg, "exclude:") || !strings.Contains(cfg, "/var/log/pods/forge-observability_*") {
		t.Error("the agent must not tail its own logs")
	}
	for _, p := range doc.Service.Pipelines {
		if !contains(p.Exporters, "otlphttp/destination") {
			t.Errorf("pipeline exports to %v, want the destination", p.Exporters)
		}
	}
	if strings.Contains(cfg, "Bearer") || strings.Contains(cfg, "authorization") {
		t.Error("the config must reference the headers by env, never inline them")
	}
	if !strings.Contains(cfg, "${env:"+agentHeadersEnv+"}") {
		t.Error("destination headers must be read from the environment")
	}
}

func TestAgentConfigSignals(t *testing.T) {
	s := agentSpec()
	s.Signals = ObservabilitySignals{Traces: true, Profiles: true}
	cfg, err := s.AgentConfig("forge-observability")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"traces:", "profiles:"} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config missing %q:\n%s", want, cfg)
		}
	}
	for _, off := range []string{"\n    logs:", "\n    metrics:"} {
		if strings.Contains(cfg, off) {
			t.Errorf("a disabled signal still has a pipeline (%q)", strings.TrimSpace(off))
		}
	}
	if args := s.CollectorArgs("/x"); len(args) != 2 || args[1] != "--feature-gates=service.profilesSupport" {
		t.Errorf("profiles need the feature gate: %v", args)
	}
}

// A collector that samples traces decides per node, so a trace that crosses
// nodes is kept in fragments. The ratio is head-sampled in the SDK instead
// (OTEL_TRACES_SAMPLER*, rendered by KCL); neither the agent nor the local
// forwarder samples.
func TestCollectorsNeverSampleTraces(t *testing.T) {
	a, err := agentSpec().AgentConfig("forge-observability")
	if err != nil {
		t.Fatal(err)
	}
	f, err := agentSpec().ForwarderConfig()
	if err != nil {
		t.Fatal(err)
	}
	for name, cfg := range map[string]string{"agent": a, "forwarder": f} {
		for _, banned := range []string{"tail_sampling", "probabilistic_sampler", "sampling_percentage", "status_code"} {
			if strings.Contains(cfg, banned) {
				t.Errorf("%s config samples traces (%q): a per-collector decision splits a trace across nodes", name, banned)
			}
		}
	}
}

func TestForwarderConfigTailsHostLogsOnly(t *testing.T) {
	cfg, err := agentSpec().ForwarderConfig()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"/var/log/forge/*/*.log", "frontend_*.log", "deployment.environment.name", "${env:FORGE_OTLP_HEADERS}"} {
		if !strings.Contains(cfg, want) {
			t.Errorf("forwarder config missing %q", want)
		}
	}
	for _, banned := range []string{"k8sattributes", "kubeletstats", "file_storage"} {
		if strings.Contains(cfg, banned) {
			t.Errorf("the local forwarder has no Kubernetes or host dir to use %q", banned)
		}
	}
}

func TestObservabilityAgentSpecValidation(t *testing.T) {
	for name, mut := range map[string]func(*ObservabilityAgentSpec){
		"bad endpoint": func(s *ObservabilityAgentSpec) { s.Endpoint = "otlp.example.com" },
		"bad protocol": func(s *ObservabilityAgentSpec) { s.Protocol = "thrift" },
		"empty secret": func(s *ObservabilityAgentSpec) { s.HeadersSecret = &ObservabilitySecretKey{Name: "x"} },
	} {
		t.Run(name, func(t *testing.T) {
			s := agentSpec()
			mut(&s)
			if _, err := RenderObservabilityAgent(s, agentCtx); err == nil {
				t.Fatal("expected a refusal")
			}
		})
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// The kata attribution finding: on a pod-cache miss k8sattributes takes the
// namespace for its label extraction from the client's own k8s.namespace.name
// and never overwrites an attribute the client set. So client-supplied
// identity is deleted BEFORE k8sattributes on every pipeline a client can
// reach, and NOT on the pipelines the node feeds itself (their k8s.*
// attributes come from the node and are what the enrichment trusts).
func TestAgentStripsClientIdentityBeforeEnrichment(t *testing.T) {
	s := agentSpec()
	s.Signals.Profiles = true
	cfg, err := s.AgentConfig("forge-observability")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Processors map[string]any `json:"processors"`
		Service    struct {
			Pipelines map[string]struct {
				Receivers  []string `json:"receivers"`
				Processors []string `json:"processors"`
			} `json:"pipelines"`
		} `json:"service"`
	}
	if err := yaml.Unmarshal([]byte(cfg), &doc); err != nil {
		t.Fatal(err)
	}
	const strip, enrich = "transform/strip_client_identity", "k8sattributes"
	if _, ok := doc.Processors[strip]; !ok {
		t.Fatal("no client-identity strip processor")
	}
	for name, p := range doc.Service.Pipelines {
		fromClient := contains(p.Receivers, "otlp")
		si, ei := indexOf(p.Processors, strip), indexOf(p.Processors, enrich)
		switch {
		case fromClient && (si < 0 || ei < 0 || si > ei):
			t.Errorf("pipeline %s takes OTLP from clients but does not strip identity before k8sattributes: %v", name, p.Processors)
		case !fromClient && si >= 0:
			t.Errorf("pipeline %s is node-derived; stripping would delete the trustworthy k8s.* attributes: %v", name, p.Processors)
		}
	}
	for _, key := range []string{"k8s.namespace.name", "k8s.pod.uid", "reliant.org.id"} {
		if !regexp.MustCompile(ClientIdentityKeyPattern).MatchString(key) {
			t.Errorf("%s is not stripped", key)
		}
	}
	for _, key := range []string{"service.name", "service.version", "deployment.environment.name"} {
		if regexp.MustCompile(ClientIdentityKeyPattern).MatchString(key) {
			t.Errorf("%s belongs to the app and must be kept", key)
		}
	}
}

// File-scraped logs carry no SDK resource, so service.version has to come from
// the pod's app.kubernetes.io/version label, on the node-derived and the pushed
// pipelines alike. k8sattributes only fills attributes that are unset, which is
// what keeps an SDK's own service.version authoritative.
func TestAgentConfigExtractsServiceVersionFromThePodLabel(t *testing.T) {
	s := agentSpec()
	cfg, err := s.AgentConfig("forge-observability")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Processors map[string]struct {
			Extract struct {
				Labels []struct {
					TagName string `json:"tag_name"`
					Key     string `json:"key"`
					From    string `json:"from"`
				} `json:"labels"`
			} `json:"extract"`
		} `json:"processors"`
		Service struct {
			Pipelines map[string]struct {
				Processors []string `json:"processors"`
			} `json:"pipelines"`
		} `json:"service"`
	}
	if err := yaml.Unmarshal([]byte(cfg), &doc); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, l := range doc.Processors["k8sattributes"].Extract.Labels {
		if l.TagName == "service.version" && l.Key == LabelVersion && l.From == "pod" {
			found = true
		}
	}
	if !found {
		t.Errorf("k8sattributes does not map pod label %s to service.version", LabelVersion)
	}
	if !contains(doc.Service.Pipelines["logs"].Processors, "k8sattributes") {
		t.Errorf("the logs pipeline does not enrich with k8sattributes: %v", doc.Service.Pipelines["logs"].Processors)
	}
}

func indexOf(l []string, s string) int {
	for i, x := range l {
		if x == s {
			return i
		}
	}
	return -1
}

func tokenAgentSpec() ObservabilityAgentSpec {
	s := agentSpec()
	s.HeadersSecret = nil
	s.TokenSecret = &ObservabilitySecretKey{Name: "otlp-token", Key: "token"}
	return s
}

// A renewable token reaches the destination through bearertokenauth reading a
// file, not an env var read once at start.
func TestAgentConfigReadsARenewableTokenFromAFile(t *testing.T) {
	cfg, err := tokenAgentSpec().AgentConfig("forge-observability")
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(cfg), &parsed); err != nil {
		t.Fatal(err)
	}
	ext := parsed["extensions"].(map[string]any)["bearertokenauth"].(map[string]any)
	if ext["filename"] != "/etc/otel-auth/token" {
		t.Errorf("bearertokenauth must read the mounted file, got %v", ext)
	}
	if retry := ext["retry_on_failure"].(map[string]any); retry["enabled"] != true {
		t.Errorf("the agent must outlive a not-yet-created Secret: %v", retry)
	}
	exp := parsed["exporters"].(map[string]any)["otlphttp/destination"].(map[string]any)
	if exp["auth"].(map[string]any)["authenticator"] != "bearertokenauth" {
		t.Errorf("the exporter must authenticate through the extension: %v", exp["auth"])
	}
	if _, ok := exp["headers"]; ok {
		t.Error("a token secret must not also render a headers env reference")
	}
	exts := parsed["service"].(map[string]any)["extensions"].([]any)
	found := false
	for _, e := range exts {
		found = found || e == "bearertokenauth"
	}
	if !found {
		t.Errorf("the extension must be enabled under service.extensions: %v", exts)
	}
}

// The token Secret is a whole-directory volume, which is the only form the
// kubelet refreshes in place; a subPath mount would pin the first value for the
// pod's lifetime and silently defeat renewal.
func TestAgentMountsTheTokenSecretWithoutSubPath(t *testing.T) {
	objs, err := RenderObservabilityAgent(tokenAgentSpec(), agentCtx)
	if err != nil {
		t.Fatal(err)
	}
	var ds appsv1.DaemonSet
	for _, o := range objs {
		if o.GetKind() == "DaemonSet" {
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(o.Object, &ds); err != nil {
				t.Fatal(err)
			}
		}
	}
	c := ds.Spec.Template.Spec.Containers[0]
	var mount *corev1.VolumeMount
	for i := range c.VolumeMounts {
		if c.VolumeMounts[i].Name == "otlp-token" {
			mount = &c.VolumeMounts[i]
		}
	}
	if mount == nil || mount.MountPath != "/etc/otel-auth" || !mount.ReadOnly || mount.SubPath != "" {
		t.Fatalf("token mount = %+v, want read-only whole-directory /etc/otel-auth", mount)
	}
	var vol *corev1.Volume
	for i, v := range ds.Spec.Template.Spec.Volumes {
		if v.Name == "otlp-token" {
			vol = &ds.Spec.Template.Spec.Volumes[i]
		}
	}
	if vol == nil || vol.Secret == nil || vol.Secret.SecretName != "otlp-token" {
		t.Fatalf("token volume = %+v", vol)
	}
	if vol.Secret.Optional == nil || !*vol.Secret.Optional {
		t.Error("the volume must be optional so the agent starts before the Secret exists")
	}
	if len(vol.Secret.Items) != 1 || vol.Secret.Items[0].Key != "token" || vol.Secret.Items[0].Path != "token" {
		t.Errorf("the declared key must surface as /etc/otel-auth/token: %+v", vol.Secret.Items)
	}
	for _, e := range c.Env {
		if e.Name == "FORGE_OTLP_HEADERS" {
			t.Error("no headers env var may be rendered for a token secret")
		}
	}
}

func TestAgentWithoutATokenSecretMountsNothingExtra(t *testing.T) {
	objs, err := RenderObservabilityAgent(agentSpec(), agentCtx)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range objs {
		if o.GetKind() != "DaemonSet" {
			continue
		}
		var ds appsv1.DaemonSet
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(o.Object, &ds); err != nil {
			t.Fatal(err)
		}
		for _, v := range ds.Spec.Template.Spec.Volumes {
			if v.Name == "otlp-token" {
				t.Error("an agent with a headers secret must not mount a token volume")
			}
		}
	}
	cfg, _ := agentSpec().AgentConfig("forge-observability")
	if strings.Contains(cfg, "bearertokenauth") {
		t.Error("the headers form must not render the bearer extension")
	}
}

func TestTokenSecretValidation(t *testing.T) {
	both := agentSpec()
	both.TokenSecret = &ObservabilitySecretKey{Name: "t", Key: "k"}
	if err := both.validate(); err == nil || !strings.Contains(err.Error(), "declare one") {
		t.Errorf("both credentials must be refused, got %v", err)
	}
	empty := tokenAgentSpec()
	empty.TokenSecret = &ObservabilitySecretKey{Name: "x"}
	if err := empty.validate(); err == nil {
		t.Error("a token secret without a key must be refused")
	}
	if _, err := tokenAgentSpec().ForwarderConfig(); err == nil {
		t.Error("the local forwarder has no mounted file: a token secret must be refused there")
	}
}
