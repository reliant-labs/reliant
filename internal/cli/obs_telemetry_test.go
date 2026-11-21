package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/internal/obsdash"
	"github.com/reliant-labs/forge/pkg/telemetry"
)

// fakeTelemetryBackend is a local backend that answers only the arrival
// read; every other verb is the nil embedded Backend's (unused here).
type fakeTelemetryBackend struct {
	obsbackend.Backend
	res  map[bool]obsbackend.TelemetryArrivalResult
	reqs []obsbackend.TelemetryArrivalRequest
}

func (f *fakeTelemetryBackend) Info() obsbackend.Info {
	return obsbackend.Info{Env: "dev", Backend: obsbackend.BackendLocal}
}

func (f *fakeTelemetryBackend) TelemetryArrival(_ context.Context, req obsbackend.TelemetryArrivalRequest) (obsbackend.TelemetryArrivalResult, error) {
	f.reqs = append(f.reqs, req)
	return f.res[req.Platform], nil
}

func obsTelemetryTargets() []telemetry.Target {
	return telemetry.Normalize([]telemetry.Target{
		{ServiceName: "temporal", Scope: telemetry.ScopeProject, Source: "hostinfra:temporal", Attachment: telemetry.AttachHostInfra, Runtime: telemetry.RuntimeHost,
			Address: "127.0.0.1", Metrics: []telemetry.MetricsScrape{{Port: 9233, SampleLimit: 8000}}, OTLP: &telemetry.OTLPPush{Signals: []string{"traces"}},
			Dashboard: telemetry.SystemDashboard{Enabled: true}, DefaultAlerts: true},
		{ServiceName: "clickhouse", Scope: telemetry.ScopePlatform, Source: "observability", Attachment: telemetry.AttachObservability, Runtime: telemetry.RuntimeHost,
			Address: "127.0.0.1", Checks: []telemetry.HealthCheck{{Kind: "http", Port: 8123}},
			Dashboards: []json.RawMessage{json.RawMessage(`{"id":"ch","title":"CH","panels":[{"id":"p","title":"P","kind":"line","signal":"logs","series":[{"label":"n","agg":"count"}]}]}`)}},
	})
}

func runObsTelemetryTest(t *testing.T, d obsDeps, targets telemetryTargetsFunc, args ...string) (string, error) {
	t.Helper()
	var o obsCommon
	cmd := &cobra.Command{}
	o.register(cmd, time.Hour, 0)
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	d.Stdout, d.Stderr = &out, io.Discard
	d.Now = func() time.Time { return obsNow }
	err := runObsTelemetry(context.Background(), d, targets, "dev", o)
	return out.String(), err
}

func staticTargets(ts []telemetry.Target) telemetryTargetsFunc {
	return func(context.Context, string) ([]telemetry.Target, error) { return ts, nil }
}

