package cli

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
	"github.com/reliant-labs/forge/pkg/telemetry"
)

// hostedTelemetryTarget is a hosted Workload's target as KCLEntities carries
// it (decoded and normalized): a metrics scrape on a declared port.
func hostedTelemetryTarget(name string) telemetry.Target {
	return telemetry.Normalize([]telemetry.Target{{
		ServiceName: name,
		Source:      "workload:" + name,
		Attachment:  telemetry.AttachWorkload,
		Runtime:     telemetry.RuntimeHosted,
		Selector:    map[string]string{"app.kubernetes.io/name": name},
		Ports:       map[string]int{"metrics": 9090},
		Metrics:     []telemetry.MetricsScrape{{PortName: "metrics", Allow: "app_.*"}},
	}})[0]
}

// The shape records only an env's hosted targets, and nothing at all for an
// env that declares none, so its shape is byte-identical to before the field.
func TestShapeTelemetryOfRecordsHostedTargetsOnly(t *testing.T) {
	for name, e := range map[string]*KCLEntities{"nil": nil, "none": {}} {
		if got, err := shapeTelemetryOf("prod", e, release.DefaultTelemetryLimits()); got != nil || err != nil {
			t.Errorf("%s: shapeTelemetryOf = %+v, %v; want nothing", name, got, err)
		}
	}
	host := hostedTelemetryTarget("temporal")
	host.Runtime, host.Attachment, host.Source, host.Selector, host.Address = telemetry.RuntimeHost, telemetry.AttachHostInfra, "hostinfra:temporal", nil, "127.0.0.1"
	got, err := shapeTelemetryOf("prod", &KCLEntities{Telemetry: []telemetry.Target{hostedTelemetryTarget("api"), host}}, release.DefaultTelemetryLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ServiceName != "api" {
		t.Fatalf("shapeTelemetryOf = %+v; want the hosted api target alone", got)
	}
	// An env with no hosted target records nothing, and its telemetry is not
	// the shape's to judge: a cluster target with no cluster does not fail it.
	bad := hostedTelemetryTarget("cache")
	bad.Runtime = telemetry.RuntimeCluster
	if got, err := shapeTelemetryOf("prod", &KCLEntities{Telemetry: []telemetry.Target{host, bad}}, release.DefaultTelemetryLimits()); got != nil || err != nil {
		t.Errorf("an env with no hosted target: %+v, %v", got, err)
	}
}

// The shape build refuses what the platform will not collect, every problem
// at once and each located at its target and field.
func TestShapeTelemetryOfRefusesHostedViolations(t *testing.T) {
	noAllow := hostedTelemetryTarget("api")
	noAllow.Metrics[0].Allow = ""
	address := hostedTelemetryTarget("cache")
	address.Selector, address.Address = nil, "cache.internal"
	undeclared := hostedTelemetryTarget("queue")
	undeclared.Metrics[0].Port = 9100
	greedy := hostedTelemetryTarget("search")
	greedy.Metrics[0].SampleLimit = 20000

	_, err := shapeTelemetryOf("prod", &KCLEntities{Telemetry: []telemetry.Target{noAllow, address, undeclared, greedy}}, release.DefaultTelemetryLimits())
	var verr *telemetry.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("shapeTelemetryOf = %v; want a *telemetry.ValidationError", err)
	}
	msg := err.Error()
	for _, want := range []string{
		`env "prod"`,
		"telemetry workload:api (service api): metrics[0].allow: is required on a hosted env",
		"telemetry workload:cache (service cache): address: is not supported on a hosted env",
		"telemetry workload:queue (service queue): metrics[0].port: 9100 is not a port the workload declares",
		"telemetry workload:search (service search): metrics[0].sample_limit: 20000 exceeds the hosted cap of 5000 series per scrape",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal lacks %q:\n%s", want, msg)
		}
	}

	var many []telemetry.Target
	for i := range release.DefaultHostedTelemetryMaxPulledTargets + 1 {
		many = append(many, hostedTelemetryTarget(fmt.Sprintf("svc%02d", i)))
	}
	_, err = shapeTelemetryOf("prod", &KCLEntities{Telemetry: many}, release.DefaultTelemetryLimits())
	if err == nil || !strings.Contains(err.Error(), "telemetry workload:svc20 (service svc20): is pulled target 21 of 21") {
		t.Errorf("21 pulled targets: %v", err)
	}
	if _, err := shapeTelemetryOf("prod", &KCLEntities{Telemetry: many[:20]}, release.DefaultTelemetryLimits()); err != nil {
		t.Errorf("20 pulled targets are within the cap: %v", err)
	}
}

