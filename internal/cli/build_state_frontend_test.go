package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/buildtarget"
	"github.com/reliant-labs/forge/internal/config"
)

// The regression these tests pin, in full, because the failure was invisible:
//
// `forge env build prod --target internal-console --docker --push` built and
// pushed the frontend image correctly, but recorded NOTHING. Only the PROJECT
// image wrote a build-state file (persistProjectBuildState matched exactly
// cfg.Name+" (docker)"), so a frontend's successful push left no trace on disk.
//
// The following `forge env deploy prod --target internal-console` then found no
// digest for that image, fell through to the release ledger's months-old pin,
// and RE-DEPLOYED THE OLD IMAGE — while reporting a successful rollout. In
// production this shipped an operator console built before native sign-in
// existed against a backend that had already moved to it, so the console
// offered an OIDC redirect the backend no longer expected. Nothing in the build
// or deploy output indicated the new image had been discarded.

// withProjectDir makes projectDirForKCL resolve to dir (it walks up for
// forge.yaml from the working directory), so persistImageBuildStates writes
// into the test's own tree. Not parallel-safe: it chdirs, like the other
// project-dir-sensitive tests in this package.
func withProjectDir(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte("name: pt\n"), 0o644); err != nil {
		t.Fatalf("write forge.yaml: %v", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
}

// TestPersistImageBuildStates_WritesFrontendState is the direct reproduction:
// a successful pushed frontend docker build must leave a per-image state file
// that deploy's digest resolver can find. Before the fix this wrote no file at
// all and the assertion below failed on the missing state.
func TestPersistImageBuildStates_WritesFrontendState(t *testing.T) {
	dir := t.TempDir()
	withProjectDir(t, dir)

	const (
		image  = "internal-console"
		tag    = "ship-20260909"
		digest = "sha256:cc7504dddc97bea53887b2e03555c43244a333caf8d095d4223401ab22056eff"
	)

	persistImageBuildStates(
		buildOptions{env: "prod", pushPlan: pushPlan{push: true, env: "prod"}},
		[]buildResult{{
			name:      image + " (docker)",
			kind:      "docker",
			image:     image,
			tag:       tag,
			digest:    digest,
			platforms: []string{"linux/amd64"},
		}},
	)

	got, err := buildtarget.ReadState(dir, "prod", image)
	if err != nil {
		t.Fatalf("read per-image state: %v", err)
	}
	if got == nil {
		t.Fatal("no build state written for the frontend image — deploy will fall back to the stale release digest and silently ship the old image")
	}
	if got.Image != image {
		t.Errorf("Image = %q, want %q (this is the key _image_ref looks up in image_digests)", got.Image, image)
	}
	if got.Digest != digest {
		t.Errorf("Digest = %q, want %q", got.Digest, digest)
	}
	if got.Tag != tag {
		t.Errorf("Tag = %q, want %q", got.Tag, tag)
	}
}

// TestResolveDeployImageDigests_IncludesFrontend closes the loop: the state the
// build now writes must actually reach the per-image digest map the KCL render
// pins each workload to. Writing a file deploy never reads would fix nothing.
func TestResolveDeployImageDigests_IncludesFrontend(t *testing.T) {
	dir := t.TempDir()
	const (
		image  = "internal-console"
		digest = "sha256:cc7504dddc97bea53887b2e03555c43244a333caf8d095d4223401ab22056eff"
	)

	if err := buildtarget.WriteState(dir, "prod", buildtarget.State{
		Service:  image,
		Image:    image,
		Tag:      "ship-20260909",
		Digest:   digest,
		PushedAt: nowRFC3339(),
	}); err != nil {
		t.Fatalf("write state: %v", err)
	}

	digests, err := resolveDeployImageDigests(dir, "prod", false)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if digests[image] != digest {
		t.Errorf("digest map[%q] = %q, want %q — the frontend's freshly built image would deploy on a stale pin", image, digests[image], digest)
	}
}

// TestDockerBuildResult_CarriesImageName pins the field the whole fix hangs
// on. The original bug was a NAME-MATCH filter: persistProjectBuildState
// recorded a result only when its name equalled cfg.Name+" (docker)", and a
// frontend's result is named "<frontend> (docker)", so it never matched and
// nothing was written. Recording now keys off buildResult.image instead, which
// is the bare image name deploy's digest map is keyed by.
//
// If a future change drops that field from the frontend docker path, the
// per-image state silently stops being written and the stale-deploy bug
// returns with no other test noticing — because every other assertion in this
// file constructs its own buildResult rather than getting one from the builder.
func TestDockerBuildResult_CarriesImageName(t *testing.T) {
	dir := t.TempDir()
	withProjectDir(t, dir)

	// No Dockerfile in path: dockerBuild takes its early "skipping docker"
	// return, which runs no docker command. That is enough to pin the
	// result's SHAPE — the display name keeps its suffix, and nothing here
	// may claim a digest it never captured.
	res := dockerBuild(t.Context(), &config.ProjectConfig{Name: "control-plane"},
		"internal-console", filepath.Join(dir, "frontends", "internal-console"), imageTagSet("ghcr.io/acme/internal-console", "v1", false, false), "")

	if res.err != nil {
		t.Fatalf("no-Dockerfile build should skip cleanly, got %v", res.err)
	}
	if res.name != "internal-console (docker)" {
		t.Errorf("display name = %q, want %q", res.name, "internal-console (docker)")
	}
	if res.digest != "" {
		t.Errorf("digest = %q, want empty: nothing was pushed, so there is no registry manifest to address", res.digest)
	}
	// The load-bearing assertion. image identifies WHICH image this result is
	// for, independently of whether the build succeeded, so persistImageBuildStates
	// can record it. Without it the result is unattributable and the state file
	// is never written — the stale-deploy bug.
	if res.image != "internal-console" {
		t.Fatalf("image = %q, want %q — an unattributable result writes no build state, "+
			"so deploy falls back to the release ledger and silently ships the old image", res.image, "internal-console")
	}
	// And it must NOT be the project image's name, which is what the old
	// name-equality filter assumed every docker result was.
	if res.image == "control-plane" {
		t.Fatal("frontend result collided with the project image name")
	}
}

// TestPersistImageBuildStates_SkipsNonImageResults keeps the writer narrow:
// only docker results carrying a bare image name describe something deploy can
// pin. A frontend's `npm run build` result (kind "frontend") names no image, and
// writing state for it would invent a digest key that matches no workload.
func TestPersistImageBuildStates_SkipsNonImageResults(t *testing.T) {
	dir := t.TempDir()
	withProjectDir(t, dir)

	persistImageBuildStates(
		buildOptions{env: "prod"},
		[]buildResult{
			{name: "internal-console", kind: "frontend"},
			{name: "control-plane (docker)", kind: "docker"}, // project image: no bare image name
		},
	)

	stateDir := filepath.Join(dir, ".forge", "state")
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		if os.IsNotExist(err) {
			return // nothing written at all is the correct outcome
		}
		t.Fatalf("read state dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "build-prod-") {
			t.Errorf("wrote per-image state %q for a result that names no image", e.Name())
		}
	}
}
