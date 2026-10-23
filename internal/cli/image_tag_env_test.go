package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// TestPinnedTagOf: a resolved ref answers its tag only for the artifact
// repository it names, matched at a `/` boundary.
func TestPinnedTagOf(t *testing.T) {
	cases := []struct {
		ref, repo, tag string
		ok             bool
	}{
		{"localhost:5051/reliant:e2e", "reliant", "e2e", true},
		{"reliant:e2e", "reliant", "e2e", true},
		{"localhost:5051/acme/app:v2", "acme/app", "v2", true},
		{"localhost:5051/acme/app:v2", "app", "v2", true},
		{"localhost:5051/myapp:v1", "app", "", false},
		{"localhost:5051/reliant", "reliant", "", false},
		{"localhost:5051/reliant@sha256:abc", "reliant", "", false},
		{"localhost:5051/other:v1", "reliant", "", false},
	}
	for _, c := range cases {
		tag, ok := pinnedTagOf(c.ref, c.repo)
		if tag != c.tag || ok != c.ok {
			t.Errorf("pinnedTagOf(%q, %q) = (%q, %v), want (%q, %v)", c.ref, c.repo, tag, ok, c.tag, c.ok)
		}
	}
}

// envImageTagFixture mirrors the control-plane cloud render shape: the
// workloads carry no per-workload pin, so their resolved spec.image carries
// the env's own image_tag ("staging") — the tag the deploy pulls. The build
// side reads it from output.image_tag and spec.image; nothing is scraped
// from the manifest stream.
const envImageTagFixture = `{"output": {
  "project": "control-plane", "env": "staging", "image_tag": "staging",
  "workloads": [
    {"name": "reliant-api-server", "kind": "service", "image": "localhost:5051/reliant",
     "runtime": {"type": "cluster", "cluster": "gke", "namespace": "control-plane-staging"},
     "spec": {"kind": "service", "image": "ghcr.io/reliant-labs/reliant:staging"}},
    {"name": "admin-server", "kind": "service", "image": "localhost:5051/control-plane",
     "runtime": {"type": "cluster", "cluster": "gke", "namespace": "control-plane-staging"},
     "spec": {"kind": "service", "image": "ghcr.io/reliant-labs/control-plane:staging"}}
  ]
}}`

// TestImagePinFor_ReadsTheBuildIdentity: an image's pin is the tag a
// workload's build_image carries; an unpinned image answers "" and so builds
// the build-wide tag (the env's image_tag — see resolveBuildImageTag).
func TestImagePinFor_ReadsTheBuildIdentity(t *testing.T) {
	ents, err := parseKCLEntities([]byte(envImageTagFixture))
	if err != nil {
		t.Fatalf("parseKCLEntities: %v", err)
	}
	if got := imagePinFor(ents, "control-plane"); got != "" {
		t.Errorf("imagePinFor(control-plane): got %q, want no pin", got)
	}
	// BuildImage is the full repository plus the pin, as the render emits it —
	// the registry is part of the image now, on the build side too.
	ents.Workloads[0].BuildImage = "localhost:5051/reliant:v1.4.2"
	if got := imagePinFor(ents, "reliant"); got != "v1.4.2" {
		t.Errorf("imagePinFor(reliant) with a pinned build_image: got %q, want v1.4.2", got)
	}
	// A pin on the resolved spec.image alone is NOT the build's: build
	// identity is read off the entity, the same on every runtime.
	ents.Workloads[0].BuildImage = "localhost:5051/reliant"
	ents.Workloads[0].Spec.Image = "ghcr.io/reliant-labs/reliant:v1.4.2"
	if got := imagePinFor(ents, "reliant"); got != "" {
		t.Errorf("imagePinFor(reliant) unpinned build_image: got %q, want no pin", got)
	}
	// An artifact with an org path matches the whole path.
	ents.Workloads[0].Image = "localhost:5051/acme/reliant"
	ents.Workloads[0].BuildImage = "localhost:5051/acme/reliant:e2e"
	if got := imagePinFor(ents, "reliant"); got != "e2e" {
		t.Errorf("imagePinFor(acme/reliant): got %q, want the pin e2e", got)
	}
	if got := imagePinFor(nil, "control-plane"); got != "" {
		t.Errorf("imagePinFor(nil): got %q, want empty", got)
	}
}

