package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/obsdash"
	"github.com/reliant-labs/forge/pkg/release"
)

// writeAlertDefaults writes what generate writes for in: the defaults file,
// and a ServicePart per service carrying its Alert.
func writeAlertDefaults(t *testing.T, in obsdash.AlertInput) string {
	t.Helper()
	dir := t.TempDir()
	b, err := obsdash.MarshalAlertDefaults(in)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{obsdash.AlertDefaultsFile: b}
	project, err := json.Marshal(obsdash.ProjectPart{Version: obsdash.PartsVersion, Project: in.Project})
	if err != nil {
		t.Fatal(err)
	}
	files[obsdash.ProjectSpecFile] = project
	for _, s := range in.Services {
		part, err := json.Marshal(obsdash.ServicePart{Version: obsdash.PartsVersion, Service: s.Name, Alert: s})
		if err != nil {
			t.Fatal(err)
		}
		files[obsdash.ServiceSpecFile(s.Name)] = part
	}
	for rel, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func alertProject() obsdash.AlertInput {
	return obsdash.AlertInput{
		Project:         "shop",
		Services:        []obsdash.AlertService{{Name: "orders", RPCService: "orders.v1.OrdersService"}},
		FrontendService: "shop-frontend",
	}
}

func obsEntities(a *AlertsEntity) *KCLEntities {
	return &KCLEntities{Observability: &ObservabilityEntity{Enabled: true, Alerts: a}}
}

// The render contract KCL emits decodes into the entity, key for key.
func TestAlertsEntity_DecodesTheRenderContract(t *testing.T) {
	doc := `{"output": {"observability": {"enabled": true, "signals": {}, "alerts": {
		"rules": [{"alert": "A", "expr": "SELECT 1 AS value", "for": "10m", "interval": "1m", "keep_firing_for": "5m",
			"labels": {"severity": "critical"}, "annotations": {"summary": "s"}, "enabled": true}],
		"defaults": {"error-rate": {"enabled": false, "labels": {}, "for": null, "params": {"threshold": 0.1}}},
		"routes": [{"match": {"severity": "critical"}, "receiver": "oncall", "continue": true}, {"match": {}, "receiver": "slack", "continue": false}]}}}}`
	e, err := parseKCLEntities([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	a := e.Observability.Alerts
	if a == nil || len(a.Rules) != 1 || a.Rules[0].For != "10m" || a.Rules[0].KeepFiringFor != "5m" || a.Rules[0].Labels["severity"] != "critical" {
		t.Fatalf("rules decoded as %+v", a)
	}
	if d := a.Defaults["error-rate"]; d.Enabled || d.Params["threshold"] != 0.1 {
		t.Errorf("defaults decoded as %+v", a.Defaults)
	}
	if len(a.Routes) != 2 || !a.Routes[0].Continue || a.Routes[0].Receiver != "oncall" {
		t.Errorf("routes decoded as %+v", a.Routes)
	}
}

func TestResolveShapeAlerts_UserRulesPlusEnabledDefaults(t *testing.T) {
	dir := writeAlertDefaults(t, alertProject())
	got, routes, err := resolveShapeAlerts(dir, "dev", obsEntities(&AlertsEntity{
		Rules: []AlertRuleEntity{
			{Alert: "OrdersBacklog", Expr: "SELECT 1 AS value", For: "5m", Interval: "1m", Labels: map[string]string{"severity": "warning"}, Enabled: true},
			{Alert: "Off", Expr: "SELECT 1 AS value", Labels: map[string]string{"severity": "info"}, Enabled: false},
		},
		Defaults: map[string]AlertDefaultEntity{
			"service-silent": {Enabled: false},
			"p95-latency":    {Enabled: true, Labels: map[string]string{"severity": "critical"}, Params: map[string]float64{"threshold_seconds": 3}},
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]release.ShapeAlert{}
	for _, a := range got {
		by[a.Alert] = a
	}
	if len(got) != 16 {
		t.Fatalf("got %d alerts %v, want OrdersBacklog + error-rate + p95 + frontend errors + LCP + 5 metadata + 3 issue markers + DeployFailed + 2 quota levels (silent disabled, Off dropped)", len(got), by)
	}
	if by["OrdersBacklog"].Source != release.AlertSourceUser || by["ServiceErrorRateHigh"].Source != release.AlertSourceDefault || by["ServiceErrorRateHigh"].DefaultID != "error-rate" {
		t.Errorf("sources wrong: %+v", by)
	}
	if _, ok := by["ServiceSilent"]; ok {
		t.Error("a disabled default is in the shape")
	}
	if _, ok := by["Off"]; ok {
		t.Error("a disabled user rule is in the shape")
	}
	if p := by["ServiceLatencyP95High"]; p.Labels["severity"] != "critical" || !strings.Contains(p.Expr, "value > 3") {
		t.Errorf("tuning not applied: %+v", p)
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].Alert > got[i].Alert {
			t.Errorf("alerts not sorted: %v", got)
		}
	}
	// No routes declared: in-app, then Slack. Never a pager.
	if len(routes) != 2 || routes[0].Receiver != release.AlertReceiverInApp || !routes[0].Continue || routes[1].Receiver != release.AlertReceiverSlack || len(routes[1].Match) != 0 {
		t.Errorf("default routes = %+v", routes)
	}
}

func TestResolveShapeAlerts_DeclaredRoutesReplaceTheDefaults(t *testing.T) {
	dir := writeAlertDefaults(t, alertProject())
	_, routes, err := resolveShapeAlerts(dir, "prod", obsEntities(&AlertsEntity{
		Routes: []AlertRouteEntity{{Match: map[string]string{"severity": "critical"}, Receiver: "pagerduty-oncall"}, {Receiver: "slack"}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 2 || routes[0].Receiver != "pagerduty-oncall" || routes[1].Receiver != "slack" {
		t.Errorf("routes = %+v", routes)
	}
}

func TestResolveShapeAlerts_DefaultsAreScopedToTheEnv(t *testing.T) {
	dir := writeAlertDefaults(t, alertProject())
	got, _, err := resolveShapeAlerts(dir, "staging", obsEntities(nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 16 {
		t.Fatalf("an env that declares nothing still gets the 16 defaults, got %d", len(got))
	}
	for _, a := range got {
		if strings.Contains(a.Expr, "'dev'") {
			t.Errorf("%s names another env: %s", a.Alert, a.Expr)
		}
		// Telemetry rules name the env; metadata and platform rules are scoped by the id the control plane binds.
		if a.DefaultID == "error-rate" && !strings.Contains(a.Expr, "'staging'") {
			t.Errorf("%s is not scoped to staging: %s", a.Alert, a.Expr)
		}
	}
}

func TestResolveShapeAlerts_NothingToEvaluateRecordsNothing(t *testing.T) {
	dir := writeAlertDefaults(t, alertProject())
	for name, e := range map[string]*KCLEntities{
		"no observability":  {},
		"observability off": {Observability: &ObservabilityEntity{Enabled: false}},
		"nil entities":      nil,
	} {
		got, routes, err := resolveShapeAlerts(dir, "dev", e)
		if err != nil || got != nil || routes != nil {
			t.Errorf("%s: got %v %v %v, want nothing", name, got, routes, err)
		}
	}
	// No generated defaults file (never generated) and no rules of its own: nothing, and no routes without alerts.
	got, routes, err := resolveShapeAlerts(t.TempDir(), "dev", obsEntities(nil))
	if err != nil || got != nil || routes != nil {
		t.Errorf("no defaults file, no rules: got %v %v %v", got, routes, err)
	}
	// The env's own rules still stand without the file.
	got, _, err = resolveShapeAlerts(t.TempDir(), "dev", obsEntities(&AlertsEntity{Rules: []AlertRuleEntity{{Alert: "Mine", Expr: "SELECT 1 AS value", Labels: map[string]string{"severity": "info"}, Enabled: true}}}))
	if err != nil || len(got) != 1 || got[0].Alert != "Mine" {
		t.Errorf("own rules without the defaults file: %v %v", got, err)
	}
}

func TestResolveShapeAlerts_Refusals(t *testing.T) {
	dir := writeAlertDefaults(t, alertProject())
	cases := map[string]*AlertsEntity{
		"a rule reusing a default's name": {Rules: []AlertRuleEntity{{Alert: "ServiceErrorRateHigh", Expr: "SELECT 1 AS value", Labels: map[string]string{"severity": "info"}, Enabled: true}}},
		"an unknown default":              {Defaults: map[string]AlertDefaultEntity{"error-rte": {Enabled: true}}},
		"an unknown param":                {Defaults: map[string]AlertDefaultEntity{"error-rate": {Enabled: true, Params: map[string]float64{"thresold": 1}}}},
	}
	for name, a := range cases {
		if _, _, err := resolveShapeAlerts(dir, "dev", obsEntities(a)); err == nil {
			t.Errorf("%s: want a refusal", name)
		}
	}
}

func TestResolveShapeAlerts_MetadataOptOutLeavesNoMetadataRules(t *testing.T) {
	dir := writeAlertDefaults(t, alertProject())
	e := obsEntities(nil)
	e.Observability.Policy = &ObservabilityPolicyEntity{Enabled: true, Disabled: []string{"metadata"}}
	got, _, err := resolveShapeAlerts(dir, "prod", e)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 11 {
		t.Fatalf("got %d alerts, want the 5 telemetry rules plus the 6 platform rules and none of the 5 metadata rules", len(got))
	}
	for _, a := range got {
		switch a.DefaultID {
		case "crash-loop", "oom-killed", "memory-near-limit", "image-pull-failed", "evicted":
			t.Errorf("%s is recorded for an env that opted out of metadata", a.Alert)
		}
	}
}

func TestResolveShapeAlerts_DisabledPlatformDefaultIsRecordedOff(t *testing.T) {
	dir := writeAlertDefaults(t, alertProject())
	got, _, err := resolveShapeAlerts(dir, "prod", obsEntities(&AlertsEntity{Defaults: map[string]AlertDefaultEntity{
		"observability-quota": {Enabled: false},
		"crash-loop":          {Enabled: false},
	}}))
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]release.ShapeAlert{}
	for _, a := range got {
		by[a.Alert] = a
	}
	if _, ok := by["WorkloadCrashLooping"]; ok {
		t.Error("a disabled telemetry default is recorded")
	}
	q := by["ObservabilityQuotaOver100"]
	if q.DefaultID != "observability-quota" || !strings.HasSuffix(q.Expr, " off */") {
		t.Errorf("a disabled platform default is recorded off: %+v", q)
	}
	if by["DeployFailed"].Source != release.AlertSourceDefault {
		t.Errorf("DeployFailed = %+v", by["DeployFailed"])
	}
}
