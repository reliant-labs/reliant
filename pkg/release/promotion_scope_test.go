package release

import (
	"reflect"
	"testing"
)

// D5: a scoped promotion records what it moved, and the append rule treats a
// promotion of the bound release as a real move when it moves something the
// current entry did not.
func TestDecideWithScopes(t *testing.T) {
	p := func(release string, targets ...string) Promotion {
		return Promotion{Env: "prod", Release: release, Kind: KindPromote, Targets: targets}
	}
	for _, tc := range []struct {
		name      string
		current   Promotion
		requested Promotion
		appends   bool
	}{
		{"whole retry", p("v2"), p("v2"), false},
		{"scoped re-apply of a wholly bound release", p("v2"), p("v2", "api"), false},
		{"scoped retry", p("v2", "api"), p("v2", "api"), false},
		{"subset of the current scope", p("v2", "api", "web"), p("v2", "web"), false},
		{"scope moves something new", p("v2", "api"), p("v2", "web"), true},
		{"whole deploy completes a scoped one", p("v2", "api"), p("v2"), true},
		{"new release, scoped", p("v1"), p("v2", "api"), true},
		{"new release, whole", p("v1", "api"), p("v2"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			existing, err := Decide([]Promotion{tc.current}, tc.requested)
			if err != nil {
				t.Fatal(err)
			}
			if (existing == nil) != tc.appends {
				t.Fatalf("Decide appends=%v, want %v", existing == nil, tc.appends)
			}
		})
	}
}

func TestPromotionMoved(t *testing.T) {
	whole := Promotion{Release: "v2"}
	scoped := Promotion{Release: "v2", Targets: []string{"api", "web"}}
	if !whole.Moved("anything") || whole.Scoped() {
		t.Fatal("a whole-release promotion moves every component")
	}
	if !scoped.Scoped() || !scoped.Moved("web") || scoped.Moved("worker") {
		t.Fatal("a scoped promotion moves exactly its targets")
	}
}

func TestPromotionTargetsAreValidatedAndNormalized(t *testing.T) {
	if got := NormalizeTargets([]string{" web", "api", "web", ""}); !reflect.DeepEqual(got, []string{"api", "web"}) {
		t.Fatalf("NormalizeTargets = %v", got)
	}
	if NormalizeTargets(nil) != nil {
		t.Fatal("an unscoped deploy normalizes to nil")
	}
	base := Promotion{Env: "prod", Release: "v2", Kind: KindPromote}
	for _, tc := range []struct {
		targets []string
		ok      bool
	}{
		{[]string{"api", "web"}, true},
		{[]string{"web", "api"}, false},
		{[]string{"api", "api"}, false},
		{[]string{" api"}, false},
		{[]string{""}, false},
		{[]string{"a\nb"}, false},
	} {
		p := base
		p.Targets = tc.targets
		if err := p.Validate(); (err == nil) != tc.ok {
			t.Fatalf("targets %q: Validate() = %v, want ok=%v", tc.targets, err, tc.ok)
		}
	}
}
