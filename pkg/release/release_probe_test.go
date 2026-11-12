package release

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

func releaseProbe(target string) ShapeReleaseProbe {
	return ShapeReleaseProbe{
		Target: target, Kind: ReleaseProbeKindFrontend, Paths: []string{"/"}, Method: "GET",
		RatePerMin: 4, TimeoutSeconds: 10, Source: ReleaseProbeSourceDefault,
	}
}

// TestReleaseProbeValidate holds the shape to forge.Probe's KCL rules, so a
// control plane that validates a recorded shape refuses what KCL would have.
func TestReleaseProbeValidate(t *testing.T) {
	if err := releaseProbe("web").validate(); err != nil {
		t.Fatalf("the default frontend probe is refused: %v", err)
	}
	workload := ShapeReleaseProbe{
		Target: "api", Kind: ReleaseProbeKindWorkload, Paths: []string{"/ok", "/flow-health"}, Method: "HEAD",
		RatePerMin: 12, TimeoutSeconds: 4, ExpectStatus: []string{"200", "3xx"}, Source: ReleaseProbeSourceUser,
	}
	if err := workload.validate(); err != nil {
		t.Fatalf("a declared workload probe is refused: %v", err)
	}
	five := []string{"/a", "/b", "/c", "/d", "/e"}
	if p := releaseProbe("web"); func() error { p.Paths = five; return p.validate() }() != nil {
		t.Fatal("five paths are refused")
	}

	for name, tc := range map[string]struct {
		mutate func(*ShapeReleaseProbe)
		want   string
	}{
		"bad target":       {func(p *ShapeReleaseProbe) { p.Target = "Web!" }, "target"},
		"empty target":     {func(p *ShapeReleaseProbe) { p.Target = "" }, "target"},
		"unknown kind":     {func(p *ShapeReleaseProbe) { p.Kind = "service" }, "kind"},
		"no paths":         {func(p *ShapeReleaseProbe) { p.Paths = nil }, "1 to 5 paths"},
		"six paths":        {func(p *ShapeReleaseProbe) { p.Paths = append(slices.Clone(five), "/f") }, "1 to 5 paths"},
		"duplicate path":   {func(p *ShapeReleaseProbe) { p.Paths = []string{"/", "/"} }, "twice"},
		"relative path":    {func(p *ShapeReleaseProbe) { p.Paths = []string{"health"} }, "start with '/'"},
		"query":            {func(p *ShapeReleaseProbe) { p.Paths = []string{"/?x=1"} }, "query"},
		"fragment":         {func(p *ShapeReleaseProbe) { p.Paths = []string{"/#top"} }, "fragment"},
		"dot dot":          {func(p *ShapeReleaseProbe) { p.Paths = []string{"/a/../b"} }, "'..'"},
		"authority":        {func(p *ShapeReleaseProbe) { p.Paths = []string{"//attacker.example/path"} }, "'//'"},
		"inner slashes":    {func(p *ShapeReleaseProbe) { p.Paths = []string{"/a//b"} }, "'//'"},
		"backslash host":   {func(p *ShapeReleaseProbe) { p.Paths = []string{`/\\attacker.example/path`} }, "character"},
		"backslash":        {func(p *ShapeReleaseProbe) { p.Paths = []string{`/a\\b`} }, "character"},
		"dot segment":      {func(p *ShapeReleaseProbe) { p.Paths = []string{"/a/./b"} }, "'.' segment"},
		"encoded dot dot":  {func(p *ShapeReleaseProbe) { p.Paths = []string{"/a/%2e%2e/b"} }, "percent-encoded"},
		"encoded slash":    {func(p *ShapeReleaseProbe) { p.Paths = []string{"/%2fattacker.example"} }, "percent-encoded"},
		"control char":     {func(p *ShapeReleaseProbe) { p.Paths = []string{"/a\tb"} }, "character"},
		"space":            {func(p *ShapeReleaseProbe) { p.Paths = []string{"/a b"} }, "character"},
		"non-ascii":        {func(p *ShapeReleaseProbe) { p.Paths = []string{"/caf\u00e9"} }, "character"},
		"long path":        {func(p *ShapeReleaseProbe) { p.Paths = []string{"/" + strings.Repeat("a", MaxReleaseProbePathLen)} }, "256"},
		"post":             {func(p *ShapeReleaseProbe) { p.Method = "POST" }, "method"},
		"no method":        {func(p *ShapeReleaseProbe) { p.Method = "" }, "method"},
		"rate zero":        {func(p *ShapeReleaseProbe) { p.RatePerMin = 0 }, "rate_per_min"},
		"rate over":        {func(p *ShapeReleaseProbe) { p.RatePerMin = 13 }, "rate_per_min"},
		"timeout zero":     {func(p *ShapeReleaseProbe) { p.TimeoutSeconds = 0 }, "timeout"},
		"timeout over":     {func(p *ShapeReleaseProbe) { p.RatePerMin, p.TimeoutSeconds = 1, 31 }, "timeout"},
		"timeout interval": {func(p *ShapeReleaseProbe) { p.RatePerMin, p.TimeoutSeconds = 6, 10 }, "interval"},
		"bad status":       {func(p *ShapeReleaseProbe) { p.ExpectStatus = []string{"20x"} }, "expect_status"},
		"unknown source":   {func(p *ShapeReleaseProbe) { p.Source = "guess" }, "source"},
	} {
		p := releaseProbe("web")
		tc.mutate(&p)
		err := p.validate()
		if err == nil || !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want ErrInvalid mentioning %q", name, err, tc.want)
		}
	}
}

