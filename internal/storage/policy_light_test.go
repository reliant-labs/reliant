package storage

import "testing"

// A static-site build is admitted under a small reserve, never a larger one
// than the policy's own (obs 4dea8e9e).
func TestLightBuildCapsTheHostReserve(t *testing.T) {
	p := DefaultPolicy()
	p.HostReserveGiB = 60
	if got := p.LightBuild().HostReserveGiB; got != LightBuildReserveGiB {
		t.Errorf("light reserve = %d, want %d", got, LightBuildReserveGiB)
	}
	if p.HostReserveGiB != 60 {
		t.Errorf("LightBuild mutated the policy")
	}
	p.HostReserveGiB = 2
	if got := p.LightBuild().HostReserveGiB; got != 2 {
		t.Errorf("light reserve = %d, want the smaller policy reserve 2", got)
	}
}
