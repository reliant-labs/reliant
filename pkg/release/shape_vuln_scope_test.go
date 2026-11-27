package release

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestVulnExemptScopeCovers(t *testing.T) {
	t.Parallel()
	o := VulnOccurrence{PackageName: "stdlib", PackagePURL: "pkg:golang/stdlib@go1.24.1", Artifact: "api", Location: "/usr/local/bin/grype"}
	for name, tc := range map[string]struct {
		e    ShapeVulnScopedExempt
		o    VulnOccurrence
		want bool
	}{
		"unscoped":                     {ShapeVulnScopedExempt{}, o, true},
		"package name":                 {ShapeVulnScopedExempt{Packages: []string{"stdlib"}}, o, true},
		"purl exactly":                 {ShapeVulnScopedExempt{Packages: []string{"pkg:golang/stdlib@go1.24.1"}}, o, true},
		"purl prefix at @":             {ShapeVulnScopedExempt{Packages: []string{"pkg:golang/stdlib"}}, o, true},
		"purl prefix inside a name":    {ShapeVulnScopedExempt{Packages: []string{"pkg:golang/std"}}, o, false},
		"another package":              {ShapeVulnScopedExempt{Packages: []string{"golang.org/x/net"}}, o, false},
		"one of two packages":          {ShapeVulnScopedExempt{Packages: []string{"golang.org/x/net", "stdlib"}}, o, true},
		"artifact":                     {ShapeVulnScopedExempt{Artifacts: []string{"api"}}, o, true},
		"another artifact":             {ShapeVulnScopedExempt{Artifacts: []string{"worker"}}, o, false},
		"artifact unknown":             {ShapeVulnScopedExempt{Artifacts: []string{"api"}}, VulnOccurrence{PackageName: "stdlib", Location: "/usr/local/bin/grype"}, false},
		"location":                     {ShapeVulnScopedExempt{Locations: []string{"/usr/local/bin/grype"}}, o, true},
		"location directory":           {ShapeVulnScopedExempt{Locations: []string{"/usr/local/bin/"}}, o, true},
		"location prefix not a dir":    {ShapeVulnScopedExempt{Locations: []string{"/usr/local/bin/gr"}}, o, false},
		"location unknown":             {ShapeVulnScopedExempt{Locations: []string{"/usr/local/bin/grype"}}, VulnOccurrence{PackageName: "stdlib", Artifact: "api"}, false},
		"all three name it":            {ShapeVulnScopedExempt{Packages: []string{"stdlib"}, Artifacts: []string{"api"}, Locations: []string{"/usr/local/bin/grype"}}, o, true},
		"two name it, one does not":    {ShapeVulnScopedExempt{Packages: []string{"stdlib"}, Artifacts: []string{"worker"}, Locations: []string{"/usr/local/bin/grype"}}, o, false},
		"an empty package entry never": {ShapeVulnScopedExempt{Packages: []string{""}}, o, false},
	} {
		if got := tc.e.ScopeCovers(tc.o); got != tc.want {
			t.Errorf("%s: ScopeCovers = %v, want %v", name, got, tc.want)
		}
	}
}

func scopedPolicy(xs ...ShapeVulnScopedExempt) *ShapeDeliveryPolicy {
	p := DefaultDeliveryPolicy()
	p.VulnGate.ScopedExemptions = xs
	return &p
}

