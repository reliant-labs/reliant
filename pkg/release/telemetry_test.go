package release

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/telemetry"
)

// hostedTarget is a hosted Workload's target as the render emits it (before
// Normalize): a metrics scrape on a declared port, a log parse, and an OTLP
// push whose env names one fact and sets one literal.
func hostedTarget(name string) telemetry.Target {
	return telemetry.Target{
		ServiceName: name,
		Source:      "workload:" + name,
		Attachment:  telemetry.AttachWorkload,
		Runtime:     telemetry.RuntimeHosted,
		Selector:    map[string]string{"app.kubernetes.io/name": name},
		Ports:       map[string]int{"http": 8080, "metrics": 9090},
		Metrics:     []telemetry.MetricsScrape{{PortName: "metrics", Allow: "app_.*"}},
		Logs:        &telemetry.LogParse{Format: "json"},
		OTLP: &telemetry.OTLPPush{Signals: []string{"traces"}, Env: map[string]telemetry.OTLPEnvValue{
			"APP_OTLP_ENDPOINT": {Ref: "endpoint"},
			"APP_OTLP_INSECURE": {Value: "true"},
		}},
	}
}

func clusterTarget(name string) telemetry.Target {
	t := hostedTarget(name)
	t.Runtime, t.Cluster, t.Namespaces = telemetry.RuntimeCluster, "ctx", []string{"app"}
	return t
}

// A hosted env's targets survive the shape's canonical encoding, a strict
// decode (what a control plane runs) and Validate, and re-encode to the same
// bytes: the shape a user read and the shape the platform stored are one.
func TestShapeTelemetryRoundTrips(t *testing.T) {
	// network_policy is tri-state: worker says false, api leaves it auto.
	worker, off := hostedTarget("worker"), false
	worker.NetworkPolicy = &off
	s := Shape{Kind: EnvPersistent, Telemetry: ShapeTelemetryOf([]telemetry.Target{worker, clusterTarget("cache"), hostedTarget("api")})}
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	enc, err := s.Encode()
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(enc))
	dec.DisallowUnknownFields()
	var got Shape
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("strict decode: %v", err)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("Validate after decode: %v", err)
	}
	again, err := got.Encode()
	if err != nil || !bytes.Equal(enc, again) {
		t.Fatalf("re-encode differs (err %v):\n%s\n%s", err, enc, again)
	}

	if n := len(got.Telemetry); n != 2 || got.Telemetry[0].ServiceName != "api" || got.Telemetry[1].ServiceName != "worker" {
		t.Fatalf("want the two hosted targets sorted (api, worker), the cluster one left to forge's agent: %+v", got.Telemetry)
	}
	api := got.Telemetry[0]
	if api.NetworkPolicy != nil || got.Telemetry[1].NetworkPolicy == nil || *got.Telemetry[1].NetworkPolicy {
		t.Errorf("network_policy did not round-trip as auto (api) and false (worker): %v, %v", api.NetworkPolicy, got.Telemetry[1].NetworkPolicy)
	}
	if !api.AdmitCollector() || got.Telemetry[1].AdmitCollector() {
		t.Errorf("AdmitCollector after the round trip: api %v, worker %v", api.AdmitCollector(), got.Telemetry[1].AdmitCollector())
	}
	if api.Metrics[0].Port != 9090 || api.Metrics[0].SampleLimit != telemetry.DefaultSampleLimit || api.Metrics[0].Allow != "app_.*" {
		t.Errorf("the target is not normalized: %+v", api.Metrics[0])
	}
	if want := map[string]telemetry.OTLPEnvValue{"APP_OTLP_ENDPOINT": {Ref: "endpoint"}}; !reflect.DeepEqual(api.OTLP.Env, want) {
		t.Errorf("otlp.env = %+v; want only the fact the key names (%+v): a literal is the workload's own env, and a shape carries no value", api.OTLP.Env, want)
	}
	for _, key := range []string{`"service_name":"api"`, `"runtime":"hosted"`, `"ref":"endpoint"`, `"allow":"app_.*"`, `"port_name":"metrics"`} {
		if !bytes.Contains(enc, []byte(key)) {
			t.Errorf("the encoded shape lacks %s:\n%s", key, enc)
		}
	}
	if n := bytes.Count(enc, []byte(`"network_policy"`)); n != 1 {
		t.Errorf("network_policy appears %d times; want once (auto is absent, false is stated):\n%s", n, enc)
	}
	if bytes.Contains(enc, []byte(`"true"`)) {
		t.Errorf("the literal OTLP env value reached the shape:\n%s", enc)
	}
}

