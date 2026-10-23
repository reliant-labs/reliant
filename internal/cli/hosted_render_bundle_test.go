package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/kclplugin"
	releasepkg "github.com/reliant-labs/forge/pkg/release"
)

// `forge env render` of a HOSTED env emits the three tier kinds under the
// hosted cluster, in the same stream the bundle is sealed from. Before this a
// hosted env rendered nothing the control plane could apply.
func TestEnvRenderOfAHostedEnvEmitsTheTierRecords(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates KCL")
	}
	kclplugin.Register()
	dir := t.TempDir()
	write := func(rel, content string) {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("forge.yaml", "name: hounders\nmodule_path: github.com/example/hounders\nversion: \"0.1.0\"\n")
	write("deploy/kcl/kcl.mod", "[package]\nname = \"hounders-deploy\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n")
	write("deploy/kcl/prod/main.k", `import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "hounders"
    env = "prod"
    control_plane = forge.ControlPlane {}
    secret_provider = forge.HostedSecrets {}
    workloads = [fw.Workload {
        name = "api"
        image = "hounders"
        args = ["api"]
        ports = [fw.Port {name = "http", port = 8080, expose = True}]
        runtime = forge.OnHosted {}
    }]
    databases = [forge.ManagedDatabase {name = "db", runtime = forge.OnHosted {}}]
})
`)
	stdout, stderr, err := runRenderCapturingProcessStdout(t, dir, "prod")
	if err != nil {
		t.Fatalf("forge env render prod: %v\nstderr:\n%s", err, stderr)
	}
	for _, want := range []string{"kind: Workload", "kind: ManagedDatabase", "# cluster: " + releasepkg.BundleHostedCluster} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the hosted render lacks %q:\n%s", want, stdout)
		}
	}
	// This render has no control plane to read a release from, so the
	// records are shown over PLACEHOLDER digests — obviously fake, and a
	// bundle refuses to seal them (see TestHostedBundleObjectsFlagPlaceholderDigests).
	if !strings.Contains(stdout, "preflight.forge.invalid/hounders@sha256:"+strings.Repeat("0", 64)) {
		t.Errorf("an unpinned hosted render must show placeholder digests, not invent a pin:\n%s", stdout)
	}
	for _, forbidden := range []string{"forge.dev/org-id", "forge.dev/deployment-id", "namespace:"} {
		if strings.Contains(stdout, forbidden) {
			t.Errorf("a hosted record carries %q; identity is the platform's to stamp:\n%s", forbidden, stdout)
		}
	}
}
