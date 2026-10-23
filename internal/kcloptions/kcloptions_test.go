package kcloptions

import (
	"os"
	"path/filepath"
	"testing"

	"kcl-lang.io/kcl-go/pkg/spec/gpyrpc"

	"github.com/reliant-labs/forge/internal/kclvendor"
)

// writeEnvProject lays down a project shaped like a real one —
// deploy/kcl/<env>/main.k under a kcl.mod that, like every forge project's,
// declares NO forge dependency — and returns the project root. Every fixture
// imports the forge module, so each one proves Discover supplies it from the
// binary: without it kcl's ListOptions walks into an unresolved symbol and
// bails, and a fixture with no imports would pass while every real project
// failed.
func writeEnvProject(t *testing.T, envName, mainK string) string {
	t.Helper()
	t.Cleanup(kclvendor.SetCacheDirForTest(t.TempDir()))
	root := t.TempDir()

	kclRoot := filepath.Join(root, "deploy", "kcl")
	envDir := filepath.Join(kclRoot, envName)
	if err := os.MkdirAll(envDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mod := "[package]\nname = \"fixture-deploy\"\nversion = \"0.1.0\"\n\n[dependencies]\n"
	if err := os.WriteFile(filepath.Join(kclRoot, "kcl.mod"), []byte(mod), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(envDir, "main.k"), []byte(mainK), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// The headline case: an env that imports the forge module (so resolution is
// required) and declares one option with full metadata.
func TestDiscoverFindsDeclaredOption(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates KCL; runs in task test")
	}
	root := writeEnvProject(t, "dev", `import forge

_host_runner = option("host_runner", type="str", default="air", help="Host launch runner")

# Force a forge-derived option into the program too, so the reserved-name
# subtraction is actually exercised rather than vacuously true.
_env = forge.env()

out = {runner = _host_runner, env = _env}
`)

	opts, discoverable, err := Discover(root, "dev")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if !discoverable {
		t.Fatal("discoverable = false — dependency resolution did not reach the forge module")
	}
	if len(opts) != 1 {
		t.Fatalf("Discover() = %+v, want exactly the project's own option", opts)
	}
	got := opts[0]
	if got.Name != "host_runner" {
		t.Errorf("Name = %q, want host_runner", got.Name)
	}
	if got.Type != "str" {
		t.Errorf("Type = %q, want str", got.Type)
	}
	if got.Default != "air" {
		t.Errorf("Default = %q, want air (unquoted)", got.Default)
	}
	if got.Help != "Host launch runner" {
		t.Errorf("Help = %q, want the declared help text", got.Help)
	}
}

// forge's own options must never surface as the project's — they are bound by
// forge and are not the caller's to set.
func TestDiscoverSubtractsReservedOptions(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates KCL; runs in task test")
	}
	root := writeEnvProject(t, "dev", `import forge

_env = forge.env()
_ns = forge.namespace("default")
_tag = forge.image_tag("dev")
_digests = forge.image_digests()

out = {a = _env, b = _ns, c = _tag, d = _digests}
`)

	opts, discoverable, err := Discover(root, "dev")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if !discoverable {
		t.Fatal("discoverable = false")
	}
	if len(opts) != 0 {
		t.Errorf("Discover() = %+v, want none — all of those are forge's", opts)
	}
}

// forge gives the image registry NO special treatment: it is declared in the
// env's KCL like any other field, and forge neither binds `-D registry=` nor
// owns an option of that name. A project that CHOOSES to read
// `option("registry")` in its own main.k has written an ordinary project
// option, which Discover must list like any other.
func TestDiscoverTreatsAProjectRegistryOptionAsTheProjects(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates KCL; runs in task test")
	}
	if _, reserved := Reserved["registry"]; reserved {
		t.Fatal(`"registry" is in Reserved — forge does not derive or bind a registry, so the name is the project's`)
	}
	root := writeEnvProject(t, "prod", `import forge

_env = forge.env()
_registry = option("registry") or "ghcr.io/acme"

out = {env = _env, registry = _registry}
`)

	opts, discoverable, err := Discover(root, "prod")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if !discoverable {
		t.Fatal("discoverable = false")
	}
	if len(opts) != 1 || opts[0].Name != "registry" {
		t.Fatalf("Discover() = %+v, want exactly the project's own `registry` option", opts)
	}
}

// A project that reads an option from several places declares it once. The
// merge must not let a bare call site erase metadata declared elsewhere.
func TestDiscoverMergesDuplicateCallSitesKeepingMetadata(t *testing.T) {
	root := writeEnvProject(t, "dev", `import forge

_declared = option("host_runner", type="str", default="air", help="Host launch runner")
_bare = option("host_runner")

out = {a = _declared, b = _bare, env = forge.env()}
`)

	opts, _, err := Discover(root, "dev")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(opts) != 1 {
		t.Fatalf("Discover() = %+v, want one merged entry", opts)
	}
	if opts[0].Help != "Host launch runner" || opts[0].Default != "air" {
		t.Errorf("merged entry lost metadata: %+v", opts[0])
	}
}

func TestDiscoverMissingEnvIsAnError(t *testing.T) {
	root := writeEnvProject(t, "dev", "import forge\nout = {env = forge.env()}\n")
	if _, _, err := Discover(root, "nope"); err == nil {
		t.Fatal("expected an error for an env with no deploy/kcl/<env>/ dir")
	}
}

// project() is the pure half — worth covering directly for the shapes that are
// awkward to provoke through real KCL.
func TestProjectFolding(t *testing.T) {
	raw := []*gpyrpc.OptionHelp{
		{Name: "env"},       // reserved
		{Name: "image_tag"}, // reserved
		{Name: "zeta", Help: "last alphabetically"}, //
		{Name: "alpha"}, // bare
		{Name: "alpha", Type: "str", DefaultValue: `"x"`}, // metadata later
		{Name: "  ", Help: "blank name is skipped"},       //
		{Name: "needy", Required: true},                   //
		{Name: "needy"},                                   // required must stick
	}
	got := project(raw)

	if len(got) != 3 {
		t.Fatalf("project() = %+v, want exactly alpha/needy/zeta", got)
	}
	// Sorted by name for stable output.
	if got[0].Name != "alpha" || got[1].Name != "needy" || got[2].Name != "zeta" {
		t.Errorf("not sorted by name: %+v", got)
	}
	if got[0].Type != "str" || got[0].Default != "x" {
		t.Errorf("alpha lost its later metadata: %+v", got[0])
	}
	if !got[1].Required {
		t.Error("needy: required must survive a later non-required call site")
	}
}

func TestUnquote(t *testing.T) {
	for in, want := range map[string]string{
		`"air"`: "air",
		`air`:   "air",
		`""`:    "",
		``:      "",
		`"`:     `"`,
	} {
		if got := unquote(in); got != want {
			t.Errorf("unquote(%q) = %q, want %q", in, got, want)
		}
	}
}
