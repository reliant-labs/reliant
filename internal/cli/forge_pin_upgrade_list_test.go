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

// Bare `project upgrade` moves the pin and must work when the old pin cannot
// be built: it stays this forge's, whichever way the versions point.
func TestUpgrade_BareUpgradeIsNeverHandedOff(t *testing.T) {
	skewedProject(t)
	t.Setenv(forgedelegate.MarkerEnv, "")
	h, installs := armDelegation(t)

	if _, err := runCheck(t, "project", "upgrade", "--check"); err != nil {
		t.Fatalf("project upgrade --check was refused: %v", err)
	}
	if h.path != "" || len(*installs) != 0 {
		t.Fatalf("project upgrade was handed to the pin (built %v)", *installs)
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
