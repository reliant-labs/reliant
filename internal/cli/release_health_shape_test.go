package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/reliant-labs/forge/internal/kclplugin"
	"github.com/reliant-labs/forge/internal/kclrender"
	"github.com/reliant-labs/forge/pkg/release"
)

// renderReleaseHealth renders a one-workload env with the given
// `release_health = ...` expression ("" for none) through the seam
// `forge env shape` uses, and returns the decoded entities.
func renderReleaseHealth(t *testing.T, decl string) *KCLEntities {
	t.Helper()
	if testing.Short() {
		t.Skip("-short: renders KCL; runs in task test")
	}
	dir := t.TempDir()
	if decl != "" {
		decl = "release_health = " + decl
	}
	files := map[string]string{
		"kcl.mod": "[package]\nname = \"releasehealth\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n",
		"main.k": `import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

output = forge.render(forge.Bundle {
    project = "demo"
    ` + decl + `
    workloads = [fw.Workload {name = "api", image = "ghcr.io/acme/api", runtime = forge.OnCluster {target = _k3d}}]
})
`,
	}
	for f, c := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	kclplugin.Register()
	out, err := kclrender.Run(dir, dir, nil)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	e, err := parseKCLEntities(out)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return e
}

// forge.ReleaseHealth {} renders exactly release.DefaultReleaseHealth: the two
// halves hold the defaults separately, and this keeps them from drifting.
func TestReleaseHealthDefaultsAgreeWithKCL(t *testing.T) {
	e := renderReleaseHealth(t, "forge.ReleaseHealth {}")
	if e.ReleaseHealth == nil {
		t.Fatal("an env that declares release_health rendered none")
	}
	if want := release.DefaultReleaseHealth(); !reflect.DeepEqual(*e.ReleaseHealth, want) {
		t.Errorf("KCL and Go disagree on the defaults:\n kcl %+v\n go  %+v", *e.ReleaseHealth, want)
	}
	if got := shapeReleaseHealthOf(e); got != nil {
		t.Errorf("an all-default policy must not be recorded in the shape, got %+v", got)
	}
	if shapeReleaseHealthOf(renderReleaseHealth(t, "")) != nil {
		t.Error("an env with no release_health recorded one")
	}
}

// KCL -> render contract -> shape field, and what KCL accepts the shape
// validates.
func TestReleaseHealthReachesTheShape(t *testing.T) {
	e := renderReleaseHealth(t, `forge.ReleaseHealth {
        window = "30m"
        max_p95_ratio = 2
        fail_on = ["latency", "new_issue", "error_rate"]
    }`)
	got := shapeReleaseHealthOf(e)
	if got == nil {
		t.Fatal("a non-default policy was not recorded")
	}
	want := release.DefaultReleaseHealth()
	want.WindowSeconds = 1800
	want.MaxP95Ratio = 2
	want.FailOn = []string{release.HealthErrorRate, release.HealthLatency, release.HealthNewIssue}
	if !reflect.DeepEqual(*got, want) {
		t.Errorf("shape policy:\n got  %+v\n want %+v", *got, want)
	}
	if err := (release.Shape{Kind: release.EnvSelfManaged, ReleaseHealth: got}).Canonical().Validate(); err != nil {
		t.Errorf("a policy KCL accepted must satisfy the shape's validation: %v", err)
	}
}