// TestBuildExternalServices_TagDefaultsToEnvImageTag is the gotcha-A
// regression: when the rendered env references image `reliant:staging`
// (the deploy ref), an external build of the `reliant` service with NO
// per-service pin must use `staging` as ${TAG} — NOT the env-wide
// build-loop tag the caller threaded (here a stand-in git-describe
// value). This is what makes `forge env build staging --push` push
// the SAME tag `forge env deploy staging` references, instead of pushing
// git-describe and deploying "staging" → ImagePullBackOff.
func TestBuildExternalServices_TagDefaultsToEnvImageTag(t *testing.T) {
	projDir := t.TempDir()
	ents, err := parseKCLEntities([]byte(envImageTagFixture))
	if err != nil {
		t.Fatalf("parseKCLEntities: %v", err)
	}
	services := []WorkloadEntity{
		// no cwd → runs from project root, writes state with the resolved tag
		shellSvc("reliant-api-server", "reliant", "true", "", nil),
	}
	opts := buildOptions{env: "staging", parallel: false, buildDocker: true}
	// The build-wide tag is the env's image_tag, ahead of git-describe —
	// the one resolution every lane then builds from.
	buildTag, source, err := resolveBuildImageTag(context.Background(), ents, opts)
	if err != nil || buildTag != "staging" || !strings.Contains(source, "image_tag") {
		t.Fatalf("resolveBuildImageTag = (%q, %q, %v), want the env's image_tag staging", buildTag, source, err)
	}
	results := buildExternalServices(
		context.Background(), services, opts, buildTag, projDir)
	if len(results) != 1 || results[0].err != nil {
		t.Fatalf("results: %+v", results)
	}
	// The persisted per-service state should record the ENV tag, not the
	// git-describe stand-in.
	st, err := ReadBuildState(projDir, "staging")
	if err != nil {
		t.Fatalf("ReadBuildState: %v", err)
	}
	if st == nil || st.Tag != "staging" {
		t.Errorf("deploy build-state tag: got %+v, want staging", st)
	}
	auditPath := filepath.Join(projDir, ".forge", "state", "build-staging-reliant-api-server.json")
	if _, err := os.Stat(auditPath); err != nil {
		t.Errorf("per-service state at %s: %v", auditPath, err)
	}
}

// TestBuildExternalServices_PerServicePinWins confirms an explicit
// per-workload tag pin on the build identity (build_image) (e2e's
// reliant_image_tag="e2e" / workspace-base "dev-per-daemon") OVERRIDES both
// the env-wide build tag and the env's image_tag — so e2e's pinned tags keep building exactly
// what the daemon pods pull. This is the property that keeps
// `forge env up e2e` building the tags it deploys.
func TestBuildExternalServices_PerServicePinWins(t *testing.T) {
	projDir := t.TempDir()
	// The env's tag is :staging, but the workload's resolved image pins
	// "dev-per-daemon".
	wsbase := shellSvc("workspace-base", "workspace-base", "true", "", nil)
	wsbase.BuildImage = "workspace-base:dev-per-daemon" // the per-workload pin
	services := []WorkloadEntity{wsbase}
	opts := buildOptions{env: "e2e", parallel: false}
	// "staging" stands in for the build-wide tag (the env's image_tag).
	results := buildExternalServices(
		context.Background(), services, opts, "staging", projDir)
	if len(results) != 1 || results[0].err != nil {
		t.Fatalf("results: %+v", results)
	}
	st, err := ReadBuildState(projDir, "e2e")
	if err != nil {
		t.Fatalf("ReadBuildState: %v", err)
	}
	if st == nil || st.Tag != "dev-per-daemon" {
		t.Errorf("deploy build-state tag: got %+v, want dev-per-daemon (the per-service pin)", st)
	}
}

// buildOnlyPinMain is control-plane e2e's workspace-base: a ShellBuild tool
// bound to BuildOnly whose image pins its own tag. Its spec.image is "-" (a
// build-only workload runs nowhere, so nothing resolves a pull ref), which is
// exactly why the pin must ride the entity rather than spec.image.
const buildOnlyPinMain = `
import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-cp", namespace = "cp-e2e"}

output = forge.render(forge.Bundle {
    project = "cp"
    cluster_target = _k3d
    image_tag = "e2e"
    workloads = [
        fw.Workload {
            name = "workspace-base"
            kind = "tool"
            image = "localhost:5051/workspace-base:dev-per-daemon"
            build = forge.ShellBuild {cmd = "true"}
            runtime = forge.BuildOnly {}
        }
        fw.Workload {
            name = "hostapp"
            image = "localhost:5051/hostapp:v3"
            build = forge.ShellBuild {cmd = "true"}
            command = ["./bin/hostapp"]
            runtime = forge.OnHost {}
        }
        fw.Workload {name = "api", image = "localhost:5051/api", build = forge.ShellBuild {cmd = "true"}, runtime = forge.OnCluster {target = _k3d}}
    ]
})
`

// TestBuildPlan_BuildOnlyPinnedTagIsTheTag: build identity does not depend on
// the runtime. A pinned tag on a BuildOnly (or host) workload is the ${TAG}
// its build is handed; an unpinned one gets the env tag.
func TestBuildPlan_BuildOnlyPinnedTagIsTheTag(t *testing.T) {
	out := renderKCLProject(t, writeKCLProject(t, buildOnlyPinMain), "env=e2e")
	ents, err := parseKCLEntities(out)
	if err != nil {
		t.Fatalf("parseKCLEntities: %v", err)
	}
	for image, want := range map[string]string{"workspace-base": "dev-per-daemon", "hostapp": "v3", "api": ""} {
		if got := imagePinFor(ents, image); got != want {
			t.Errorf("imagePinFor(%s) = %q, want %q", image, got, want)
		}
	}
	report := planBuild(context.Background(), planInputs{
		cfg:         &config.ProjectConfig{Name: "cp"},
		entities:    ents,
		opts:        buildOptions{env: "e2e"},
		resolvedTag: "e2e",
		projectDir:  t.TempDir(),
	})
	whats := map[string]string{}
	for _, s := range report.steps {
		if s.kind == "external" {
			whats[s.name] = s.what
		}
	}
	for name, want := range map[string]string{"workspace-base": "tag dev-per-daemon", "hostapp": "tag v3", "api": "tag e2e"} {
		if !strings.Contains(whats[name], want) {
			t.Errorf("plan step %s = %q, want it to carry %s", name, whats[name], want)
		}
	}
}
