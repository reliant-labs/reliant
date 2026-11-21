package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/telemetry"
)

// The telemetry golden (testdata/render_contract/telemetry.json) is the KCL
// render of forge.Telemetry on all four attachments. These tests drive the
// production consumers from it: the strict decoder, Normalize, Validate and
// the --json document.

func TestRenderContract_TelemetryDecodesNormalizesAndValidates(t *testing.T) {
	e, _ := loadContract(t, "telemetry")
	if len(e.Telemetry) != 4 {
		t.Fatalf("decoded %d targets, want 4 (workload, hostinfra, manifests, observability)", len(e.Telemetry))
	}
	if err := validateEnvTelemetry("dev", e); err != nil {
		t.Fatalf("the golden's telemetry is invalid: %v", err)
	}
	by := map[string]telemetry.Target{}
	for _, tg := range e.Telemetry {
		by[tg.ServiceName] = tg
	}

	tmp := by["temporal"]
	if tmp.Attachment != telemetry.AttachWorkload || tmp.Runtime != telemetry.RuntimeCluster || tmp.Cluster != "k3d-acme" {
		t.Errorf("temporal placement = %s/%s/%s", tmp.Attachment, tmp.Runtime, tmp.Cluster)
	}
	if m := tmp.Metrics[0]; m.Port != 9090 || m.PortName != "metrics" {
		t.Errorf("the named scrape port did not resolve: %+v", m)
	}
	if m := tmp.Metrics[0]; m.LabelLimit == nil || m.LabelValueLengthLimit == nil || *m.LabelLimit != 0 || *m.LabelValueLengthLimit != 0 {
		t.Errorf("explicit disabled label limits were not preserved: %+v", m)
	}
	if c := tmp.Checks[0]; c.Port != 7233 {
		t.Errorf("the named check port did not resolve: %+v", c)
	}
	if v := tmp.OTLP.Env["TEMPORAL_OTEL_ENDPOINT"]; v.Ref != "grpc_address" || v.Value != "otel-agent.platform-otel.svc:4317" {
		t.Errorf("remapped fact = %+v", v)
	}
	if tmp.OTLP.SamplerRatio == nil || *tmp.OTLP.SamplerRatio != 0.25 {
		t.Errorf("sampler ratio = %v", tmp.OTLP.SamplerRatio)
	}

	if n := by["nats"]; n.Metrics[0].Port != 8222 || n.Address != "127.0.0.1" || n.Runtime != telemetry.RuntimeHost {
		t.Errorf("hostinfra target = %+v", n)
	}
	ch := by["clickhouse"]
	if !ch.Isolated || ch.Scope != telemetry.ScopePlatform || ch.Namespaces[0] != "observability" {
		t.Errorf("manifests target = %+v", ch)
	}
	if b := ch.Metrics[0].BearerToken; b == nil || b.Path != "ch-metrics/token" || ch.Metrics[0].TLS.CA.Path != "ch-ca/ca.crt" {
		t.Errorf("secret mounts = %+v %+v", b, ch.Metrics[0].TLS)
	}
	if cm := by["cert-manager"]; cm.Attachment != telemetry.AttachObservability || cm.Logs.Severity.Mapping["debug"][0] != "3" {
		t.Errorf("observability target = %+v", cm)
	}
	if !ch.AdmitCollector() || tmp.NetworkPolicy != nil || !tmp.AdmitCollector() || by["cert-manager"].AdmitCollector() {
		t.Errorf("network_policy: clickhouse explicit true, temporal auto (admits), cert-manager auto (does not)")
	}
	// The raw target that pulls with network_policy unset gets a note, and
	// only it.
	if w := envTelemetryWarnings(e); len(w) != 1 || w[0].ServiceName != "cert-manager" {
		t.Errorf("warnings = %+v, want one for cert-manager", w)
	}
}

// An env that declares no Telemetry decodes to none, from every golden that
// predates it: the render is unchanged, and nothing downstream sees a target.
func TestRenderContract_NoTelemetryWithoutDeclaration(t *testing.T) {
	for _, name := range []string{"host", "compose", "cluster", "hosted", "build-only", "mixed", "unit-split", "flux-delivery", "secrets-components"} {
		e, raw := loadContract(t, name)
		if e.Telemetry != nil {
			t.Errorf("%s: decoded telemetry %+v from an env that declares none", name, e.Telemetry)
		}
		if bytes.Contains(raw, []byte(`"telemetry"`)) || bytes.Contains(raw, []byte(`"temporal_metrics_port"`)) {
			t.Errorf("%s: the golden carries a telemetry key: an env without Telemetry must render as before", name)
		}
		if err := validateEnvTelemetry(name, e); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestParseKCLEntitiesRefusesUnknownTelemetryKey(t *testing.T) {
	_, err := parseKCLEntities([]byte(`{"output": {"workloads": [], "telemetry": [{"service_name": "a", "scrape_everything": true}]}}`))
	if err == nil || !strings.Contains(err.Error(), "scrape_everything") {
		t.Fatalf("parseKCLEntities = %v, want the unknown key refused", err)
	}
}

func TestValidateEnvTelemetryChecksClusters(t *testing.T) {
	e, _ := loadContract(t, "telemetry")
	e.Telemetry[0].Cluster = "k3d-nowhere"
	err := validateEnvTelemetry("dev", e)
	if err == nil || !strings.Contains(err.Error(), `"k3d-nowhere" is not a cluster this env declares (it declares: k3d-acme)`) {
		t.Fatalf("validateEnvTelemetry = %v", err)
	}
}

func TestWriteRenderedJSON(t *testing.T) {
	e, _ := loadContract(t, "telemetry")
	var buf bytes.Buffer
	objects := []renderedObject{{Kind: "Deployment", Name: "temporal", Namespace: "acme-dev", App: "temporal", Clusters: []string{"k3d-acme"}}}
	if err := writeRenderedJSON(&buf, "acme", "dev", "acme-dev", []string{"k3d-acme"}, objects, e); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Schema    string               `json:"schema"`
		Objects   []envRenderDocObject `json:"objects"`
		Telemetry []telemetry.Target   `json:"telemetry"`
	}
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Schema != envRenderJSONSchema || len(doc.Objects) != 1 || len(doc.Telemetry) != 4 {
		t.Errorf("document = %s", buf.String())
	}

	// No telemetry is [], not null: a consumer iterates it unconditionally.
	buf.Reset()
	if err := writeRenderedJSON(&buf, "acme", "dev", "", nil, nil, &KCLEntities{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"telemetry": []`) || !strings.Contains(buf.String(), `"objects": []`) {
		t.Errorf("empty document = %s", buf.String())
	}
}

func TestEnvRenderJSONFlag(t *testing.T) {
	cmd := newEnvRenderCmd()
	if cmd.Flags().Lookup("json") == nil {
		t.Fatal("forge env render has no --json")
	}
	cmd.SetArgs([]string{"dev", "--json", "--list"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "none of the others can be") {
		t.Fatalf("--json with --list = %v, want them refused together", err)
	}
}

func TestCheckEnvTelemetryPrintsNotes(t *testing.T) {
	e, _ := loadContract(t, "telemetry")
	var errOut bytes.Buffer
	if err := checkEnvTelemetry(&errOut, "dev", e); err != nil {
		t.Fatal(err)
	}
	if got := errOut.String(); !strings.Contains(got, "telemetry note: observability (service cert-manager): network_policy") {
		t.Errorf("notes on stderr = %q", got)
	}
}
