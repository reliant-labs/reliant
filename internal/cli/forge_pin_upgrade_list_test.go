package cli

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/forgedelegate"
)

// Forge reports 727a5325 and a8173760: `reliant forge project upgrade list`,
// run by a forge OLDER than the project's pin, listed that forge's own stale
// playbooks (up to v1.4.0, or v0.1.44+ as pending) while `skill load` handed
// off to the pin. A forge older than the pin hands the listing to the pin.
func TestUpgradeList_OlderForgeHandsOffToThePin(t *testing.T) {
	skewedProject(t) // the binary is older than skewTestPin
	t.Setenv(forgedelegate.MarkerEnv, "")
	h, _ := armDelegation(t)

	out, err := runCheck(t, "project", "upgrade", "list")
	if err != nil {
		t.Fatalf("upgrade list was refused: %v", err)
	}
	if h.path == "" {
		t.Fatalf("upgrade list ran as a forge older than the pin:\n%s", out)
	}
	if got := strings.Join(h.args, " "); got != "project upgrade list" {
		t.Errorf("args %q, want the original invocation", got)
	}
}

// The upgrade target — a forge NEWER than the pin — lists its own playbooks:
// that is what `project upgrade list` is for before the pin moves.
func TestUpgradeList_NewerForgeListsItsOwnPlaybooks(t *testing.T) {
	skewedProject(t)
	buildinfo.Set("v0.1.46-0.20261101000000-6c0c3edb1322", "", "6c0c3edb1322")
	t.Setenv(forgedelegate.MarkerEnv, "")
	h, installs := armDelegation(t)

	out, err := runCheck(t, "project", "upgrade", "list")
	if err != nil {
		t.Fatalf("upgrade list was refused: %v", err)
	}
	if h.path != "" || len(*installs) != 0 {
		t.Fatalf("the upgrade target handed its listing to the older pin (built %v)", *installs)
	}
	if !strings.Contains(out, "lists its own playbooks") {
		t.Errorf("want the upgrade-target notice:\n%s", out)
	}
}

// `project upgrade` with no --to moves the pin to this forge, so run by a
// forge OLDER than the pin it would move the pin backwards: forge report
// 17382bac, "v1.11.0 → v1.10.1 (dev build)" from `upgrade --check`. It is
// handed to the newer pin, where it is the no-op it should be.
func TestUpgrade_OlderForgeHandsAnImplicitTargetToThePin(t *testing.T) {
	for _, args := range [][]string{{"project", "upgrade", "--check"}, {"project", "upgrade", "--dry-run"}, {"project", "upgrade"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			skewedProject(t) // the binary is older than skewTestPin
			t.Setenv(forgedelegate.MarkerEnv, "")
			h, _ := armDelegation(t)

			out, err := runCheck(t, args...)
			if err != nil {
				t.Fatalf("refused: %v", err)
			}
			if h.path == "" {
				t.Fatalf("ran as a forge older than the pin:\n%s", out)
			}
			if got := strings.Join(h.args, " "); got != strings.Join(args, " ") {
				t.Errorf("args %q, want the original invocation", got)
			}
		})
	}
}

// --to names the target, so the run is the user's explicit choice: it stays
// this forge's, as `project upgrade` always was.
func TestUpgrade_ExplicitTargetIsNeverHandedOff(t *testing.T) {
	skewedProject(t)
	t.Setenv(forgedelegate.MarkerEnv, "")
	h, installs := armDelegation(t)

	_, _ = runCheck(t, "project", "upgrade", "--check", "--to", skewTestPin)
	if h.path != "" || len(*installs) != 0 {
		t.Fatalf("project upgrade --to was handed to the pin (built %v)", *installs)
	}
}

// The upgrade target — a forge NEWER than the pin — moves the pin itself and
// must work when the old pin cannot be built: it is never handed off.
func TestUpgrade_NewerForgeIsNeverHandedOff(t *testing.T) {
	skewedProject(t)
	buildinfo.Set("v0.1.46-0.20261101000000-6c0c3edb1322", "", "6c0c3edb1322")
	t.Setenv(forgedelegate.MarkerEnv, "")
	h, installs := armDelegation(t)
	unobtainablePin(t)

	_, _ = runCheck(t, "project", "upgrade", "--check")
	if h.path != "" || len(*installs) != 0 {
		t.Fatalf("project upgrade was handed to the older pin (built %v)", *installs)
	}
}

// When the newer pin cannot be had, an implicit-target upgrade runs here with
// the skew warning — where the upgrade itself refuses the downgrade
// (TestUpgradeConverge_RefusesAnImplicitDowngrade).
func TestUpgrade_UnobtainablePinRunsHere(t *testing.T) {
	skewedProject(t)
	t.Setenv(forgedelegate.MarkerEnv, "")
	h, _ := armDelegation(t)
	unobtainablePin(t)

	out, err := runCheck(t, "project", "upgrade", "--check")
	if err != nil {
		t.Fatalf("failed instead of running here: %v", err)
	}
	if h.path != "" {
		t.Fatal("handed over a forge that was never built")
	}
	if !strings.Contains(out, "running this forge instead") {
		t.Errorf("want the fallback notice:\n%s", out)
	}
}

// refuseImplicitDowngrade: only a target that is this forge (no --to), and
// semver-older than the pin, is refused.
func TestRefuseImplicitDowngrade(t *testing.T) {
	for _, tc := range []struct {
		from, target, to string
		refused          bool
	}{
		{"v1.11.0", "v1.10.1-0.20261123064811-54313eead62f", "", true},
		{"v1.11.0", "v1.11.0", "", false},
		{"v1.10.1", "v1.11.0", "", false},
		{"v1.11.0", "v1.10.1", "v1.10.1", false},
		{"v1.11.0", "(devel)", "", false},
		{"", "v1.10.1", "", false},
	} {
		if err := refuseImplicitDowngrade(tc.from, tc.target, tc.to); (err != nil) != tc.refused {
			t.Errorf("refuseImplicitDowngrade(%q, %q, to=%q) = %v, want refused=%v", tc.from, tc.target, tc.to, err, tc.refused)
		}
	}
}

// When the newer pin cannot be had, the listing still runs — here, with the
// skew warning in text mode naming the pin, never silently.
func TestUpgradeList_UnobtainablePinRunsHereWithTheWarning(t *testing.T) {
	skewedProject(t)
	t.Setenv(forgedelegate.MarkerEnv, "")
	h, _ := armDelegation(t)
	unobtainablePin(t)

	out, err := runCheck(t, "project", "upgrade", "list")
	if err != nil {
		t.Fatalf("upgrade list failed instead of running here: %v", err)
	}
	if h.path != "" {
		t.Fatal("handed over a forge that was never built")
	}
	for _, want := range []string{"could not be obtained", "running this forge instead", "⚠️", skewTestPin} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}
