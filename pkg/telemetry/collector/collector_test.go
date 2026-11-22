package collector

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/reliant-labs/forge/pkg/telemetry"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden.yaml from the current render")

// fragmentDoc is f as a document, in a fixed shape for the goldens.
func fragmentDoc(f Fragment) map[string]any {
	return map[string]any{
		"extensions":              f.Extensions,
		"receivers":               f.Receivers,
		"processors":              f.Processors,
		"exporters":               f.Exporters,
		"pipelines":               f.Pipelines,
		"service_extensions":      f.ServiceExtensions,
		"logs_preserve":           f.Logs.Preserve,
		"logs_processors":         f.Logs.Processors,
		"node_metrics_processors": f.NodeMetricsProcessors,
		"k8sattributes_labels":    f.K8sAttributesLabels,
		"warnings":                f.Warnings,
	}
}

func golden(t *testing.T, name string, doc any) {
	t.Helper()
	got, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("testdata", name+".golden.yaml")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run `go test ./pkg/telemetry/collector -update`)", err)
	}
	if string(got) != string(want) {
		t.Errorf("%s differs; run `go test ./pkg/telemetry/collector -update` and review the diff\n--- got\n%s", path, got)
	}
}

func mustBuild(t *testing.T, targets []telemetry.Target, opts Options) Fragment {
	t.Helper()
	f, err := Build(targets, opts)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// One golden per mechanism, so a change to one shows in its own file.
func TestGolden(t *testing.T) {
	noProfiles := func(o Options) Options { o.EnableProfiles = false; return o }
	cases := []struct {
		name    string
		targets []telemetry.Target
		opts    Options
	}{
		{"metrics_cluster_pods", []telemetry.Target{clusterManifests(), clusterWorkload()}, noProfiles(clusterOptions())},
		{"metrics_cluster_secured", []telemetry.Target{clusterSecured()}, noProfiles(clusterOptions())},
		{"metrics_host", []telemetry.Target{hostEngine()}, noProfiles(hostOptions())},
		{"profiles_cluster", []telemetry.Target{clusterManifests(), clusterSecured()}, clusterOptions()},
		{"profiles_host", []telemetry.Target{hostEngine()}, hostOptions()},
		{"logs_cluster", []telemetry.Target{clusterManifests(), clusterSecured()}, noProfiles(clusterOptions())},
		{"logs_host", []telemetry.Target{hostEngine(), hostRenamed()}, noProfiles(hostOptions())},
		{"isolated_addresses", []telemetry.Target{clusterAddress(), clusterManifests()}, func() Options {
			o := clusterOptions()
			o.Discovery = DiscoverAddresses
			return o
		}()},
		{"cluster_all", clusterTargets(), clusterOptions()},
		{"host_all", hostTargets(), hostOptions()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			golden(t, c.name, fragmentDoc(mustBuild(t, c.targets, c.opts)))
		})
	}
}

// Shared log processors must not replace the unconditional identity and counter
// transforms used by a target's metrics and profiles pipelines.
func TestTargetTransformsDoNotCollideWithSharedProcessors(t *testing.T) {
	var targets []telemetry.Target
	for _, service := range []string{"identity", "logs"} {
		target := clusterManifests()
		target.ServiceName = service
		targets = append(targets, target)
	}
	f := mustBuild(t, targets, clusterOptions())
	if len(f.Processors) != 4 {
		t.Fatalf("got %d processors, want two target transforms and two shared processors", len(f.Processors))
	}
	for _, target := range targets {
		service := target.ServiceName
		for _, signal := range []string{"metrics", "profiles"} {
			pipeline := f.Pipelines[signal+"/telemetry_"+service].(map[string]any)
			found := false
			for _, id := range pipeline["processors"].([]any) {
				name := id.(string)
				if name == IdentityProcessor || name == LogsProcessor {
					t.Fatalf("%s pipeline for %s references shared processor %s", signal, service, name)
				}
				processor, ok := f.Processors[name]
				if !ok {
					continue // Caller-owned pre/post processors.
				}
				found = true
				cfg := processor.(map[string]any)
				key := "metric_statements"
				if signal == "profiles" {
					key = "profile_statements"
				}
				groups := cfg[key].([]any)
				identity := groups[0].(map[string]any)["statements"].([]any)[0]
				want := fmt.Sprintf(`set(attributes["service.name"], %q)`, service)
				if identity != want {
					t.Errorf("%s identity = %v, want %s", signal, identity, want)
				}
				if signal == "metrics" && !strings.Contains(fmt.Sprint(groups), "convert_gauge_to_sum") {
					t.Error("target metrics lost counter conversion")
				}
			}
			if !found {
				t.Errorf("%s pipeline for %s has no target transform", signal, service)
			}
		}
	}
	for _, id := range []string{IdentityProcessor, LogsProcessor} {
		cfg := f.Processors[id].(map[string]any)
		if _, ok := cfg["log_statements"]; !ok {
			t.Errorf("shared processor %s lost log processing", id)
		}
	}
}

