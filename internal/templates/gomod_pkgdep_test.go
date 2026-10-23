// File: internal/templates/gomod_pkgdep_test.go
//
// Pins the forge dependency block emitted by go.mod.tmpl and gen-go.mod.tmpl
// (see internal/generator/project_pkgdep.go). A scaffold pins a clean
// `require github.com/reliant-labs/forge vX.Y.Z` with NO replace and NO
// vendoring — forge/pkg/* are packages inside that one module.
//
// A `replace` must NEVER appear: maintainers building against unreleased
// forge bridge with a gitignored go.work, which lives outside the template.
//
// The empty-version case is the load-bearing one now. It no longer means
// "this project kind doesn't use forge" — it means the generating binary has
// no proxy-resolvable version, and emitting a require anyway is precisely
// what used to tell projects to pin a release that could not satisfy the
// generated code.
package templates

import (
	"strings"
	"testing"
)

type goModPkgDepData struct {
	Module       string
	GoVersion    string
	RESTEnabled  bool
	ForgeVersion string
	GenModule    bool
}

func renderGoMod(t *testing.T, data goModPkgDepData) string {
	t.Helper()
	out, err := ProjectTemplates().Render("go.mod.tmpl", data)
	if err != nil {
		t.Fatalf("render go.mod.tmpl: %v", err)
	}
	return string(out)
}

func TestGoModTemplate_ForgePkgCleanPin(t *testing.T) {
	got := renderGoMod(t, goModPkgDepData{
		Module: "github.com/example/demo", GoVersion: "1.26",
		ForgeVersion: "v0.3.0", GenModule: true,
	})
	if !strings.Contains(got, "github.com/reliant-labs/forge v0.3.0") {
		t.Errorf("missing pinned require, got:\n%s", got)
	}
	if strings.Contains(got, "replace github.com/reliant-labs/forge") {
		t.Errorf("must not emit a forge replace, got:\n%s", got)
	}
	// The project-local gen replace must survive untouched.
	if !strings.Contains(got, "replace github.com/example/demo/gen => ./gen") {
		t.Errorf("lost the ./gen replace, got:\n%s", got)
	}
}

// A pseudo-version pin — what `go install .../cmd/forge@main` produces — is
// pinned verbatim, like any other resolvable version. It is a real ref a
// proxy can serve, and control-plane's commit-pinning mode depends on it.
func TestGoModTemplate_ForgePseudoVersionPin(t *testing.T) {
	const pseudo = "v0.1.16-0.20260916085636-c01e07ec6ef2"
	got := renderGoMod(t, goModPkgDepData{
		Module: "github.com/example/demo", GoVersion: "1.26",
		ForgeVersion: pseudo, GenModule: true,
	})
	if !strings.Contains(got, "github.com/reliant-labs/forge "+pseudo) {
		t.Errorf("missing pseudo-version require, got:\n%s", got)
	}
	if strings.Contains(got, "replace github.com/reliant-labs/forge") {
		t.Errorf("must not emit a forge replace, got:\n%s", got)
	}
	if strings.Contains(got, "forge v0.0.0\n") {
		t.Errorf("must never pin forge to the unresolvable v0.0.0 placeholder, got:\n%s", got)
	}
}

// Empty version: the require is OMITTED, never invented. This is the
// unreleasable-build path — a local or dirty forge, whose bytes no proxy can
// serve — and the whole point is that the scaffold gets a go.work bridge to
// that source instead of a version that contradicts the generated code.
func TestGoModTemplate_ForgeAbsent(t *testing.T) {
	got := renderGoMod(t, goModPkgDepData{
		Module: "github.com/example/demo", GoVersion: "1.26",
	})
	if strings.Contains(got, "reliant-labs/forge") {
		t.Errorf("empty version: go.mod must not mention forge at all, got:\n%s", got)
	}
}

// No gen module (a project without codegen, or one whose root module owns
// gen/): go.mod neither requires nor replaces one. It once did both, for a
// gen/go.mod nothing wrote, and every go command in the project failed on it.
func TestGoModTemplate_NoGenModule(t *testing.T) {
	got := renderGoMod(t, goModPkgDepData{
		Module: "github.com/example/demo", GoVersion: "1.26",
		ForgeVersion: "v0.3.0",
	})
	if strings.Contains(got, "github.com/example/demo/gen") {
		t.Errorf("go.mod names a gen module that does not exist, got:\n%s", got)
	}
	if !strings.Contains(got, "github.com/reliant-labs/forge v0.3.0\n)\n") {
		t.Errorf("the require block lost its forge pin or its shape, got:\n%s", got)
	}
	if !strings.HasSuffix(got, ")\n") {
		t.Errorf("go.mod must end with the require block and one newline, got:\n%q", got)
	}
}

// --- gen/go.mod (the separate gen submodule) --------------------------------

type genGoModPkgDepData struct {
	Module       string
	GoVersion    string
	ForgeVersion string
}

func renderGenGoMod(t *testing.T, data genGoModPkgDepData) string {
	t.Helper()
	out, err := ProjectTemplates().Render("gen-go.mod.tmpl", data)
	if err != nil {
		t.Fatalf("render gen-go.mod.tmpl: %v", err)
	}
	return string(out)
}

// gen/ mirrors the root pin (same version), no replace.
func TestGenGoModTemplate_ForgeCleanPin(t *testing.T) {
	got := renderGenGoMod(t, genGoModPkgDepData{
		Module: "github.com/example/demo", GoVersion: "1.26",
		ForgeVersion: "v0.0.3",
	})
	if !strings.Contains(got, "github.com/reliant-labs/forge v0.0.3") {
		t.Errorf("gen pin: missing pinned require, got:\n%s", got)
	}
	if strings.Contains(got, "replace github.com/reliant-labs/forge") {
		t.Errorf("gen pin: must not emit a replace, got:\n%s", got)
	}
}

// No version (root has no forge require): gen/ omits forge entirely rather
// than emitting an unresolvable pin, so `go mod tidy` or the go.work bridge
// resolves it.
func TestGenGoModTemplate_ForgeAbsent(t *testing.T) {
	got := renderGenGoMod(t, genGoModPkgDepData{
		Module: "github.com/example/demo", GoVersion: "1.26",
	})
	if strings.Contains(got, "reliant-labs/forge") {
		t.Errorf("gen absent: must not mention forge, got:\n%s", got)
	}
}
