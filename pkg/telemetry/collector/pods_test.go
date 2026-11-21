package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/otelcoltest"
	"github.com/reliant-labs/forge/pkg/telemetry"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// A pod's pulls never depend on the pod spec declaring the port: a manifest
// may serve on a port no container lists. Metrics address every running pod
// on the scrape port from its IP; checks and profiles match the observer's
// pod endpoints (one per pod), not its declared-port endpoints.
func TestPodPullsIgnoreDeclaredPorts(t *testing.T) {
	f := mustBuild(t, []telemetry.Target{clusterManifests()}, clusterOptions())

	job := f.Receivers["prometheus/telemetry_temporal"].(map[string]any)["config"].(map[string]any)["scrape_configs"].([]any)[0].(map[string]any)
	addresses := map[string]string{}
	for _, r := range job["relabel_configs"].([]any) {
		r := r.(map[string]any)
		src := fmt.Sprint(r["source_labels"])
		if strings.Contains(src, "container_port") {
			t.Errorf("relabel %v selects on a declared container port", r)
		}
		if r["target_label"] == "__address__" {
			addresses[r["regex"].(string)] = r["replacement"].(string)
			if src != "[__meta_kubernetes_pod_ip]" {
				t.Errorf("__address__ is rewritten from %s, want the pod IP", src)
			}
		}
	}
	if want := map[string]string{"([^:]+)": "$${1}:9090", "(.+:.+)": "[$${1}]:9090"}; fmt.Sprint(addresses) != fmt.Sprint(want) {
		t.Errorf("__address__ rewrites = %v, want %v (IPv4 and bracketed IPv6, on the scrape port)", addresses, want)
	}

	for name, port := range map[string]string{"receiver_creator/telemetry_temporal_checks": "7233", "receiver_creator/telemetry_temporal_profiles": "7936"} {
		templates := f.Receivers[name].(map[string]any)["receivers"].(map[string]any)
		if len(templates) == 0 {
			t.Fatalf("%s has no receivers", name)
		}
		for key, tpl := range templates {
			tpl := tpl.(map[string]any)
			rule := tpl["rule"].(string)
			if !strings.HasPrefix(rule, `type == "pod" && `) || strings.Contains(rule, "port") {
				t.Errorf("%s/%s rule %q: want a pod-endpoint rule that names no port", name, key, rule)
			}
			if cfg := fmt.Sprint(tpl["config"]); !strings.Contains(cfg, "`endpoint") || !strings.Contains(cfg, "`:"+port) {
				t.Errorf("%s/%s config %s does not target the pod endpoint on port %s", name, key, cfg, port)
			}
		}
	}
}

