package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/reliant-labs/forge/internal/otelcoltest"
	"github.com/reliant-labs/forge/pkg/telemetry"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// findCollector is the pinned collector (internal/otelcoltest), shared with
// pkg/deploy's node-agent test.
func findCollector(t *testing.T) otelcoltest.Collector { return otelcoltest.Find(t) }

func writeYAML(t *testing.T, path string, doc any) {
	t.Helper()
	b, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// hostChain is a host collector's filelog operators the way forge's local
// collectors order them: service.name from the file name, then the Preserve
// operator, then a generic JSON stage like pkg/deploy's logLines (time,
// level and msg promoted), which the per-target parse must win over.
func hostChain(preserve map[string]any) []any {
	ops := []any{
		map[string]any{"type": "regex_parser", "regex": `^.*/(?P<service>[^/]+)\.log$`, "parse_from": `attributes["log.file.path"]`},
		map[string]any{"type": "move", "from": "attributes.service", "to": `resource["service.name"]`},
		map[string]any{"type": "remove", "field": `attributes["log.file.path"]`},
	}
	if preserve != nil {
		ops = append(ops, preserve)
	}
	return append(ops,
		map[string]any{
			"type": "json_parser", "if": `body matches "^\\s*\\{"`, "parse_from": "body", "on_error": "send_quiet",
			"timestamp": map[string]any{"parse_from": "attributes.time", "layout_type": "gotime", "layout": "2006-01-02T15:04:05.999999999Z07:00"},
			"severity":  map[string]any{"parse_from": "attributes.level"},
		},
		map[string]any{"type": "remove", "if": "attributes.level != nil", "field": "attributes.level"},
		map[string]any{"type": "move", "if": "attributes.msg != nil", "from": "attributes.msg", "to": "body"},
	)
}

// The merged configs of a cluster agent and a host agent, as a caller
// splices every part of the fragment, pass the pinned collector's own
// `validate`: every component exists in contrib 0.162.0, every key is one it
// reads, and every OTTL statement and receiver_creator rule compiles.
func TestFragmentsValidateInThePinnedCollector(t *testing.T) {
	testkit.Slow(t, "runs the pinned otelcol-contrib validate (docker or a local binary)")
	col := findCollector(t)
	for _, c := range []struct {
		name    string
		targets []telemetry.Target
		opts    Options
		cluster bool
	}{
		{"cluster", clusterTargets(), clusterOptions(), true},
		{"cluster-addresses", clusterTargets(), func() Options { o := clusterOptions(); o.Discovery = DiscoverAddresses; return o }(), true},
		{"host", hostTargets(), hostOptions(), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			// Prometheus stats a credentials file when it loads its config,
			// so the mounted secrets must exist, as they do in a pod.
			c.opts.MountRoot = filepath.Join(dir, "secrets")
			for _, m := range []string{"openbao-metrics/token", "openbao-ca/ca.crt"} {
				p := filepath.Join(c.opts.MountRoot, m)
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			f := mustBuild(t, c.targets, c.opts)
			cfg := baseConfig(f, c.opts, c.cluster)
			if err := f.MergeInto(cfg); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "config.yaml")
			writeYAML(t, path, cfg)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			out, err := otelcoltest.Command(ctx, col, dir, fmt.Sprintf("forge-telemetry-validate-%d", time.Now().UnixNano()), nil,
				"validate", "--config="+path, "--feature-gates=service.profilesSupport").CombinedOutput()
			if err != nil {
				cfgText, _ := os.ReadFile(path)
				t.Fatalf("otelcol validate: %v\n%s\nconfig:\n%s", err, out, cfgText)
			}
		})
	}
}