// A pod scrape never stamps a target label an exporter may also write: the
// pod's namespace goes under NamespaceLabel, which the target's transform
// moves to k8s.namespace.name, so an exporter's own `namespace` survives
// instead of being renamed exported_namespace. An address scrape stamps
// neither and moves nothing.
func TestPodScrapeKeepsExportersNamespaceLabel(t *testing.T) {
	f := mustBuild(t, []telemetry.Target{clusterManifests()}, clusterOptions())
	job := f.Receivers["prometheus/telemetry_temporal"].(map[string]any)["config"].(map[string]any)["scrape_configs"].([]any)[0].(map[string]any)
	if job["honor_labels"] != nil {
		t.Errorf("honor_labels = %v: a scraped pod must not override the target labels", job["honor_labels"])
	}
	targets := map[string]string{}
	for _, r := range job["relabel_configs"].([]any) {
		r := r.(map[string]any)
		if l, ok := r["target_label"].(string); ok && l != "__address__" {
			targets[l] = fmt.Sprint(r["source_labels"])
		}
	}
	if want := map[string]string{"pod": "[__meta_kubernetes_pod_name]", "forge_k8s_namespace": "[__meta_kubernetes_namespace]"}; fmt.Sprint(targets) != fmt.Sprint(want) {
		t.Errorf("target labels = %v, want %v (never `namespace`, which exporters write)", targets, want)
	}

	groups := f.Processors["transform/telemetry_target/temporal"].(map[string]any)["metric_statements"].([]any)
	last := groups[len(groups)-1].(map[string]any)
	want := []any{
		`set(resource.attributes["k8s.namespace.name"], datapoint.attributes["forge_k8s_namespace"]) where datapoint.attributes["forge_k8s_namespace"] != nil`,
		`delete_key(datapoint.attributes, "forge_k8s_namespace")`,
	}
	if last["context"] != "datapoint" || fmt.Sprint(last["statements"]) != fmt.Sprint(want) {
		t.Errorf("last metric group = %v, want the datapoint move %v", last, want)
	}

	host := mustBuild(t, hostTargets(), hostOptions())
	for name, p := range host.Processors {
		if strings.Contains(fmt.Sprint(p), "forge_k8s_namespace") {
			t.Errorf("host processor %s moves forge_k8s_namespace, which only a pod scrape stamps", name)
		}
	}
}

func TestNetworkPoliciesGolden(t *testing.T) {
	ref := CollectorRef{Namespace: "forge-observability", PodLabels: map[string]string{"app.kubernetes.io/name": "otel-agent"}}
	noNS := clusterSecured()
	noNS.ServiceName, noNS.Namespaces = "openbao-anywhere", nil
	off := clusterManifests()
	off.NetworkPolicy = ptr(false)
	addr := clusterAddress()
	addr.NetworkPolicy = ptr(true)
	policies, skipped := NetworkPolicies([]telemetry.Target{
		clusterManifests(), // auto on Manifests: none
		clusterSecured(),   // explicit true: one
		clusterWorkload(),  // auto on a Workload: none here (WorkloadIngressRule)
		off, noNS, addr,
	}, ref)
	rule, ok := WorkloadIngressRule(clusterWorkload(), ref)
	if !ok {
		t.Fatal("WorkloadIngressRule: an auto Workload target gets the collector rule")
	}
	golden(t, "network_policies", map[string]any{"policies": policies, "skipped": skipped, "workload_rule": rule})
}