func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// The pod relabels in the pinned collector's Prometheus: targets shaped like
// pod discovery's (one per declared container port, one port-free per
// container that declares none, an init container's, a pod that is not
// running), none of which declares the scrape port, yield exactly one
// scrape per running pod, on the scrape port, with only per-pod labels; and
// the exporter's own `namespace` label survives as written while the pod's
// namespace lands on the resource as k8s.namespace.name.
// Static targets stand in for kubernetes_sd_configs, which needs an API
// server; the relabels are the generated ones, unchanged.
func TestPodScrapeIgnoresDeclaredPortsInThePinnedCollector(t *testing.T) {
	testkit.Slow(t, "runs the pinned otelcol-contrib against a local metrics server (docker or a local binary)")
	col := findCollector(t)
	col.HostNetwork = true

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		// Like Flux or cert-manager, the exporter reports on objects in
		// another namespace and labels them with it.
		_, _ = fmt.Fprintln(w, "# TYPE app_ready gauge\napp_ready{namespace=\"flux-system\"} 1")
	}))
	defer srv.Close()
	_, portText, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port := mustAtoi(t, portText)

	target := telemetry.Target{
		ServiceName: "web", Scope: telemetry.ScopePlatform, Source: "manifests:web", Attachment: telemetry.AttachManifests,
		Runtime: telemetry.RuntimeCluster, Cluster: "k3d-hub", Namespaces: []string{"shop"}, Selector: map[string]string{"app": "web"},
		Metrics: []telemetry.MetricsScrape{{Port: port, Path: "/metrics", Scheme: "http", Interval: "10s", Timeout: "10s"}},
	}
	opts := clusterOptions()
	opts.Discovery = DiscoverAll
	f := mustBuild(t, []telemetry.Target{target}, opts)
	recv := f.Receivers["prometheus/telemetry_web"].(map[string]any)
	job := recv["config"].(map[string]any)["scrape_configs"].([]any)[0].(map[string]any)
	delete(job, "kubernetes_sd_configs")
	// Below the declarable floor, so the test needs seconds, not minutes.
	job["scrape_interval"], job["scrape_timeout"] = "1s", "1s"

	sd := func(pod, phase, init, containerPort string) any {
		labels := map[string]any{
			"__meta_kubernetes_pod_ip": "127.0.0.1", "__meta_kubernetes_pod_name": pod, "__meta_kubernetes_namespace": "shop",
			"__meta_kubernetes_pod_phase": phase, "__meta_kubernetes_pod_container_init": init,
			"__meta_kubernetes_pod_container_name": "c-" + containerPort,
		}
		addr := "127.0.0.1"
		if containerPort != "" {
			labels["__meta_kubernetes_pod_container_port_number"] = containerPort
			labels["__meta_kubernetes_pod_container_port_name"] = "p" + containerPort
			addr += ":" + containerPort
		}
		return map[string]any{"targets": strs(addr), "labels": labels}
	}
	job["static_configs"] = []any{
		// pod a: two declared ports and a container with none.
		sd("a", "Running", "false", "1111"), sd("a", "Running", "false", "2222"), sd("a", "Running", "false", ""),
		// pod b: one declared port that is not the scrape port.
		sd("b", "Running", "false", "3333"),
		// pod c: an init container's target alone.
		sd("c", "Running", "true", "4444"),
		// pod d: not running.
		sd("d", "Pending", "false", ""),
	}

	dir := t.TempDir()
	out := filepath.Join(dir, "out.json")
	transform := "transform/telemetry_target/web"
	cfg := map[string]any{
		"receivers":  map[string]any{"prometheus/telemetry_web": recv},
		"processors": map[string]any{transform: f.Processors[transform]},
		"exporters":  map[string]any{"file": map[string]any{"path": out}},
		"service": map[string]any{"pipelines": map[string]any{
			"metrics": map[string]any{"receivers": strs("prometheus/telemetry_web"), "processors": strs(transform), "exporters": strs("file")},
		}},
	}
	path := filepath.Join(dir, "config.yaml")
	writeYAML(t, path, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	name := fmt.Sprintf("forge-telemetry-pods-%d", time.Now().UnixNano())
	cmd := otelcoltest.Command(ctx, col, dir, name, nil, "--config="+path)
	var stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stderr, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		if col.Binary == "" {
			_ = exec.Command("docker", "rm", "-fv", name).Run()
		} else {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}
	t.Cleanup(stop)

	// Every target's loop starts at once, so once a and b have each been
	// scraped twice, c and d would have been scraped too, were they kept.
	var ups []upPoint
	for deadline := time.Now().Add(2 * time.Minute); time.Now().Before(deadline); time.Sleep(300 * time.Millisecond) {
		ups = readPoints(out, "up")
		if n := countBy(ups); n["a"] >= 2 && n["b"] >= 2 {
			break
		}
	}
	stop()
	n := countBy(ups)
	if n["a"] < 2 || n["b"] < 2 {
		t.Fatalf("up samples per pod = %v, want pods a and b scraped\ncollector output:\n%s", n, stderr.String())
	}
	want := fmt.Sprintf("127.0.0.1:%d", port)
	series := map[string]bool{}
	for _, u := range ups {
		if u.Pod == "c" || u.Pod == "d" {
			t.Errorf("pod %s was scraped: init containers and pods that are not running are dropped", u.Pod)
		}
		if u.Instance != want {
			t.Errorf("pod %s scraped at %s, want the pod IP on the scrape port, %s", u.Pod, u.Instance, want)
		}
		if u.Value != 1 {
			t.Errorf("pod %s: up = %v, want 1 (the undeclared port serves)", u.Pod, u.Value)
		}
		keys := make([]string, 0, len(u.Attrs))
		for k := range u.Attrs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if fmt.Sprint(keys) != "[pod]" {
			t.Errorf("pod %s: up attributes %v, want only the per-pod pod (a per-container label splits the pod into several targets; the namespace is the resource's)", u.Pod, u.Attrs)
		}
		if ns := u.Resource["k8s.namespace.name"]; ns != "shop" {
			t.Errorf("pod %s: up resource k8s.namespace.name = %q, want the pod's namespace, shop", u.Pod, ns)
		}
		series[fmt.Sprint(u.Instance, u.Attrs)] = true
	}
	// Identical label sets are one target in Prometheus's scrape pool: pod a's
	// three discovered targets collapse to one series.
	if len(series) != 2 {
		t.Errorf("up series = %v, want one per pod (a and b)", series)
	}

	ready := readPoints(out, "app_ready")
	if len(ready) == 0 {
		t.Fatalf("no app_ready samples\ncollector output:\n%s", stderr.String())
	}
	for _, p := range ready {
		if ns := p.Attrs["namespace"]; ns != "flux-system" {
			t.Errorf("pod %s: app_ready namespace = %q, want the exporter's own flux-system (attributes %v)", p.Pod, ns, p.Attrs)
		}
		for _, k := range []string{"exported_namespace", "forge_k8s_namespace"} {
			if v, ok := p.Attrs[k]; ok {
				t.Errorf("pod %s: app_ready carries %s=%q: the pod's namespace displaced the exporter's, or was not moved", p.Pod, k, v)
			}
		}
		if ns := p.Resource["k8s.namespace.name"]; ns != "shop" {
			t.Errorf("pod %s: app_ready resource k8s.namespace.name = %q, want the pod's namespace, shop", p.Pod, ns)
		}
	}
}

