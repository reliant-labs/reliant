package depsurface

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/templates"
)

// narrowedMethods is how many methods the lint fixture's handler calls
// through its wide Deps field: one past the scaffolded .golangci.yml's
// interfacebloat max (12), the shape control-plane's daemon handler had
// (13 delegating rpcs over svcdaemon.Service).
const narrowedMethods = 13

// writeLintFixture writes a module laid out like a forge project — a
// scaffolded .golangci.yml, a third-party module, a generated api package
// whose name is not its path's last element — whose daemon handler calls
// narrowedMethods of a 14-method foreign interface, through signatures that
// need a stdlib, a third-party and a local import.
func writeLintFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	golangci, err := templates.ProjectTemplates().Render("golangci.yml.tmpl", struct {
		Module, TypedAccessGuard, LoaderPackage string
	}{"example.com/fx", "error", "pkg/config"})
	if err != nil {
		t.Fatal(err)
	}
	var api, iface, handlers strings.Builder
	api.WriteString("// Package apiv1 stands in for a generated proto package.\npackage apiv1\n")
	iface.WriteString(`// Package svc is the domain service every handler used to take whole.
package svc

import (
	"context"

	"example.org/wire"

	apiv1 "example.com/fx/gen/api/v1"
)

// Service is the wide service.
type Service interface {
`)
	handlers.WriteString(`package daemon

import (
	"context"

	"example.org/wire"

	apiv1 "example.com/fx/gen/api/v1"
)
`)
	for i := 1; i <= narrowedMethods+1; i++ {
		fmt.Fprintf(&api, "\n// Req%02[1]d is a request.\ntype Req%02[1]d struct{}\n\n// Resp%02[1]d is a response.\ntype Resp%02[1]d struct{}\n", i)
		fmt.Fprintf(&iface, "\tM%02[1]d(ctx context.Context, req *wire.Request[apiv1.Req%02[1]d]) (*wire.Response[apiv1.Resp%02[1]d], error)\n", i)
		if i <= narrowedMethods {
			fmt.Fprintf(&handlers, "\n// M%02[1]d delegates.\nfunc (s *Service) M%02[1]d(ctx context.Context, req *wire.Request[apiv1.Req%02[1]d]) (*wire.Response[apiv1.Resp%02[1]d], error) {\n\treturn s.deps.Svc.M%02[1]d(ctx, req)\n}\n", i)
		}
	}
	iface.WriteString("}\n")
	files := map[string]string{
		".golangci.yml":           string(golangci),
		"go.mod":                  "module example.com/fx\n\ngo 1.22\n\nrequire example.org/wire v0.0.0\n\nreplace example.org/wire => ./third_party/wire\n",
		"third_party/wire/go.mod": "module example.org/wire\n\ngo 1.22\n",
		"third_party/wire/wire.go": `// Package wire stands in for connect.
package wire

// Request is a request envelope.
type Request[T any] struct{ Msg *T }

// Response is a response envelope.
type Response[T any] struct{ Msg *T }
`,
		"gen/api/v1/api.go":    api.String(),
		"internal/svc/svc.go":  iface.String(),
		"pkg/config/config.go": "// Package config is the root config.\npackage config\n\n// Config is the root config.\ntype Config struct{ Port int }\n",
		"internal/handlers/daemon/service.go": `// Package daemon serves the daemon rpcs.
package daemon

import (
	"log/slog"

	"example.com/fx/internal/svc"
)

// Deps holds the service's dependencies.
type Deps struct {
	Logger *slog.Logger
	Svc    svc.Service
}

// Service serves the daemon rpcs.
type Service struct{ deps Deps }

// New returns the service.
func New(d Deps) *Service { return &Service{deps: d} }
`,
		"internal/handlers/daemon/handlers.go": handlers.String(),
	}
	for name, src := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// golangciIssue is one finding of `golangci-lint run --output.json.path`.
type golangciIssue struct {
	FromLinter string
	Text       string
	Pos        struct{ Filename string }
}

