package telemetry

import (
	"encoding/json"
	"errors"
	"io/fs"
	"reflect"
	"regexp"
	"strings"
	"testing"

	forgekcl "github.com/reliant-labs/forge/kcl"
)

// workloadTarget is a cluster Workload's target as the render emits it:
// before Normalize, with a named port.
func workloadTarget() Target {
	return Target{
		ServiceName: "temporal",
		Source:      "workload:temporal",
		Attachment:  AttachWorkload,
		Runtime:     RuntimeCluster,
		Cluster:     "k3d-acme",
		Namespaces:  []string{"acme-dev"},
		Selector:    map[string]string{"app.kubernetes.io/name": "temporal"},
		Ports:       map[string]int{"grpc": 7233, "metrics": 9090},
		Metrics:     []MetricsScrape{{PortName: "metrics", Allow: "service_.*"}},
		Checks:      []HealthCheck{{PortName: "grpc"}},
		Profiles:    []PprofScrape{{Port: 7936}},
	}
}

func TestDecodeIsStrict(t *testing.T) {
	if got, err := Decode(nil); err != nil || got != nil {
		t.Fatalf("Decode(nil) = %v, %v; want no targets", got, err)
	}
	if got, err := Decode([]byte("null")); err != nil || got != nil {
		t.Fatalf("Decode(null) = %v, %v; want no targets", got, err)
	}
	got, err := Decode([]byte(`[{"service_name":"a","source":"observability","metrics":[{"port":0,"port_name":"m"}]}]`))
	if err != nil || len(got) != 1 || got[0].Metrics[0].PortName != "m" {
		t.Fatalf("Decode = %+v, %v", got, err)
	}
	// A key the contract does not know is a field the render emits and no
	// collector would read: refused, not dropped.
	_, err = Decode([]byte(`[{"service_name":"a","metrics":[{"port":1,"alow":"x"}]}]`))
	if err == nil || !strings.Contains(err.Error(), "alow") {
		t.Fatalf("Decode accepted an unknown key: %v", err)
	}
}

func TestNormalizeResolvesPortsAndDefaults(t *testing.T) {
	in := workloadTarget()
	in.Metrics[0].BearerToken = &SecretMount{Secret: "tok", Key: "token"}
	in.Metrics[0].TLS = &TLS{CA: &SecretMount{Secret: "ca", Key: "ca.crt"}}
	in.OTLP = &OTLPPush{}
	in.Logs = &LogParse{Timestamp: &LogTimestamp{Field: "ts"}}
	before, _ := json.Marshal(in)

	out := Normalize([]Target{in})
	got := out[0]

	if after, _ := json.Marshal(in); string(after) != string(before) {
		t.Errorf("Normalize modified its input")
	}
	m := got.Metrics[0]
	if m.Port != 9090 || m.Path != DefaultMetricsPath || m.Scheme != "http" || m.Interval != "30s" || m.Timeout != "10s" ||
		m.SampleLimit != DefaultSampleLimit || *m.LabelLimit != DefaultLabelLimit || *m.LabelValueLengthLimit != DefaultLabelValueLengthLimit {
		t.Errorf("metrics not resolved/defaulted: %+v", m)
	}
	if m.Params == nil || m.DropLabels == nil {
		t.Errorf("metrics lists must be empty, not nil: %+v", m)
	}
	if m.BearerToken.Path != "tok/token" || m.TLS.CA.Path != "ca/ca.crt" {
		t.Errorf("mount paths = %q, %q", m.BearerToken.Path, m.TLS.CA.Path)
	}
	if c := got.Checks[0]; c.Port != 7233 || c.Kind != "tcp" || c.Path != "/" || c.Interval != DefaultCheckInterval {
		t.Errorf("check not resolved/defaulted: %+v", c)
	}
	if p := got.Profiles[0]; !reflect.DeepEqual(p.Types, []string{"cpu", "heap"}) || p.Interval != "5m" || p.CPUSeconds != 5 || p.PathPrefix != "/debug/pprof" {
		t.Errorf("profile not defaulted: %+v", p)
	}
	if got.OTLP.Protocol != DefaultOTLPProtocol || !reflect.DeepEqual(got.OTLP.Signals, []string{"traces"}) || got.OTLP.Env == nil {
		t.Errorf("otlp not defaulted: %+v", got.OTLP)
	}
	if got.Logs.Format != "auto" || got.Logs.Timestamp.Layout != "rfc3339" || got.Logs.Drop == nil || got.Logs.Rules == nil {
		t.Errorf("logs not defaulted: %+v", got.Logs)
	}
	if got.Scope != ScopeProject {
		t.Errorf("scope = %q", got.Scope)
	}
	if again := Normalize(out); !reflect.DeepEqual(again, out) {
		t.Errorf("Normalize is not idempotent:\n%+v\n%+v", again, out)
	}
	// An unresolvable name stays 0 for Validate to name.
	in.Metrics[0].PortName = "nope"
	if p := Normalize([]Target{in})[0].Metrics[0].Port; p != 0 {
		t.Errorf("unresolved port = %d, want 0", p)
	}
}