func TestNetworkPoliciesKeepDistinctServiceIdentities(t *testing.T) {
	services := []string{"foo.bar", "foo_bar", "foo-bar", strings.Repeat("long-", 13) + "a", strings.Repeat("long-", 13) + "b"}
	var targets []telemetry.Target
	serviceBySelector := map[string]string{}
	for i, service := range services {
		selector := fmt.Sprintf("target-%d", i)
		serviceBySelector[selector] = service
		target := clusterSecured()
		target.ServiceName = service
		target.Namespaces = []string{"shared"}
		target.Selector = map[string]string{"app": selector}
		targets = append(targets, target)
	}
	policies, skipped := NetworkPolicies(targets, CollectorRef{Namespace: "obs"})
	if len(skipped) != 0 || len(policies) != len(services) {
		t.Fatalf("got %d policies and skipped %v; want one for each service", len(policies), skipped)
	}
	seen := map[string]string{}
	for _, policy := range policies {
		meta := policy["metadata"].(map[string]any)
		name := meta["name"].(string)
		if len(name) > 63 || notDNS.MatchString(name) || strings.HasSuffix(name, "-") {
			t.Errorf("policy name %q is not a DNS label", name)
		}
		spec := policy["spec"].(map[string]any)
		selector := spec["podSelector"].(map[string]any)["matchLabels"].(map[string]any)["app"].(string)
		service, ok := serviceBySelector[selector]
		if !ok || meta["namespace"] != "shared" {
			t.Fatalf("policy %q has unexpected selector %q or namespace %v", name, selector, meta["namespace"])
		}
		delete(serviceBySelector, selector)
		if old, ok := seen[name]; ok {
			t.Errorf("services %q and %q share NetworkPolicy %q", old, service, name)
		}
		seen[name] = service
	}
}

func TestWorkloadIngressRule(t *testing.T) {
	ref := CollectorRef{Namespace: "obs"}
	w := clusterWorkload()
	w.NetworkPolicy = ptr(false)
	if _, ok := WorkloadIngressRule(w, ref); ok {
		t.Error("network_policy false still adds a rule")
	}
	w.NetworkPolicy = ptr(true)
	if _, ok := WorkloadIngressRule(w, ref); !ok {
		t.Error("network_policy true on a Workload adds no rule to forge's policy")
	}
	if _, ok := WorkloadIngressRule(clusterManifests(), ref); ok {
		t.Error("a Manifests target got a rule for forge's workload policy")
	}
	w.Metrics = nil
	if _, ok := WorkloadIngressRule(w, ref); ok {
		t.Error("a target that pulls nothing got a rule")
	}
}