// An env with no hosted target encodes, and so hashes, exactly as a shape
// did before the field: no key, and nothing for a reader to be asked about.
func TestShapeWithoutHostedTelemetryIsByteIdentical(t *testing.T) {
	base := Shape{Kind: EnvPersistent, Workloads: []ShapeWorkload{{Name: "api", Runtime: "hosted"}}}
	want, err := base.Encode()
	if err != nil {
		t.Fatal(err)
	}
	for name, targets := range map[string][]telemetry.Target{
		"none":         nil,
		"cluster only": {clusterTarget("cache")},
	} {
		s := base
		s.Telemetry = ShapeTelemetryOf(targets)
		if s.Telemetry != nil {
			t.Errorf("%s: ShapeTelemetryOf = %+v; want nil", name, s.Telemetry)
		}
		got, err := s.Encode()
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s: the shape encodes differently (err %v):\n got %s\nwant %s", name, err, got, want)
		}
		if bytes.Contains(got, []byte(`"telemetry"`)) || s.HasOptionalParts() {
			t.Errorf("%s: a shape with no hosted target carries the key or asks the reader", name)
		}
	}
	// Spelling an empty list is the same as leaving it off.
	empty := base
	empty.Telemetry = []ShapeTelemetry{}
	if got, _ := empty.Encode(); !bytes.Equal(got, want) {
		t.Errorf("an empty Telemetry encodes differently:\n%s", got)
	}
}

// A control plane that does not name the feature gets the shape without the
// targets (its strict decoder would refuse the whole shape), and HasOptionalParts
// asks it first; one that names it, and this forge's own ledger, get them.
func TestShapeTelemetryIsStrippedForOldControlPlanes(t *testing.T) {
	s := Shape{Kind: EnvPersistent, Telemetry: ShapeTelemetryOf([]telemetry.Target{hostedTarget("api")})}
	if !s.HasOptionalParts() {
		t.Fatal("a shape with hosted telemetry does not ask the reader what it decodes")
	}
	old := &BundleReader{Features: []string{BundleFeatureDatabaseDashboards}}
	if got := old.LimitShape(s); got.Telemetry != nil {
		t.Errorf("a reader without %s was sent the targets: %+v", BundleFeatureTelemetry, got.Telemetry)
	}
	if len(s.Telemetry) != 1 {
		t.Fatal("LimitShape modified the shape it was given")
	}
	for name, r := range map[string]*BundleReader{
		"decodes and collects": {Features: []string{BundleFeatureTelemetry, CapabilityCollectsTelemetry}},
		"nil":                  nil,
	} {
		if got := r.LimitShape(s); !reflect.DeepEqual(got.Telemetry, s.Telemetry) {
			t.Errorf("%s: LimitShape changed the targets: %+v", name, got.Telemetry)
		}
	}
	if !slices.Contains(BundleDocFeatures(), BundleFeatureTelemetry) {
		t.Errorf("BundleDocFeatures does not list %s", BundleFeatureTelemetry)
	}
}

