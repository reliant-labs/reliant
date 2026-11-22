package graphkit_test

import (
	"slices"
	"testing"

	"github.com/reliant-labs/forge/pkg/graphkit"
)

func TestPlan_StartAndStopAreTheSupervisedNodes(t *testing.T) {
	p := graphkit.Plan{
		{Name: "probe_listener", Run: true, Drain: true, Close: true, Ready: true},
		{Name: "db", Close: true, Ready: true},
		{Name: "routes"},
		{Name: "tick", Run: true},
		{Name: "api_listener", Run: true, Drain: true, Ready: true},
	}
	if got := p.Names(); !slices.Equal(got, []string{"probe_listener", "db", "routes", "tick", "api_listener"}) {
		t.Errorf("Names = %v", got)
	}
	if got := p.Start(); !slices.Equal(got, []string{"probe_listener", "db", "tick", "api_listener"}) {
		t.Errorf("Start = %v: a node with no lifecycle is not supervised", got)
	}
	if got := p.Stop(); !slices.Equal(got, []string{"api_listener", "tick", "db", "probe_listener"}) {
		t.Errorf("Stop = %v: the reverse of Start", got)
	}
	if got := (graphkit.Plan{}).Stop(); len(got) != 0 {
		t.Errorf("an empty plan stops nothing: %v", got)
	}
}