// Build is a pure function of its input: the same targets in the same order
// render the same bytes.
func TestDeterministic(t *testing.T) {
	render := func() string {
		b, err := yaml.Marshal(fragmentDoc(mustBuild(t, clusterTargets(), clusterOptions())))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	first := render()
	for range 20 {
		if render() != first {
			t.Fatal("two renders of the same targets differ")
		}
	}
}

func TestNoTargetsRendersNothing(t *testing.T) {
	f := mustBuild(t, nil, clusterOptions())
	if len(f.Receivers)+len(f.Processors)+len(f.Pipelines)+len(f.Extensions)+len(f.Exporters) != 0 || f.Logs.Preserve != nil || len(f.Logs.Processors) != 0 {
		t.Errorf("no targets rendered %+v", f)
	}
}

func TestBuildRefuses(t *testing.T) {
	isolatedNoExporter := clusterOptions()
	isolatedNoExporter.Isolated = nil
	noMount := clusterOptions()
	noMount.MountRoot = ""
	noExporters := clusterOptions()
	noExporters.MetricsExporters = nil
	bad := clusterManifests()
	bad.Metrics[0].Allow = "("
	// An unnamed first scrape's job, and an unnamed first check's receiver,
	// end in "0": an entry named "0" beside it would share the name.
	scrapeCollides := clusterAddress()
	scrapeCollides.Metrics = append(scrapeCollides.Metrics, telemetry.MetricsScrape{Name: "0", Port: 9094, Allow: "x"})
	checkCollides := clusterAddress()
	checkCollides.Checks = append(checkCollides.Checks, telemetry.HealthCheck{Name: "0", Kind: "tcp", Port: 9094})
	cases := []struct {
		name    string
		targets []telemetry.Target
		opts    Options
		want    string
	}{
		{"runtime", nil, Options{Runtime: telemetry.RuntimeHosted}, "Options.Runtime"},
		{"discovery", nil, Options{Runtime: telemetry.RuntimeCluster, Discovery: "everything"}, "Options.Discovery"},
		{"isolated", []telemetry.Target{clusterAddress()}, func() Options { o := isolatedNoExporter; o.Discovery = DiscoverAll; return o }(), "Options.Isolated"},
		{"mount root", []telemetry.Target{clusterSecured()}, noMount, "Options.MountRoot"},
		{"exporters", []telemetry.Target{clusterManifests()}, noExporters, "MetricsExporters"},
		{"invalid", []telemetry.Target{bad}, clusterOptions(), "not an RE2 expression"},
		{"scrape named like an index", []telemetry.Target{scrapeCollides}, clusterOptions(), `metrics[1].name: "0" is all digits`},
		{"check named like an index", []telemetry.Target{checkCollides}, clusterOptions(), `checks[1].name: "0" is all digits`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Build(c.targets, c.opts)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("Build error = %v, want one naming %q", err, c.want)
			}
		})
	}
}

// A `$` in a declaration reaches the component as written: the collector's
// resolver reads `$$` as one `$`.
func TestDollarIsEscaped(t *testing.T) {
	tg := hostEngine()
	tg.Metrics[0].Allow = `foo$|bar`
	f := mustBuild(t, []telemetry.Target{tg}, noProfilesHost())
	b, _ := yaml.Marshal(f.Receivers)
	if !strings.Contains(string(b), `up|(?:foo$$|bar)`) {
		t.Errorf("allow regex not escaped for the resolver:\n%s", b)
	}
}

func noProfilesHost() Options { o := hostOptions(); o.EnableProfiles = false; return o }

func TestFieldPath(t *testing.T) {
	cases := map[string][]string{
		"level":                 {"level"},
		"@level":                {"@level"},
		"record.error_severity": {"record", "error_severity"},
		"t.$date":               {"t", "$date"},
		`["k8s.pod"]`:           {"k8s.pod"},
		`a.["b.c"].d`:           {"a", "b.c", "d"},
	}
	for in, want := range cases {
		got, err := fieldPath(in)
		if err != nil || strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("fieldPath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "a..b", "a.", `["x`} {
		if _, err := fieldPath(bad); err == nil {
			t.Errorf("fieldPath(%q) accepted", bad)
		}
	}
}

func TestMergeInto(t *testing.T) {
	f := mustBuild(t, clusterTargets(), clusterOptions())
	cfg := map[string]any{
		"receivers": map[string]any{"otlp": map[string]any{}},
		"service":   map[string]any{"extensions": []any{"health_check"}, "pipelines": map[string]any{"traces": map[string]any{}}},
	}
	if err := f.MergeInto(cfg); err != nil {
		t.Fatal(err)
	}
	svc := cfg["service"].(map[string]any)
	if ext := svc["extensions"].([]any); len(ext) != 1+len(f.ServiceExtensions) || ext[0] != "health_check" {
		t.Errorf("service.extensions = %v", ext)
	}
	if _, ok := svc["pipelines"].(map[string]any)["metrics/telemetry_temporal"]; !ok {
		t.Error("pipelines not merged")
	}
	if err := f.MergeInto(cfg); err == nil || !strings.Contains(err.Error(), "already in the config") {
		t.Errorf("merging twice = %v, want a collision error", err)
	}
}
