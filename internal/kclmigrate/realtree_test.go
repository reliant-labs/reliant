package kclmigrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fixtures under testdata/ are TRIMMED COPIES OF REAL PROJECTS, not shapes
// invented to match the implementation. That distinction is the whole reason
// this file exists.
//
// The migration shipped with fixtures that used literal registries
// (`registry = "ghcr.io/acme"`) and call-style binders (`_on_cluster(wl.api)`).
// Both are legal KCL and both passed. Neither is what the two projects that
// actually ran the migration had written: one declares `_registry = "…"` once
// and writes `registry = _registry`, binds with pipes, and factors its bindings
// into a shared library; the other declares no image at all. On both trees the
// migration found nothing, reported success, wrote nothing, and left a tree
// that fails at render.
//
// So the rule these tests encode is: a fixture is copied from a real tree, or
// it is not evidence.

// copyTree materializes a testdata fixture into a writable temp dir, so a test
// can assert that a refused migration wrote NOTHING without risking the
// fixture itself.
func copyTree(t *testing.T, fixture string) string {
	t.Helper()
	src := filepath.Join("testdata", fixture)
	root := t.TempDir()
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		dst := filepath.Join(root, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// snapshot reads every file under root, so "wrote nothing" can be asserted
// over the whole tree rather than over the one or two files a test remembered
// to check.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		rel, _ := filepath.Rel(root, path)
		out[rel] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func assertUnchanged(t *testing.T, before, after map[string]string) {
	t.Helper()
	for path, was := range before {
		if now, ok := after[path]; !ok {
			t.Errorf("a refused migration deleted %s", path)
		} else if now != was {
			t.Errorf("a refused migration wrote to %s — a partial rewrite leaves a tree neither forge understands", path)
		}
	}
}

// TestImageRegistry_ControlPlaneTreeRefuses is the regression for the reported
// defect. Against control-plane's REAL tree shape the migration returned
// Applied=false Refused=false with empty results and generate exited 0 — a
// silent success on a tree it had not read, which then failed at render with
// `Cannot add member 'registry' to schema 'ClusterTarget'`.
//
// Three things in this fixture each defeated a matcher, and each one emptied an
// input to the ambiguity check:
//
//   - `registry = _registry`, with the literal on its own line above. The old
//     matcher accepted only `registry = "<literal>"`, so every env resolved to
//     the empty registry and the pull map was empty.
//   - pipe-style bindings (`wl.x | _host_only | {runtime = …}`). The old
//     matcher accepted only call syntax, so no env recorded any binding.
//   - bindings that live in lib/stack.k, reached through a `full_stack` call,
//     rather than in the env's own main.k.
//
// With both inputs empty nothing was ambiguous, so it "succeeded".
func TestImageRegistry_ControlPlaneTreeRefuses(t *testing.T) {
	root := copyTree(t, "controlplane")
	before := snapshot(t, root)

	res, err := ImageRegistry(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Refused() {
		t.Fatalf("migration did not refuse on control-plane's real tree: Applied=%v rewrites=%v dropped=%v — a silent success here is the defect",
			res.Applied(), res.Rewrites, res.Dropped)
	}
	assertUnchanged(t, before, snapshot(t, root))

	// The three images control-plane pulls, each from a local registry in
	// dev/dev-k8s/e2e and from GAR in prod.
	byImage := map[string]AmbiguousImage{}
	for _, a := range res.Ambiguous {
		byImage[a.Image] = a
	}
	for _, image := range []string{"control-plane", "reliant", "reliant-daemon-gateway"} {
		a, ok := byImage[image]
		if !ok {
			t.Errorf("image %q is pulled from two registries but was not reported ambiguous; got %v", image, sortedImages(res.Ambiguous))
			continue
		}
		if a.ByEnv["prod"] != "us-central1-docker.pkg.dev/reliant-labs-475814/reliant-prod" {
			t.Errorf("image %q: prod registry = %q, want the GAR repository", image, a.ByEnv["prod"])
		}
		local := 0
		for _, env := range []string{"dev", "dev-k8s", "e2e"} {
			if a.ByEnv[env] == "localhost:5051" {
				local++
			}
		}
		if local == 0 {
			t.Errorf("image %q: no local-registry env recorded, got %v", image, a.ByEnv)
		}
	}

	// The refusal must print the per-env KCL, or it is an error message
	// rather than a runbook.
	book := byImage["control-plane"].Runbook()
	for _, want := range []string{
		`| {image = "localhost:5051/control-plane"}`,
		`| {image = "us-central1-docker.pkg.dev/reliant-labs-475814/reliant-prod/control-plane"}`,
		"deploy/kcl/prod/main.k",
	} {
		if !strings.Contains(book, want) {
			t.Errorf("runbook is missing %q:\n%s", want, book)
		}
	}
}

// TestImageRegistry_ControlPlaneDevBindsHostAndCluster pins the binding read
// that the ambiguity judgment rests on. dev binds admin-server to the HOST and
// daemon-gateway to a CLUSTER, in the same file, in pipe style. Reading those
// as one runtime — or as none — is what turns a real refusal into a silent
// success.
func TestImageRegistry_ControlPlaneDevBindsHostAndCluster(t *testing.T) {
	root := copyTree(t, "controlplane")
	tree, err := loadTree(filepath.Join(root, "deploy", "kcl"))
	if err != nil {
		t.Fatal(err)
	}
	got := envBindings(tree, "dev")
	for ident, want := range map[string]string{
		"admin_server":         "OnHost",
		"admin_api":            "OnHost",
		"reliant_api_server":   "OnHost",
		"daemon_gateway":       "OnCluster",
		"workspace_controller": "OnCluster",
		"workspace_proxy":      "OnCluster",
	} {
		if got[ident] != want {
			t.Errorf("dev binds %s to %q, want %q", ident, got[ident], want)
		}
	}
}

// TestImageRegistry_ControlPlaneProdBindsThroughLibrary: prod's main.k binds
// nothing itself — every binding is in lib/stack.k, reached through the
// full_stack call. A reader that stops at main.k concludes prod pulls nothing
// and drops its registry.
func TestImageRegistry_ControlPlaneProdBindsThroughLibrary(t *testing.T) {
	root := copyTree(t, "controlplane")
	tree, err := loadTree(filepath.Join(root, "deploy", "kcl"))
	if err != nil {
		t.Fatal(err)
	}
	got := envBindings(tree, "prod")
	if len(got) == 0 {
		t.Fatal("prod recorded no bindings: they live in lib/stack.k, which the env imports")
	}
	if got["admin_server"] != "OnCluster" {
		t.Errorf("prod binds admin_server to %q, want OnCluster (via lib/stack.k)", got["admin_server"])
	}
}

// TestImageRegistry_IndirectRegistryResolves pins the resolution that the
// control-plane tree needs: a registry named by a variable assigned one string
// literal elsewhere in the file.
func TestImageRegistry_IndirectRegistryResolves(t *testing.T) {
	root := copyTree(t, "controlplane")
	tree, err := loadTree(filepath.Join(root, "deploy", "kcl"))
	if err != nil {
		t.Fatal(err)
	}
	sites, bad := scanRegistrySites(tree)
	if len(bad) != 0 {
		t.Errorf("every registry in this tree resolves to a literal, got unresolved: %v", bad)
	}
	found := map[string]bool{}
	for _, s := range sites {
		found[s.Value] = true
	}
	for _, want := range []string{"localhost:5051", "us-central1-docker.pkg.dev/reliant-labs-475814/reliant-prod"} {
		if !found[want] {
			t.Errorf("registry %q was not resolved from the tree; resolved: %v", want, found)
		}
	}
}

// TestImageRegistry_HoundersNoImageLineIsCompleted is the third silent no-op.
//
// hounders' workloads declare NO image: forge derives it from the build's
// `output_name`, so `migrate` and `membership` both resolve to `hounders`. The
// old reader matched only a literal `image = "…"` line, found no bare images,
// removed `registry = "ghcr.io/reliant-01"` from forge.ControlPlane, moved the
// value nowhere and exited clean. The tree then refused at render: "workload
// 'migrate': image 'hounders' names no registry host, and it is bound to
// forge.OnHosted".
//
// One registry, one hosted env: this is NOT ambiguous, so the migration must
// complete it rather than refuse — by ADDING the image line that was never
// there.
func TestImageRegistry_HoundersNoImageLineIsCompleted(t *testing.T) {
	root := copyTree(t, "hounders")

	res, err := ImageRegistry(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Refused() {
		t.Fatalf("refused a single-registry hosted env: %v %v", res.Ambiguous, res.Unaccounted)
	}
	if !res.Applied() {
		t.Fatal("migration wrote nothing: the registry was removed from the ControlPlane and moved nowhere — the exact silent no-op this test exists for")
	}

	got := readFile(t, root, "deploy/kcl/workloads.k")
	// Both workloads derive `hounders` from the shared build, so both get the
	// same full reference — what the hounders agent had to hand-write.
	if strings.Count(got, `image = "ghcr.io/reliant-01/hounders"`) != 2 {
		t.Errorf("both derived workloads must carry the full reference:\n%s", got)
	}
	// Match a `registry = …` ASSIGNMENT, not the word: the file's own prose
	// mentions the registry, and a test that cannot tell a comment from a
	// declaration would fail on a correct migration.
	if env := readFile(t, root, "deploy/kcl/prod/main.k"); registryAssignRe.MatchString(env) {
		t.Errorf("the ControlPlane registry survived the migration:\n%s", env)
	}
}

// TestImageRegistry_DerivedNameUsesOutputNameThenName pins the derivation
// against kcl/render.k's `_artifact`: a declared image wins, else the build's
// output_name, else the workload's name. Deriving a different name than the
// renderer would is how a migration writes a reference to a repository the
// build never pushes to.
func TestImageRegistry_DerivedNameUsesOutputNameThenName(t *testing.T) {
	for _, tc := range []struct {
		name string
		decl declaration
		want string
		ok   bool
	}{
		{"declared image wins", declaration{Image: "api", HasBuild: true, OutputName: "srv", Name: "n"}, "api", true},
		{"output_name next", declaration{HasBuild: true, OutputName: "hounders", Name: "migrate"}, "hounders", true},
		{"then the name", declaration{HasBuild: true, Name: "migrate"}, "migrate", true},
		{"no build, no artifact", declaration{Name: "nats"}, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := tc.decl.artifact()
			if got != tc.want || ok != tc.ok {
				t.Errorf("artifact() = (%q, %v), want (%q, %v)", got, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestImageRegistry_UnresolvableRegistryIsRefusedWithLocation: a computed
// registry cannot be resolved to one value, and "cannot resolve" must be a
// REFUSAL naming the file and line — never a quiet skip that leaves the
// declaration in a tree the new schema rejects.
func TestImageRegistry_UnresolvableRegistryIsRefusedWithLocation(t *testing.T) {
	root := writeTree(t, map[string]string{
		"deploy/kcl/workloads.k": workloadsWithBareImage,
		"deploy/kcl/prod/main.k": `import forge
import forge.workloads as wl

_registry = option("registry") or "ghcr.io/acme"

_target = forge.ClusterTarget {
    cluster = "c"
    namespace = "ns"
    registry = _registry
}

_on_cluster = lambda w: fw.Workload -> fw.Workload {
    w | {runtime = forge.OnCluster {target = _target}}
}

_workloads = [
    _on_cluster(wl.api)
]
`,
	})
	before := snapshot(t, root)

	res, err := ImageRegistry(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Refused() {
		t.Fatal("a registry forge cannot resolve must be refused: leaving it in place is a tree that will not render")
	}
	assertUnchanged(t, before, snapshot(t, root))

	if len(res.Unaccounted) == 0 {
		t.Fatal("the refusal named no location")
	}
	u := res.Unaccounted[0]
	if u.File != "deploy/kcl/prod/main.k" {
		t.Errorf("File = %q, want deploy/kcl/prod/main.k", u.File)
	}
	if u.Line <= 0 {
		t.Errorf("Line = %d, want the line of the declaration", u.Line)
	}
	if !strings.Contains(u.String(), "_registry") {
		t.Errorf("the refusal must quote the expression it could not resolve: %s", u)
	}
}

// TestImageRegistry_NeverSilentSuccess is the governing rule, stated directly:
// if the tree still contains a `registry = …` the new schema rejects, the
// migration must either have rewritten it or have refused. Reporting success
// while leaving one behind is the defect class, independent of which matcher
// missed it.
func TestImageRegistry_NeverSilentSuccess(t *testing.T) {
	for _, fixture := range []string{"controlplane", "hounders"} {
		t.Run(fixture, func(t *testing.T) {
			root := copyTree(t, fixture)
			res, err := ImageRegistry(root, true)
			if err != nil {
				t.Fatal(err)
			}
			if res.Refused() {
				return // refusing is an accounted outcome
			}
			for path, content := range snapshot(t, root) {
				if !strings.HasSuffix(path, ".k") {
					continue
				}
				if registryAssignRe.MatchString(content) {
					t.Errorf("%s still declares a registry after a migration that reported success:\n%s",
						path, content)
				}
			}
		})
	}
}

func readFile(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func sortedImages(in []AmbiguousImage) []string {
	out := make([]string, 0, len(in))
	for _, a := range in {
		out = append(out, a.Image)
	}
	return out
}