// Decoding is not collecting. A control plane advertises BundleDocFeatures
// verbatim on every forge pin bump, so one that names shape-telemetry but not
// collects-telemetry (or names collects-telemetry alone, which it cannot
// decode) is sent no targets and warned about, naming what it lacks. Only a
// reader that names both, and this forge's own ledger, gets them.
func TestHostedTelemetryNeedsTheCollectsCapability(t *testing.T) {
	s := Shape{Kind: EnvPersistent, Telemetry: ShapeTelemetryOf([]telemetry.Target{hostedTarget("api")})}
	for name, c := range map[string]struct {
		r       *BundleReader
		keep    bool
		missing string
	}{
		"a pin bump: every decode feature": {&BundleReader{Features: BundleDocFeatures()}, false, `"collects-telemetry"`},
		"shape-telemetry alone":            {&BundleReader{Features: []string{BundleFeatureTelemetry}}, false, `"collects-telemetry"`},
		"collects-telemetry alone":         {&BundleReader{Features: []string{CapabilityCollectsTelemetry}}, false, `"shape-telemetry"`},
		"neither":                          {&BundleReader{}, false, `"shape-telemetry" or "collects-telemetry"`},
		"both":                             {&BundleReader{Features: append(BundleDocFeatures(), CapabilityCollectsTelemetry)}, true, ""},
		"this forge's ledger":              {nil, true, ""},
	} {
		if got := c.r.CollectsTelemetry(); got != c.keep {
			t.Errorf("%s: CollectsTelemetry = %v, want %v", name, got, c.keep)
		}
		if kept := c.r.LimitShape(s).Telemetry != nil; kept != c.keep {
			t.Errorf("%s: LimitShape kept the targets = %v, want %v", name, kept, c.keep)
		}
		if kept := c.r.Limit(BundleDoc{Shape: s}).Shape.Telemetry != nil; kept != c.keep {
			t.Errorf("%s: Limit kept the targets = %v, want %v", name, kept, c.keep)
		}
		msg := c.r.TelemetryWarning("prod", s)
		if c.keep {
			if msg != "" {
				t.Errorf("%s: warned %q", name, msg)
			}
			continue
		}
		if want := "(it does not advertise " + c.missing + ")"; !strings.Contains(msg, want) {
			t.Errorf("%s: the warning does not name %s:\n%s", name, want, msg)
		}
	}
}

// collects-telemetry is advertised by hand, never by a pin bump: it is not in
// the decode list a control plane copies verbatim.
func TestCollectsTelemetryIsNotADecodeFeature(t *testing.T) {
	if slices.Contains(BundleDocFeatures(), CapabilityCollectsTelemetry) {
		t.Errorf("BundleDocFeatures claims %s; a control plane adds collection by hand", CapabilityCollectsTelemetry)
	}
	if CapabilityCollectsTelemetry != "collects-telemetry" {
		t.Errorf("CapabilityCollectsTelemetry = %q; the wire string is frozen", CapabilityCollectsTelemetry)
	}
}

// The deploy-time warning names the feature and every target that will go
// uncollected, with its source and signals, and is silent whenever nothing is
// withheld.
func TestTelemetryWarningNamesTheUncollectedTargets(t *testing.T) {
	push := hostedTarget("relay")
	push.Metrics, push.Logs = nil, nil
	s := Shape{Kind: EnvPersistent, Telemetry: ShapeTelemetryOf([]telemetry.Target{hostedTarget("api"), push})}

	got := (&BundleReader{}).TelemetryWarning("prod", s)
	want := `env prod declares telemetry this control plane does not collect yet (it does not advertise "shape-telemetry" or "collects-telemetry"), ` +
		`so it is left out of the release, and these targets will NOT be collected:
  - api (workload:api): logs, metrics, traces
  - relay (workload:relay): traces
The deploy continues and the workloads run; their declared telemetry is collected once the control plane is upgraded.`
	if got != want {
		t.Errorf("warning:\n%s\nwant:\n%s", got, want)
	}
	for name, c := range map[string]struct {
		r *BundleReader
		s Shape
	}{
		"a reader that collects": {&BundleReader{Features: []string{BundleFeatureTelemetry, CapabilityCollectsTelemetry}}, s},
		"this forge's ledger":    {nil, s},
		"no hosted target":       {&BundleReader{}, Shape{Kind: EnvPersistent}},
	} {
		if msg := c.r.TelemetryWarning("prod", c.s); msg != "" {
			t.Errorf("%s: warned %q", name, msg)
		}
	}
}

