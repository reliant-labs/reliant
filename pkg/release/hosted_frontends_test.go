package release

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func optOut(name string) ShapeHostedFrontend {
	return ShapeHostedFrontend{Frontend: name, ReleaseURLs: new(bool)}
}

// TestShapeHostedFrontendsCanonical: only opt-outs are recorded, sorted, so a
// shape that spells the default out encodes and hashes like one that declares
// nothing, and a shape with no opt-out carries no key at all.
func TestShapeHostedFrontendsCanonical(t *testing.T) {
	on := true
	s := Shape{Kind: EnvPersistent, HostedFrontends: []ShapeHostedFrontend{
		optOut("web"), {Frontend: "admin", ReleaseURLs: &on}, {Frontend: "docs"}, optOut("blog"),
	}}
	got := s.Canonical().HostedFrontends
	want := []ShapeHostedFrontend{optOut("blog"), optOut("web")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("canonical hosted_frontends = %+v, want %+v", got, want)
	}
	if off := s.ReleaseURLsDisabled(); !reflect.DeepEqual(off, []string{"blog", "web"}) {
		t.Errorf("ReleaseURLsDisabled = %v, want [blog web]", off)
	}

	defaults := Shape{Kind: EnvPersistent, HostedFrontends: []ShapeHostedFrontend{{Frontend: "admin", ReleaseURLs: &on}}}
	raw, err := json.Marshal(defaults.Canonical())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "hosted_frontends") {
		t.Errorf("a shape declaring only defaults encodes hosted_frontends: %s", raw)
	}
}

func TestShapeHostedFrontendsValidate(t *testing.T) {
	for name, fs := range map[string][]ShapeHostedFrontend{
		"no frontend": {{ReleaseURLs: new(bool)}},
		"twice":       {optOut("web"), optOut("web")},
	} {
		if err := (Shape{Kind: EnvPersistent, HostedFrontends: fs}).Validate(); err == nil {
			t.Errorf("%s: Validate = nil, want a refusal", name)
		}
	}
	if err := (Shape{Kind: EnvPersistent, HostedFrontends: []ShapeHostedFrontend{optOut("web")}}).Validate(); err != nil {
		t.Errorf("one opt-out: Validate = %v", err)
	}
}

// TestBundleReader_CheckReleaseURLs: an opt-out is refused only to a reader
// that serves release URLs but cannot read it; every other reader passes, and
// one that does not decode it is sent the shape without the key.
func TestBundleReader_CheckReleaseURLs(t *testing.T) {
	s := Shape{Kind: EnvPersistent, HostedFrontends: []ShapeHostedFrontend{optOut("web")}}
	for name, r := range map[string]*BundleReader{
		"machine ledger":      nil,
		"serves no URLs":      {},
		"decodes the opt-out": {Features: []string{BundleFeatureReleaseURLs, CapabilityServesReleaseURLs}},
	} {
		if err := r.CheckReleaseURLs(s); err != nil {
			t.Errorf("%s: CheckReleaseURLs = %v, want nil", name, err)
		}
	}
	err := (&BundleReader{Features: []string{CapabilityServesReleaseURLs}}).CheckReleaseURLs(s)
	if err == nil || !strings.Contains(err.Error(), "web") || !strings.Contains(err.Error(), BundleFeatureReleaseURLs) {
		t.Fatalf("serves without decoding: CheckReleaseURLs = %v, want a refusal naming the frontend and the feature", err)
	}
	if err := (&BundleReader{Features: []string{CapabilityServesReleaseURLs}}).CheckReleaseURLs(Shape{Kind: EnvPersistent}); err != nil {
		t.Errorf("no opt-out: CheckReleaseURLs = %v, want nil", err)
	}
	if got := (&BundleReader{}).LimitShape(s).HostedFrontends; got != nil {
		t.Errorf("LimitShape for a reader without %s kept hosted_frontends: %+v", BundleFeatureReleaseURLs, got)
	}
	if got := (&BundleReader{Features: []string{BundleFeatureReleaseURLs}}).LimitShape(s).HostedFrontends; len(got) != 1 {
		t.Errorf("LimitShape for a reader with %s dropped hosted_frontends: %+v", BundleFeatureReleaseURLs, got)
	}
}
