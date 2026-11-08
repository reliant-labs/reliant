package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

// A project linked to a forge checkout (go.work `use`, what --link-forge
// writes) compiles against that checkout; its go.mod require is the published
// floor tidy resolves without the workspace. Generate used to "follow" that
// floor and move the pin BACK from the version project new wrote (owner's
// dogfood: "forge_version v0.1.47-0.…-67ceee830b1b → v0.1.46"). The pin
// follows the linked checkout's commit instead.
func TestReconcileForgePin_LinkedCheckoutIsThePin(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a git checkout to derive its pseudo-version; runs in task test")
	}
	checkout := t.TempDir()
	writeFileForTest(t, filepath.Join(checkout, "go.mod"), "module github.com/reliant-labs/forge\n\ngo 1.24\n")
	writeFileForTest(t, filepath.Join(checkout, "pkg", "doc.go"), "package pkg\n")
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.email=t@forge.test", "-c", "user.name=t"}, args...)...)
		cmd.Dir = checkout
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("add", ".")
	git("commit", "-q", "-m", "init")
	git("tag", "v0.1.46")
	writeFileForTest(t, filepath.Join(checkout, "pkg", "more.go"), "package pkg\n")
	git("add", ".")
	git("commit", "-q", "-m", "after the tag")
	sha := git("rev-parse", "--short=12", "HEAD")

	const forgeYAML = "name: app\nmodule_path: example.com/app\nforge_version: v0.1.46\n"
	dir, ctx := writePinProject(t, forgeYAML,
		"module example.com/app\n\ngo 1.24\n\nrequire github.com/reliant-labs/forge v0.1.46\n")
	writeFileForTest(t, filepath.Join(dir, "go.work"), "go 1.24\n\nuse (\n\t.\n\t"+checkout+"\n)\n")

	out := captureStdout(t, func() {
		if err := stepReconcileForgePin(ctx); err != nil {
			t.Fatalf("stepReconcileForgePin: %v", err)
		}
	})
	got, _ := os.ReadFile(filepath.Join(dir, "forge.yaml"))
	want := regexp.MustCompile(`(?m)^forge_version: v0\.1\.47-0\.\d{14}-` + sha + `$`)
	if !want.Match(got) {
		t.Fatalf("forge_version does not name the linked checkout's commit %s (no +dev/+dirty either):\n%s\noutput:\n%s", sha, got, out)
	}
	if !strings.Contains(out, "the forge checkout go.work links") {
		t.Errorf("the pin change must name the linked checkout as its source:\n%s", out)
	}

	// Converged: a second run says nothing and writes nothing.
	again := captureStdout(t, func() {
		if err := stepReconcileForgePin(ctx); err != nil {
			t.Fatalf("stepReconcileForgePin: %v", err)
		}
	})
	if again != "" {
		t.Errorf("a converged pin must be left alone, got:\n%s", again)
	}
}
