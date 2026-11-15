package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/reliant-labs/forge/pkg/observe"
)

type collectorDoc struct {
	Receivers  map[string]map[string]any `json:"receivers"`
	Processors map[string]map[string]any `json:"processors"`
	Service    struct {
		Pipelines map[string]struct {
			Receivers  []string `json:"receivers"`
			Processors []string `json:"processors"`
		} `json:"pipelines"`
	} `json:"service"`
}

func parseCollector(t *testing.T, cfg string) collectorDoc {
	t.Helper()
	var doc collectorDoc
	if err := yaml.Unmarshal([]byte(cfg), &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// moveTargets maps each move operator's source to its destination.
func moveTargets(doc collectorDoc) map[string]string {
	out := map[string]string{}
	ops, _ := doc.Receivers["filelog"]["operators"].([]any)
	for _, o := range ops {
		op, _ := o.(map[string]any)
		if op["type"] == "move" {
			out[fmt.Sprint(op["from"])] = fmt.Sprint(op["to"])
		}
	}
	return out
}

// Both collectors forge renders lift every key of a line's resource object
// into the record's resource. The node agent parks them until k8sattributes
// has run, so a pod label still wins; the forwarder, which has no
// Kubernetes, lifts them into place where the file path left a key unset.
func TestCollectorsLiftTheLineResourceIntoTheResource(t *testing.T) {
	agentCfg, err := agentSpec().AgentConfig("forge-observability")
	if err != nil {
		t.Fatal(err)
	}
	fwdCfg, err := agentSpec().ForwarderConfig()
	if err != nil {
		t.Fatal(err)
	}
	agent, fwd := parseCollector(t, agentCfg), parseCollector(t, fwdCfg)
	for _, k := range observe.LogResourceKeys {
		from := `attributes.resource["` + k + `"]`
		if got, want := moveTargets(agent)[from], `resource["forge.line.`+k+`"]`; got != want {
			t.Errorf("agent: %s moves to %q, want %q", from, got, want)
		}
		if got, want := moveTargets(fwd)[from], `resource["`+k+`"]`; got != want {
			t.Errorf("forwarder: %s moves to %q, want %q", from, got, want)
		}
	}
	logs := agent.Service.Pipelines["logs"].Processors
	if k8s, lift := indexOf(logs, "k8sattributes"), indexOf(logs, "transform/line_resource"); k8s < 0 || lift < k8s {
		t.Errorf("agent logs processors = %v: transform/line_resource must run after k8sattributes, so a pod label wins", logs)
	}
	stmts := fmt.Sprint(agent.Processors["transform/line_resource"])
	for _, k := range observe.LogResourceKeys {
		if !strings.Contains(stmts, `set(attributes["`+k+`"], attributes["forge.line.`+k+`"]) where attributes["`+k+`"] == nil`) {
			t.Errorf("transform/line_resource does not fill %s only where unset: %s", k, stmts)
		}
	}
	if !strings.Contains(stmts, `delete_matching_keys(attributes, "^forge[.]line[.]")`) {
		t.Errorf("transform/line_resource leaves the parked keys behind: %s", stmts)
	}
}

const contribImage = "ghcr.io/open-telemetry/opentelemetry-collector-releases/opentelemetry-collector-contrib:0.162.0@sha256:39923a8e431bd1f57be82411999d389fcfe40857492e4365456d97a4c1f74be6"

// lineFixture is forge's JSON line shape, a stamped one and three that must
// pass through unchanged.
func lineFixture() string {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	return strings.Join([]string{
		`{"time":"` + now + `","level":"INFO","msg":"stamped","resource":{"service.name":"line-name","service.version":"v1.7.58","service.namespace":"reliant","deployment.environment.name":"line-env","service.instance.id":"pod-1"},"procedure":"/x.v1.X/Get"}`,
		`panic: boom`,
		`{"time":"` + now + `","level":"INFO","msg":"unstamped"}`,
		`{"time":"` + now + `","level":"INFO","msg":"app field","resource":"tasks/42"}`,
	}, "\n") + "\n"
}

type exportedRecord struct {
	Body     string
	Resource map[string]string
	Attrs    map[string]string
}

// runCollector runs cfg's filelog receiver (minus its checkpoint store) and
// the given processors in the pinned contrib image over the files under
// hostLogs, mounted at mountAt, and returns every record it exported.
func runCollector(t *testing.T, cfg collectorDoc, processors map[string]any, order []string, hostLogs, mountAt, want string) []exportedRecord {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker unavailable: " + err.Error())
	}
	fl := map[string]any{}
	for k, v := range cfg.Receivers["filelog"] {
		if k != "storage" {
			fl[k] = v
		}
	}
	fl["start_at"] = "beginning"
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	if err := os.Mkdir(out, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(out, 0o777); err != nil { // the collector runs as uid 10001
		t.Fatal(err)
	}
	pipeline := map[string]any{"receivers": []string{"filelog"}, "exporters": []string{"file"}}
	if len(order) > 0 {
		pipeline["processors"] = order
	}
	b, err := yaml.Marshal(map[string]any{
		"receivers":  map[string]any{"filelog": fl},
		"processors": processors,
		"exporters":  map[string]any{"file": map[string]any{"path": "/out/logs.json"}},
		"service":    map[string]any{"pipelines": map[string]any{"logs": pipeline}},
	})
	if err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, b, 0o644); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("forge-line-resource-%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-fv", name).Run() })
	if o, err := exec.CommandContext(context.Background(), "docker", "run", "-d", "--name", name, "--user", "10001:10001",
		"-v", cfgPath+":/etc/otelcol/config.yaml:ro", "-v", hostLogs+":"+mountAt+":ro", "-v", out+":/out",
		contribImage, "--config=/etc/otelcol/config.yaml").CombinedOutput(); err != nil {
		t.Fatalf("docker run: %v\n%s", err, o)
	}
	path := filepath.Join(out, "logs.json")
	for deadline := time.Now().Add(2 * time.Minute); ; time.Sleep(250 * time.Millisecond) {
		if b, _ := os.ReadFile(path); strings.Contains(string(b), want) || time.Now().After(deadline) {
			break
		}
	}
	_ = exec.Command("docker", "stop", "-t", "10", name).Run()
	raw, _ := os.ReadFile(path)
	logs, _ := exec.Command("docker", "logs", name).CombinedOutput()

	var recs []exportedRecord
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var doc struct {
			ResourceLogs []struct {
				Resource struct {
					Attributes []kv `json:"attributes"`
				} `json:"resource"`
				ScopeLogs []struct {
					LogRecords []struct {
						Body       struct{ StringValue string } `json:"body"`
						Attributes []kv                         `json:"attributes"`
					} `json:"logRecords"`
				} `json:"scopeLogs"`
			} `json:"resourceLogs"`
		}
		if err := json.Unmarshal([]byte(line), &doc); err != nil {
			t.Fatalf("exporter line: %v\n%s", err, line)
		}
		for _, rl := range doc.ResourceLogs {
			for _, sl := range rl.ScopeLogs {
				for _, lr := range sl.LogRecords {
					recs = append(recs, exportedRecord{Body: lr.Body.StringValue, Resource: kvs(rl.Resource.Attributes), Attrs: kvs(lr.Attributes)})
				}
			}
		}
	}
	if len(recs) < 4 {
		t.Fatalf("collector exported %d records, want 4:\n%s\ncollector logs:\n%s", len(recs), raw, logs)
	}
	return recs
}

