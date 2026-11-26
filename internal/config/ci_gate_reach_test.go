package config

import "testing"

func TestCIGateAffectedReach_Load(t *testing.T) {
	cfg, err := LoadProject([]byte(gateRulesBase+"    affected_reach:\n      mode: direct\n      transitive_cap: 20\n"), "forge.yaml")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if r := cfg.CI.Gate.AffectedReach; r == nil || r.Mode != "direct" || r.TransitiveCap != 20 {
		t.Fatalf("affected_reach = %+v", r)
	}
}

func TestCIGateAffectedReach_Invalid(t *testing.T) {
	for _, tc := range []struct {
		name, yaml string
		want       []string
	}{
		{"bad mode", "    affected_reach:\n      mode: near\n", []string{`ci.gate.affected_reach.mode "near" is not all or direct`}},
		{"negative cap", "    affected_reach:\n      mode: direct\n      transitive_cap: -1\n", []string{"transitive_cap is negative"}},
		{"cap without direct", "    affected_reach:\n      transitive_cap: 5\n", []string{"transitive_cap is set without mode: direct"}},
		{"unknown key", "    affected_reach:\n      mode: direct\n      cap: 5\n", []string{"unknown key", "affected_reach.cap"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadProject([]byte(gateRulesBase+tc.yaml), "forge.yaml")
			ve := requireValidationError(t, err)
			if !containsAll(ve.Error(), tc.want...) {
				t.Errorf("error missing %q:\n%s", tc.want, ve.Error())
			}
		})
	}
}
