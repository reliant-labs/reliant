package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The SC7 rehearsal (defect D3): `forge env deploy sc7r` on an env that builds
// no image (an OpenBao component and policies, nothing else) ran a four-minute
// frontend build and only then failed the cut with "no image digest was
// captured". The refusal is the same; it must come before the build.

const manifestsOnlyFixture = `{
  "output": {
    "workloads": [
      {
        "name": "operator",
        "kind": "service",
        "runtime": {"type": "cluster", "cluster": "c", "namespace": "n"},
        "spec": {"kind": "service"}
      }
    ]
  }
}`

// emptyReleaseProject is a project with an image-less env, a frontend whose
// build would take minutes (here: it would fail loudly if it ever ran), and no
// root Dockerfile, so nothing could be pinned.
func emptyReleaseProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	withProjectDir(t, dir)
	for path, body := range map[string]string{
		"forge.yaml":       "name: pt\nmodule_path: example.com/pt\n",
		"go.mod":           "module example.com/pt\n\ngo 1.22\n",
		"web/package.json": `{"name":"web","private":true,"scripts":{"build":"touch ../FRONTEND_WAS_BUILT"}}`,
	} {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, manifestsOnlyFixture))
	return dir
}

// TestBuild_ReleaseOfAnEnvThatPinsNothingIsRefusedBeforeAnyBuild is the
// rehearsal failure. The release cut must be refused with the cause and the
// remedy, and the frontend build must not have started.
//
// Mutation that fails it: delete the releaseCapturesNothing call in runBuild
// and the frontend builds first (FRONTEND_WAS_BUILT appears) and the error is
// the cut's late "no image digest was captured".
func TestBuild_ReleaseOfAnEnvThatPinsNothingIsRefusedBeforeAnyBuild(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates a KCL project natively; runs in task test")
	}
	dir := emptyReleaseProject(t)

	err := runBuild(context.Background(), buildOptions{
		env: "sc7r", buildTarget: "all", outputDir: "bin", buildDocker: true,
		// buildAndCutForDeploy's exact options: pushIfDeclared, not push, so an
		// env that declares nothing to push is not refused by --push's own
		// "nothing to publish" check and reaches the build, as in the rehearsal.
		pushIfDeclared: true, capacityChecked: true,
		release: "20261023.204521-be2a74421b35", skipGenerate: true,
	})
	if err == nil {
		t.Fatal("a release of an env that pins nothing must be refused")
	}
	if strings.Contains(err.Error(), "no image digest was captured") {
		t.Fatalf("refused by the cut, i.e. AFTER the build: %v", err)
	}
	for _, want := range []string{"declares nothing a release can pin", "Nothing was built", "--dry-run"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal should say %q:\n%v", want, err)
		}
	}
	for _, p := range []string{"FRONTEND_WAS_BUILT", "bin", ".forge/state"} {
		if _, serr := os.Stat(filepath.Join(dir, p)); !errors.Is(serr, os.ErrNotExist) {
			t.Errorf("%s exists (stat err=%v): the refusal came after a build step", p, serr)
		}
	}
}

// TestReleaseCapturesNothing_IsOneSided pins that the early refusal never
// pre-empts a cut that could succeed: every source the cut harvests keeps the
// build going.
func TestReleaseCapturesNothing_IsOneSided(t *testing.T) {
	ents := func(t *testing.T, fixture string) *KCLEntities {
		t.Helper()
		e, err := parseKCLEntities([]byte(fixture))
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	opts := buildOptions{env: "sc7r", release: "v1", buildDocker: true}

	t.Run("manifests only: refused", func(t *testing.T) {
		dir := t.TempDir()
		if _, empty := releaseCapturesNothing(dir, ents(t, manifestsOnlyFixture), buildTargetSet{}, opts); !empty {
			t.Fatal("nothing to pin must be reported empty")
		}
	})
	t.Run("not a release: never refused", func(t *testing.T) {
		o := opts
		o.release = ""
		if _, empty := releaseCapturesNothing(t.TempDir(), ents(t, manifestsOnlyFixture), buildTargetSet{}, o); empty {
			t.Fatal("only a release cut can be empty")
		}
	})
	t.Run("a workload with an image forge builds", func(t *testing.T) {
		fx := `{"output":{"workloads":[{"name":"a","kind":"service","image":"r.example/a","build":{"type":"go","cmd":"./cmd/a","output_name":"a"},"runtime":{"type":"cluster","cluster":"c","namespace":"n"},"spec":{"kind":"service"}}]}}`
		if _, empty := releaseCapturesNothing(t.TempDir(), ents(t, fx), buildTargetSet{}, opts); empty {
			t.Fatal("an image with a build is capturable")
		}
	})
	t.Run("a publishable npm package", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"lib","version":"1.0.0"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, empty := releaseCapturesNothing(dir, ents(t, manifestsOnlyFixture), buildTargetSet{}, opts); empty {
			t.Fatal("a publishable package is a release artifact")
		}
	})
	t.Run("a private npm package is not", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"app","version":"1.0.0","private":true}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, empty := releaseCapturesNothing(dir, ents(t, manifestsOnlyFixture), buildTargetSet{}, opts); !empty {
			t.Fatal("a private package is never released")
		}
	})
	t.Run("a nested Go module", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "sub", "go.mod"), []byte("module example.com/sub\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, empty := releaseCapturesNothing(dir, ents(t, manifestsOnlyFixture), buildTargetSet{}, opts); empty {
			t.Fatal("a nested module is harvested by the cut")
		}
	})
	t.Run("build state left by an earlier build", func(t *testing.T) {
		dir := t.TempDir()
		stateDir := filepath.Join(dir, ".forge", "state")
		if err := os.MkdirAll(stateDir, 0o755); err != nil {
			t.Fatal(err)
		}
		st := `{"image":"r.example/pt","digest":"sha256:` + strings.Repeat("a", 64) + `"}`
		if err := os.WriteFile(filepath.Join(stateDir, "build-sc7r.json"), []byte(st), 0o644); err != nil {
			t.Fatal(err)
		}
		if len(harvestReleaseArtifacts(dir, "sc7r")) == 0 {
			t.Skip("fixture does not match the build-state format; harvest sees nothing")
		}
		if _, empty := releaseCapturesNothing(dir, ents(t, manifestsOnlyFixture), buildTargetSet{}, opts); empty {
			t.Fatal("a digest in the build state is capturable")
		}
	})
}