// One job per (env, target): a shape naming a target twice is refused.
func TestReleaseProbesRefuseADuplicateTarget(t *testing.T) {
	err := validateReleaseProbes([]ShapeReleaseProbe{releaseProbe("web"), releaseProbe("web")})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("err = %v, want ErrInvalid naming the target twice", err)
	}
	if err := validateReleaseProbes(nil); err != nil {
		t.Fatalf("no probes is refused: %v", err)
	}
}

// Canonical sorts by target and spells the default statuses as absent, so a
// producer that lists them hashes the same as one that leaves them out. The
// order of paths is the request rotation, so it is kept.
func TestReleaseProbesCanonical(t *testing.T) {
	if canonicalReleaseProbes(nil) != nil || canonicalReleaseProbes([]ShapeReleaseProbe{}) != nil {
		t.Fatal("no probes canonicalizes to something")
	}
	web := releaseProbe("web")
	web.ExpectStatus = []string{"3xx", "2xx"}
	api := releaseProbe("api")
	api.Kind, api.Source, api.Paths, api.ExpectStatus = ReleaseProbeKindWorkload, ReleaseProbeSourceUser, []string{"/z", "/a"}, []string{"204", "200"}
	in := []ShapeReleaseProbe{web, api}
	got := canonicalReleaseProbes(in)
	if len(got) != 2 || got[0].Target != "api" || got[1].Target != "web" {
		t.Fatalf("not sorted by target: %+v", got)
	}
	if got[1].ExpectStatus != nil {
		t.Errorf("the default statuses were recorded: %v", got[1].ExpectStatus)
	}
	if want := []string{"200", "204"}; !reflect.DeepEqual(got[0].ExpectStatus, want) {
		t.Errorf("declared statuses = %v, want %v", got[0].ExpectStatus, want)
	}
	if want := []string{"/z", "/a"}; !reflect.DeepEqual(got[0].Paths, want) {
		t.Errorf("paths reordered: %v, want %v", got[0].Paths, want)
	}
	if in[0].Target != "web" || in[0].ExpectStatus[0] != "3xx" {
		t.Error("canonicalReleaseProbes modified its input")
	}
}