func TestShape_ScopedExemptions(t *testing.T) {
	t.Parallel()
	grype := ShapeVulnScopedExempt{ID: "GO-2026-6859", Reason: "r", Expires: "2026-12-31", Locations: []string{"/usr/local/bin/syft", "/usr/local/bin/grype"}}
	t.Run("valid, recorded, and a separate declaration from an unscoped one", func(t *testing.T) {
		p := scopedPolicy(grype)
		p.VulnGate.Exemptions = []ShapeVulnExempt{{ID: "GO-2026-6859", Reason: "r", Expires: "2026-12-31"}}
		if err := (Shape{Kind: EnvLocal, DeliveryPolicy: p}).Canonical().Validate(); err != nil {
			t.Fatal(err)
		}
		if p.IsDefault() {
			t.Fatal("a policy with a scoped exemption reads as the default")
		}
	})
	t.Run("canonical puts each entry in its list and sorts the scope", func(t *testing.T) {
		unscoped := ShapeVulnScopedExempt{ID: "CVE-2026-1", Reason: "r", Expires: "2026-12-31"}
		p := DefaultDeliveryPolicy()
		p.VulnGate.ScopedExemptions = []ShapeVulnScopedExempt{grype, unscoped}
		got := p.Resolved().VulnGate
		if len(got.Exemptions) != 1 || got.Exemptions[0].ID != unscoped.ID || len(got.ScopedExemptions) != 1 || got.ScopedExemptions[0].Locations[0] != "/usr/local/bin/grype" {
			t.Fatalf("canonical vuln_gate = %+v", got)
		}
	})
	t.Run("absent from the wire when none is declared", func(t *testing.T) {
		b, err := json.Marshal(DefaultDeliveryPolicy().VulnGate)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "scoped_exemptions") || strings.Contains(string(b), "packages") {
			t.Fatalf("an unscoped gate encodes scope keys: %s", b)
		}
	})
	for name, tc := range map[string]struct {
		p    *ShapeDeliveryPolicy
		want string
	}{
		"same scope twice":  {scopedPolicy(grype, ShapeVulnScopedExempt{ID: "GO-2026-6859", Reason: "s", Expires: "2026-12-01", Locations: []string{"/usr/local/bin/grype", "/usr/local/bin/syft"}}), "twice with the same scope"},
		"relative location": {scopedPolicy(ShapeVulnScopedExempt{ID: "CVE-2026-1", Reason: "r", Expires: "2026-12-31", Locations: []string{"bin/grype"}}), "not an absolute path"},
		"empty artifact":    {scopedPolicy(ShapeVulnScopedExempt{ID: "CVE-2026-1", Reason: "r", Expires: "2026-12-31", Artifacts: []string{""}}), "artifacts entry"},
		"padded package":    {scopedPolicy(ShapeVulnScopedExempt{ID: "CVE-2026-1", Reason: "r", Expires: "2026-12-31", Packages: []string{" stdlib"}}), "packages entry"},
		"scoped no reason":  {scopedPolicy(ShapeVulnScopedExempt{ID: "CVE-2026-1", Expires: "2026-12-31", Artifacts: []string{"api"}}), "no reason"},
	} {
		err := Shape{Kind: EnvLocal, DeliveryPolicy: tc.p}.Canonical().Validate()
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want ErrInvalid containing %q", name, err, tc.want)
		}
	}
}

// A reader that does not decode scoped exemptions is sent the policy without
// them (so it judges the findings they cover as unexempted), and nothing
// else of the policy changes; a policy that was default but for them is
// sent as no policy.
func TestScopedExemptionsAreAFeature(t *testing.T) {
	t.Parallel()
	grype := ShapeVulnScopedExempt{ID: "GO-2026-6859", Reason: "r", Expires: "2026-12-31", Artifacts: []string{"api"}}
	p := scopedPolicy(grype)
	p.VulnGate.Mode = DeliveryEnforce
	old := &BundleReader{Features: []string{}}
	got := old.LimitShape(Shape{Kind: EnvLocal, DeliveryPolicy: p})
	if got.DeliveryPolicy == nil || got.DeliveryPolicy.VulnGate.ScopedExemptions != nil || got.DeliveryPolicy.VulnGate.Mode != DeliveryEnforce {
		t.Fatalf("limited policy = %+v", got.DeliveryPolicy)
	}
	if len(p.VulnGate.ScopedExemptions) != 1 {
		t.Fatal("LimitShape modified the policy it was given")
	}
	if got := old.LimitShape(Shape{Kind: EnvLocal, DeliveryPolicy: scopedPolicy(grype)}); got.DeliveryPolicy != nil {
		t.Fatalf("a policy default but for scoped exemptions was sent as %+v", got.DeliveryPolicy)
	}
	if got := (&BundleReader{Features: []string{BundleFeatureVulnScopedExemptions}}).LimitShape(Shape{Kind: EnvLocal, DeliveryPolicy: p}); len(got.DeliveryPolicy.VulnGate.ScopedExemptions) != 1 {
		t.Fatal("a reader of scoped exemptions was not sent them")
	}
}