func TestNormalizedJSONHasStableKeys(t *testing.T) {
	b, err := json.Marshal(Normalize([]Target{{ServiceName: "a", Source: "observability", Attachment: AttachObservability, Runtime: RuntimeHost, Address: "127.0.0.1"}})[0])
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	for _, k := range []string{"namespaces", "selector", "ports", "metrics", "profiles", "checks"} {
		if v, ok := m[k]; !ok || v == nil {
			t.Errorf("%s is %v: a list or map is always present, empty rather than null, so a KCL consumer can iterate it", k, v)
		}
	}
}

func problemsOf(t *testing.T, err error) []string {
	t.Helper()
	if err == nil {
		return nil
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("Validate returned %T, want *ValidationError", err)
	}
	var out []string
	for _, p := range ve.Problems {
		out = append(out, p.Error())
	}
	return out
}

func requireProblem(t *testing.T, err error, want ...string) {
	t.Helper()
	all := strings.Join(problemsOf(t, err), "\n")
	for _, w := range want {
		if !strings.Contains(all, w) {
			t.Errorf("no problem names %q; got:\n%s", w, all)
		}
	}
}

func TestValidateAcceptsAWellFormedEnv(t *testing.T) {
	ts := Normalize([]Target{
		workloadTarget(),
		{ServiceName: "nats", Source: "hostinfra:nats", Attachment: AttachHostInfra, Runtime: RuntimeHost, Address: "127.0.0.1",
			Ports: map[string]int{"server": 4222, "monitor": 8222}, Metrics: []MetricsScrape{{PortName: "monitor", Path: "/varz"}}},
		{ServiceName: "ch", Scope: ScopePlatform, Source: "manifests:obs", Attachment: AttachManifests, Runtime: RuntimeCluster, Cluster: "k3d-acme",
			Selector: map[string]string{"app": "ch"}, Metrics: []MetricsScrape{{Port: 9363, Scheme: "https", BearerToken: &SecretMount{Secret: "s", Key: "k"}}}},
		{ServiceName: "api", Source: "workload:api", Attachment: AttachWorkload, Runtime: RuntimeHosted,
			Selector: map[string]string{"app.kubernetes.io/name": "api"}, Ports: map[string]int{"metrics": 9100},
			Metrics: []MetricsScrape{{Port: 9100, Allow: "app_.*"}}},
	})
	if err := Validate(ts, Options{Env: "dev", Clusters: []string{"k3d-acme"}}); err != nil {
		t.Fatal(err)
	}
}

func TestValidateUniqueServiceName(t *testing.T) {
	a := workloadTarget()
	b := workloadTarget()
	b.Source = "manifests:temporal"
	b.Attachment = AttachManifests
	requireProblem(t, Validate(Normalize([]Target{a, b}), Options{}), `"temporal" is also the service of workload:temporal`)
}

func TestValidatePortNames(t *testing.T) {
	tg := workloadTarget()
	tg.Metrics[0].PortName = "prom"
	requireProblem(t, Validate(Normalize([]Target{tg}), Options{}), `metrics[0].port: "prom" is not a port of workload:temporal (it declares: grpc, metrics)`)
}

// An unnamed scrape or check is identified by its index, so a name that is
// all digits could take the same job or receiver name: an unnamed first
// entry followed by one named "0" is refused, as is a repeated name.
func TestValidateScrapeAndCheckNames(t *testing.T) {
	tg := workloadTarget()
	tg.Metrics = append(tg.Metrics, MetricsScrape{Name: "0", PortName: "metrics", Allow: "x"})
	tg.Checks = append(tg.Checks, HealthCheck{Name: "0", PortName: "grpc"})
	requireProblem(t, Validate(Normalize([]Target{tg}), Options{}),
		`metrics[1].name: "0" is all digits, which are reserved for an unnamed scrape's index`,
		`checks[1].name: "0" is all digits, which are reserved for an unnamed check's index`)

	tg = workloadTarget()
	tg.Metrics = []MetricsScrape{{Name: "a", PortName: "metrics"}, {Name: "a", PortName: "metrics"}, {Name: "Bad_Name", PortName: "metrics"}}
	tg.Checks = []HealthCheck{{Name: "up", PortName: "grpc"}, {Name: "up", PortName: "grpc"}}
	requireProblem(t, Validate(Normalize([]Target{tg}), Options{}),
		`metrics[1].name: "a" is also the name of scrape 0`,
		`metrics[2].name: "Bad_Name" must be an RFC-1123 label`,
		`checks[1].name: "up" is also the name of check 0`)

	tg = workloadTarget()
	tg.Metrics = append(tg.Metrics, MetricsScrape{Name: "0a", PortName: "metrics"})
	tg.Checks = append(tg.Checks, HealthCheck{Name: "9-x", PortName: "grpc"})
	if err := Validate(Normalize([]Target{tg}), Options{}); err != nil {
		t.Fatalf("a name with a digit and a letter is not an index: %v", err)
	}
}

