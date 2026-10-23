package obsdash_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/obsdash"
	"github.com/reliant-labs/forge/pkg/alerting"
)

// dockerQuerier is an alerting.Querier that runs inside the ClickHouse container
// with the evaluator's own guard settings, as `forge obs alerts check` does.
type dockerQuerier struct{ container string }

func (q dockerQuerier) Query(ctx context.Context, _ string, query string, opts alerting.QueryOptions) ([]alerting.Row, error) {
	if err := alerting.ValidateExpr(query); err != nil {
		return nil, err
	}
	args := []string{"exec", "-i", q.container, "clickhouse-client", "--password", "pw", "--format", "JSONEachRow",
		"--output_format_json_quote_64bit_integers=0", "--query", query}
	for k, v := range opts.Settings() {
		args = append(args, fmt.Sprintf("--%s=%v", k, v))
	}
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%v: %s", err, out)
	}
	var rows []alerting.Row
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var r map[string]any
		if err := dec.Decode(&r); err != nil {
			return nil, err
		}
		rows = append(rows, alerting.Row(r))
	}
	return rows, nil
}

type nopNotifier struct{}

func (nopNotifier) Notify(context.Context, []alerting.Alert) error { return nil }

// TestDefaultAlertsThroughTheEvaluator drives the generated defaults through
// pkg/alerting's real Evaluator: the guard accepts every expr, the readonly and
// row-cap settings are accepted by ClickHouse, and the labels the evaluator
// derives from the result columns are the ones the rules promise.
func TestDefaultAlertsThroughTheEvaluator(t *testing.T) {
	container := os.Getenv("FORGE_ALERT_CLICKHOUSE")
	if testing.Short() || container == "" {
		t.Skip("needs a ClickHouse container; set FORGE_ALERT_CLICKHOUSE=<container>")
	}
	cmd := exec.Command("docker", "exec", "-i", container, "clickhouse-client", "--password", "pw", "--joined_subquery_requires_alias=0", "--multiquery")
	seed, err := os.ReadFile("testdata/clickhouse-seed.sql")
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdin = bytes.NewReader(seed)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("seed: %v\n%s", err, out)
	}
	in := obsdash.AlertInput{FrontendService: "shop-frontend", Services: []obsdash.AlertService{
		{Name: "api", RPCService: "a.v1.S"}, {Name: "quiet", RPCService: "q.v1.Q"}, {Name: "reset", RPCService: "r.v1.R"}, {Name: "delta", RPCService: "d.v1.D"}, {Name: "fast", RPCService: "f.v1.F"},
		{Name: "stopped", RPCService: "s.v1.T"}, {Name: "healthy", RPCService: "h.v1.H"}, {Name: "sparse", RPCService: "p.v1.P"}, {Name: "young", RPCService: "y.v1.Y"}}}
	rules, err := obsdash.ResolveAlertDefaults(in, "dev", nil)
	if err != nil {
		t.Fatal(err)
	}
	ev, err := alerting.NewEvaluator(alerting.EvaluatorDeps{
		Querier: dockerQuerier{container}, Notifier: nopNotifier{}, Store: alerting.NewMemoryStore(),
		Limits: alerting.Limits{MaxExecutionTime: 20 * time.Second, MaxInstancesPerRule: 200},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, r := range rules {
		if obsdash.IsPlatformExpr(r.Expr) || strings.Contains(r.Expr, "{reliant_env_id") {
			continue // TestMetadataAlertsThroughTheEvaluator binds the env id
		}
		spec := alerting.RuleSpec{Alert: r.Alert, Expr: r.Expr, Labels: r.Labels, Annotations: r.Annotations, Source: alerting.SourceDefault}
		if err := spec.Validate(); err != nil {
			t.Fatalf("%s: the evaluator's own validation refuses the default: %v", r.ID, err)
		}
		after, err := ev.EvaluateRule(context.Background(), spec.Rule("t", "dev", nil), nil)
		if err != nil {
			t.Fatalf("%s: %v", r.ID, err)
		}
		for _, inst := range after {
			if inst.Labels["alertname"] != r.Alert || inst.Labels["severity"] != r.Labels["severity"] {
				t.Errorf("%s: instance labels %v lack alertname/severity", r.ID, inst.Labels)
			}
			got[r.ID] = append(got[r.ID], inst.Labels["service"])
		}
		sort.Strings(got[r.ID])
	}
	want := map[string]string{
		"error-rate": "api,delta", "p95-latency": "api", "service-silent": "stopped", "frontend-js-errors": "shop-frontend",
	}
	for id, w := range want {
		if strings.Join(got[id], ",") != w {
			t.Errorf("%s: instances for services %v, want %s", id, got[id], w)
		}
	}
}

// TestMetadataAlertsThroughTheEvaluator drives the metadata defaults through the
// real Evaluator with the env id bound, as the control plane does: the guard
// accepts every expr, the settings are accepted by ClickHouse, a firing instance
// carries the workload label, and the platform markers return no row at all.
func TestMetadataAlertsThroughTheEvaluator(t *testing.T) {
	container := os.Getenv("FORGE_ALERT_CLICKHOUSE")
	if testing.Short() || container == "" {
		t.Skip("needs a ClickHouse container; set FORGE_ALERT_CLICKHOUSE=<container>")
	}
	seed, err := os.ReadFile("testdata/clickhouse-seed-metadata.sql")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("docker", "exec", "-i", container, "clickhouse-client", "--password", "pw", "--joined_subquery_requires_alias=0", "--multiquery")
	cmd.Stdin = bytes.NewReader(seed)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("seed: %v\n%s", err, out)
	}
	rules, err := obsdash.ResolveAlertDefaults(obsdash.AlertInput{Project: "shop", Services: []obsdash.AlertService{{Name: "api", RPCService: "a.v1.S"}}}, "dev", nil)
	if err != nil {
		t.Fatal(err)
	}
	ev, err := alerting.NewEvaluator(alerting.EvaluatorDeps{
		Querier: dockerQuerier{container}, Notifier: nopNotifier{}, Store: alerting.NewMemoryStore(),
		Limits: alerting.Limits{MaxExecutionTime: 20 * time.Second, MaxInstancesPerRule: 200},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, r := range rules {
		spec := alerting.RuleSpec{Alert: r.Alert, Expr: obsdash.BindEnvID(r.Expr, "env-1"), Labels: r.Labels, Annotations: r.Annotations, Source: alerting.SourceDefault}
		if err := spec.Validate(); err != nil {
			t.Fatalf("%s: the evaluator's own validation refuses the bound default: %v", r.ID, err)
		}
		after, err := ev.EvaluateRule(context.Background(), spec.Rule("t", "env-1", nil), nil)
		if err != nil {
			t.Fatalf("%s: %v", r.ID, err)
		}
		for _, inst := range after {
			if inst.Labels["alertname"] != r.Alert || inst.Labels["severity"] != r.Labels["severity"] {
				t.Errorf("%s: instance labels %v lack alertname/severity", r.ID, inst.Labels)
			}
			if r.ID == "crash-loop" || r.ID == "oom-killed" || r.ID == "memory-near-limit" || r.ID == "image-pull-failed" || r.ID == "evicted" {
				got[r.ID] = append(got[r.ID], inst.Labels["workload"])
			} else if inst.Labels["workload"] == "" && obsdash.IsPlatformExpr(r.Expr) {
				got[r.ID] = append(got[r.ID], "PLATFORM")
			}
		}
		sort.Strings(got[r.ID])
	}
	want := map[string]string{
		"crash-loop": "backoff,crashy", "oom-killed": "oomer,oomevent,oomres", "memory-near-limit": "hot",
		"image-pull-failed": "imgfail,pulling", "evicted": "evictee",
	}
	for id, w := range want {
		if strings.Join(got[id], ",") != w {
			t.Errorf("%s: instances for workloads %v, want %s", id, got[id], w)
		}
	}
	if len(got["deploy-failed"]) != 0 || len(got["observability-quota"]) != 0 {
		t.Errorf("a platform marker returned rows from ClickHouse: %v", got)
	}
}
