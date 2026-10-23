package release

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// A shape that opts out of nothing encodes without the field, so every shape
// recorded before it existed hashes and diffs exactly as it did.
func TestShape_ObservabilityPolicyOmittedWhenAbsent(t *testing.T) {
	b, err := Shape{Kind: EnvLocal}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "observability_policy") {
		t.Errorf("an absent policy must not be encoded: %s", b)
	}
}

func TestShape_ObservabilityPolicyRoundTrip(t *testing.T) {
	in := Shape{Kind: EnvSelfManaged, ObservabilityPolicy: &ShapeObservabilityPolicy{
		Enabled: true, Disabled: []string{ObsCategoryMetadata, ObsCategoryLogs},
	}}
	b, err := in.Encode()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"observability_policy"`, `"enabled":true`, `"disabled":["logs","metadata"]`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("encoded shape lacks %s: %s", want, b)
		}
	}
	var out Shape
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if err := out.Validate(); err != nil {
		t.Fatalf("a round-tripped shape is invalid: %v", err)
	}
	if want := in.Canonical(); !reflect.DeepEqual(out.ObservabilityPolicy, want.ObservabilityPolicy) {
		t.Errorf("round trip changed the policy:\n got %+v\nwant %+v", out.ObservabilityPolicy, want.ObservabilityPolicy)
	}
}

func TestShape_ObservabilityPolicyAllOff(t *testing.T) {
	in := Shape{Kind: EnvSelfManaged, ObservabilityPolicy: &ShapeObservabilityPolicy{Enabled: false, Disabled: ObsCategories}}
	if err := in.Canonical().Validate(); err != nil {
		t.Fatalf("a fully opted-out policy is valid: %v", err)
	}
	for _, c := range ObsCategories {
		if !in.ObservabilityPolicy.Off(c) {
			t.Errorf("Off(%q) = false under enabled=false", c)
		}
	}
}

func TestShape_ObservabilityPolicyOptIn(t *testing.T) {
	in := Shape{Kind: EnvSelfManaged, ObservabilityPolicy: &ShapeObservabilityPolicy{Enabled: true, OptIn: []string{ObsOptInReplay}}}
	b, err := in.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"opt_in":["replay"]`) {
		t.Errorf("opt_in not encoded: %s", b)
	}
	if !in.ObservabilityPolicy.OptedIn(ObsOptInReplay) || in.ObservabilityPolicy.OptedIn("nonsense") {
		t.Error("OptedIn wrong")
	}
	// An env that disabled everything is never opted in.
	off := &ShapeObservabilityPolicy{Enabled: false, Disabled: ObsCategories, OptIn: []string{ObsOptInReplay}}
	if off.OptedIn(ObsOptInReplay) {
		t.Error("a fully disabled env must not count as opted in")
	}
	for name, p := range map[string]*ShapeObservabilityPolicy{
		"unknown opt-in": {Enabled: true, OptIn: []string{"audio"}},
		"duplicate":      {Enabled: true, OptIn: []string{"replay", "replay"}},
	} {
		if err := (Shape{Kind: EnvLocal, ObservabilityPolicy: p}).Canonical().Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: want ErrInvalid, got %v", name, err)
		}
	}
}

func TestShape_ObservabilityPolicyRefusals(t *testing.T) {
	for name, p := range map[string]*ShapeObservabilityPolicy{
		"unknown category":       {Enabled: true, Disabled: []string{"infra"}},
		"duplicate":              {Enabled: true, Disabled: []string{"logs", "logs"}},
		"off without categories": {Enabled: false},
	} {
		err := Shape{Kind: EnvLocal, ObservabilityPolicy: p}.Canonical().Validate()
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: want ErrInvalid, got %v", name, err)
		}
	}
}