func TestReleaseProbeAccessors(t *testing.T) {
	p := releaseProbe("web")
	if p.Interval() != 15*time.Second || p.Timeout() != 10*time.Second {
		t.Errorf("interval %s timeout %s, want 15s and 10s", p.Interval(), p.Timeout())
	}
	if (ShapeReleaseProbe{}).Interval() != 0 {
		t.Error("a zero rate must not divide by zero")
	}
	for status, want := range map[int]bool{200: true, 204: true, 301: true, 404: false, 429: false, 500: false} {
		if p.StatusOK(status) != want {
			t.Errorf("default StatusOK(%d) = %v, want %v", status, !want, want)
		}
	}
	p.ExpectStatus = []string{"200", "4xx"}
	for status, want := range map[int]bool{200: true, 204: false, 301: false, 404: true, 500: false} {
		if p.StatusOK(status) != want {
			t.Errorf("declared StatusOK(%d) = %v, want %v", status, !want, want)
		}
	}
}

// An env with no probes encodes without the key, so every shape recorded
// before the field hashes as it did; one with probes round-trips and is
// validated with the shape.
func TestShape_ReleaseProbes(t *testing.T) {
	b, err := Shape{Kind: EnvLocal}.Canonical().Encode()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "release_probes") {
		t.Fatalf("a shape without probes encodes the key: %s", b)
	}
	in := Shape{Kind: EnvSelfManaged, ReleaseProbes: []ShapeReleaseProbe{releaseProbe("web")}}
	c := in.Canonical()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	b, err = c.Encode()
	if err != nil {
		t.Fatal(err)
	}
	want := `"release_probes":[{"target":"web","kind":"frontend","paths":["/"],"method":"GET","rate_per_min":4,"timeout_seconds":10,"source":"default"}]`
	if !strings.Contains(string(b), want) {
		t.Fatalf("encoded shape lacks %s:\n%s", want, b)
	}
	var out Shape
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out.ReleaseProbes, c.ReleaseProbes) {
		t.Errorf("round trip: got %+v want %+v", out.ReleaseProbes, c.ReleaseProbes)
	}
	bad := in
	bad.ReleaseProbes = []ShapeReleaseProbe{releaseProbe("web")}
	bad.ReleaseProbes[0].Method = "POST"
	if err := bad.Validate(); !errors.Is(err, ErrInvalid) {
		t.Errorf("Shape.Validate accepted a POST probe: %v", err)
	}
}

// A reader that predates release probes gets no probes, and no probe finding
// code in fail_on: its ShapeReleaseHealth.validate refuses a code it does not
// know, which would refuse the whole shape.
func TestLimitShapeWithholdsReleaseProbesFromAnOldReader(t *testing.T) {
	health := DefaultReleaseHealth()
	health.WindowSeconds = 1800
	health.FailOn = append(health.FailOn, HealthProbeLatency)
	full := Shape{Kind: EnvSelfManaged, ReleaseHealth: &health, ReleaseProbes: []ShapeReleaseProbe{releaseProbe("web")}}

	old := (&BundleReader{Features: []string{BundleFeatureReleaseHealth}}).LimitShape(full)
	if old.ReleaseProbes != nil {
		t.Errorf("a reader without %s was sent probes: %+v", BundleFeatureReleaseProbes, old.ReleaseProbes)
	}
	if old.ReleaseHealth == nil {
		t.Fatal("the release-health policy was dropped with the probes")
	}
	want := []string{HealthAlertFiring, HealthErrorRate, HealthNewIssue, HealthRegressedIssue}
	if !reflect.DeepEqual(old.ReleaseHealth.FailOn, want) || old.ReleaseHealth.WindowSeconds != 1800 {
		t.Errorf("old reader's policy = %+v, want fail_on %v and the declared window", old.ReleaseHealth, want)
	}
	if !slices.Contains(full.ReleaseHealth.FailOn, HealthProbeFailed) || len(full.ReleaseProbes) != 1 {
		t.Fatal("LimitShape modified the shape it was given")
	}
	if !full.HasOptionalParts() {
		t.Error("a shape with probes reports no optional parts, so its reader's features are never asked")
	}

	for name, r := range map[string]*BundleReader{
		"every feature": {Features: BundleDocFeatures()},
		"nil reader":    nil,
	} {
		if got := r.LimitShape(full); !reflect.DeepEqual(got, full) {
			t.Errorf("%s: LimitShape changed the shape:\n got %+v\nwant %+v", name, got, full)
		}
	}
	probesOnly := (&BundleReader{Features: []string{BundleFeatureReleaseProbes}}).LimitShape(full)
	if len(probesOnly.ReleaseProbes) != 1 || probesOnly.ReleaseHealth != nil {
		t.Errorf("a reader of release probes only: %+v", probesOnly)
	}
}

