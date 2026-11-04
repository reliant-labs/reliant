package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/kclplugin"
)

// `forge env render <env>` is the AUTHORITATIVE off-base check (ADR-0003 F1),
// and it is an error here rather than a warning for one reason: this render is
// what the deploy applies, so printing manifests the control plane will refuse
// to publish makes the render a document nobody can act on. The same fact in
// `forge lint` is a finding, because lint judges envs nobody is deploying.
//
// THE RENDER MAKES NO CALL, and these tests prove it: each one runs against a
// bare temp directory with no credential that resolves and no control plane to
// reach, and the comparison still happens — because both halves now come out
// of the checkout. The base is composed from the env's own
// `control_plane.organization` and `registry_host`.
//
// The split that carries the weight is verified vs unverified. An env that
// declares no organization has no base, and the render must NOT fail there,
// because failing would assert "outside the registry" about an image forge
// never compared. KCL requires an organization of anything hosted, so that
// state is reachable only for an env declaring a control plane and nothing
// bound to it.

// testRenderOrg is the organization the fixtures' CREDENTIAL acts for (stubbed;
// nothing in the KCL names it), and testRenderPushBase is the base it composes
// with the project name and the declared registry host.
const (
	testRenderRegistryHost = "registry.reliant.dev"
	testRenderOrg          = "org-7"
	testRenderPushBase     = testRenderRegistryHost + "/" + testRenderOrg + "/hostedrender"
)

// writeHostedRenderProject is a hosted env with one OnHosted workload whose
// image is `image`. The control plane declares its registry host and nothing
// about an org: that comes from the credential, stubbed by the caller.
func writeHostedRenderProject(t *testing.T, image string) string {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("forge.yaml", "name: hostedrender\nmodule_path: github.com/example/hostedrender\nversion: \"0.1.0\"\n")
	write("deploy/kcl/kcl.mod", "[package]\nname = \"hostedrender-deploy\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n")
	orgDecl := "\n        registry_host = \"" + testRenderRegistryHost + "\""
	write("deploy/kcl/prod/main.k", `import forge
import forge.workloads as fw

_bundle = forge.Bundle {
    project = "hostedrender"
    control_plane = forge.ControlPlane {
        endpoint = "https://cp.example"
        token_env = "HOSTEDRENDER_CP_TOKEN"`+orgDecl+`
    }
    workloads = [fw.Workload {
        name = "api"
        image = "`+image+`"
        ports = [fw.Port {name = "http", port = 8080, expose = True}]
        runtime = forge.OnHosted {}
    }]
}

output = forge.render(_bundle)
`)
	markServiceProject(t, dir)
	return dir
}

// A host-bearing image forge can PROVE is off-base fails the render, naming
// the subtree and the exact replacement.
func TestEnvRender_RefusesAVerifiedOffBaseHostedImage(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL; skipped in -short")
	}
	kclplugin.Register()
	stubControlPlaneOrg(t, testRenderOrg)
	dir := writeHostedRenderProject(t, "ghcr.io/acme/api")
	t.Setenv("HOSTEDRENDER_CP_TOKEN", "rlat_test")

	stdout, stderr, err := runRenderCapturingProcessStdout(t, dir, "prod")
	if err == nil {
		t.Fatalf("render must refuse an image outside the push base\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	for _, want := range []string{"would refuse to publish", testRenderPushBase, `image = "api"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal missing %q:\n%v", want, err)
		}
	}
}

// WITH NO CREDENTIAL THE RENDER STILL WORKS. It runs on a fresh checkout, so it
// cannot require the org: it composes no base, and reports the host-bearing
// image as the weaker, unverified fact rather than refusing over something it
// never checked.
func TestEnvRender_NoCredentialRendersAndWarnsUnverified(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL; skipped in -short")
	}
	kclplugin.Register()
	stubControlPlaneOrgError(t, errors.New("no control-plane credential"))
	dir := writeHostedRenderProject(t, "ghcr.io/acme/api")

	_, stderr, err := runRenderCapturingProcessStdout(t, dir, "prod")
	if err != nil {
		t.Fatalf("a render with no credential must not fail: %v\nstderr:\n%s", err, stderr)
	}
	if !strings.Contains(stderr, "host-bearing image") {
		t.Errorf("the unverified warning is missing:\n%s", stderr)
	}
}

// A BARE hosted image — the shape ADR-0003 F1 wants — renders clean, with no
// warning and no refusal, with a base declared.
func TestEnvRender_BareHostedImageIsClean(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL; skipped in -short")
	}
	kclplugin.Register()
	stubControlPlaneOrg(t, testRenderOrg)
	dir := writeHostedRenderProject(t, "api")
	t.Setenv("HOSTEDRENDER_CP_TOKEN", "rlat_test")

	_, stderr, err := runRenderCapturingProcessStdout(t, dir, "prod")
	if err != nil {
		t.Fatalf("a bare hosted image must render: %v\nstderr:\n%s", err, stderr)
	}
	for _, gone := range []string{"drop the host", "would refuse to publish"} {
		if strings.Contains(stderr, gone) {
			t.Errorf("a bare hosted image produced %q:\n%s", gone, stderr)
		}
	}
}

// An image already UNDER the platform's base is redundant, not wrong: it
// renders clean too.
func TestEnvRender_ImageUnderThePushBaseIsClean(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL; skipped in -short")
	}
	kclplugin.Register()
	stubControlPlaneOrg(t, testRenderOrg)
	dir := writeHostedRenderProject(t, testRenderPushBase+"/api")
	t.Setenv("HOSTEDRENDER_CP_TOKEN", "rlat_test")

	_, stderr, err := runRenderCapturingProcessStdout(t, dir, "prod")
	if err != nil {
		t.Fatalf("an image under the base must render: %v\nstderr:\n%s", err, stderr)
	}
	if strings.Contains(stderr, "would refuse to publish") {
		t.Errorf("an image under the base was refused:\n%s", stderr)
	}
}
