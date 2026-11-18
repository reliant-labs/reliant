package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEnsureCoreComponentsRefusesCaseCollisions: reliant's web app had
// Button.tsx / Modal.tsx / Input.tsx / Card.tsx / Badge.tsx, and the starter
// set's button.tsx etc. landed beside them — one file each on macOS, TS1261 on
// Linux. A colliding component is refused, named, and the rest still install.
func TestEnsureCoreComponentsRefusesCaseCollisions(t *testing.T) {
	fe := t.TempDir()
	ui := filepath.Join(fe, "src", "components", "forge-ui")
	if err := os.MkdirAll(ui, 0o755); err != nil {
		t.Fatal(err)
	}
	const mine = "export const Button = () => null;\n"
	if err := os.WriteFile(filepath.Join(ui, "Button.tsx"), []byte(mine), 0o644); err != nil {
		t.Fatal(err)
	}

	err := EnsureCoreComponentsIn(fe, "src/components/forge-ui")
	if err == nil || !strings.Contains(err.Error(), "button.tsx") || !strings.Contains(err.Error(), "Button.tsx") {
		t.Fatalf("EnsureCoreComponentsIn err = %v, want a refusal naming button.tsx and Button.tsx", err)
	}
	entries, _ := os.ReadDir(ui)
	var lower int
	for _, e := range entries {
		if strings.EqualFold(e.Name(), "button.tsx") {
			lower++
		}
	}
	if lower != 1 {
		t.Fatalf("%d case-variants of button.tsx in %s, want only the app's own", lower, ui)
	}
	if got, _ := os.ReadFile(filepath.Join(ui, "Button.tsx")); string(got) != mine {
		t.Fatalf("the app's Button.tsx was changed:\n%s", got)
	}
	// Everything that does not collide still installs, into components_dir.
	if _, err := os.Stat(filepath.Join(ui, "card.tsx")); err != nil {
		t.Fatalf("non-colliding components were not installed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fe, "src", "components", "ui")); !os.IsNotExist(err) {
		t.Fatalf("components_dir was ignored: src/components/ui exists (err=%v)", err)
	}
}

func TestCaseCollision(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Modal.tsx"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := CaseCollision(dir, "modal.tsx"); got != "Modal.tsx" {
		t.Errorf("CaseCollision(modal.tsx) = %q", got)
	}
	if got := CaseCollision(dir, "Modal.tsx"); got != "" {
		t.Errorf("the same name is not a collision: %q", got)
	}
	if got := CaseCollision(filepath.Join(dir, "missing"), "x.tsx"); got != "" {
		t.Errorf("missing dir: %q", got)
	}
}