// A target forge rendered on its own (telemetry.OriginDefault, a
// ManagedDatabase's default) is recorded in the shape, with its origin, and
// withheld from a reader that does not collect hosted telemetry (it lacks
// shape-telemetry, collects-telemetry, or both) like any other; but the
// warning names only what the project declared, with the missing capability,
// and says nothing when every withheld target is a default.
func TestTelemetryWarningSkipsDefaultTargets(t *testing.T) {
	db := hostedTarget("db-postgres")
	db.Source, db.Attachment, db.Origin = "manageddatabase:db", telemetry.AttachManagedDatabase, telemetry.OriginDefault
	db.OTLP = nil

	defaults := Shape{Kind: EnvPersistent, Telemetry: ShapeTelemetryOf([]telemetry.Target{db})}
	if len(defaults.Telemetry) != 1 || defaults.Telemetry[0].Origin != telemetry.OriginDefault {
		t.Fatalf("the shape did not record the default target with its origin: %+v", defaults.Telemetry)
	}
	mixed := Shape{Kind: EnvPersistent, Telemetry: ShapeTelemetryOf([]telemetry.Target{db, hostedTarget("api")})}

	for name, c := range map[string]struct {
		r       *BundleReader
		missing string
	}{
		"neither":                          {&BundleReader{}, `"shape-telemetry" or "collects-telemetry"`},
		"a pin bump: every decode feature": {&BundleReader{Features: BundleDocFeatures()}, `"collects-telemetry"`},
		"collects-telemetry alone":         {&BundleReader{Features: []string{CapabilityCollectsTelemetry}}, `"shape-telemetry"`},
	} {
		if got := c.r.LimitShape(defaults); got.Telemetry != nil {
			t.Errorf("%s: a reader that does not collect was sent the default target: %+v", name, got.Telemetry)
		}
		if msg := c.r.TelemetryWarning("prod", defaults); msg != "" {
			t.Errorf("%s: warned about a default target nobody declared:\n%s", name, msg)
		}

		if got := c.r.LimitShape(mixed); got.Telemetry != nil {
			t.Errorf("%s: a reader that does not collect was sent the targets: %+v", name, got.Telemetry)
		}
		msg := c.r.TelemetryWarning("prod", mixed)
		if !strings.Contains(msg, "  - api (workload:api)") || strings.Contains(msg, "db-postgres") {
			t.Errorf("%s: the warning must name the declared target and only it:\n%s", name, msg)
		}
		if want := "(it does not advertise " + c.missing + ")"; !strings.Contains(msg, want) {
			t.Errorf("%s: the warning does not name %s:\n%s", name, want, msg)
		}
	}

	// A reader that collects is sent the default target with the rest, and
	// nothing is said.
	both := &BundleReader{Features: append(BundleDocFeatures(), CapabilityCollectsTelemetry)}
	if got := both.LimitShape(mixed); len(got.Telemetry) != 2 {
		t.Errorf("a reader that collects was not sent every target: %+v", got.Telemetry)
	}
	if msg := both.TelemetryWarning("prod", mixed); msg != "" {
		t.Errorf("warned a reader that collects:\n%s", msg)
	}
}

func problemFields(ps []telemetry.Problem) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Source + " " + p.Field
	}
	return out
}