// baseConfig is the caller's side of a config: its own receivers,
// processors, exporters and pipelines, with the fragment's logs and node
// processors spliced where a caller puts them.
func baseConfig(f Fragment, opts Options, cluster bool) map[string]any {
	exporters := map[string]any{}
	for _, e := range append(append([]string{}, opts.MetricsExporters...), opts.ProfilesExporters...) {
		exporters[e] = map[string]any{"endpoint": "http://127.0.0.1:4318"}
	}
	processors := map[string]any{
		"memory_limiter": map[string]any{"check_interval": "1s", "limit_mib": 200, "spike_limit_mib": 50},
		"batch":          map[string]any{},
	}
	ops := hostChain(f.Logs.Preserve)
	include := "/var/log/forge/*/*.log"
	logsProcs := []string{"memory_limiter"}
	nodeProcs := []string{"memory_limiter"}
	if cluster {
		ops = []any{map[string]any{"type": "container", "id": "container-parser"}}
		if f.Logs.Preserve != nil {
			ops = append(ops, f.Logs.Preserve)
		}
		include = "/var/log/pods/*/*/*.log"
		processors["k8sattributes"] = map[string]any{
			"auth_type": "serviceAccount",
			"extract":   map[string]any{"labels": f.K8sAttributesLabels},
		}
		logsProcs = append(logsProcs, "k8sattributes")
		nodeProcs = append(nodeProcs, "k8sattributes")
	}
	logsProcs = append(append(logsProcs, f.Logs.Processors...), "batch")
	nodeProcs = append(append(nodeProcs, f.NodeMetricsProcessors...), "batch")
	exp := strs(opts.MetricsExporters...)
	return map[string]any{
		"receivers": map[string]any{
			"otlp":    map[string]any{"protocols": map[string]any{"http": map[string]any{"endpoint": "127.0.0.1:4318"}}},
			"filelog": map[string]any{"include": strs(include), "include_file_path": true, "operators": ops},
		},
		"processors": processors,
		"exporters":  exporters,
		"service": map[string]any{"pipelines": map[string]any{
			"logs":         map[string]any{"receivers": strs("filelog"), "processors": strs(logsProcs...), "exporters": exp},
			"metrics/node": map[string]any{"receivers": strs("otlp"), "processors": strs(nodeProcs...), "exporters": exp},
		}},
	}
}

type record struct {
	Body     string
	Severity int
	SevText  string
	TimeNano string
	TraceID  string
	Resource map[string]string
	Attrs    map[string]string
}

