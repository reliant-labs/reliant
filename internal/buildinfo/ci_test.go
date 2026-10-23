package buildinfo_test

import (
	"testing"

	"github.com/reliant-labs/forge/internal/buildinfo"
)

// TestIsCI_EnvTruthTable pins which environment variables turn IsCI on.
//
// The cases are not interchangeable. CI is the cross-provider convention and
// carries the check almost everywhere on its own; GITHUB_ACTIONS is the
// provider forge's own workflows run on, and exists so a runner that somehow
// lost CI is still recognised. Both are asserted so that dropping either is a
// test failure rather than a silent narrowing.
func TestIsCI_EnvTruthTable(t *testing.T) {
	// Not parallel: t.Setenv, and IsCI reads process environment.
	cases := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{
			name: "no CI vars set — a developer's machine",
			env:  map[string]string{"CI": "", "GITHUB_ACTIONS": ""},
			want: false,
		},
		{
			name: "CI=true — the cross-provider convention",
			env:  map[string]string{"CI": "true", "GITHUB_ACTIONS": ""},
			want: true,
		},
		{
			name: "GITHUB_ACTIONS alone — a runner that lost CI",
			env:  map[string]string{"CI": "", "GITHUB_ACTIONS": "true"},
			want: true,
		},
		{
			name: "both set — the ordinary GitHub Actions runner",
			env:  map[string]string{"CI": "true", "GITHUB_ACTIONS": "true"},
			want: true,
		},
		{
			// Presence, not truthiness: some providers set CI=1, and a
			// value-sniffing check that only accepted "true" would silently
			// treat those runners as developer machines.
			name: "CI=1 — presence is what counts, not the literal true",
			env:  map[string]string{"CI": "1", "GITHUB_ACTIONS": ""},
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if got := buildinfo.IsCI(); got != tc.want {
				t.Fatalf("IsCI() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestSetCI_OverridesEnvironment proves the test seam wins over the ambient
// environment in BOTH directions. The second direction is the one that
// matters: forge's own suite runs on CI, so a seam that could only force IsCI
// true would leave the "not CI" branch untested precisely where it ships.
func TestSetCI_OverridesEnvironment(t *testing.T) {
	t.Setenv("CI", "true")
	t.Setenv("GITHUB_ACTIONS", "true")

	buildinfo.SetCI(false)
	t.Cleanup(buildinfo.ClearCI)
	if buildinfo.IsCI() {
		t.Fatal("SetCI(false) did not override CI=true in the environment")
	}

	buildinfo.SetCI(true)
	if !buildinfo.IsCI() {
		t.Fatal("SetCI(true) did not report CI")
	}

	buildinfo.ClearCI()
	if !buildinfo.IsCI() {
		t.Fatal("ClearCI did not restore the environment read (CI=true is set)")
	}
}

// TestDevWebRuntimeLinkForced covers the escape hatch. Per forge's "the 20%
// are never disempowered" rule the CI suppression is a default, not a wall —
// so any non-empty value must re-enable the bridge.
func TestDevWebRuntimeLinkForced(t *testing.T) {
	t.Setenv("FORGE_DEV_WEBRUNTIME_LINK", "")
	if buildinfo.DevWebRuntimeLinkForced() {
		t.Fatal("unset FORGE_DEV_WEBRUNTIME_LINK must not force the link")
	}

	for _, v := range []string{"1", "true", "yes"} {
		t.Setenv("FORGE_DEV_WEBRUNTIME_LINK", v)
		if !buildinfo.DevWebRuntimeLinkForced() {
			t.Fatalf("FORGE_DEV_WEBRUNTIME_LINK=%q must force the link", v)
		}
	}
}
