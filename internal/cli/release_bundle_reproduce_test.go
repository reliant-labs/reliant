package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/kclplugin"
)

// 2026-10-19, control-plane prod: a versioned redeploy from the release's own
// commit, with the release's own forge, could not reproduce the release's
// bundle. One object differed — the otel gateway, whose image is composed by
// hand as forge.image_ref_for(<repo>, image_tag). The cut rendered
// `<repo>:<release>@sha256:…` because its build state held a digest under
// `<repo>:<release>`; the redeploy's build state did not, so the same call
// fell back to the bare `<repo>@sha256:…`.
//
// A release's images are pushed under the release's name, so the digest it
// pins for a repository IS the digest of `<repo>:<release>`, and a render of
// that release must know it from the release alone.
func TestResolveDeployDigests_NamedReleasePinsItsImagesUnderTheReleaseTag(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	const repo = "reg.example.com/acme/otelcol-reliant"
	if err := testCutRelease(t, dir, rel("v1.0.0", "2026-01-01T00:00:00Z", "", false, map[string]string{repo: sha("a")})); err != nil {
		t.Fatalf("cut: %v", err)
	}
	named := withHostedPinRelease(ctx, "v1.0.0")

	// No build state at all: a fresh checkout of the release's commit.
	got, bound, err := resolveDeployDigests(named, dir, "prod", false, testBindings(t, dir), testReleases(t, dir))
	if err != nil || bound != "v1.0.0" {
		t.Fatalf("resolve: bound=%q err=%v", bound, err)
	}
	if got[repo] != sha("a") || got[repo+":v1.0.0"] != sha("a") {
		t.Fatalf("named release pins = %v; want %s under both %s and %s:v1.0.0", got, sha("a"), repo, repo)
	}

	// A LATER build on this machine, under another tag: the release still
	// pins its own tag, and the later tag is redirected to the release.
	if err := WriteBuildState(dir, "prod", BuildState{Image: repo, Tag: "v2.0.0", Pushed: true, Digest: sha("b")}); err != nil {
		t.Fatal(err)
	}
	got, _, err = resolveDeployDigests(named, dir, "prod", false, testBindings(t, dir), testReleases(t, dir))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	for _, key := range []string{repo, repo + ":v1.0.0", repo + ":v2.0.0"} {
		if got[key] != sha("a") {
			t.Errorf("%s = %q, want the release's %s", key, got[key], sha("a"))
		}
	}
}

// End to end through the bundle's own render: the release's objects are the
// same whatever build state the machine holds — the cut's, a later build's, or
// none.
func TestRenderEnvShape_NamedReleaseReproducesImageRefForWithoutTheCutsBuildState(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates KCL; runs in task test")
	}
	kclplugin.Register()
	dir := writeRenderStdoutProject(t)
	main := filepath.Join(dir, "deploy", "kcl", "prod", "main.k")
	src, err := os.ReadFile(main)
	if err != nil {
		t.Fatal(err)
	}
	// control-plane's otel gateway, in miniature: an image ref composed by
	// hand from the env's image_tag.
	patched := strings.Replace(string(src), `fw.Workload {name = "api", image = "reg.example.com/rendertest"}`,
		`fw.Workload {name = "api", image = "reg.example.com/rendertest", env = {GATEWAY_IMAGE = forge.image_ref_for("reg.example.com/rendertest", _tagopt)}}`, 1)
	patched = strings.Replace(patched, "_bundle = forge.Bundle {", "_tagopt = option(\"image_tag\") or \"prod\"\n_bundle = forge.Bundle {", 1)
	if patched == string(src) {
		t.Fatal("fixture patch did not apply")
	}
	if err := os.WriteFile(main, []byte(patched), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := testCutRelease(t, dir, rel("v1.0.0", "2026-01-01T00:00:00Z", "", false,
		map[string]string{"reg.example.com/rendertest": renderReleaseDigest})); err != nil {
		t.Fatalf("cut: %v", err)
	}
	t.Chdir(dir)

	render := func(state *BuildState) string {
		t.Helper()
		_ = os.Remove(filepath.Join(dir, ".forge", "state", "build-prod.json"))
		if state != nil {
			if err := WriteBuildState(dir, "prod", *state); err != nil {
				t.Fatal(err)
			}
		}
		doc, err := renderEnvShape(withHostedPinRelease(context.Background(), "v1.0.0"), io.Discard, dir, "prod")
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		return shapeObjectNamed(t, doc.Shape, "Deployment", "api").Hash
	}
	// What the cut's machine held: the release's images, pushed under its name.
	cut := render(&BuildState{Image: "reg.example.com/rendertest", Tag: "v1.0.0", Pushed: true,
		PushedAt: "2026-09-24T00:00:00Z", Digest: renderReleaseDigest})
	if fresh := render(nil); fresh != cut {
		t.Errorf("a checkout with no build state renders the release's Deployment differently (%s vs the cut's %s)", fresh, cut)
	}
	if later := render(&BuildState{Image: "reg.example.com/rendertest", Tag: "later", Pushed: true,
		PushedAt: "2026-09-25T00:00:00Z", Digest: renderBuiltDigest}); later != cut {
		t.Errorf("a later build's state changes the release's Deployment (%s vs the cut's %s)", later, cut)
	}
}