type kv struct {
	Key   string `json:"key"`
	Value struct {
		StringValue string `json:"stringValue"`
	} `json:"value"`
}

func kvs(l []kv) map[string]string {
	out := map[string]string{}
	for _, a := range l {
		out[a.Key] = a.Value.StringValue
	}
	return out
}

func byBody(t *testing.T, recs []exportedRecord, body string) exportedRecord {
	t.Helper()
	for _, r := range recs {
		if r.Body == body {
			return r
		}
	}
	t.Fatalf("no record with body %q in %+v", body, recs)
	return exportedRecord{}
}

// resourceSubset is r's LogResourceKeys entries.
func resourceSubset(r map[string]string) map[string]string {
	out := map[string]string{}
	for _, k := range observe.LogResourceKeys {
		if v, ok := r[k]; ok {
			out[k] = v
		}
	}
	return out
}

// The node agent's filelog chain and transform/line_resource, as rendered,
// in the pinned collector. A resource processor stands in for k8sattributes
// (which needs an API server) and sets the two keys a pod's labels name.
func TestNodeAgentLiftsTheLineResourceInTheCollector(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the collector image in docker; runs in task test")
	}
	cfg := parseCollector(t, mustAgentConfig(t))
	pods := t.TempDir()
	logDir := filepath.Join(pods, "env-a_api-7d7b89c69d-x2k4q_0f0f0f0f-0000-0000-0000-000000000002", "api")
	writeCRI(t, logDir, lineFixture())
	openTree(t, pods)

	k8s := map[string]any{"attributes": []any{
		map[string]any{"key": "service.name", "value": "api", "action": "upsert"},
		map[string]any{"key": "deployment.environment.name", "value": "prod", "action": "upsert"},
	}}
	recs := runCollector(t, cfg, map[string]any{
		"resource/k8s":            k8s,
		"transform/line_resource": cfg.Processors["transform/line_resource"],
	}, []string{"resource/k8s", "transform/line_resource"}, pods, "/var/log/pods", "app field")

	stamped := byBody(t, recs, "stamped")
	want := map[string]string{
		"service.name":                "api",  // the pod label wins
		"deployment.environment.name": "prod", // the pod label wins
		"service.version":             "v1.7.58",
		"service.namespace":           "reliant",
		"service.instance.id":         "pod-1",
	}
	if got := resourceSubset(stamped.Resource); !maps.Equal(got, want) {
		t.Errorf("stamped line's resource = %v, want %v", got, want)
	}
	if stamped.Resource["k8s.pod.name"] != "api-7d7b89c69d-x2k4q" {
		t.Errorf("the container parser's k8s.* resource keys are gone: %v", stamped.Resource)
	}
	assertLiftLeftNoTrace(t, recs)
	for _, body := range []string{"panic: boom", "unstamped", "app field"} {
		if v, ok := byBody(t, recs, body).Resource["service.version"]; ok {
			t.Errorf("%q carries service.version %q; only a stamped line has one", body, v)
		}
	}
}

