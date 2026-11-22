package v1alpha1

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// Named same-origin APIs (StaticSiteSpec.APIs): a name the route can carry,
// a workload reference each, a bound, and no runtime config key that would
// collide with config.js's `apis` object.
func TestStaticSiteSpec_ValidatesNamedAPIs(t *testing.T) {
	ok := StaticSiteSpec{APIs: map[string]WorkloadURLRef{"billing": {Name: "billing-api"}, "a": {Name: "auth"}}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid apis refused: %v", err)
	}
	for _, bad := range []string{"Billing", "1st", "-x", "with_underscore", "a.b", "", strings.Repeat("a", 32)} {
		mustFail(t, StaticSiteSpec{APIs: map[string]WorkloadURLRef{bad: {Name: "api"}}}.Validate(), "api name")
	}
	mustFail(t, StaticSiteSpec{APIs: map[string]WorkloadURLRef{"billing": {Name: "https://x"}}}.Validate(), "apis.billing: workloadURL.name")
	big := map[string]WorkloadURLRef{}
	for i := 0; i <= MaxSiteAPIs; i++ {
		big[fmt.Sprintf("a%d", i)] = WorkloadURLRef{Name: "api"}
	}
	mustFail(t, StaticSiteSpec{APIs: big}.Validate(), "at most 16")
	lit := "x"
	mustFail(t, StaticSiteSpec{
		APIs:          map[string]WorkloadURLRef{"billing": {Name: "api"}},
		RuntimeConfig: map[string]RuntimeConfigValue{"apis": {Value: &lit}},
	}.Validate(), "runtimeConfig.apis collides")
	// Without named APIs a runtime config key "apis" is the author's own.
	if err := (StaticSiteSpec{RuntimeConfig: map[string]RuntimeConfigValue{"apis": {Value: &lit}}}).Validate(); err != nil {
		t.Errorf("runtimeConfig.apis without named APIs refused: %v", err)
	}
}

// The field is additive: a spec without it encodes exactly as before, and
// one with it round-trips through DeepCopy without sharing the map.
func TestStaticSiteSpec_NamedAPIsWire(t *testing.T) {
	b, err := json.Marshal(StaticSiteSpec{BasePath: "/x"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "apis") {
		t.Errorf("a spec without apis encodes the key: %s", b)
	}
	in := StaticSiteSpec{APIs: map[string]WorkloadURLRef{"billing": {Name: "billing-api"}}}
	b, _ = json.Marshal(in)
	if want := `{"apis":{"billing":{"name":"billing-api"}}}`; string(b) != want {
		t.Errorf("wire = %s, want %s", b, want)
	}
	out := in.DeepCopy()
	out.APIs["billing"] = WorkloadURLRef{Name: "changed"}
	if in.APIs["billing"].Name != "billing-api" {
		t.Error("DeepCopy shares the apis map")
	}
}
