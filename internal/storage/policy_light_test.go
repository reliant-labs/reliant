package storage

import (
	"strings"
	"testing"
)

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

// A host build (one binary for a process forge env up runs, and air's rebuild
// of it) is admitted under HostBuildReserveGiB, never above the policy's own
// reserve: a 60 GiB machine reserve refused a ~30 MB rebuild with 39 GiB free
// (Ranger friction #1).
func TestHostBuildCapsTheHostReserve(t *testing.T) {
	p := DefaultPolicy()
	p.HostReserveGiB = 60
	if got := p.HostBuild().HostReserveGiB; got != HostBuildReserveGiB {
		t.Errorf("host reserve = %d, want %d", got, HostBuildReserveGiB)
	}
	if p.HostReserveGiB != 60 {
		t.Errorf("HostBuild mutated the policy")
	}
	p.HostReserveGiB = 3
	if got := p.HostBuild().HostReserveGiB; got != 3 {
		t.Errorf("host reserve = %d, want the smaller policy reserve 3", got)
	}
	if HostBuildReserveGiB <= LightBuildReserveGiB {
		t.Errorf("a host go build (%d GiB) must keep more headroom than a light build (%d GiB)", HostBuildReserveGiB, LightBuildReserveGiB)
	}
}

func TestReserveOverride(t *testing.T) {
	env := func(v string) func(string) string {
		return func(k string) string {
			if k == ReserveOverrideEnv {
				return v
			}
			return ""
		}
	}
	if n, ok, err := ReserveOverride(env("")); n != 0 || ok || err != nil {
		t.Errorf("unset = %d %v %v, want no override", n, ok, err)
	}
	if n, ok, err := ReserveOverride(env(" 12 ")); n != 12 || !ok || err != nil {
		t.Errorf("12 = %d %v %v, want 12", n, ok, err)
	}
	for _, bad := range []string{"0", "-1", "ten", "1.5", "10GiB"} {
		if _, _, err := ReserveOverride(env(bad)); err == nil || !strings.Contains(err.Error(), ReserveOverrideEnv) {
			t.Errorf("%q = %v, want an error naming %s", bad, err, ReserveOverrideEnv)
		}
	}
}

// The refusal names the per-run way out, not only the machine-wide cleanup.
func TestReserveErrorNamesTheOverride(t *testing.T) {
	msg := (&ReserveError{Path: "/x", Available: 39 * GiB, ReserveGiB: 60}).Error()
	for _, want := range []string{"39.0 GiB free", "60 GiB reserve", "forge storage gc --apply", ReserveOverrideEnv} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q lacks %q", msg, want)
		}
	}
}