// The local forwarder: the path names service.name and the env, and wins.
func TestForwarderLiftsTheLineResourceInTheCollector(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the collector image in docker; runs in task test")
	}
	fwd, err := agentSpec().ForwarderConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg := parseCollector(t, fwd)
	logs := t.TempDir()
	writeFile(t, filepath.Join(logs, "dev", "api.log"), lineFixture())
	openTree(t, logs)

	recs := runCollector(t, cfg, map[string]any{}, nil, logs, "/var/log/forge", "app field")
	stamped := byBody(t, recs, "stamped")
	want := map[string]string{
		"service.name":                "api", // the file name
		"deployment.environment.name": "dev", // the directory
		"service.version":             "v1.7.58",
		"service.namespace":           "reliant",
		"service.instance.id":         "pod-1",
	}
	if got := resourceSubset(stamped.Resource); !maps.Equal(got, want) {
		t.Errorf("stamped line's resource = %v, want %v", got, want)
	}
	assertLiftLeftNoTrace(t, recs)
	if got := resourceSubset(byBody(t, recs, "unstamped").Resource); !maps.Equal(got, map[string]string{"service.name": "api", "deployment.environment.name": "dev"}) {
		t.Errorf("an unstamped line's resource = %v, want only what the path names", got)
	}
}

func assertLiftLeftNoTrace(t *testing.T, recs []exportedRecord) {
	t.Helper()
	for _, r := range recs {
		for k := range r.Resource {
			if strings.HasPrefix(k, lineResourcePrefix) {
				t.Errorf("%q: parked key %s reached the exporter", r.Body, k)
			}
		}
		for k := range r.Attrs {
			if k == "resource" || strings.HasPrefix(k, "resource.") {
				if r.Body != "app field" {
					t.Errorf("%q: the line's resource object stayed an attribute (%s)", r.Body, k)
				}
			}
		}
	}
	if got := byBody(t, recs, "app field").Attrs["resource"]; got != "tasks/42" {
		t.Errorf("an app's own string field named resource = %q, want it kept as an attribute", got)
	}
	if got := byBody(t, recs, "stamped").Attrs["procedure"]; got != "/x.v1.X/Get" {
		t.Errorf("the stamped line lost its own attributes: %v", byBody(t, recs, "stamped").Attrs)
	}
}

func mustAgentConfig(t *testing.T) string {
	t.Helper()
	cfg, err := agentSpec().AgentConfig("forge-observability")
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// writeCRI writes lines into dir/0.log in the CRI format the kubelet writes.
func writeCRI(t *testing.T, dir, lines string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var b strings.Builder
	for _, l := range strings.Split(strings.TrimSuffix(lines, "\n"), "\n") {
		b.WriteString(now + " stdout F " + l + "\n")
	}
	writeFile(t, filepath.Join(dir, "0.log"), b.String())
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// openTree makes root and everything under it readable by the collector,
// which runs as uid 10001 (t.TempDir is 0700).
func openTree(t *testing.T, root string) {
	t.Helper()
	if err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Chmod(p, info.Mode().Perm()|0o755)
	}); err != nil {
		t.Fatal(err)
	}
}
