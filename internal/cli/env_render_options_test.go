package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/kclplugin"
)

// `forge env render` previews what an env renders, and an env's declared
// option() values are part of that. It used to refuse `-D` outright ("unknown
// shorthand flag: 'D'") while `forge env up` and `forge env build` took it, so
// checking a render under an option meant dropping to `forge kcl eval`
// (forge bug report e5700acd). One flag, one spelling, every env command that
// renders the declaration.
func TestEnvRenderTakesTheSameDAsEnvUpAndBuild(t *testing.T) {
	for name, newCmd := range map[string]func() *cobra.Command{
		"env up":     newEnvUpCmd,
		"env build":  newEnvBuildCmd,
		"env render": newEnvRenderCmd,
	} {
		f := newCmd().Flags().ShorthandLookup("D")
		if f == nil {
			t.Errorf("forge %s has no -D", name)
			continue
		}
		if f.Name != "option" || f.Value.Type() != "stringArray" {
			t.Errorf("forge %s -D is --%s (%s), want --option (stringArray) like the others", name, f.Name, f.Value.Type())
		}
	}
}

// The manifest render takes the options as raw KCL bindings beside the env
// config: a -D binding wins over a same-named key, and nothing is added when
// no -D was passed (so every other render is byte-identical).
func TestWithRenderOptionKV(t *testing.T) {
	kv := map[string]string{"region": `"us"`, "keep": `"x"`}
	if got := withRenderOptionKV(kv, nil); len(got) != 2 {
		t.Fatalf("no -D changed the bindings: %v", got)
	}
	dArgs, err := parseRenderOptions([]string{"region=eu", "tier=gold"})
	if err != nil {
		t.Fatal(err)
	}
	got := withRenderOptionKV(kv, dArgs)
	want := map[string]string{"region": `"eu"`, "keep": `"x"`, "tier": `"gold"`}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q (all: %v)", k, got[k], v, got)
		}
	}
	if kv["region"] != `"us"` {
		t.Errorf("withRenderOptionKV mutated its input: %v", kv)
	}
}

// End to end: a -D option reaches the manifest stream the render prints, an
// undeclared name is refused like `forge env up` refuses it, and the binding
// does not outlive the command.
func TestEnvRender_DOptionReachesTheManifests(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates KCL; runs in task test")
	}
	kclplugin.Register()
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
	write("forge.yaml", "name: renderopt\nmodule_path: github.com/example/renderopt\nversion: \"0.1.0\"\n")
	write("deploy/kcl/kcl.mod", "[package]\nname = \"renderopt-deploy\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n")
	write("deploy/kcl/prod/main.k", `import forge
import forge.workloads as fw

_tier = option("tier", type="str", default="std", help="Service tier")

_target = forge.ClusterTarget {
    cluster = "gke_example_us-central1_prod"
    namespace = "renderopt-prod"
}

output = forge.render(forge.Bundle {
    project = "renderopt"
    cluster_target = _target
    workloads = [fw.Workload {
        name = "api-" + _tier
        image = "reg.example.com/renderopt"
        runtime = forge.OnCluster {target = _target}
    }]
})
`)
	ensureLedgerReady(t, dir)

	stdout, stderr, err := runRenderCapturingProcessStdout(t, dir, "prod", "--no-digest", "--tag", "t1", "-D", "tier=gold")
	if err != nil {
		t.Fatalf("forge env render prod -D tier=gold: %v\nstderr:\n%s", err, stderr)
	}
	if !strings.Contains(stdout, "name: api-gold") {
		t.Errorf("the manifests do not reflect -D tier=gold (want a Deployment named api-gold):\n%s", stdout)
	}
	if got := activeRenderOptionDArgs(); got != nil {
		t.Errorf("the -D binding outlived the render: %v", got)
	}

	stdout, stderr, err = runRenderCapturingProcessStdout(t, dir, "prod", "--no-digest", "--tag", "t1")
	if err != nil {
		t.Fatalf("forge env render prod: %v\nstderr:\n%s", err, stderr)
	}
	if !strings.Contains(stdout, "name: api-std") {
		t.Errorf("without -D the render should use the declared default (api-std):\n%s", stdout)
	}

	_, _, err = runRenderCapturingProcessStdout(t, dir, "prod", "--no-digest", "--tag", "t1", "-D", "teir=gold")
	if err == nil || !strings.Contains(err.Error(), "declares no option named") {
		t.Errorf("an undeclared -D name must be refused like `forge env up` refuses it, got %v", err)
	}
}