// The hosted caps refuse, each located at its target and field: a scrape
// over the sample cap, the pulled target past the cap, and platform scope.
// Push-only targets cost no pull, non-hosted targets are not the platform's,
// and a control plane's advertised caps replace the defaults.
func TestHostedTelemetryCaps(t *testing.T) {
	var targets []telemetry.Target
	for i := range DefaultHostedTelemetryMaxPulledTargets {
		targets = append(targets, hostedTarget(fmt.Sprintf("svc%02d", i)))
	}
	push := hostedTarget("relay")
	push.Metrics = nil
	targets = append(targets, push, clusterTarget("cache"))
	targets = telemetry.Normalize(targets)
	if ps := HostedTelemetryProblems(targets, DefaultTelemetryLimits()); len(ps) != 0 {
		t.Fatalf("%d pulled targets at default sample limits: %v", DefaultHostedTelemetryMaxPulledTargets, ps)
	}

	over := telemetry.Normalize([]telemetry.Target{hostedTarget("svc20")})[0]
	over.Metrics[0].SampleLimit = DefaultHostedTelemetryMaxSampleLimit + 1
	over.Scope = telemetry.ScopePlatform
	ps := HostedTelemetryProblems(append(slices.Clone(targets), over), TelemetryLimits{})
	if got, want := problemFields(ps), []string{"workload:svc20 scope", "workload:svc20 metrics[0].sample_limit", "workload:svc20 "}; !reflect.DeepEqual(got, want) {
		t.Fatalf("problems at %q; want %q:\n%v", got, want, ps)
	}
	if msg := ps[1].Error(); !strings.Contains(msg, "5001 exceeds the hosted cap of 5000 series per scrape") {
		t.Errorf("sample-limit problem: %s", msg)
	}
	if msg := ps[2].Error(); !strings.Contains(msg, "is pulled target 21 of 21") || !strings.Contains(msg, "at most 20 targets") ||
		!strings.Contains(msg, "svc00") || strings.Contains(msg, "relay") || strings.Contains(msg, "cache") {
		t.Errorf("pulled-target problem: %s", msg)
	}

	// Advertised caps replace the defaults.
	tight := TelemetryLimits{MaxSampleLimit: 1000, MaxPulledTargets: 2}
	ps = HostedTelemetryProblems(targets[:3], tight)
	if got := problemFields(ps); len(got) != 4 || got[3] != "workload:svc02 " {
		t.Fatalf("under %+v: %q (%v)", tight, got, ps)
	}
	if got := (&BundleReader{Telemetry: &TelemetryLimits{MaxPulledTargets: 2}}).TelemetryCaps(); got != (TelemetryLimits{MaxSampleLimit: 5000, MaxPulledTargets: 2}) {
		t.Errorf("TelemetryCaps = %+v; an unset cap is the default", got)
	}
	if got := (*BundleReader)(nil).TelemetryCaps(); got != DefaultTelemetryLimits() {
		t.Errorf("nil reader caps = %+v", got)
	}
}

// Validate holds a stored shape to the rules a hosted target is built under,
// because it is read by a different program than the one that wrote it.
func TestShapeValidateRefusesBadTelemetry(t *testing.T) {
	good := ShapeTelemetryOf([]telemetry.Target{hostedTarget("api")})[0]
	for name, mutate := range map[string]func(*ShapeTelemetry){
		"not hosted":  func(s *ShapeTelemetry) { s.Runtime = telemetry.RuntimeCluster },
		"a value":     func(s *ShapeTelemetry) { s.OTLP.Env["K"] = telemetry.OTLPEnvValue{Value: "secret"} },
		"no allow":    func(s *ShapeTelemetry) { s.Metrics[0].Allow = "" },
		"an address":  func(s *ShapeTelemetry) { s.Address = "db.internal" },
		"undeclared":  func(s *ShapeTelemetry) { s.Metrics[0].Port = 9100 },
		"twice named": nil,
	} {
		bad := ShapeTelemetryOf([]telemetry.Target{good.Target})[0]
		list := []ShapeTelemetry{bad}
		if mutate == nil {
			list = append(list, bad)
		} else {
			mutate(&list[0])
		}
		err := Shape{Kind: EnvPersistent, Telemetry: list}.Validate()
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: Validate = %v; want ErrInvalid", name, err)
		}
	}
}

// TestShapeTelemetryKeysAreFrozen: a control plane naming shape-telemetry
// strict-decodes exactly the keys it was built with. A field added to
// telemetry.Target lands in ShapeTelemetry by embedding, and would make every
// such control plane refuse the whole shape: it needs a NEW bundle feature
// that withholds it (bundle_features.go), and only then this fixture's update.
func TestShapeTelemetryKeysAreFrozen(t *testing.T) {
	const fixture = "testdata/shape_telemetry_keys.txt"
	f, err := os.Open(fixture)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var frozen []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" && !strings.HasPrefix(line, "#") {
			frozen = append(frozen, line)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	got := bundleDocKeyPaths(reflect.TypeFor[ShapeTelemetry]())
	for _, p := range got {
		if !slices.Contains(frozen, p) {
			t.Errorf("ShapeTelemetry key %q is not in %s: every control plane that advertises %q refuses a shape carrying it. "+
				"Add a bundle feature that withholds it from those readers (BundleDocFeatures, LimitShape, bundleFeatureKeys), then add it here.",
				p, fixture, BundleFeatureTelemetry)
		}
	}
	for _, p := range frozen {
		if !slices.Contains(got, p) {
			t.Errorf("ShapeTelemetry no longer has key %q: a removal breaks the readers that send it back; fields are added, never removed", p)
		}
	}
}