func TestShapeObservabilityPolicy_Off(t *testing.T) {
	var none *ShapeObservabilityPolicy
	if none.Off(ObsCategoryLogs) {
		t.Error("a nil policy opts out of nothing")
	}
	p := &ShapeObservabilityPolicy{Enabled: true, Disabled: []string{ObsCategoryLogs}}
	if !p.Off(ObsCategoryLogs) || p.Off(ObsCategoryTraces) {
		t.Errorf("Off wrong for %+v", p)
	}
}

func TestShape_ObservabilityRetentionDefaultsAreTheAbsenceOfOne(t *testing.T) {
	defaults := &ShapeRetention{ReplayDays: DefaultReplayDays, ErrorsDays: DefaultErrorsDays, ProductEventsDays: DefaultProductEventsDays}
	spelled := Shape{Kind: EnvSelfManaged, ObservabilityPolicy: &ShapeObservabilityPolicy{Enabled: true, Retention: defaults}}
	bare := Shape{Kind: EnvSelfManaged, ObservabilityPolicy: &ShapeObservabilityPolicy{Enabled: true}}
	a, err := spelled.Encode()
	if err != nil {
		t.Fatal(err)
	}
	b, err := bare.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) || strings.Contains(string(a), "retention") {
		t.Errorf("a producer that spells the defaults out must encode like one that declares nothing:\n%s\n%s", a, b)
	}
}

