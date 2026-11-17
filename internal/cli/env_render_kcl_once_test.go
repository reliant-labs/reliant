package cli

// How many times one `forge env render` evaluates the env's declaration.
//
// An evaluation of a large env is seconds of CPU — control-plane's prod
// measured ~4.5 CPU-seconds each — and `forge env render` used to make four:
// the probe that decides whether the env runs on this machine, the render's
// own RenderKCL, the ledger selection's, and the manifest render. Two of those
// were the same evaluation in the same render context. For an env that runs
// nowhere here the probe leaves exactly the context it rendered in, so its
// entities ARE the declaration; and the ledger selection reads the render's
// entities instead of rendering them again. These pin the declaration count
// (the manifest render, which binds the tag and namespace, is separate):
//
//   - a cloud env: ONE evaluation;
//   - a local env: TWO — the probe renders with the block allocator disarmed,
//     and arming it changes what a keyed allocate_port resolves to, so the
//     render proper must evaluate again.
//
// A count that creeps back up is a render that got slower for every project,
// silently; nothing else would notice.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/kclplugin"
)

// writeKCLOnceProject is a one-workload project whose prod targets cluster.
func writeKCLOnceProject(t *testing.T, cluster string) string {
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
	write("forge.yaml", "name: kclonce\nmodule_path: github.com/example/kclonce\nversion: \"0.1.0\"\n")
	write("deploy/kcl/kcl.mod", "[package]\nname = \"kclonce-deploy\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n")
	write("deploy/kcl/prod/main.k", `import forge
import forge.workloads as fw

_target = forge.ClusterTarget {
    cluster = "`+cluster+`"
    namespace = "kclonce-prod"
}

_bundle = forge.Bundle {
    project = "kclonce"
    cluster_target = _target
    workloads = [_w | {runtime = forge.OnCluster {target = _target}} if not _w.runtime else _w for _w in [
        fw.Workload {name = "api", image = "reg.example.com/kclonce"}
    ]]
}

output = forge.render(_bundle)
`)
	ensureLedgerReady(t, dir)
	return dir
}

func declarationRendersFor(t *testing.T, dir string) int64 {
	t.Helper()
	kclplugin.Register()
	before := declarationRenders.Load()
	if _, stderr, err := runRenderCapturingProcessStdout(t, dir, "prod", "--no-digest", "--tag", "kclonce"); err != nil {
		t.Fatalf("forge env render prod: %v\nstderr:\n%s", err, stderr)
	}
	return declarationRenders.Load() - before
}

func TestEnvRender_EvaluatesACloudEnvsDeclarationOnce(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates KCL; runs in task test")
	}
	if n := declarationRendersFor(t, writeKCLOnceProject(t, "gke_example_us-central1_prod")); n != 1 {
		t.Errorf("`forge env render prod` of an env that runs nowhere on this machine evaluated its declaration %d times, want 1. "+
			"The purpose probe's render is the declaration (activateDevStackRendered), and the ledger selection reads it "+
			"(ledgerForRendered); every extra evaluation is seconds of CPU per render.", n)
	}
}

func TestEnvRender_EvaluatesALocalEnvsDeclarationTwice(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates KCL; runs in task test")
	}
	if n := declarationRendersFor(t, writeKCLOnceProject(t, "k3d-kclonce")); n != 2 {
		t.Errorf("`forge env render prod` of an env that runs on this machine evaluated its declaration %d times, want 2: "+
			"the purpose probe (allocator disarmed), then the render in the armed context. The ledger selection must "+
			"read the render's entities (ledgerForRendered), not evaluate a third time.", n)
	}
}
