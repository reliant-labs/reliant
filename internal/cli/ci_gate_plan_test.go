package cli

import (
	"reflect"
	"testing"

	"github.com/reliant-labs/forge/internal/cigate"
	"github.com/reliant-labs/forge/internal/config"
)

// --plan computes from every ci.gate key the gate run reads, so it never
// prints a plan the gate does not run.
func TestPlanAffectedInput_CarriesEveryGateKey(t *testing.T) {
	g := config.CIGateConfig{
		AffectedIgnore:  []string{"docs/**"},
		AffectedRules:   []config.CIGateAffectedRule{{Paths: []string{"proto/**"}}},
		AffectedReach:   &config.CIGateAffectedReach{Mode: "direct", TransitiveCap: 20},
		AffectedReaders: true,
		AffectedModules: cigate.AffectedModulesAuto,
	}
	in := planAffectedInput(g, "/r", "origin/main", "", nil, []string{"a.go"})
	if !reflect.DeepEqual(in.Ignore, g.AffectedIgnore) || len(in.Rules) != 1 || in.Reach != g.AffectedReach ||
		!in.Readers || in.Modules != cigate.AffectedModulesAuto || in.BaseRef != "origin/main" || in.Files[0] != "a.go" {
		t.Errorf("input = %+v, want every ci.gate key carried", in)
	}
}

// --plan reports each path-gated row and whether it runs.
func TestPlanChecks_ReportsPathGatedRows(t *testing.T) {
	checks := []cigate.Check{
		{Name: "unit-tests"},
		{Name: "electron", Paths: []string{"electron/**"}},
		{Name: "docs", Paths: []string{"docs/**"}},
	}
	got := planChecks(checks, cigate.Affected{Changed: []string{"electron/main.ts"}})
	if len(got) != 2 || got[0].Name != "electron" || !got[0].Runs || got[1].Name != "docs" || got[1].Runs || got[1].Note == "" {
		t.Errorf("checks = %+v, want electron runs and docs N/A with a note", got)
	}
}