// A probed env that keeps the default policy records none: the default,
// which now fails on probe_failed, is implicit. A reader that predates probes
// but reads release health is sent that default explicitly, without the
// probe codes, so the policy it judges by is the producer's, stated, not its
// own default by coincidence. That explicit policy is exactly the default
// every release-health reader has had since F4a.
func TestLimitShapeStatesTheImplicitPolicyForAnOldReader(t *testing.T) {
	full := Shape{Kind: EnvSelfManaged, ReleaseProbes: []ShapeReleaseProbe{releaseProbe("web")}}
	preProbeDefault := ShapeReleaseHealth{
		WindowSeconds: 900, MinRequests: 50, MaxErrorRateIncrease: 0.01, MaxP95Ratio: 1.5, NewIssueMinEvents: 3,
		FailOn: []string{HealthAlertFiring, HealthErrorRate, HealthNewIssue, HealthRegressedIssue},
	}

	old := (&BundleReader{Features: []string{BundleFeatureReleaseHealth}}).LimitShape(full)
	if old.ReleaseProbes != nil {
		t.Errorf("probes sent to a reader without %s", BundleFeatureReleaseProbes)
	}
	if old.ReleaseHealth == nil || !reflect.DeepEqual(*old.ReleaseHealth, preProbeDefault) {
		t.Fatalf("old reader's policy = %+v, want the pre-probe default %+v", old.ReleaseHealth, preProbeDefault)
	}
	c := old.Canonical()
	if c.ReleaseHealth == nil || !reflect.DeepEqual(c.ReleaseHealth.FailOn, preProbeDefault.FailOn) {
		t.Errorf("Canonical dropped or changed the stated policy: %+v", c.ReleaseHealth)
	}
	if err := c.Validate(); err != nil {
		t.Errorf("the limited shape does not validate: %v", err)
	}
	if full.ReleaseHealth != nil {
		t.Fatal("LimitShape modified the shape it was given")
	}

	// A reader that does not read release health is sent none, as before.
	if none := (&BundleReader{}).LimitShape(full); none.ReleaseHealth != nil || none.ReleaseProbes != nil {
		t.Errorf("a reader with no features: policy %+v, probes %+v", none.ReleaseHealth, none.ReleaseProbes)
	}
	// A reader of probes keeps the implicit default.
	if cur := (&BundleReader{Features: []string{BundleFeatureReleaseHealth, BundleFeatureReleaseProbes}}).LimitShape(full); cur.ReleaseHealth != nil || len(cur.ReleaseProbes) != 1 {
		t.Errorf("a current reader: policy %+v, probes %+v", cur.ReleaseHealth, cur.ReleaseProbes)
	}
	// An env without probes is sent to an old reader exactly as before.
	if plain := (&BundleReader{Features: []string{BundleFeatureReleaseHealth}}).LimitShape(Shape{Kind: EnvSelfManaged}); plain.ReleaseHealth != nil {
		t.Errorf("an unprobed env gained a policy: %+v", plain.ReleaseHealth)
	}
}

