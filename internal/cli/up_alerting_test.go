package cli

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/clickstack"
	"github.com/reliant-labs/forge/pkg/alerting"
	"github.com/reliant-labs/forge/pkg/alerting/amconfig"
	"github.com/reliant-labs/forge/pkg/release"
)

func localAlertingEntities(alerts *AlertsEntity) *KCLEntities {
	return &KCLEntities{Observability: &ObservabilityEntity{
		Enabled: true, LocalPort: 4318, AlertmanagerPort: 9093, AlertsPort: 9095, Alerts: alerts,
	}}
}

// The local config is the control plane's renderer's output for one org: the
// in-app receiver is always there and points at the evaluator's sink, a
// declared webhook on this machine is reached through host.docker.internal,
// and its secret is a FILE whose value never reaches the config.
func TestPrepareLocalAlertingRendersWithTheSharedRenderer(t *testing.T) {
	dir := t.TempDir()
	e := localAlertingEntities(&AlertsEntity{
		Rules: []AlertRuleEntity{{Alert: "Always", Expr: "SELECT 1 AS value FROM otel_logs", For: "0s", Interval: "1m", Labels: map[string]string{"severity": "critical"}, Enabled: true}},
		Receivers: []AlertReceiverEntity{
			{Name: "hook", Type: "webhook", URL: "http://localhost:9999/hook", Secret: "HOOK_TOKEN"},
			{Name: "pager", Type: "pagerduty", Secret: "PD_KEY"}, // not in the store
		},
		Routes: []AlertRouteEntity{
			{Match: map[string]string{"severity": "critical"}, Receiver: "hook", Continue: true},
			{Match: map[string]string{}, Receiver: "reliant-inapp"},
		},
	})
	la, err := prepareLocalAlerting(dir, "dev", e, map[string]string{"HOOK_TOKEN": "s3cret-hook-token"})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := os.ReadFile(filepath.Join(dir, clickstack.AlertmanagerDir, clickstack.AlertmanagerConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	y := string(cfg)
	for _, want := range []string{
		`reliant_org_id="local"`,
		`reliant_env_id="dev"`,
		"local/reliant-inapp",
		"http://host.docker.internal:9095/alerts",
		"http://host.docker.internal:9999/hook",
		"credentials_file: " + amconfig.SecretDir + "/local.hook",
	} {
		if !strings.Contains(y, want) {
			t.Errorf("config lacks %q:\n%s", want, y)
		}
	}
	if strings.Contains(y, "s3cret-hook-token") {
		t.Fatal("a secret value reached the config")
	}
	sec := filepath.Join(dir, clickstack.AlertmanagerDir, "secrets")
	if b, _ := os.ReadFile(filepath.Join(sec, "local.hook")); string(b) != "s3cret-hook-token" {
		t.Fatalf("hook secret file = %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(sec, clickstack.AlertsInAppTokenFile)); len(b) < 32 {
		t.Fatalf("in-app token = %q", b)
	}
	if len(la.Skipped) != 1 || !strings.Contains(la.Skipped[0], "forge secret set --env dev PD_KEY") {
		t.Fatalf("skipped = %v, want the missing PagerDuty key named with its fix", la.Skipped)
	}

	raw, err := os.ReadFile(filepath.Join(dir, clickstack.AlertRulesFile))
	if err != nil {
		t.Fatal(err)
	}
	rules, err := alerting.ParseRulesDocument(raw)
	if err != nil || len(rules) != 1 || rules[0].Alert != "Always" {
		t.Fatalf("rules document = %v, %v", rules, err)
	}

	// A re-render keeps the token a running evaluator read, and a receiver
	// removed from the declaration takes its secret file with it.
	tok, _ := os.ReadFile(filepath.Join(sec, clickstack.AlertsInAppTokenFile))
	e.Observability.Alerts.Receivers = nil
	e.Observability.Alerts.Routes = nil
	if _, err := prepareLocalAlerting(dir, "dev", e, nil); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(filepath.Join(sec, clickstack.AlertsInAppTokenFile)); string(again) != string(tok) {
		t.Fatal("the in-app token changed on re-render")
	}
	if _, err := os.Stat(filepath.Join(sec, "local.hook")); !os.IsNotExist(err) {
		t.Fatalf("a removed receiver's secret survived: %v", err)
	}
}

// Without both local ports there is no local loop, and nothing is written.
func TestPrepareLocalAlertingIsOffWithoutTheLoop(t *testing.T) {
	dir := t.TempDir()
	e := localAlertingEntities(nil)
	e.Observability.AlertmanagerPort, e.Observability.AlertsPort = 0, 0
	if la, err := prepareLocalAlerting(dir, "dev", e, nil); la != nil || err != nil {
		t.Fatalf("got %v, %v", la, err)
	}
	if _, err := os.Stat(filepath.Join(dir, clickstack.AlertmanagerDir)); !os.IsNotExist(err) {
		t.Fatal("rendered with no local loop")
	}
}

// An autopilot receiver observes this env only, with its declared filter.
func TestLocalAutopilotReceiverIsScopedToTheEnv(t *testing.T) {
	in, values, skipped := localAlertInput("dev", 9095, []AlertReceiverEntity{{
		Name: "autopilot", Type: amconfig.TypeAutopilot, URL: "http://127.0.0.1:8080/hooks/a", Secret: "AP",
		Severities: []string{"critical"}, AlwaysAlertnames: []string{"NewIssue"},
	}}, []release.ShapeAlertRoute{{Receiver: "reliant-inapp"}}, map[string]string{"AP": "tok"})
	if len(skipped) != 0 {
		t.Fatal(skipped)
	}
	ap := in.Receivers[1]
	if ap.Autopilot == nil || strings.Join(ap.Autopilot.EnvIDs, ",") != "dev" || ap.Settings["url"] != "http://host.docker.internal:8080/hooks/a" || values[ap.SecretRef] != "tok" {
		t.Fatalf("autopilot receiver = %+v values=%v", ap, values)
	}
	r, err := amconfig.Render(in)
	if err != nil || len(r.Skipped) != 0 {
		t.Fatalf("render: %v %v", err, r.Skipped)
	}
}

// The in-app sink takes only the bearer forge generated, and prints one line
// per alert with the platform's identity labels left out.
func TestAlertSinkHandler(t *testing.T) {
	var out strings.Builder
	h := alertSinkHandler("tok", &out)
	body := `{"receiver":"local/reliant-inapp","status":"firing","alerts":[{"status":"firing","labels":{"alertname":"Slow","severity":"critical","service":"api","reliant_org_id":"local"},"annotations":{"summary":"api is slow"}}]}`

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, clickstack.AlertsSinkPath, strings.NewReader(body)))
	if rec.Code != http.StatusUnauthorized || out.Len() != 0 {
		t.Fatalf("no bearer: %d %q", rec.Code, out.String())
	}

	req := httptest.NewRequest(http.MethodPost, clickstack.AlertsSinkPath, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if got, want := strings.TrimSpace(out.String()), "ALERT firing   Slow service=api severity=critical — api is slow"; got != want {
		t.Fatalf("line = %q, want %q", got, want)
	}
}

func TestSelfForgeCommand(t *testing.T) {
	if got := selfForgeCommand([]string{"go", "run", "."}); strings.Join(got, " ") != "go run ." {
		t.Fatalf("a non-forge command changed: %v", got)
	}
	got := selfForgeCommand([]string{"forge", "obs", "alerts", "run"})
	exe, _ := os.Executable()
	if len(got) < 4 || got[0] != exe || strings.Join(got[len(got)-3:], " ") != "obs alerts run" {
		t.Fatalf("forge command = %v, want the running executable then the args", got)
	}
}
