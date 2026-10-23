package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// forge.yaml's forge_version used to move only under `forge project upgrade`.
// After `go get github.com/reliant-labs/forge@vX` + `forge generate` it stayed
// on the old version, which is a real drift source: it is the version CI and
// `forge project upgrade` read as the project's baseline. For a project whose
// go.mod requires forge, the pin now FOLLOWS that require, and generate — the
// declarative reconcile step — is what converges it.

func writePinProject(t *testing.T, forgeYAML, goMod string) (string, *pipelineContext) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte(forgeYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	if goMod != "" {
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.LoadProject([]byte(forgeYAML), filepath.Join(dir, "forge.yaml"))
	if err != nil {
		t.Fatalf("load forge.yaml: %v", err)
	}
	return dir, &pipelineContext{ProjectDir: dir, AbsPath: dir, Cfg: cfg}
}

func TestReconcileForgePin_FollowsGoModRequire(t *testing.T) {
	const forgeYAML = "# my manifest\nname: app\nmodule_path: example.com/app\nforge_version: v0.1.17 # pinned\n"
	dir, ctx := writePinProject(t, forgeYAML,
		"module example.com/app\n\ngo 1.24\n\nrequire (\n\tgithub.com/reliant-labs/forge v0.1.18\n\tgolang.org/x/mod v0.41.0\n)\n")

	out := captureStdout(t, func() {
		if err := stepReconcileForgePin(ctx); err != nil {
			t.Fatalf("stepReconcileForgePin: %v", err)
		}
	})
	got, _ := os.ReadFile(filepath.Join(dir, "forge.yaml"))
	if !strings.Contains(string(got), "forge_version: v0.1.18") {
		t.Fatalf("forge_version did not converge to go.mod's forge require:\n%s", got)
	}
	if !strings.Contains(string(got), "# my manifest") {
		t.Errorf("the pin update rewrote more than one scalar:\n%s", got)
	}
	if ctx.Cfg.ForgeVersion != "v0.1.18" {
		t.Errorf("in-memory config still pins %q; later steps would read the stale value", ctx.Cfg.ForgeVersion)
	}
	if !strings.Contains(out, "v0.1.18") {
		t.Errorf("a pin change must be reported, got:\n%s", out)
	}
}

// A CLI or library that never links forge has no go.mod version to follow:
// forge.yaml IS the source of truth there (CI's install script falls back to
// it), and generate must leave it alone.
func TestReconcileForgePin_NoForgeRequireLeavesPin(t *testing.T) {
	const forgeYAML = "name: tool\nmodule_path: example.com/tool\nforge_version: v0.1.17\n"
	dir, ctx := writePinProject(t, forgeYAML,
		"module example.com/tool\n\ngo 1.24\n\nrequire github.com/spf13/cobra v1.8.0\n")
	captureStdout(t, func() {
		if err := stepReconcileForgePin(ctx); err != nil {
			t.Fatalf("stepReconcileForgePin: %v", err)
		}
	})
	if got, _ := os.ReadFile(filepath.Join(dir, "forge.yaml")); string(got) != forgeYAML {
		t.Errorf("forge.yaml changed for a project with no forge require:\n%s", got)
	}
}

// A hop whose codemod still has something to rewrite is `forge project
// upgrade`'s to cross: moving the baseline silently would make upgrade skip
// it. Generate leaves the pin and names both commands. A codemod with
// nothing to rewrite here holds nothing back.
func TestReconcileForgePin_HopNeedingUpgradeIsLeftToUpgrade(t *testing.T) {
	const forgeYAML = "name: app\nmodule_path: example.com/app\nforge_version: v0.1.45\n"
	const goMod = "module example.com/app\n\ngo 1.24\n\nrequire github.com/reliant-labs/forge v0.1.46\n"

	dir, ctx := writePinProject(t, forgeYAML, goMod)
	lifecycle := filepath.Join(dir, "internal", "app", "lifecycle.go")
	if err := os.MkdirAll(filepath.Dir(lifecycle), 0o755); err != nil {
		t.Fatal(err)
	}
	const src = "package app\n\nimport \"github.com/reliant-labs/forge/pkg/lifecyclekit\"\n\nvar _ lifecyclekit.Options\n"
	if err := os.WriteFile(lifecycle, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if err := stepReconcileForgePin(ctx); err != nil {
			t.Fatalf("stepReconcileForgePin: %v", err)
		}
	})
	if got, _ := os.ReadFile(filepath.Join(dir, "forge.yaml")); string(got) != forgeYAML {
		t.Errorf("generate moved the pin across a hop that needs `forge project upgrade`:\n%s", got)
	}
	if !strings.Contains(out, "project upgrade") || !strings.Contains(out, "forge fix operatorkit") {
		t.Errorf("leaving the pin must name `forge project upgrade` and `forge fix operatorkit`, got:\n%s", out)
	}
	if got, _ := os.ReadFile(lifecycle); string(got) != src {
		t.Errorf("generate must not run the codemod itself:\n%s", got)
	}

	// The same hop over a project with nothing for the codemod to rewrite
	// moves the pin.
	clean, cleanCtx := writePinProject(t, forgeYAML, goMod)
	captureStdout(t, func() {
		if err := stepReconcileForgePin(cleanCtx); err != nil {
			t.Fatalf("stepReconcileForgePin: %v", err)
		}
	})
	if got, _ := os.ReadFile(filepath.Join(clean, "forge.yaml")); !strings.Contains(string(got), "forge_version: v0.1.46") {
		t.Errorf("a hop with no codemod work left must move the pin:\n%s", got)
	}
}