func TestValidateRE2AndDurations(t *testing.T) {
	tg := workloadTarget()
	tg.Metrics[0].Allow = "service_(requests"
	tg.Metrics[0].Deny = `(?<=x)y` // lookbehind: valid PCRE, not RE2
	tg.Metrics[0].Interval = "5s"
	tg.Logs = &LogParse{Format: "regex", Regex: `^\S+ .*$`, Rules: []LogSeverityRule{{BodyMatch: "[", Severity: "warning"}}}
	requireProblem(t, Validate(Normalize([]Target{tg}), Options{}),
		"metrics[0].allow", "metrics[0].deny", `metrics[0].interval: "5s" is below the 10s floor`,
		"logs.regex: has no named group", "logs.rules[0].body_match", `logs.rules[0].severity: "warning"`)
}

func TestValidateAttachmentRules(t *testing.T) {
	noTarget := Target{ServiceName: "x", Source: "manifests:x", Attachment: AttachManifests, Runtime: RuntimeCluster, Cluster: "c", Checks: []HealthCheck{{Port: 80}}}
	noCluster := Target{ServiceName: "y", Source: "observability", Attachment: AttachObservability, Runtime: RuntimeCluster, Selector: map[string]string{"a": "b"}, Checks: []HealthCheck{{Port: 80}}}
	unknownCluster := Target{ServiceName: "z", Source: "observability", Attachment: AttachObservability, Runtime: RuntimeCluster, Cluster: "elsewhere", Selector: map[string]string{"a": "b"}, Checks: []HealthCheck{{Port: 80}}}
	hostSelector := Target{ServiceName: "w", Source: "observability", Attachment: AttachObservability, Runtime: RuntimeHost, Selector: map[string]string{"a": "b"}, Checks: []HealthCheck{{Port: 80}}}
	external := Target{ServiceName: "v", Source: "workload:v", Attachment: AttachWorkload, Runtime: RuntimeExternal, Address: "x", Checks: []HealthCheck{{Port: 80}}}
	requireProblem(t, Validate(Normalize([]Target{noTarget, noCluster, unknownCluster, hostSelector, external}), Options{Clusters: []string{"c"}}),
		"a manifests target needs a selector (or an address)",
		"telemetry observability (service y): cluster: a cluster target needs the cluster",
		`"elsewhere" is not a cluster this env declares (it declares: c)`,
		"service w): selector: selects pods, which only a cluster has",
		"forge.OnExternal")
}

func TestValidateHostedRules(t *testing.T) {
	tg := Target{
		ServiceName: "api", Source: "workload:api", Attachment: AttachWorkload, Runtime: RuntimeHosted,
		Selector: map[string]string{"app.kubernetes.io/name": "api"}, Ports: map[string]int{"http": 8080},
		Metrics:  []MetricsScrape{{Port: 9100, Scheme: "https", BearerToken: &SecretMount{Secret: "s", Key: "k"}}},
		Profiles: []PprofScrape{{Port: 8080}},
	}
	requireProblem(t, Validate(Normalize([]Target{tg}), Options{}),
		"metrics[0].allow: is required on a hosted env",
		"metrics[0].bearer_token: is not supported on a hosted env",
		"metrics[0].port: 9100 is not a port the workload declares (declared: http)",
		"profiles: are not supported on a hosted env")
}

func TestValidateReportsEveryProblem(t *testing.T) {
	tg := workloadTarget()
	tg.Metrics[0].Allow = "("
	tg.Checks[0].PortName = "nope"
	got := problemsOf(t, Validate(Normalize([]Target{tg}), Options{Env: "dev"}))
	if len(got) != 2 {
		t.Fatalf("got %d problems, want both reported at once: %v", len(got), got)
	}
	if err := Validate(nil, Options{}); err != nil {
		t.Fatalf("no targets: %v", err)
	}
}