// The logs fragment, run in the pinned collector over real lines: each
// target's format, timestamp layout, severity mapping, body, trace context,
// drops and rules take effect, they win over the generic JSON stage, a file
// no target names passes through untouched, and no scratch attribute
// survives. A host chain, so no Kubernetes is needed; the cluster chain
// differs only in how service.name is stamped, which the identity goldens
// pin.
func TestLogsFragmentParsesInThePinnedCollector(t *testing.T) {
	testkit.Slow(t, "runs the pinned otelcol-contrib over log files (docker or a local binary)")
	col := findCollector(t)

	natsT := hostEngine()
	temporal := telemetry.Target{
		ServiceName: "temporal-server", Scope: telemetry.ScopeProject, Source: "hostinfra:temporal",
		Attachment: telemetry.AttachHostInfra, Runtime: telemetry.RuntimeHost, Address: "127.0.0.1",
		Logs: clusterManifests().Logs,
	}
	cnpg := telemetry.Target{
		ServiceName: "postgres", Scope: telemetry.ScopePlatform, Source: "hostinfra:postgres",
		Attachment: telemetry.AttachHostInfra, Runtime: telemetry.RuntimeHost, Address: "127.0.0.1",
		Logs: &telemetry.LogParse{
			Format:    "json",
			Timestamp: &telemetry.LogTimestamp{Field: "ts", Layout: "rfc3339"},
			Severity:  &telemetry.LogSeverity{Field: "record.error_severity"},
			Body:      "record.message",
			Drop:      []string{"logger"},
		},
	}
	mongo := telemetry.Target{
		ServiceName: "mongo", Scope: telemetry.ScopePlatform, Source: "hostinfra:mongo",
		Attachment: telemetry.AttachHostInfra, Runtime: telemetry.RuntimeHost, Address: "127.0.0.1",
		Logs: &telemetry.LogParse{
			Format:    "json",
			Timestamp: &telemetry.LogTimestamp{Field: "t.$date", Layout: "rfc3339"},
			Severity:  &telemetry.LogSeverity{Field: "s", Mapping: map[string][]string{"warn": {"W"}, "error": {"E"}, "info": {"I"}}},
			Body:      "msg",
		},
	}
	certManager := telemetry.Target{
		ServiceName: "cert-manager", Scope: telemetry.ScopePlatform, Source: "hostinfra:cert-manager",
		Attachment: telemetry.AttachHostInfra, Runtime: telemetry.RuntimeHost, Address: "127.0.0.1",
		Logs: &telemetry.LogParse{
			Format:   "json",
			Severity: &telemetry.LogSeverity{Field: "v", Mapping: map[string][]string{"debug": {"2"}}},
			Body:     "message",
		},
	}
	f := mustBuild(t, []telemetry.Target{natsT, hostRenamed(), temporal, cnpg, mongo, certManager}, hostOptions())

	dir := t.TempDir()
	logs := filepath.Join(dir, "logs", "dev")
	if err := os.MkdirAll(logs, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"nats.log":         "[42] 2026/11/20 05:00:00.123456 [WRN] slow consumer detected\n",
		"worker.log":       `level=warning msg="charge retried" trace_id=4bf92f3577b34da6a3ce929d0e0e4736 span_id=00f067aa0ba902b7 invoice=inv_1` + "\n",
		"temporal.log":     `{"level":"error","ts":1763614800.5,"msg":"Workflow execution not found","logging-call-at":"handler.go:42"}` + "\n" + `{"level":"error","ts":1763614801.25,"msg":"persistence failed","error":"context canceled"}` + "\n" + `{"level":"error","ts":1763614802,"msg":"shard lost"}` + "\n",
		"postgres.log":     `{"level":"info","ts":"2026-11-20T05:00:03.5Z","logger":"postgres","record":{"error_severity":"ERROR","message":"duplicate key value"}}` + "\n",
		"mongo.log":        `{"t":{"$date":"2026-11-20T05:00:04.000+00:00"},"s":"W","c":"NETWORK","msg":"slow query"}` + "\n",
		"cert-manager.log": `{"v":2,"message":"certificate cache hit"}` + "\n",
		"other.log":        `{"time":"2026-11-20T05:00:05Z","level":"info","msg":"untouched","keep":"me"}` + "\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(logs, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	recs, output := runLogs(t, col, dir, hostChain(f.Logs.Preserve), f.Processors, f.Logs.Processors, filepath.Join(logs, "*.log"), 9)

	by := func(body string) record {
		t.Helper()
		for _, r := range recs {
			if r.Body == body {
				return r
			}
		}
		t.Fatalf("no record with body %q in %+v\ncollector output:\n%s", body, recs, output)
		return record{}
	}
	check := func(r record, svc string, sev int, sevText, timeNano string) {
		t.Helper()
		if r.Resource["service.name"] != svc {
			t.Errorf("%q: service.name = %q, want %q", r.Body, r.Resource["service.name"], svc)
		}
		if r.Severity != sev || r.SevText != sevText {
			t.Errorf("%q: severity = %d %q, want %d %q", r.Body, r.Severity, r.SevText, sev, sevText)
		}
		if timeNano != "" && r.TimeNano != timeNano {
			t.Errorf("%q: time = %s, want %s", r.Body, r.TimeNano, timeNano)
		}
		for k := range r.Attrs {
			if strings.HasPrefix(k, "forge.telemetry.") {
				t.Errorf("%q: scratch attribute %s survived", r.Body, k)
			}
		}
	}
	nanos := func(s string) string {
		ts, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			t.Fatal(err)
		}
		return fmt.Sprint(ts.UnixNano())
	}

	// regex + strptime + mapping + drop.
	n := by("slow consumer detected")
	check(n, "nats", 13, "WRN", nanos("2026-11-20T05:00:00.123456Z"))
	for _, k := range []string{"pid", "ts", "lvl", "msg"} {
		if _, ok := n.Attrs[k]; ok {
			t.Errorf("nats: consumed or dropped attribute %s survived: %v", k, n.Attrs)
		}
	}
	// logfmt + default mapping + trace context + rename.
	w := by("charge retried")
	check(w, "billing-worker", 13, "warning", "")
	if w.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" || w.Attrs["invoice"] != "inv_1" {
		t.Errorf("worker: trace %q attrs %v", w.TraceID, w.Attrs)
	}
	// json + epoch_s + rules, first match wins, over the generic stage.
	check(by("Workflow execution not found"), "temporal-server", 13, "WARN", "1763614800500000000")
	if _, ok := by("Workflow execution not found").Attrs["logging-call-at"]; ok {
		t.Error("temporal: dropped logging-call-at survived")
	}
	check(by("persistence failed"), "temporal-server", 9, "INFO", "1763614801250000000")
	check(by("shard lost"), "temporal-server", 17, "error", "1763614802000000000")
	// nested severity and body, rfc3339, drop.
	pg := by("duplicate key value")
	check(pg, "postgres", 17, "ERROR", nanos("2026-11-20T05:00:03.5Z"))
	if _, ok := pg.Attrs["logger"]; ok {
		t.Error("postgres: dropped logger survived")
	}
	// a `$` key and a short-code mapping.
	check(by("slow query"), "mongo", 13, "W", nanos("2026-11-20T05:00:04Z"))
	// A numeric JSON field matches the declared string verbosity mapping.
	check(by("certificate cache hit"), "cert-manager", 5, "2", "")
	// No target: the generic stage alone.
	o := by("untouched")
	check(o, "other", 9, "info", nanos("2026-11-20T05:00:05Z"))
	if o.Attrs["keep"] != "me" {
		t.Errorf("other: attributes changed: %v", o.Attrs)
	}
}