// A bundle write to a control plane that does not collect hosted telemetry
// warns, naming each target, and still writes; to one that advertises caps of
// its own, a target over them is refused, located.
func TestCheckBundleTelemetry(t *testing.T) {
	// The warning is process-global; isolate repeated runs of this test while
	// preserving the assertion that two writes in one run warn only once.
	telemetryUncollectedNoted.Delete("prod-warn-test")
	t.Cleanup(func() { telemetryUncollectedNoted.Delete("prod-warn-test") })
	shape := release.Shape{Kind: release.EnvPersistent, Telemetry: release.ShapeTelemetryOf([]telemetry.Target{hostedTelemetryTarget("api"), hostedTelemetryTarget("worker")})}

	var out bytes.Buffer
	if err := checkBundleTelemetry(&out, "prod-warn-test", shape, &release.BundleReader{Features: []string{release.BundleFeatureStorage}}); err != nil {
		t.Fatalf("an old control plane refused the write: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		`[bundle] Warning: env prod-warn-test declares telemetry this control plane does not collect yet (it does not advertise "shape-telemetry" or "collects-telemetry")`,
		"[bundle]     - api (workload:api): metrics\n",
		"[bundle]     - worker (workload:worker): metrics\n",
		"[bundle]   The deploy continues",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the warning lacks %q:\n%s", want, got)
		}
	}
	out.Reset()
	_ = checkBundleTelemetry(&out, "prod-warn-test", shape, &release.BundleReader{})
	if out.Len() != 0 {
		t.Errorf("the warning repeated for the same env: %s", out.String())
	}

	collects := []string{release.BundleFeatureTelemetry, release.CapabilityCollectsTelemetry}
	for name, r := range map[string]*release.BundleReader{
		"collects, no caps": {Features: collects},
		"this forge":        nil,
		"roomy caps":        {Features: collects, Telemetry: &release.TelemetryLimits{MaxPulledTargets: 2}},
	} {
		out.Reset()
		if err := checkBundleTelemetry(&out, "prod-"+name, shape, r); err != nil || out.Len() != 0 {
			t.Errorf("%s: err %v, output %q", name, err, out.String())
		}
	}
	tight := &release.BundleReader{Features: collects, Telemetry: &release.TelemetryLimits{MaxPulledTargets: 1, MaxSampleLimit: 1000}}
	err := checkBundleTelemetry(&out, "prod", shape, tight)
	if err == nil {
		t.Fatal("caps tighter than the shape's were not enforced")
	}
	for _, want := range []string{"(1000 series per scrape, 1 pulled targets)", "workload:api (service api): metrics[0].sample_limit: 5000 exceeds the hosted cap of 1000", "workload:worker (service worker): is pulled target 2 of 2"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal lacks %q:\n%v", want, err)
		}
	}
}

// A control plane on a newer forge pin advertises every decode feature,
// shape-telemetry included, before it collects anything: the write still
// warns, naming the capability it lacks, and is not held to caps it will not
// apply to targets it never receives.
func TestCheckBundleTelemetryWarnsAReaderThatOnlyDecodes(t *testing.T) {
	const env = "prod-decodes-only-test"
	telemetryUncollectedNoted.Delete(env)
	t.Cleanup(func() { telemetryUncollectedNoted.Delete(env) })
	shape := release.Shape{Kind: release.EnvPersistent, Telemetry: release.ShapeTelemetryOf([]telemetry.Target{hostedTelemetryTarget("api"), hostedTelemetryTarget("worker")})}
	decodes := &release.BundleReader{Features: release.BundleDocFeatures(), Telemetry: &release.TelemetryLimits{MaxPulledTargets: 1, MaxSampleLimit: 1000}}

	var out bytes.Buffer
	if err := checkBundleTelemetry(&out, env, shape, decodes); err != nil {
		t.Fatalf("a control plane that only decodes refused the write: %v", err)
	}
	want := `[bundle] Warning: env ` + env + ` declares telemetry this control plane does not collect yet (it does not advertise "collects-telemetry")`
	if !strings.Contains(out.String(), want) {
		t.Errorf("the warning lacks %q:\n%s", want, out.String())
	}
	if got := decodes.LimitShape(shape).Telemetry; got != nil {
		t.Errorf("the targets were sent to a control plane that only decodes them: %+v", got)
	}
}

// From the KCL render to the recorded shape: the hosted golden's target is
// recorded as the platform reads it (normalized, its OTLP env reduced to the
// fact it names), and it passes the hosted rules.
func TestRenderContract_HostedTelemetryReachesTheShape(t *testing.T) {
	e, _ := loadContract(t, "telemetry-hosted")
	got, err := shapeTelemetryOf("prod", e, release.DefaultTelemetryLimits())
	if err != nil {
		t.Fatalf("the hosted golden's telemetry is refused: %v", err)
	}
	if len(got) != 1 || got[0].ServiceName != "search" {
		t.Fatalf("shapeTelemetryOf = %+v; want the search target alone", got)
	}
	shape := release.Shape{Kind: release.EnvPersistent, Telemetry: got}
	enc, err := shape.Encode()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"telemetry":[{"service_name":"search","scope":"project","source":"workload:search","attachment":"workload","runtime":"hosted"`,
		`"selector":{"app.kubernetes.io/name":"search"}`,
		`"ports":{"http":9200,"metrics":9114}`,
		`"port":9114,"port_name":"metrics"`,
		`"allow":"search_(queries_total|query_latency_seconds_(bucket|sum|count))"`,
		`"sample_limit":5000`,
		`"env":{"SEARCH_OTLP_ENDPOINT":{"ref":"traces_endpoint"}}`,
		`"logs":{"format":"json"`,
	} {
		if !strings.Contains(string(enc), want) {
			t.Errorf("the recorded shape lacks %s:\n%s", want, enc)
		}
	}
	if strings.Contains(string(enc), `"network_policy"`) || !got[0].AdmitCollector() {
		t.Errorf("an auto network_policy is not recorded absent (admit %v):\n%s", got[0].AdmitCollector(), enc)
	}
	if strings.Contains(string(enc), "SEARCH_OTLP_ENABLED") {
		t.Errorf("a literal OTLP env value reached the shape:\n%s", enc)
	}
}
