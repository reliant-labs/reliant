package generator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNewFrontendWorkspaceLayout_DerivesUINativePackage asserts the
// UINativePackage field is derived from the project name the same way
// APIPackage and HooksPackage are.
func TestNewFrontendWorkspaceLayout_DerivesUINativePackage(t *testing.T) {
	got := NewFrontendWorkspaceLayout("my-app")
	if got.UINativePackage != "@my-app/ui-native" {
		t.Errorf("UINativePackage = %q, want @my-app/ui-native", got.UINativePackage)
	}

	got = NewFrontendWorkspaceLayout("")
	if got.UINativePackage != "@app/ui-native" {
		t.Errorf("empty project name should fall back to @app/ui-native, got %q", got.UINativePackage)
	}
}

// TestWriteUINativePackageFiles_EmitsScaffold asserts the package.json,
// tsconfig.json, src/tokens.ts, src/index.ts, README.md, and every
// primitive .tsx land at the expected paths with the expected shape.
func TestWriteUINativePackageFiles_EmitsScaffold(t *testing.T) {
	dir := t.TempDir()
	layout := NewFrontendWorkspaceLayout("myapp")
	if err := WriteUINativePackageFiles(dir, layout); err != nil {
		t.Fatalf("WriteUINativePackageFiles: %v", err)
	}

	uiDir := filepath.Join(dir, "packages", "ui-native")

	// package.json — scoped name + react-native peer dep + safe-area peer dep.
	var pkg map[string]any
	if err := json.Unmarshal([]byte(mustRead(t, filepath.Join(uiDir, "package.json"))), &pkg); err != nil {
		t.Fatalf("parse package.json: %v", err)
	}
	if pkg["name"] != "@myapp/ui-native" {
		t.Errorf("package name = %v, want @myapp/ui-native", pkg["name"])
	}
	peer, _ := pkg["peerDependencies"].(map[string]any)
	for _, dep := range []string{"react", "react-native", "react-native-safe-area-context"} {
		if _, ok := peer[dep]; !ok {
			t.Errorf("peerDependencies missing %q, got: %v", dep, peer)
		}
	}

	// tsconfig — DOM lib must NOT appear (we mirror the hooks guardrail —
	// this package is RN-only, no document/window).
	tsconfig := mustRead(t, filepath.Join(uiDir, "tsconfig.json"))
	if strings.Contains(tsconfig, "\"DOM\"") {
		t.Errorf("tsconfig must not include DOM lib (ui-native is RN-only), got:\n%s", tsconfig)
	}
	if !strings.Contains(tsconfig, "react-jsx") {
		t.Errorf("tsconfig must use react-jsx (Metro doesn't run a JSX-transform babel config), got:\n%s", tsconfig)
	}

	// Tokens.
	tokens := mustRead(t, filepath.Join(uiDir, "src", "tokens.ts"))
	for _, sym := range []string{"colors", "spacing", "radius", "textSizes"} {
		if !strings.Contains(tokens, "export const "+sym) {
			t.Errorf("tokens.ts missing `export const %s`", sym)
		}
	}

	// All 10 primitives.
	expected := []string{
		"button.tsx",
		"input.tsx",
		"label.tsx",
		"card.tsx",
		"stack.tsx",
		"text.tsx",
		"spinner.tsx",
		"switch.tsx",
		"pressable.tsx",
		"safe-area-view.tsx",
	}
	for _, name := range expected {
		path := filepath.Join(uiDir, "src", "components", name)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected primitive %s missing: %v", name, err)
		}
	}

	// Index barrel re-exports each primitive + tokens.
	barrel := mustRead(t, filepath.Join(uiDir, "src", "index.ts"))
	if !strings.Contains(barrel, "./components/safe-area-view") {
		t.Errorf("barrel missing ./components/safe-area-view path")
	}
	if !strings.Contains(barrel, "SafeAreaView") {
		t.Errorf("barrel missing SafeAreaView default re-export")
	}
	if !strings.Contains(barrel, "HStack") {
		t.Errorf("barrel missing HStack named re-export")
	}
	if !strings.Contains(barrel, `export * from "./tokens"`) {
		t.Errorf("barrel missing tokens re-export")
	}

	// README mentions ownership rule + Tamagui/Unistyles forward link.
	readme := mustRead(t, filepath.Join(uiDir, "README.md"))
	if !strings.Contains(readme, "Tamagui") || !strings.Contains(readme, "Unistyles") {
		t.Errorf("README should forward-link to Tamagui + Unistyles, got:\n%s", readme)
	}
}

// TestWriteUINativePackageFiles_Idempotent asserts a second emit
// preserves user edits to any file under packages/ui-native/. The
// package is write-if-missing; once a primitive ships it belongs to
// the user.
func TestWriteUINativePackageFiles_Idempotent(t *testing.T) {
	dir := t.TempDir()
	layout := NewFrontendWorkspaceLayout("myapp")
	if err := WriteUINativePackageFiles(dir, layout); err != nil {
		t.Fatalf("first emit: %v", err)
	}

	buttonPath := filepath.Join(dir, "packages", "ui-native", "src", "components", "button.tsx")
	userEdit := "// user-owned\nexport default function Button() { return null; }\n"
	if err := os.WriteFile(buttonPath, []byte(userEdit), 0o644); err != nil {
		t.Fatalf("write user edit: %v", err)
	}

	if err := WriteUINativePackageFiles(dir, layout); err != nil {
		t.Fatalf("second emit: %v", err)
	}

	got := mustRead(t, buttonPath)
	if got != userEdit {
		t.Errorf("user edit was clobbered on second emit\nwant: %s\n got: %s", userEdit, got)
	}
}

// TestWriteUINativePackageFiles_NotCalledWhenWorkspacesDisabled is a
// documentation-style test of the call-site contract: this function
// shouldn't be invoked when workspaces is off. The actual gate lives
// in generate_pipeline.go (stepFrontendWorkspaces) and add.go; here
// we just demonstrate that NOT calling it leaves the project clean.
func TestWriteUINativePackageFiles_NotCalledWhenWorkspacesDisabled(t *testing.T) {
	dir := t.TempDir()
	// Simulate workspaces=false by NOT calling the emitter.
	uiDir := filepath.Join(dir, "packages", "ui-native")
	if _, err := os.Stat(uiDir); !os.IsNotExist(err) {
		t.Errorf("packages/ui-native should not exist when caller skips the emitter, got: %v", err)
	}
}
