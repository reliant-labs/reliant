package kcleval_test

// kcleval_eval_test.go exercises the WHOLE path: a real project on disk, a
// real KCL evaluation through the embedded runtime, a real selection.
//
// These tests deliberately do not stub the render. The defect `forge kcl eval`
// exists to remove is that consumers reconstructed forge's render setup from
// outside and it drifted; a test that mocked the evaluation would pass with
// the module unresolvable, which is exactly the failure it must catch. Every
// test here asserts against a project whose KCL says `import forge`, with no
// `kcl` on PATH and no FORGE_KCL_MODULE_CACHE set by the caller.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/kcleval"
	"github.com/reliant-labs/forge/internal/kclvendor"
)

// kclMod is a dependency-free package manifest — what forge scaffolds. The
// absence of a `forge` dependency is the point: `import forge` must resolve
// from the binary.
const kclMod = "[package]\nname = \"eval_probe\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n\n[dependencies]\n"

// project writes a tree under a fresh temp dir and returns its root. Paths are
// slash-separated and relative.
func project(t *testing.T, files map[string]string) string {
	t.Helper()
	t.Cleanup(kclvendor.SetCacheDirForTest(t.TempDir()))
	dir := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestEvalResolvesTheForgeModuleFromTheBinary is the contract that makes this
// command worth having: a file importing forge evaluates with NO `kcl` binary
// on PATH and no caller-supplied module cache.
//
// PATH is emptied for the duration so the assertion cannot be satisfied by an
// external kcl that happened to be installed — which is precisely how the
// workaround this replaces behaved, and why a green test on a developer laptop
// said nothing about CI.
func TestEvalResolvesTheForgeModuleFromTheBinary(t *testing.T) {
	dir := project(t, map[string]string{
		"deploy/kcl/kcl.mod":    kclMod,
		"deploy/kcl/lib/vals.k": "import forge\n\nhas_bundle = forge.Bundle is not None\nname = \"kata-pool\"\n",
	})
	t.Setenv("PATH", "")

	res, err := kcleval.Eval(kcleval.Request{ProjectDir: dir, File: "deploy/kcl/lib/vals.k"})
	if err != nil {
		t.Fatalf("Eval of a file importing forge must resolve the module from the binary; got: %v", err)
	}
	doc, ok := res.Value.(map[string]any)
	if !ok {
		t.Fatalf("whole-document Eval = %T, want an object", res.Value)
	}
	if doc["has_bundle"] != true {
		t.Errorf("has_bundle = %v, want true (the forge module did not resolve)", doc["has_bundle"])
	}
	if res.Scalar {
		t.Error("a whole document is not a scalar")
	}
}

// TestEvalSelects covers the selection surface in one place: a scalar, an int,
// a nested path, a list index, a whole sub-object, and a list — which is the
// case kcl's own -S gets wrong by emitting one YAML document per element.
func TestEvalSelects(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates KCL; runs in task test")
	}
	dir := project(t, map[string]string{
		"deploy/kcl/kcl.mod": kclMod,
		"deploy/kcl/lib/pool.k": `import forge

sbd = {
    image_family = "workspace-sbd"
    disk_size_gb = 200
    enabled = True
}
pool_defaults = {
    node_label_key = "reliant.dev/pool"
    taint_key = "reliant.dev/kata"
}
tiers = ["small", "large"]
_forge_is_present = forge.Bundle is not None
`,
	})

	for _, tc := range []struct {
		name string
		sel  string
		want any
	}{
		{"nested string", "sbd.image_family", "workspace-sbd"},
		{"nested int", "sbd.disk_size_gb", float64(200)},
		{"nested bool", "sbd.enabled", true},
		{"second object", "pool_defaults.node_label_key", "reliant.dev/pool"},
		{"list index", "tiers.1", "large"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := kcleval.Eval(kcleval.Request{
				ProjectDir: dir, File: "deploy/kcl/lib/pool.k", Selectors: []string{tc.sel},
			})
			if err != nil {
				t.Fatalf("Eval -S %s: %v", tc.sel, err)
			}
			if res.Value != tc.want {
				t.Errorf("-S %s = %#v, want %#v", tc.sel, res.Value, tc.want)
			}
			if !res.Scalar {
				t.Errorf("-S %s should be scalar", tc.sel)
			}
		})
	}

	// A selected LIST comes back as ONE list. kcl's `-S tiers` prints two
	// YAML documents, which is unusable without knowing the element count in
	// advance — control-plane's check-kata-pool-staleness.sh carries a
	// hand-written workaround for exactly this.
	t.Run("a list stays one list", func(t *testing.T) {
		res, err := kcleval.Eval(kcleval.Request{
			ProjectDir: dir, File: "deploy/kcl/lib/pool.k", Selectors: []string{"tiers"},
		})
		if err != nil {
			t.Fatal(err)
		}
		got, ok := res.Value.([]any)
		if !ok {
			t.Fatalf("-S tiers = %T, want a list", res.Value)
		}
		if len(got) != 2 || got[0] != "small" || got[1] != "large" {
			t.Errorf("-S tiers = %#v, want [small large]", got)
		}
		if res.Scalar {
			t.Error("a list is not a scalar; --format raw must refuse it")
		}
	})
}