func TestObsTelemetryReportsArrivalPerSignalAndScrape(t *testing.T) {
	seen := obsNow.Add(-2 * time.Minute)
	b := &fakeTelemetryBackend{res: map[bool]obsbackend.TelemetryArrivalResult{
		false: {
			Signals: []obsbackend.TelemetrySignalArrival{{Service: "temporal", Signal: "metrics", LastSeen: seen, Rows: 40}, {Service: "temporal", Signal: "logs", LastSeen: seen, Rows: 3}},
			Scrapes: []obsbackend.TelemetryScrapeResult{{Service: "temporal", Instance: "127.0.0.1:9233", Up: 0, Samples: 9100, LastSeen: seen}},
			Unread:  map[string]string{},
		},
		true: {Unread: map[string]string{"metrics": "table missing"}},
	}}
	d := obsDeps{
		Facts: func(context.Context, io.Writer, string) (obsbackend.Facts, error) { return devFacts(), nil },
		Local: func(string) obsbackend.Backend { return b },
	}
	out, err := runObsTelemetryTest(t, d, staticTargets(obsTelemetryTargets()), "--json")
	if err != nil {
		t.Fatal(err)
	}
	var doc obsTelemetryDoc
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if doc.Schema != schemaObsTelemetry || !doc.OK || doc.Arrival != telemetryArrivalRead || len(doc.Targets) != 2 {
		t.Fatalf("doc = %s", out)
	}
	if len(b.reqs) != 2 || !slices.Equal(b.reqs[0].Narrow.Services, []string{"temporal"}) || b.reqs[0].Platform ||
		!slices.Equal(b.reqs[1].Narrow.Services, []string{"clickhouse"}) || !b.reqs[1].Platform {
		t.Errorf("a platform system must be read apart, without the env: %+v", b.reqs)
	}
	tmp := doc.Targets[0]
	status := map[string]obsTelemetrySignalDoc{}
	for _, s := range tmp.Signals {
		status[s.Signal] = s
	}
	if status["metrics"].Status != telemetrySignalArriving || status["metrics"].Rows != 40 || status["logs"].Status != telemetrySignalArriving ||
		status["traces"].Status != telemetrySignalAbsent {
		t.Errorf("temporal signals = %+v", tmp.Signals)
	}
	if _, asked := status["profiles"]; asked {
		t.Error("a signal the target does not declare is not reported")
	}
	if len(tmp.Scrapes) != 1 || tmp.Scrapes[0].Up != 0 || tmp.Scrapes[0].Samples != 9100 || tmp.Scrapes[0].SampleLimit != 8000 {
		t.Errorf("scrapes = %+v", tmp.Scrapes)
	}
	if !slices.Equal(tmp.Dashboards, []string{"telemetry-temporal"}) || !slices.Equal(doc.Targets[1].Dashboards, []string{"ch"}) {
		t.Errorf("dashboards = %v, %v", tmp.Dashboards, doc.Targets[1].Dashboards)
	}
	ch := doc.Targets[1].Signals
	if ch[0].Signal != "metrics" || ch[0].Status != telemetrySignalUnknown || ch[0].Note != "table missing" {
		t.Errorf("an unread signal is unknown, with why (a check's results are metrics): %+v", ch)
	}

	text, err := runObsTelemetryTest(t, d, staticTargets(obsTelemetryTargets()))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"SERVICE", "temporal", "arriving", "127.0.0.1:9233", "9100", "8000", "telemetry-temporal"} {
		if !strings.Contains(text, want) {
			t.Errorf("the human form lacks %q:\n%s", want, text)
		}
	}
}

func TestObsTelemetryListsTargetsWhenArrivalCannotBeRead(t *testing.T) {
	down := obsDeps{Facts: func(context.Context, io.Writer, string) (obsbackend.Facts, error) {
		return obsbackend.Facts{}, obsbackend.Undetermined("the local ClickStack cannot be reached")
	}}
	out, err := runObsTelemetryTest(t, down, staticTargets(obsTelemetryTargets()), "--json")
	if err != nil {
		t.Fatalf("an unreachable store is not a failure of the listing: %v", err)
	}
	var doc obsTelemetryDoc
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Arrival != telemetryArrivalUnknown || !strings.Contains(doc.ArrivalNote, "cannot be reached") || len(doc.Targets) != 2 || doc.Targets[0].Signals[0].Status != telemetrySignalUnknown {
		t.Errorf("doc = %s", out)
	}

	cloud := obsDeps{
		Facts: func(context.Context, io.Writer, string) (obsbackend.Facts, error) { return devFacts(), nil },
		Local: func(string) obsbackend.Backend { return struct{ obsbackend.Backend }{&fakeTelemetryBackend{}} },
	}
	out, err = runObsTelemetryTest(t, cloud, staticTargets(obsTelemetryTargets()), "--json")
	if err != nil || !strings.Contains(out, "does not serve per-target arrival") {
		t.Errorf("a backend with no arrival read says so: %v\n%s", err, out)
	}
}

