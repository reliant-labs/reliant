package obsdash

import (
	"reflect"
	"strings"
	"testing"
)

// TestOverviewScopesToDeployedIdentities pins what forge generate writes for
// the overview: the deployed service.names and workloads it found across the
// environments, never the no-deployed-process sentinel, and no per-process
// dashboards (those are the selected environment's, built at bundle time).
// The overview once filtered every panel on the sentinel after generate, and
// every local dashboard drew nothing.
func TestOverviewScopesToDeployedIdentities(t *testing.T) {
	in := fixture()
	in.Processes = nil
	in.Deployed = Identities{ServiceNames: []string{"shop-api", "shop-worker"}, Workloads: []string{"shop-api", "shop-worker"}}
	sp := Build(in)
	overview := sp.Dashboards[0]
	if overview.ID != "overview" {
		t.Fatalf("first dashboard = %s, want the overview", overview.ID)
	}
	for _, d := range sp.Dashboards {
		if strings.HasPrefix(d.ID, "process-") {
			t.Errorf("identities alone built a process dashboard: %s", d.ID)
		}
	}
	scoped := 0
	for _, p := range overview.Panels {
		for _, w := range p.Where {
			if w.Value == "__forge_no_deployed_process__" {
				t.Errorf("panel %s still filters on the no-deployed-process sentinel", p.ID)
			}
			if w.Field == FieldService && w.Op == OpIn {
				scoped++
				if !reflect.DeepEqual(w.Values, []string{"shop-api", "shop-worker"}) {
					t.Errorf("panel %s scoped to %v, want the deployed service.names", p.ID, w.Values)
				}
			}
		}
	}
	if scoped == 0 {
		t.Fatal("no overview panel is scoped to the deployed service.names")
	}

	// With nothing deployed anywhere the sentinel is still the honest answer.
	in.Deployed = Identities{}
	empty := Build(in).Dashboards[0]
	if p := empty.Panels[0]; len(p.Where) == 0 || p.Where[0].Value != "__forge_no_deployed_process__" {
		t.Errorf("an undeployed project's overview = %+v, want the sentinel", p.Where)
	}
}