// TestEvalRepeatedSelectYieldsAnObject: two -S flags compose an object keyed
// by selector, which is what repeated -S means to kcl.
func TestEvalRepeatedSelectYieldsAnObject(t *testing.T) {
	dir := project(t, map[string]string{
		"deploy/kcl/kcl.mod": kclMod,
		"deploy/kcl/lib/pg.k": `import forge

cloudnative_pg = {
    name = "cloudnative-pg"
    namespace = "cnpg-system"
}
_p = forge.Bundle is not None
`,
	})
	res, err := kcleval.Eval(kcleval.Request{
		ProjectDir: dir, File: "deploy/kcl/lib/pg.k",
		Selectors: []string{"cloudnative_pg.name", "cloudnative_pg.namespace"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := res.Value.(map[string]any)
	if !ok {
		t.Fatalf("repeated -S = %T, want an object", res.Value)
	}
	if got["cloudnative_pg.name"] != "cloudnative-pg" || got["cloudnative_pg.namespace"] != "cnpg-system" {
		t.Errorf("repeated -S = %#v", got)
	}
	if res.Scalar {
		t.Error("an object of two selections is not a scalar, whatever the members are")
	}
}

// TestEvalResolvesRelativeImportsFromASubdirectoryCwd is the bug this must not
// repeat. control-plane's lib/platform_local.k says `import lib.barman_plugin`,
// which KCL resolves against the kcl.mod PACKAGE ROOT (deploy/kcl) — every
// shell script reading one of those files cd's there first, and that step is
// what a caller should not have to know.
//
// The test runs from a SUBDIRECTORY of the project, because `forge -C <dir> env
// render` once resolved its source from the cwd and broke exactly here. The
// answer must not depend on where the caller stands.
func TestEvalResolvesRelativeImportsFromASubdirectoryCwd(t *testing.T) {
	dir := project(t, map[string]string{
		"deploy/kcl/kcl.mod":    kclMod,
		"deploy/kcl/lib/base.k": "version = \"1.28.0\"\n",
		// `import lib.base` binds the name `base` (which is why
		// control-plane writes `import lib.barman_plugin as barman_plugin`).
		"deploy/kcl/lib/top.k": "import forge\nimport lib.base\n\nplugin_version = base.version\n_p = forge.Bundle is not None\n",
	})
	sub := filepath.Join(dir, "internal", "operators", "shared")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)

	res, err := kcleval.Eval(kcleval.Request{
		ProjectDir: dir, File: "deploy/kcl/lib/top.k", Selectors: []string{"plugin_version"},
	})
	if err != nil {
		t.Fatalf("a relative `lib.*` import must resolve from the file's package root regardless of cwd; got: %v", err)
	}
	if res.Value != "1.28.0" {
		t.Errorf("plugin_version = %#v, want \"1.28.0\"", res.Value)
	}
}

// TestEvalPassesOptions: a -D binding reaches the file's option() call.
func TestEvalPassesOptions(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates KCL; runs in task test")
	}
	dir := project(t, map[string]string{
		"deploy/kcl/kcl.mod":   kclMod,
		"deploy/kcl/lib/opt.k": "import forge\n\ntier = option(\"tier\", type=\"str\", default=\"free\")\n_p = forge.Bundle is not None\n",
	})

	res, err := kcleval.Eval(kcleval.Request{
		ProjectDir: dir, File: "deploy/kcl/lib/opt.k",
		Selectors: []string{"tier"}, Options: []string{"tier=paid"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Value != "paid" {
		t.Errorf("-D tier=paid gave tier = %#v, want \"paid\" (the option never reached the file)", res.Value)
	}

	// And without the binding, the file's own default stands — so a passing
	// assertion above cannot be an accident of the default matching.
	res, err = kcleval.Eval(kcleval.Request{
		ProjectDir: dir, File: "deploy/kcl/lib/opt.k", Selectors: []string{"tier"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Value != "free" {
		t.Errorf("with no -D, tier = %#v, want the file's default \"free\"", res.Value)
	}
}

// TestEvalRefusesAMissingField: a selector typo is an ERROR naming what the
// document does have, never an empty value.
//
// This is the failure mode that made control-plane's kata-pool script pass a
// TIER NAME through as data: `kcl run -S <bad-path>` returned nothing, the
// caller read nothing as a value, and the next command received a tier name
// where it expected a pool field.
func TestEvalRefusesAMissingField(t *testing.T) {
	dir := project(t, map[string]string{
		"deploy/kcl/kcl.mod":    kclMod,
		"deploy/kcl/lib/pool.k": "import forge\n\npool_defaults = {taint_key = \"k\"}\n_p = forge.Bundle is not None\n",
	})
	_, err := kcleval.Eval(kcleval.Request{
		ProjectDir: dir, File: "deploy/kcl/lib/pool.k", Selectors: []string{"pool_defaults.taint_ky"},
	})
	if !errors.Is(err, kcleval.ErrNoSuchField) {
		t.Fatalf("a typo'd selector = %v; want ErrNoSuchField", err)
	}
	// The message must name the fields that WERE there — that is what turns
	// the refusal into a fix rather than a puzzle.
	if !strings.Contains(err.Error(), "taint_key") {
		t.Errorf("refusal does not name the available field:\n%v", err)
	}
}

// TestEvalRefusesAFileOutsideTheProject: the forge module, the plugin
// namespace and the kcl.mod checks are supplied on a project's identity, so a
// path escaping it is refused rather than evaluated under borrowed context.
func TestEvalRefusesAFileOutsideTheProject(t *testing.T) {
	dir := project(t, map[string]string{"deploy/kcl/kcl.mod": kclMod})
	outside := filepath.Join(t.TempDir(), "elsewhere.k")
	if err := os.WriteFile(outside, []byte("x = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{outside, "../escape.k"} {
		if _, err := kcleval.Eval(kcleval.Request{ProjectDir: dir, File: f}); err == nil {
			t.Errorf("Eval(%q) succeeded; a file outside the project must be refused", f)
		}
	}
}

// TestEvalDirectoryPointsAtEnvRender: naming a directory is the mistake of
// someone who wanted an environment, so the refusal names that command.
func TestEvalDirectoryPointsAtEnvRender(t *testing.T) {
	dir := project(t, map[string]string{"deploy/kcl/kcl.mod": kclMod})
	_, err := kcleval.Eval(kcleval.Request{ProjectDir: dir, File: "deploy/kcl"})
	if err == nil || !strings.Contains(err.Error(), "env render") {
		t.Fatalf("Eval of a directory = %v; want a refusal pointing at `env render`", err)
	}
}

// TestEvalIsReadOnly: evaluating a file claims no port block and writes no
// port store. A read-only command never claims a port block (#272).
func TestEvalIsReadOnly(t *testing.T) {
	dir := project(t, map[string]string{
		"deploy/kcl/kcl.mod": kclMod,
		"deploy/kcl/lib/p.k": "import forge\n\nname = \"x\"\n_p = forge.Bundle is not None\n",
	})
	if _, err := kcleval.Eval(kcleval.Request{ProjectDir: dir, File: "deploy/kcl/lib/p.k"}); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{".forge/blocks.json", ".forge/ports-dev.json"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Errorf("evaluation created %s (stat err %v); it must claim nothing", rel, err)
		}
	}
}

// TestSelectDecodedDocument pins the traversal without paying for a render —
// the cheap unit half, for the cases a real KCL file cannot easily produce.
func TestSelectDecodedDocument(t *testing.T) {
	var doc any
	if err := json.Unmarshal([]byte(`{"a":{"b":[{"c":"deep"}]},"n":null,"num":7}`), &doc); err != nil {
		t.Fatal(err)
	}
	if got, err := kcleval.Select(doc, "a.b.0.c"); err != nil || got != "deep" {
		t.Errorf("a.b.0.c = %#v, %v", got, err)
	}
	if got, err := kcleval.Select(doc, "n"); err != nil || got != nil {
		t.Errorf("n = %#v, %v; want nil", got, err)
	}
	// Indexing a non-list, and fielding a scalar, both refuse with the type
	// named — "num is a number, which has no field x" localizes the mistake.
	for _, bad := range []string{"num.x", "a.b.9", "a.b.notanindex", "a..b"} {
		if _, err := kcleval.Select(doc, bad); err == nil {
			t.Errorf("Select(%q) succeeded; want a refusal", bad)
		}
	}
}
