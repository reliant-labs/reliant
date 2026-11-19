package audit

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/cli/audittype"
	"github.com/reliant-labs/forge/internal/config"
)

// A binary OLDER than the project's pin is an ERROR, not the generic
// "does NOT match binary" warning the two directions used to share.
//
// The directions are not symmetric. A newer binary has every schema field
// the pin declared, so `forge project upgrade` realigns at the user's
// convenience — a warning. An older binary is MISSING schema fields the
// project is entitled to use, and because forge's KCL module is embedded in
// the binary, the render fails blaming the project's own line number. That
// is a broken install, and audit should say so at error level.
//
// `forge project audit` is the command someone runs when something is
// already wrong; a warning buried among warnings is how the original report
// got read as "probably fine".
func TestAuditVersion_BinaryOlderThanPinIsAnError(t *testing.T) {
	t.Cleanup(func() { buildinfo.Set("dev", "unknown", "unknown") })
	buildinfo.Set("v0.1.42", "unknown", "unknown")

	cat := auditVersion(&config.ProjectConfig{ForgeVersion: "v0.1.43"}, t.TempDir())

	if cat.Status != audittype.StatusError {
		t.Errorf("status = %q, want error for a binary behind the project's pin (summary=%q)",
			cat.Status, cat.Summary)
	}
	for _, want := range []string{"v0.1.43", "v0.1.42", "older"} {
		if !strings.Contains(cat.Summary, want) {
			t.Errorf("summary missing %q: %s", want, cat.Summary)
		}
	}
	// The install command is the actionable part.
	hint, _ := cat.Details["hint"].(string)
	const install = "go install github.com/reliant-labs/forge/cmd/forge@v0.1.43"
	if !strings.Contains(hint, install) {
		t.Errorf("hint does not name the pinned install %q:\n%s", install, hint)
	}
}

// The newer-binary direction stays a WARNING. Escalating it too would make
// every project mid-upgrade fail its audit, which is the ordinary state of a
// project between a forge release and its `forge project upgrade`.
func TestAuditVersion_BinaryNewerThanPinStaysAWarning(t *testing.T) {
	t.Cleanup(func() { buildinfo.Set("dev", "unknown", "unknown") })
	buildinfo.Set("v0.1.43", "unknown", "unknown")

	cat := auditVersion(&config.ProjectConfig{ForgeVersion: "v0.1.42"}, t.TempDir())

	if cat.Status != audittype.StatusWarn {
		t.Errorf("status = %q, want warn for a binary ahead of the pin (summary=%q)",
			cat.Status, cat.Summary)
	}
}