// The release-probe codes are release-health findings; the default policy
// fails on a probe failure and warns on probe latency, as it warns on latency.
func TestReleaseProbeFindings(t *testing.T) {
	for _, c := range []string{HealthProbeFailed, HealthProbeLatency} {
		if !slices.Contains(HealthFindings, c) {
			t.Errorf("HealthFindings lacks %q", c)
		}
	}
	def := DefaultReleaseHealth()
	if !def.Fails(HealthProbeFailed) || def.Fails(HealthProbeLatency) || def.Fails(HealthLatency) {
		t.Errorf("default fail_on = %v: want probe_failed, not probe_latency or latency", def.FailOn)
	}
	h := DefaultReleaseHealth()
	h.FailOn = []string{HealthProbeLatency, HealthProbeFailed}
	if err := h.validate(); err != nil {
		t.Errorf("a policy failing on probe findings is refused: %v", err)
	}
}

// The default fail_on, and the set of findings, are declared twice: here and
// in forge.ReleaseHealth (kcl/schema.k). This holds them equal without a
// render, so it runs in the short tier; TestReleaseHealthDefaultsAgreeWithKCL
// (internal/cli) proves the same through a render.
func TestDefaultFailOnMatchesKCL(t *testing.T) {
	src, err := os.ReadFile("../../kcl/schema.k")
	if err != nil {
		t.Fatal(err)
	}
	list := func(re string) []string {
		t.Helper()
		m := regexp.MustCompile(re).FindSubmatch(src)
		if m == nil {
			t.Fatalf("kcl/schema.k has no match for %s", re)
		}
		var out []string
		if err := json.Unmarshal(append(append([]byte("["), m[1]...), ']'), &out); err != nil {
			t.Fatalf("%s: %v", re, err)
		}
		slices.Sort(out)
		return out
	}
	if got, want := list(`(?m)^_RELEASE_HEALTH_FINDINGS = \[(.*)\]$`), slices.Sorted(slices.Values(HealthFindings)); !reflect.DeepEqual(got, want) {
		t.Errorf("KCL _RELEASE_HEALTH_FINDINGS = %v, Go HealthFindings = %v", got, want)
	}
	if got, want := list(`(?m)^    fail_on: \[str\] = \[(.*)\]$`), DefaultReleaseHealth().canonical().FailOn; !reflect.DeepEqual(got, want) {
		t.Errorf("KCL ReleaseHealth.fail_on default = %v, Go DefaultReleaseHealth().FailOn = %v", got, want)
	}
}

// The names below are read by the control plane's prober, edge and judge and
// by forge's queries; a change to any value is a wire break between releases.
func TestReleaseProbeContractIsStable(t *testing.T) {
	for _, c := range []struct{ got, want string }{
		{ReleaseProbeKindFrontend, "frontend"},
		{ReleaseProbeKindWorkload, "workload"},
		{ReleaseProbeSourceUser, "user"},
		{ReleaseProbeSourceDefault, "default"},
		{ProbeHeader, "Forge-Probe"},
		{ProbeCacheBustParam, "_fprobe"},
		{ProbeEdgeHeader, "X-Reliant-Probe"},
		{ProbeAttr, "forge.probe"},
		{ProbeSource, "probe"},
		{ProbeMetricRequests, "reliant.probe.requests"},
		{ProbeMetricDuration, "reliant.probe.duration"},
		{ProbeAttrPath, "probe.path"},
		{ProbeAttrOutcome, "probe.outcome"},
		{ProbeAttrStatusClass, "status_class"},
		{ProbeAttrError, "error.type"},
		{ProbeOutcomeOK, "ok"},
		{ProbeOutcomeFailed, "failed"},
		{ProbeOutcomeThrottled, "throttled"},
		{ProbeOutcomeDegraded, "degraded"},
		{HealthProbeFailed, "probe_failed"},
		{HealthProbeLatency, "probe_latency"},
		{BundleFeatureReleaseProbes, "shape-release-probes"},
	} {
		if c.got != c.want {
			t.Errorf("contract value %q, want %q", c.got, c.want)
		}
	}
}