// The cluster chain in the pinned collector: identity matches a pod by its
// namespace and the selector labels k8sattributes extracted, stamps
// service.name and the project's resource attributes, deletes the extracted
// labels, and the per-target parse then runs for the declared container
// only. A pod outside the selector keeps what k8sattributes gave it. The
// filelog `add` operators and a resource processor stand in for the
// container parser's path metadata and for k8sattributes, which need a
// kubelet's directory layout and an API server.
func TestClusterIdentityInThePinnedCollector(t *testing.T) {
	testkit.Slow(t, "runs the pinned otelcol-contrib over log files (docker or a local binary)")
	col := findCollector(t)
	f := mustBuild(t, []telemetry.Target{clusterManifests()}, func() Options { o := clusterOptions(); o.EnableProfiles = false; return o }())

	dir := t.TempDir()
	logs := filepath.Join(dir, "pods")
	for _, d := range []string{"frontend", "sidecar", "history"} {
		if err := os.MkdirAll(filepath.Join(logs, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	line := func(msg string) string {
		return `{"level":"error","ts":1763614800,"msg":"` + msg + `","logging-call-at":"x.go:1"}` + "\n"
	}
	for path, body := range map[string]string{
		"frontend/0.log": line("Workflow execution not found"), // the target's container
		"sidecar/0.log":  line("sidecar line"),                 // the target's pod, another container
		"history/0.log":  line("history line"),                 // another component: not selected
	} {
		if err := os.WriteFile(filepath.Join(logs, path), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Path metadata, as the container parser derives it: every file is in
	// namespace temporal; the directory names the container.
	ops := []any{
		map[string]any{"type": "regex_parser", "regex": `^.*/(?P<dir>[^/]+)/0\.log$`, "parse_from": `attributes["log.file.path"]`},
		map[string]any{"type": "add", "field": `resource["k8s.namespace.name"]`, "value": "temporal"},
		map[string]any{"type": "add", "if": `attributes.dir == "sidecar"`, "field": `resource["k8s.container.name"]`, "value": "istio-proxy"},
		map[string]any{"type": "add", "if": `attributes.dir != "sidecar"`, "field": `resource["k8s.container.name"]`, "value": "temporal"},
		map[string]any{"type": "add", "if": `attributes.dir == "history"`, "field": `resource["component"]`, "value": "history"},
		map[string]any{"type": "add", "if": `attributes.dir != "history"`, "field": `resource["component"]`, "value": "frontend"},
		map[string]any{"type": "remove", "field": "attributes.dir"},
		map[string]any{"type": "remove", "field": `attributes["log.file.path"]`},
		f.Logs.Preserve,
		map[string]any{"type": "json_parser", "if": `body matches "^\\s*\\{"`, "parse_from": "body", "severity": map[string]any{"parse_from": "attributes.level"}},
		map[string]any{"type": "remove", "if": "attributes.level != nil", "field": "attributes.level"},
		map[string]any{"type": "move", "if": "attributes.msg != nil", "from": "attributes.msg", "to": "body"},
	}
	// k8sattributes: the selector labels, under the tags K8sAttributesLabels
	// names, and service.name from app.kubernetes.io/name.
	k8s := map[string]any{"transform/k8s": map[string]any{"log_statements": []any{map[string]any{"context": "resource", "statements": strs(
		`set(attributes["service.name"], "k8s-name")`,
		`set(attributes["`+LabelAttributePrefix+`app.kubernetes.io/name"], "temporal")`,
		`set(attributes["`+LabelAttributePrefix+`app.kubernetes.io/component"], attributes["component"])`,
		`delete_key(attributes, "component")`,
	)}}}}
	if len(f.K8sAttributesLabels) != 2 {
		t.Fatalf("K8sAttributesLabels = %v, want the selector's two keys", f.K8sAttributesLabels)
	}
	processors := map[string]any{}
	for k, v := range f.Processors {
		processors[k] = v
	}
	for k, v := range k8s {
		processors[k] = v
	}
	recs, output := runLogs(t, col, dir, ops, processors, append([]string{"transform/k8s"}, f.Logs.Processors...), filepath.Join(logs, "*", "0.log"), 3)

	by := func(body string) record {
		t.Helper()
		for _, r := range recs {
			if r.Body == body {
				return r
			}
		}
		t.Fatalf("no record with body %q in %+v\ncollector output:\n%s", body, recs, output)
		return record{}
	}
	target := by("Workflow execution not found")
	if target.Resource["service.name"] != "temporal" || target.Resource["deployment.environment.name"] != "dev" || target.Resource["service.namespace"] != "acme" {
		t.Errorf("target pod's resource = %v, want service.name temporal and the env's attributes", target.Resource)
	}
	if target.Severity != 13 || target.SevText != "WARN" {
		t.Errorf("target container's line: severity %d %q, want the demotion rule's 13 WARN", target.Severity, target.SevText)
	}
	sidecar := by("sidecar line")
	if sidecar.Resource["service.name"] != "temporal" {
		t.Errorf("another container of the target's pod: service.name = %q, want temporal (identity is per pod)", sidecar.Resource["service.name"])
	}
	if sidecar.Severity != 17 {
		t.Errorf("another container's line was parsed: severity %d, want the generic stage's 17", sidecar.Severity)
	}
	if _, ok := sidecar.Attrs["logging-call-at"]; !ok {
		t.Errorf("another container's line lost its attributes: %v", sidecar.Attrs)
	}
	if other := by("history line"); other.Resource["service.name"] != "k8s-name" || other.Resource["deployment.environment.name"] != "" {
		t.Errorf("a pod outside the selector was stamped: %v", other.Resource)
	}
	for _, r := range recs {
		for k := range r.Resource {
			if strings.HasPrefix(k, LabelAttributePrefix) {
				t.Errorf("%q: extracted label %s was stored", r.Body, k)
			}
		}
		for k := range r.Attrs {
			if strings.HasPrefix(k, "forge.telemetry.") {
				t.Errorf("%q: scratch attribute %s survived", r.Body, k)
			}
		}
	}
}

// runLogs runs a filelog receiver with ops over the files include matches,
// then the processors in order, in the pinned collector, until it has
// exported want records (or two minutes pass). It returns the records and
// the collector's output.
func runLogs(t *testing.T, col otelcoltest.Collector, dir string, ops []any, processors map[string]any, order []string, include string, want int) ([]record, string) {
	t.Helper()
	out := filepath.Join(dir, "out.json")
	pipeline := map[string]any{"receivers": strs("filelog"), "exporters": strs("file")}
	if len(order) > 0 {
		pipeline["processors"] = strs(order...)
	}
	cfg := map[string]any{
		"receivers": map[string]any{"filelog": map[string]any{
			"include": strs(include), "include_file_path": true, "start_at": "beginning", "operators": ops,
		}},
		"processors": processors,
		"exporters":  map[string]any{"file": map[string]any{"path": out}},
		"service":    map[string]any{"pipelines": map[string]any{"logs": pipeline}},
	}
	path := filepath.Join(dir, "config.yaml")
	writeYAML(t, path, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	name := fmt.Sprintf("forge-telemetry-logs-%d", time.Now().UnixNano())
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

	var recs []record
	for deadline := time.Now().Add(2 * time.Minute); time.Now().Before(deadline); time.Sleep(300 * time.Millisecond) {
		if recs = readRecords(t, out); len(recs) >= want {
			break
		}
	}
	stop()
	if len(recs) < want {
		t.Fatalf("collector exported %d records, want %d\ncollector output:\n%s", len(recs), want, stderr.String())
	}
	return recs, stderr.String()
}

func readRecords(t *testing.T, path string) []record {
	t.Helper()
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
	var out []record
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
						TimeUnixNano   string                       `json:"timeUnixNano"`
						SeverityNumber int                          `json:"severityNumber"`
						SeverityText   string                       `json:"severityText"`
						TraceID        string                       `json:"traceId"`
						Body           struct{ StringValue string } `json:"body"`
						Attributes     []kv                         `json:"attributes"`
					} `json:"logRecords"`
				} `json:"scopeLogs"`
			} `json:"resourceLogs"`
		}
		if err := json.Unmarshal([]byte(line), &doc); err != nil {
			return out // a line still being written
		}
		for _, rl := range doc.ResourceLogs {
			for _, sl := range rl.ScopeLogs {
				for _, lr := range sl.LogRecords {
					out = append(out, record{
						Body: lr.Body.StringValue, Severity: lr.SeverityNumber, SevText: lr.SeverityText,
						TimeNano: lr.TimeUnixNano, TraceID: lr.TraceID,
						Resource: kvs(rl.Resource.Attributes), Attrs: kvs(lr.Attributes),
					})
				}
			}
		}
	}
	return out
}