func TestShape_ObservabilityRetentionRoundTripAndBounds(t *testing.T) {
	in := Shape{Kind: EnvSelfManaged, ObservabilityPolicy: &ShapeObservabilityPolicy{
		Enabled: true, OptIn: []string{ObsOptInReplay},
		Retention: &ShapeRetention{ReplayDays: 7, ErrorsDays: 60, ProductEventsDays: 180},
	}}
	b, err := in.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"retention":{"replay_days":7,"errors_days":60,"product_events_days":180}`) {
		t.Errorf("retention not encoded: %s", b)
	}
	var out Shape
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if err := out.Validate(); err != nil {
		t.Fatalf("round-tripped retention is invalid: %v", err)
	}
	if !reflect.DeepEqual(out.ObservabilityPolicy.Retention, in.ObservabilityPolicy.Retention) {
		t.Errorf("round trip changed retention: %+v", out.ObservabilityPolicy.Retention)
	}
	for name, r := range map[string]*ShapeRetention{
		"replay over the cap":  {ReplayDays: MaxReplayDays + 1, ErrorsDays: 30, ProductEventsDays: 90},
		"errors zero":          {ReplayDays: 14, ErrorsDays: 0, ProductEventsDays: 90},
		"product events over":  {ReplayDays: 14, ErrorsDays: 30, ProductEventsDays: MaxProductEventsDays + 1},
		"a half-set retention": {ReplayDays: 7},
	} {
		bad := Shape{Kind: EnvSelfManaged, ObservabilityPolicy: &ShapeObservabilityPolicy{Enabled: true, Retention: r}}
		if err := bad.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: Validate = %v, want ErrInvalid", name, err)
		}
	}
}

func TestShape_FrontendTelemetryCanonicalAndValidated(t *testing.T) {
	f := func(id string) ShapeFunnel {
		return ShapeFunnel{ID: id, Steps: []string{"a", "b"}, Window: "1h", By: "session"}
	}
	in := Shape{Kind: EnvSelfManaged, FrontendTelemetry: []ShapeFrontendTelemetry{
		{Frontend: "web", Sourcemaps: SourcemapsPublish}, // default: dropped
		{Frontend: "zeta", Funnels: []ShapeFunnel{f("b"), f("a")}},
		{Frontend: "admin", Sourcemaps: SourcemapsOff, ErrorsIgnore: []string{"AbortError", "re:^Failed"}},
	}}
	canon := in.Canonical()
	if len(canon.FrontendTelemetry) != 2 || canon.FrontendTelemetry[0].Frontend != "admin" || canon.FrontendTelemetry[1].Frontend != "zeta" {
		t.Fatalf("canonical = %+v, want admin and zeta (sorted), the default entry dropped", canon.FrontendTelemetry)
	}
	if z := canon.FrontendTelemetry[1]; z.Funnels[0].ID != "a" || z.Funnels[1].ID != "b" {
		t.Errorf("funnels not sorted by id: %+v", z.Funnels)
	}
	if in.FrontendTelemetry[1].Funnels[0].ID != "b" {
		t.Error("Canonical sorted the caller's funnels in place")
	}
	if err := canon.Validate(); err != nil {
		t.Fatalf("a valid declaration is refused: %v", err)
	}
	if !canon.HasFrontendTelemetry() || (Shape{Kind: EnvSelfManaged}).HasFrontendTelemetry() {
		t.Error("HasFrontendTelemetry wrong")
	}
	none := Shape{Kind: EnvSelfManaged, FrontendTelemetry: []ShapeFrontendTelemetry{{Frontend: "web"}}}
	if b, _ := none.Encode(); strings.Contains(string(b), "frontend_telemetry") {
		t.Errorf("a frontend that declares nothing must leave no key behind: %s", b)
	}

	for name, bad := range map[string]ShapeFrontendTelemetry{
		"no frontend":       {Funnels: []ShapeFunnel{f("a")}},
		"bad sourcemaps":    {Frontend: "web", Sourcemaps: "inline"},
		"empty filter":      {Frontend: "web", ErrorsIgnore: []string{" "}},
		"one step":          {Frontend: "web", Funnels: []ShapeFunnel{{ID: "a", Steps: []string{"x"}, Window: "1h", By: "user"}}},
		"bad by":            {Frontend: "web", Funnels: []ShapeFunnel{{ID: "a", Steps: []string{"x", "y"}, Window: "1h", By: "device"}}},
		"duplicate funnel":  {Frontend: "web", Funnels: []ShapeFunnel{f("a"), f("a")}},
		"funnel without id": {Frontend: "web", Funnels: []ShapeFunnel{f("")}},
	} {
		if err := (Shape{Kind: EnvSelfManaged, FrontendTelemetry: []ShapeFrontendTelemetry{bad}}).Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: Validate = %v, want ErrInvalid", name, err)
		}
	}
	twice := Shape{Kind: EnvSelfManaged, FrontendTelemetry: []ShapeFrontendTelemetry{{Frontend: "web", Sourcemaps: SourcemapsOff}, {Frontend: "web", Sourcemaps: SourcemapsPublic}}}
	if err := twice.Validate(); !errors.Is(err, ErrInvalid) {
		t.Errorf("a frontend named twice: Validate = %v", err)
	}
}

// A reader that predates the feature gets a shape it can strict-decode: the
// telemetry parts are gone, nothing else is, and the caller's shape is intact.
func TestBundleReader_LimitShapeWithholdsFrontendTelemetry(t *testing.T) {
	full := Shape{Kind: EnvSelfManaged,
		ObservabilityPolicy: &ShapeObservabilityPolicy{Enabled: true, OptIn: []string{ObsOptInReplay}, Retention: &ShapeRetention{ReplayDays: 7, ErrorsDays: 30, ProductEventsDays: 90}},
		FrontendTelemetry:   []ShapeFrontendTelemetry{{Frontend: "web", Sourcemaps: SourcemapsOff}},
	}
	old := (&BundleReader{}).LimitShape(full)
	if old.FrontendTelemetry != nil || old.ObservabilityPolicy.Retention != nil || !old.ObservabilityPolicy.OptedIn(ObsOptInReplay) {
		t.Errorf("an old reader's shape = %+v", old)
	}
	if full.FrontendTelemetry == nil || full.ObservabilityPolicy.Retention == nil {
		t.Error("LimitShape modified its argument")
	}
	if got := (&BundleReader{Features: BundleDocFeatures()}).LimitShape(full); !reflect.DeepEqual(got, full) {
		t.Errorf("a reader of every feature must get the shape whole: %+v", got)
	}
	if got := (*BundleReader)(nil).LimitShape(full); !reflect.DeepEqual(got, full) {
		t.Error("the nil reader (this forge's own ledger) must get the shape whole")
	}
}