func TestSignalsAndPulled(t *testing.T) {
	tg := workloadTarget()
	tg.OTLP = &OTLPPush{Signals: []string{"traces", "logs"}}
	if got := tg.Signals(); !reflect.DeepEqual(got, []string{"checks", "logs", "metrics", "profiles", "traces"}) {
		t.Errorf("Signals = %v", got)
	}
	push := Target{OTLP: &OTLPPush{Signals: []string{"traces"}}, Logs: &LogParse{}}
	if push.Pulled() || !tg.Pulled() {
		t.Errorf("Pulled: push-only = %v, scrape = %v", push.Pulled(), tg.Pulled())
	}
}

// TestKCLVocabularyMatches holds this package's vocabulary equal to the KCL
// schema's (kcl/telemetry.k), read from the module forge embeds: a value one
// side accepts and the other refuses would fail at render, far from either.
func TestKCLVocabularyMatches(t *testing.T) {
	src, err := fs.ReadFile(forgekcl.Module, "telemetry.k")
	if err != nil {
		t.Fatal(err)
	}
	for kclName, goList := range map[string][]string{
		"TELEMETRY_SEVERITIES": Severities,
		"OTLP_REF_KEYS":        OTLPRefKeys,
		"PPROF_TYPES":          PprofTypes,
	} {
		m := regexp.MustCompile(`(?s)` + kclName + `: \[str\] = \[(.*?)\]`).FindSubmatch(src)
		if m == nil {
			t.Errorf("kcl/telemetry.k declares no %s", kclName)
			continue
		}
		var kclList []string
		for _, q := range regexp.MustCompile(`"([^"]+)"`).FindAllSubmatch(m[1], -1) {
			kclList = append(kclList, string(q[1]))
		}
		if !reflect.DeepEqual(kclList, goList) {
			t.Errorf("%s = %v in kcl/telemetry.k, %v here", kclName, kclList, goList)
		}
	}
}

func TestNetworkPolicyIsTriState(t *testing.T) {
	yes, no := true, false
	for _, c := range []struct {
		attach Attachment
		np     *bool
		want   bool
	}{
		{AttachWorkload, nil, true},       // auto: joins the policy forge already renders
		{AttachHostInfra, nil, true},      // auto: forge owns it
		{AttachManifests, nil, false},     // auto: forge cannot know the pods' policies
		{AttachObservability, nil, false}, // auto: same
		{AttachManifests, &yes, true},
		{AttachWorkload, &no, false},
	} {
		if got := (Target{Attachment: c.attach, NetworkPolicy: c.np}).AdmitCollector(); got != c.want {
			t.Errorf("%s with network_policy %v: AdmitCollector = %v, want %v", c.attach, c.np, got, c.want)
		}
	}
	// Unset is absent from the JSON, not false: auto must survive a round trip.
	b, _ := json.Marshal(Normalize([]Target{{ServiceName: "a"}})[0])
	if strings.Contains(string(b), "network_policy") {
		t.Errorf("unset network_policy marshals as %s", b)
	}
	got, err := Decode([]byte(`[{"service_name":"a","network_policy":false}]`))
	if err != nil || got[0].NetworkPolicy == nil || *got[0].NetworkPolicy {
		t.Errorf("explicit false did not survive decode: %+v, %v", got, err)
	}
	in := []Target{{NetworkPolicy: &yes}}
	out := Normalize(in)
	*out[0].NetworkPolicy = false
	if !*in[0].NetworkPolicy {
		t.Error("Normalize aliases NetworkPolicy with its input")
	}
}

func TestWarningsNoteUnadmittedRawTargets(t *testing.T) {
	yes := true
	raw := Target{ServiceName: "ch", Source: "manifests:obs", Attachment: AttachManifests, Runtime: RuntimeCluster, Cluster: "c",
		Selector: map[string]string{"app": "ch"}, Metrics: []MetricsScrape{{Port: 9363}}}
	explicit := raw
	explicit.ServiceName, explicit.NetworkPolicy = "ch2", &yes
	pushOnly := Target{ServiceName: "z", Source: "observability", Attachment: AttachObservability, Runtime: RuntimeCluster, Cluster: "c",
		Selector: map[string]string{"app": "z"}, Logs: &LogParse{Format: "json"}}
	ts := Normalize([]Target{workloadTarget(), raw, explicit, pushOnly})

	w := Warnings(ts)
	if len(w) != 1 || w[0].ServiceName != "ch" || !w[0].Warning || w[0].Field != "network_policy" {
		t.Fatalf("Warnings = %+v, want one note for the pulled Manifests target with network_policy unset", w)
	}
	if !strings.HasPrefix(w[0].Error(), "telemetry note: manifests:obs") {
		t.Errorf("a note must read as one: %s", w[0].Error())
	}
	// A note is never a refusal.
	if err := Validate(ts, Options{Clusters: []string{"c", "k3d-acme"}}); err != nil {
		t.Errorf("Validate refused an env whose only finding is a note: %v", err)
	}
}
