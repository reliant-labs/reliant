package release

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestUnitWaves(t *testing.T) {
	cases := []struct {
		name     string
		declared []DeliveryUnit
		present  []string
		want     [][]string
	}{
		{
			name: "no units declared is the one base wave",
			want: [][]string{{"base"}},
		},
		{
			name:    "an undeclared unit is after base",
			present: []string{"", "extras"},
			want:    [][]string{{"base"}, {"extras"}},
		},
		{
			// control-plane's NATS broker: the permissions roll before every
			// consumer of them.
			name: "base after a unit that waits for nothing",
			declared: []DeliveryUnit{
				{Name: "nats-broker", After: []string{}},
				{Name: "base", After: []string{"nats-broker"}},
			},
			present: []string{"base", "nats-broker"},
			want:    [][]string{{"nats-broker"}, {"base"}},
		},
		{
			name: "independent units share a wave, sorted",
			declared: []DeliveryUnit{
				{Name: "b", After: []string{}},
				{Name: "a", After: []string{}},
				{Name: "c", After: []string{"a", "b"}},
			},
			want: [][]string{{"a", "b", "base"}, {"c"}},
		},
		{
			name: "a declared unit with nothing rendered still orders the chain",
			declared: []DeliveryUnit{
				{Name: "first", After: []string{}},
				{Name: "middle", After: []string{"first"}},
				{Name: "base", After: []string{"middle"}},
			},
			present: []string{"first", "base"},
			want:    [][]string{{"first"}, {"middle"}, {"base"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := UnitWaves(tc.declared, tc.present)
			if err != nil {
				t.Fatalf("UnitWaves: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("waves = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestUnitWaves_Refusals(t *testing.T) {
	cases := map[string]struct {
		declared []DeliveryUnit
		want     string
	}{
		"cycle": {
			declared: []DeliveryUnit{{Name: "a", After: []string{"b"}}, {Name: "b", After: []string{"a"}}},
			want:     "cycle among a, b",
		},
		"base after its own dependent": {
			declared: []DeliveryUnit{{Name: "x"}, {Name: "base", After: []string{"x"}}},
			want:     "cycle among base, x",
		},
		"typo": {
			declared: []DeliveryUnit{{Name: "a", After: []string{"nats-brokr"}}},
			want:     `"nats-brokr", which nothing declares or renders`,
		},
		"after itself": {
			declared: []DeliveryUnit{{Name: "a", After: []string{"a"}}},
			want:     "after itself",
		},
		"twice": {
			declared: []DeliveryUnit{{Name: "a"}, {Name: "a"}},
			want:     "declared twice",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := UnitWaves(tc.declared, nil)
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want ErrInvalid containing %q", err, tc.want)
			}
		})
	}
}

// A move between two units forge applies directly is not a stop: no reconciler
// prunes the object out of the tree it leaves, and direct apply only rolls it in
// its new unit's wave. With a reconciler on either side it still is.
//
// Mutation that fails it: drop the forgeAppliesTree branch in BuildPlan (the
// direct-apply rows go back to stop), or make it ignore the hub (the hub rows
// stop being one).
func TestBuildPlan_TreeMoveStopsOnlyUnderAReconciler(t *testing.T) {
	moved := func(tree string) ShapeObject {
		o := obj("StatefulSet", "nats-cluster", "a")
		o.Tree = tree
		return o
	}
	forgeUnits := []ShapeUnit{{Name: "base", Applier: UnitApplierForge}, {Name: "nats-broker", Applier: UnitApplierForge}}
	hubBroker := []ShapeUnit{{Name: "base", Applier: UnitApplierForge}, {Name: "nats-broker", Applier: UnitApplierHub}}
	cases := []struct {
		name                 string
		liveUnits, candUnits []ShapeUnit
		liveApplier          string
		want                 FindingClass
	}{
		{name: "both units forge-applied", liveUnits: forgeUnits, candUnits: forgeUnits, want: ClassInfo},
		{name: "applier forge, no split yet on the live side", liveApplier: ApplierForge, candUnits: forgeUnits, want: ClassInfo},
		{name: "the unit it joins is the hub's", liveUnits: forgeUnits, candUnits: hubBroker, want: ClassStop},
		{name: "an env that declares nothing (the hub may converge it)", want: ClassStop},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			live := shapeOf(moved("base/rollout"))
			live.Units, live.Applier = tc.liveUnits, tc.liveApplier
			cand := shapeOf(moved("nats-broker/rollout"))
			cand.Units = tc.candUnits
			if tc.liveApplier != "" && tc.candUnits == nil {
				cand.Applier = tc.liveApplier
			}
			p, err := BuildPlan(PlanInput{EnvironmentID: "e", BundleID: "b", Candidate: cand, Live: &live, Drift: noDrift()})
			if err != nil {
				t.Fatal(err)
			}
			var got []FindingClass
			for _, f := range p.Findings {
				if f.Code == FindingTreeMoved {
					got = append(got, f.Class)
				}
			}
			if len(got) != 1 || got[0] != tc.want {
				t.Fatalf("tree_moved classes = %v, want [%s] (findings %+v)", got, tc.want, p.Findings)
			}
		})
	}
}