type upPoint struct {
	Pod      string
	Instance string
	Value    float64
	Attrs    map[string]string
	Resource map[string]string
}

func countBy(ups []upPoint) map[string]int {
	n := map[string]int{}
	for _, u := range ups {
		n[u.Pod]++
	}
	return n
}

// readPoints is every gauge sample of metric the file exporter wrote to
// path, with its datapoint and resource attributes.
func readPoints(path, metric string) []upPoint {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	type kv struct {
		Key   string `json:"key"`
		Value struct {
			StringValue string `json:"stringValue"`
		} `json:"value"`
	}
	kvs := func(l []kv) map[string]string {
		m := map[string]string{}
		for _, a := range l {
			m[a.Key] = a.Value.StringValue
		}
		return m
	}
	var out []upPoint
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var doc struct {
			ResourceMetrics []struct {
				Resource struct {
					Attributes []kv `json:"attributes"`
				} `json:"resource"`
				ScopeMetrics []struct {
					Metrics []struct {
						Name  string `json:"name"`
						Gauge struct {
							DataPoints []struct {
								Attributes []kv     `json:"attributes"`
								AsDouble   *float64 `json:"asDouble"`
							} `json:"dataPoints"`
						} `json:"gauge"`
					} `json:"metrics"`
				} `json:"scopeMetrics"`
			} `json:"resourceMetrics"`
		}
		if err := json.Unmarshal([]byte(line), &doc); err != nil {
			return out // a line still being written
		}
		for _, rm := range doc.ResourceMetrics {
			res := kvs(rm.Resource.Attributes)
			for _, sm := range rm.ScopeMetrics {
				for _, m := range sm.Metrics {
					if m.Name != metric {
						continue
					}
					for _, dp := range m.Gauge.DataPoints {
						attrs := kvs(dp.Attributes)
						u := upPoint{Pod: attrs["pod"], Instance: res["service.instance.id"], Attrs: attrs, Resource: res}
						if dp.AsDouble != nil {
							u.Value = *dp.AsDouble
						}
						out = append(out, u)
					}
				}
			}
		}
	}
	return out
}
