package cli

import (
	"strings"
	"testing"
)

func TestDebugDashboardRecipeRemainsDiscoverable(t *testing.T) {
	body, err := RenderSkillContentAt("", "debug/dashboards", SkillAudienceForge, "reliant forge")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"obs dashboards audit", "data_outside_window", "data_in_other_env", "probe_failed"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("dashboard skill lacks %q", want)
		}
	}
}
