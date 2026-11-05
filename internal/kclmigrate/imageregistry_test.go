package kclmigrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTree materializes a deploy/kcl tree from a path→content map.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func read(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const workloadsWithBareImage = `import forge
import forge.workloads as fw

api = fw.Workload {
    name = "api"
    kind = "service"
    image = "shop"
    build = forge.GoBuild {cmd = "./cmd/shop", output_name = "shop"}
}
`

// envMain builds an env main.k with one declared registry and one binding, in
// the shape forge scaffolds: the runtime lives in a binder LAMBDA, so the
// migration must read the lambda to know whether this env pulls an image.
func envMain(registry, binder, runtime string) string {
	return `import forge
import forge.workloads as wl

_target = forge.ClusterTarget {
    cluster = "c"
    namespace = "ns"
    registry = "` + registry + `"
}

` + binder + ` = lambda w: fw.Workload -> fw.Workload {
    w | {runtime = forge.` + runtime + ` {target = _target}}
}

_workloads = [
    ` + binder + `(wl.api)
]

output = forge.render(forge.Bundle {project = "shop", workloads = _workloads})
`
}

// TestImageRegistry_UnambiguousRewritesTheImage: one env, bound to a cluster,
// with one declared registry. The registry moves onto the image.
func TestImageRegistry_UnambiguousRewritesTheImage(t *testing.T) {
	root := writeTree(t, map[string]string{
		"deploy/kcl/workloads.k": workloadsWithBareImage,
		"deploy/kcl/prod/main.k": envMain("ghcr.io/acme", "_on_cluster", "OnCluster"),
	})

	res, err := ImageRegistry(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Refused() {
		t.Fatalf("refused an unambiguous migration: %+v", res.Ambiguous)
	}
	if got := read(t, root, "deploy/kcl/workloads.k"); !strings.Contains(got, `image = "ghcr.io/acme/shop"`) {
		t.Errorf("image was not completed with the registry that env declared:\n%s", got)
	}
	if got := read(t, root, "deploy/kcl/prod/main.k"); strings.Contains(got, "registry") {
		t.Errorf("env registry survived the migration:\n%s", got)
	}
}

// TestImageRegistry_OnHostEnvRegistryIsDropped: an env that binds the workload
// to the HOST declared a registry, but nothing there ever pulled an image — so
// that registry says nothing about where the image should live. It is dropped,
// and it must NOT be prefixed onto the image.
func TestImageRegistry_OnHostEnvRegistryIsDropped(t *testing.T) {
	root := writeTree(t, map[string]string{
		"deploy/kcl/workloads.k": workloadsWithBareImage,
		"deploy/kcl/dev/main.k":  envMain("localhost:5050", "_on_host", "OnHost"),
		"deploy/kcl/prod/main.k": envMain("ghcr.io/acme", "_on_cluster", "OnCluster"),
	})

	res, err := ImageRegistry(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Refused() {
		t.Fatalf("refused, but dev's registry was never a candidate: %+v", res.Ambiguous)
	}
	// prod's registry wins because prod is the only env that pulls.
	if got := read(t, root, "deploy/kcl/workloads.k"); !strings.Contains(got, `image = "ghcr.io/acme/shop"`) {
		t.Errorf("expected prod's registry on the image, got:\n%s", got)
	}
	if got := read(t, root, "deploy/kcl/dev/main.k"); strings.Contains(got, "registry") {
		t.Errorf("dev's registry survived:\n%s", got)
	}
	if len(res.Dropped) == 0 {
		t.Error("dropping dev's registry was not reported; a silent drop is indistinguishable from a bug")
	}
}

// TestImageRegistry_AmbiguousRefusesWithPerEnvKCL: the control-plane shape —
// several local-registry envs and a cloud one, all binding the same workload to
// a CLUSTER. There is no single correct reference, so forge refuses and prints
// the per-env KCL rather than picking one.
func TestImageRegistry_AmbiguousRefusesWithPerEnvKCL(t *testing.T) {
	root := writeTree(t, map[string]string{
		"deploy/kcl/workloads.k":    workloadsWithBareImage,
		"deploy/kcl/dev/main.k":     envMain("localhost:5051", "_on_k3d", "OnCluster"),
		"deploy/kcl/dev-k8s/main.k": envMain("localhost:5051", "_on_k3d", "OnCluster"),
		"deploy/kcl/e2e/main.k":     envMain("localhost:5051", "_on_k3d", "OnCluster"),
		"deploy/kcl/prod/main.k":    envMain("us-central1-docker.pkg.dev/proj/repo", "_on_cluster", "OnCluster"),
	})
	before := read(t, root, "deploy/kcl/workloads.k")

	res, err := ImageRegistry(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Refused() {
		t.Fatal("expected a refusal: the same workload pulls from two different registries")
	}
	if got := read(t, root, "deploy/kcl/workloads.k"); got != before {
		t.Error("a refused migration must not write: a partial rewrite leaves a tree neither forge understands")
	}

	a := res.Ambiguous[0]
	if a.Workload != "api" {
		t.Errorf("Workload = %q, want api", a.Workload)
	}
	// Every pulling env must appear, with its own registry.
	for env, want := range map[string]string{
		"dev": "localhost:5051", "dev-k8s": "localhost:5051", "e2e": "localhost:5051",
		"prod": "us-central1-docker.pkg.dev/proj/repo",
	} {
		if a.ByEnv[env] != want {
			t.Errorf("ByEnv[%q] = %q, want %q", env, a.ByEnv[env], want)
		}
	}

	// The runbook must be copy-pasteable KCL, per env, naming both registries.
	book := a.Runbook()
	for _, want := range []string{
		`wl.api | {image = "localhost:5051/shop"}`,
		`wl.api | {image = "us-central1-docker.pkg.dev/proj/repo/shop"}`,
		"deploy/kcl/prod/main.k",
	} {
		if !strings.Contains(book, want) {
			t.Errorf("runbook is missing %q:\n%s", want, book)
		}
	}
}

// TestImageRegistry_CompleteImageIsLeftAlone: a workload that already names its
// registry — and a third-party image — must not be rewritten.
func TestImageRegistry_CompleteImageIsLeftAlone(t *testing.T) {
	workloads := `import forge
import forge.workloads as fw

api = fw.Workload {
    name = "api"
    image = "ghcr.io/acme/api"
    build = forge.GoBuild {cmd = "./cmd/api", output_name = "api"}
}

nats = fw.Workload {
    name = "nats"
    image = "docker.io/library/nats:2.10"
}
`
	root := writeTree(t, map[string]string{
		"deploy/kcl/workloads.k": workloads,
		"deploy/kcl/prod/main.k": envMain("registry.example.com/other", "_on_cluster", "OnCluster"),
	})

	res, err := ImageRegistry(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Refused() {
		t.Fatalf("refused, but no image was bare: %+v", res.Ambiguous)
	}
	got := read(t, root, "deploy/kcl/workloads.k")
	if !strings.Contains(got, `image = "ghcr.io/acme/api"`) {
		t.Error("an already-complete reference was rewritten")
	}
	if !strings.Contains(got, `image = "docker.io/library/nats:2.10"`) {
		t.Error("a third-party reference was rewritten")
	}
	if strings.Contains(got, "registry.example.com") {
		t.Errorf("a removed env registry was prefixed onto an image that already had one:\n%s", got)
	}
}

// TestImageRegistry_NeverEditsBinderExpressions pins the safety rule: the
// binder lambdas are READ to learn which runtime each env uses, and never
// rewritten. A migration that edits a lambda body is how it corrupts a file it
// does not understand.
func TestImageRegistry_NeverEditsBinderExpressions(t *testing.T) {
	root := writeTree(t, map[string]string{
		"deploy/kcl/workloads.k": workloadsWithBareImage,
		"deploy/kcl/prod/main.k": envMain("ghcr.io/acme", "_deploy_it", "OnCluster"),
	})

	if _, err := ImageRegistry(root, true); err != nil {
		t.Fatal(err)
	}
	got := read(t, root, "deploy/kcl/prod/main.k")
	for _, want := range []string{
		"_deploy_it = lambda w: fw.Workload -> fw.Workload {",
		"w | {runtime = forge.OnCluster {target = _target}}",
		"_deploy_it(wl.api)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("binder expression was altered; missing %q:\n%s", want, got)
		}
	}
}

// TestImageRegistry_DryRunWritesNothing: the report is computed without
// touching the tree, so `forge generate` can refuse and explain before anything
// is modified.
func TestImageRegistry_DryRunWritesNothing(t *testing.T) {
	root := writeTree(t, map[string]string{
		"deploy/kcl/workloads.k": workloadsWithBareImage,
		"deploy/kcl/prod/main.k": envMain("ghcr.io/acme", "_on_cluster", "OnCluster"),
	})
	before := read(t, root, "deploy/kcl/workloads.k")
	beforeEnv := read(t, root, "deploy/kcl/prod/main.k")

	res, err := ImageRegistry(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Applied() {
		t.Error("dry run reported no rewrites, but there was one to make")
	}
	if read(t, root, "deploy/kcl/workloads.k") != before || read(t, root, "deploy/kcl/prod/main.k") != beforeEnv {
		t.Error("dry run wrote to disk")
	}
}
