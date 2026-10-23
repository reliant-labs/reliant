package cli

import (
	"fmt"
	"strings"
	"testing"
)

// upgrade_window_test.go — the supported-upgrade-window policy.
//
// forge accepts an upgrade starting at most supportedUpgradeWindowMinors
// minor releases back (the Istio model). A project older than that is
// told to do a STAGED upgrade through an intermediate release, and the
// message has to name that release — "you are too far back" without a
// next step leaves the user to work out the arithmetic themselves.

// TestUpgradeWindow_TooOldProjectGetsTheStagedUpgradeMessage is the
// too-old path end to end. A project 5 minors back cannot upgrade in one
// step; the refusal must name how far behind it is AND the exact
// intermediate version to run first.
func TestUpgradeWindow_TooOldProjectGetsTheStagedUpgradeMessage(t *testing.T) {
	const oldPin = "v0.1.0"
	const target = "v0.6.0"

	dir := newTestProjectWithVersion(t, oldPin)

	var err error
	withCwd(t, dir, func() {
		err = runUpgrade(true /* check */, false, nil, target)
	})
	if err == nil {
		t.Fatalf("a project %s upgrading to %s is outside the %d-minor window but was allowed through",
			oldPin, target, supportedUpgradeWindowMinors)
	}

	msg := err.Error()
	t.Logf("staged-upgrade message:\n%s", msg)

	// The intermediate release is the far edge of the window: v0.1 + 2 = v0.3.
	wantStaged := fmt.Sprintf("--to v%s", minorPlus(oldPin, supportedUpgradeWindowMinors))
	for _, want := range []string{
		"5 minor releases behind",
		fmt.Sprintf("at most %d minors back", supportedUpgradeWindowMinors),
		wantStaged,
		"staged upgrade",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("staged-upgrade message missing %q:\n%s", want, msg)
		}
	}
}

// TestUpgradeWindow_InsideTheWindowIsAllowed: the guard must not fire on
// the hops it exists to permit. Exactly N minors back is INSIDE the
// window — the window is how far back forge supports starting from, so
// its far edge is supported, not the first rejected step.
func TestUpgradeWindow_InsideTheWindowIsAllowed(t *testing.T) {
	for _, tc := range []struct{ from, to string }{
		{"v0.4.0", "v0.4.0"}, // same minor
		{"v0.4.0", "v0.5.0"}, // one back
		{"v0.4.0", "v0.6.0"}, // exactly N back — the far edge
	} {
		hop := minorHopDistance(tc.from, tc.to)
		if hop > supportedUpgradeWindowMinors {
			t.Errorf("%s → %s: hop %d should be inside the %d-minor window",
				tc.from, tc.to, hop, supportedUpgradeWindowMinors)
		}
	}

	// One past the edge is where the refusal starts.
	if hop := minorHopDistance("v0.4.0", "v0.7.0"); hop <= supportedUpgradeWindowMinors {
		t.Errorf("v0.4.0 → v0.7.0: hop %d should be OUTSIDE the %d-minor window",
			hop, supportedUpgradeWindowMinors)
	}
}

// TestMinorPlus names the intermediate release for the staged-upgrade
// message. A wrong answer here sends the user to a version that either
// doesn't help or doesn't exist.
func TestMinorPlus(t *testing.T) {
	tests := []struct {
		in   string
		n    int
		want string
	}{
		{"0.1", 2, "0.3"},
		{"v0.1.0", 2, "0.3"},
		{"v1.4.3", 2, "1.6"},
		{"v0.0.4", 2, "0.2"},
		{"v0.1.0", 1, "0.2"},
		// Unparseable input falls back to the input string so the
		// caller's message stays informative rather than printing "".
		{"dev", 2, "dev"},
		{"", 2, ""},
	}
	for _, tt := range tests {
		if got := minorPlus(tt.in, tt.n); got != tt.want {
			t.Errorf("minorPlus(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
		}
	}
}

// TestUpgradeWindow_CrossMajorIsNotAStagedHop: minorHopDistance returns
// -1 across a major boundary, so the window guard must not fire on it. A
// major upgrade is a different conversation than a staged minor walk,
// and reporting "-1 minor releases behind" would be nonsense.
func TestUpgradeWindow_CrossMajorIsNotAStagedHop(t *testing.T) {
	if hop := minorHopDistance("v0.9.0", "v1.0.0"); hop > supportedUpgradeWindowMinors {
		t.Errorf("cross-major hop reported as %d — the window guard would fire on it", hop)
	}
}