// golangciRun runs golangci-lint over pkgs under the fixture's own config
// and returns its findings.
func golangciRun(t *testing.T, dir string, pkgs ...string) []golangciIssue {
	t.Helper()
	cmd := exec.Command("golangci-lint", append([]string{"run", "--output.json.path=stdout", "--output.text.path=stderr", "--show-stats=false"}, pkgs...)...)
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if code := cmd.ProcessState.ExitCode(); err != nil && code != 1 {
		t.Fatalf("golangci-lint run %v: %v (exit %d)\n%s", pkgs, err, code, stderr.String())
	}
	var report struct{ Issues []golangciIssue }
	if err := json.Unmarshal(out, &report); err != nil {
		t.Fatalf("golangci-lint JSON: %v\n%s\n%s", err, out, stderr.String())
	}
	return report.Issues
}

// TestNarrow_OutputIsCleanUnderTheProjectsLint pins narrow-deps' output to
// the lint a forge project gates with, by running it: the deps.go it writes,
// and the Deps file it retypes, have no golangci-lint finding under the
// scaffolded .golangci.yml — goimports' grouping (stdlib, third-party, the
// module's own) included, and interfacebloat when the package calls more
// methods than its max. control-plane's forge f1e8f2eb pin bump hand-fixed
// both (control-plane#1171). The unit tests in depsurface_test.go assert the
// shapes the fix writes; this one asserts the linter accepts them, so a
// directive golangci-lint does not read, or a grouping its goimports does
// not share, fails here.
func TestNarrow_OutputIsCleanUnderTheProjectsLint(t *testing.T) {
	if testing.Short() {
		t.Skip("loads a fixture module through go/packages and runs golangci-lint over it (seconds to a minute); runs in task test")
	}
	if _, err := exec.LookPath("golangci-lint"); err != nil {
		t.Skip("golangci-lint not on PATH")
	}
	dir := writeLintFixture(t)

	// Control: the lanes this test relies on are live in the fixture's
	// config. A goimports-misgrouped file and a 13-method interface are
	// findings, so a clean run below is not a lane that never ran.
	const control = "internal/handlers/control/control.go"
	if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(control)), 0o755); err != nil {
		t.Fatal(err)
	}
	var wide strings.Builder
	wide.WriteString("// Package control holds known findings.\npackage control\n\nimport (\n\t\"context\"\n\t\"example.org/wire\"\n)\n\n// Wide is too wide.\ntype Wide interface {\n")
	for i := 1; i <= narrowedMethods; i++ {
		fmt.Fprintf(&wide, "\tM%02d(ctx context.Context) wire.Request[int]\n", i)
	}
	wide.WriteString("}\n")
	if err := os.WriteFile(filepath.Join(dir, control), []byte(wide.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	linters := map[string]bool{}
	for _, is := range golangciRun(t, dir, "./internal/handlers/control/") {
		linters[is.FromLinter] = true
	}
	if !linters["goimports"] || !linters["interfacebloat"] {
		t.Fatalf("control: the scaffolded .golangci.yml must flag misgrouped imports (goimports) and a 13-method interface (interfacebloat); got %v", linters)
	}
	if err := os.RemoveAll(filepath.Join(dir, filepath.Dir(control))); err != nil {
		t.Fatal(err)
	}

	res, err := Narrow(dir, "internal/handlers/daemon")
	if err != nil {
		t.Fatal(err)
	}
	if res.File != "internal/handlers/daemon/deps.go" || len(res.Narrowed) != 1 || len(res.Narrowed[0].Methods) != narrowedMethods {
		t.Fatalf("result = %+v, want Svc narrowed to %d methods in deps.go", res, narrowedMethods)
	}
	if len(res.Bloated) != 1 || res.Bloated[0].Methods != narrowedMethods || res.Bloated[0].Limit != 12 || !res.Bloated[0].Suppressed {
		t.Fatalf("bloated = %+v, want the %d-method interface over the scaffolded max of 12, suppressed", res.Bloated, narrowedMethods)
	}
	deps, err := os.ReadFile(filepath.Join(dir, res.File))
	if err != nil {
		t.Fatal(err)
	}
	if issues := golangciRun(t, dir, "./internal/handlers/daemon/"); len(issues) > 0 {
		var b strings.Builder
		for _, is := range issues {
			fmt.Fprintf(&b, "  %s: %s (%s)\n", is.Pos.Filename, is.Text, is.FromLinter)
		}
		t.Fatalf("narrow-deps output fails the project's lint:\n%s\ndeps.go:\n%s", b.String(), deps)
	}
}