func TestObsTelemetryRefusalsAndEmpty(t *testing.T) {
	d := obsDeps{Facts: func(context.Context, io.Writer, string) (obsbackend.Facts, error) { return devFacts(), nil }}
	if _, err := runObsTelemetryTest(t, d, staticTargets(nil), "--filter", "a=b"); err == nil {
		t.Error("--filter is refused")
	}
	out, err := runObsTelemetryTest(t, d, staticTargets(nil))
	if err != nil || !strings.Contains(out, "declares no forge.Telemetry") {
		t.Errorf("an env with no telemetry says so: %v %q", err, out)
	}
	out, err = runObsTelemetryTest(t, d, staticTargets(obsTelemetryTargets()), "--service", "nope")
	if err != nil || !strings.Contains(out, "declares no forge.Telemetry for [nope]") {
		t.Errorf("--service narrows the targets: %v %q", err, out)
	}
	boom := func(context.Context, string) ([]telemetry.Target, error) { return nil, errors.New("render failed") }
	if _, err := runObsTelemetryTest(t, d, boom, "--json"); err == nil || !strings.Contains(err.Error(), "render failed") {
		t.Errorf("a render failure fails the verb: %v", err)
	}
}

// The release bundle of an env carries the dashboards of the systems THAT env
// declares, even one with no forge process (they need none).
func TestSelectedEnvBundleCarriesItsTelemetryDashboards(t *testing.T) {
	dir := t.TempDir()
	for rel, body := range map[string]string{
		"forge.yaml": "name: sample\nmodule_path: example.com/sample\n",
		"go.mod":     "module example.com/sample\n\ngo 1.25\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := obsdash.Write(dir, obsdash.Input{Project: "sample"}, &checksums.FileChecksums{}); err != nil {
		t.Fatal(err)
	}
	sp, err := dashboardSpecForEntities(dir, &KCLEntities{Env: "prod", Telemetry: obsTelemetryTargets()})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, d := range sp.Dashboards {
		ids = append(ids, d.ID)
	}
	if !slices.Contains(ids, "telemetry-temporal") || !slices.Contains(ids, "ch") || slices.Contains(ids, "telemetry-clickhouse") {
		t.Fatalf("dashboards = %v: want temporal's system dashboard and clickhouse's declared one (its system dashboard is off)", ids)
	}
	set, err := collectRenderedObservabilityFiles(dir, "", sp)
	if err != nil {
		t.Fatalf("the telemetry dashboards must pass the release cut's validation: %v", err)
	}
	if len(set.Dashboards) == 0 {
		t.Fatal("empty bundle set")
	}
}

// An env's alert rules carry the telemetry defaults of the systems it
// declares, and a rule of its own may not take one's name.
func TestShapeAlertsCarryTelemetryDefaults(t *testing.T) {
	dir := t.TempDir()
	if _, err := obsdash.Write(dir, obsdash.Input{Project: "sample"}, &checksums.FileChecksums{}); err != nil {
		t.Fatal(err)
	}
	e := &KCLEntities{Env: "prod", Telemetry: obsTelemetryTargets(), Observability: &ObservabilityEntity{Enabled: true}}
	alerts, _, err := resolveShapeAlerts(context.Background(), io.Discard, dir, "prod", e)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, a := range alerts {
		names = append(names, a.Alert)
	}
	temporalAlert := obsdash.TelemetryAlertName("temporal", "TargetDown")
	clickhouseAlert := obsdash.TelemetryAlertName("clickhouse", "CheckFailing")
	if !slices.Contains(names, temporalAlert) || slices.Contains(names, clickhouseAlert) {
		t.Errorf("alerts = %v: temporal arms its defaults, clickhouse opted out (default_alerts unset)", names)
	}
	e.Observability.Alerts = &AlertsEntity{Rules: []AlertRuleEntity{{Alert: temporalAlert, Expr: "SELECT 1 AS value", Enabled: true}}}
	if _, _, err := resolveShapeAlerts(context.Background(), io.Discard, dir, "prod", e); err == nil || !strings.Contains(err.Error(), "default_alerts = False") {
		t.Errorf("a rule that takes a telemetry default's name must be refused: %v", err)
	}
}
