package codegen

import (
	"go/format"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/goexec"
)

// ownedCompositionApp is internal/app of a project that composes by hand
// (compose.go and lifecycle.go disowned): Components with no worker or
// operator wired yet. extra is appended verbatim (an accessor added by hand).
func ownedCompositionApp(extra string) string {
	return `package app

import (
	"github.com/reliant-labs/forge/pkg/serverkit"
)

type Components struct{}

type OperatorEntry struct{ Name string }

func (c *Components) AllWorkers() []serverkit.Worker { return nil }
` + extra
}

func writeAccessorFixture(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func readAccessor(t *testing.T, root, group, name string) (string, bool) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, componentAccessorPath("app", group, name)))
	if os.IsNotExist(err) {
		return "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b), true
}

// The bug this pins: on a project that owns its composition, a scaffolded
// worker's subcommand named c.Worker<X>() — an accessor forge never writes
// there — so every later `forge generate` failed validation and rolled back.
// The binding is a compiling placeholder until internal/app declares the
// accessor, and the method expression from the run after it does.
func TestGenerateComponentAccessors_PlaceholderUntilTheAccessorExists(t *testing.T) {
	root := t.TempDir()
	writeAccessorFixture(t, root, map[string]string{
		"go.mod":                          "module example.com/app\n\ngo 1.26\n",
		"internal/app/lifecycle.go":       ownedCompositionApp(""),
		"cmd/app/cmd/workers/register.go": "package workers\n",
		"cmd/app/cmd/workers/mailer.go":   "package workers\n\nvar _ = workerMailer(nil)\n",
		"cmd/app/cmd/operators/fleet.go":  "package operators\n\nvar _ = operatorFleet(nil)\n",
		// Scaffolded before the split: names the accessor directly, so it
		// references no binding and must get none.
		"cmd/app/cmd/workers/ticker.go": "package workers\n\nvar _ = (*app.Components).WorkerTicker\n",
	})

	if err := GenerateComponentAccessors(root, "app", nil); err != nil {
		t.Fatalf("GenerateComponentAccessors: %v", err)
	}
	for _, tc := range []struct{ group, name, method string }{
		{"workers", "mailer", "WorkerMailer"},
		{"operators", "fleet", "OperatorFleet"},
	} {
		got, ok := readAccessor(t, root, tc.group, tc.name)
		if !ok {
			t.Fatalf("no binding written for %s/%s", tc.group, tc.name)
		}
		if _, err := format.Source([]byte(got)); err != nil {
			t.Fatalf("%s binding is not valid Go: %v\n%s", tc.name, err, got)
		}
		if strings.Contains(got, "(*app.Components)."+tc.method+"\n") {
			t.Errorf("%s binding names the missing method %s, which would not compile:\n%s", tc.name, tc.method, got)
		}
		if p := PendingComponentAccessor(root, "app", tc.group, tc.name); p != tc.method {
			t.Errorf("PendingComponentAccessor(%s) = %q, want %q", tc.name, p, tc.method)
		}
	}
	if _, ok := readAccessor(t, root, "workers", "ticker"); ok {
		t.Error("a binding was written for a subcommand that names its accessor directly; it would be an unused var")
	}
	if _, ok := readAccessor(t, root, "workers", "register"); ok {
		t.Error("the group anchor register.go was treated as a subcommand")
	}

	// The accessor is added by hand: the next run names it directly.
	writeAccessorFixture(t, root, map[string]string{
		"internal/app/lifecycle.go": ownedCompositionApp("\nfunc (c *Components) WorkerMailer() serverkit.Worker { return nil }\n"),
	})
	if err := GenerateComponentAccessors(root, "app", nil); err != nil {
		t.Fatalf("GenerateComponentAccessors (accessor added): %v", err)
	}
	got, _ := readAccessor(t, root, "workers", "mailer")
	if !strings.Contains(got, "var workerMailer = (*app.Components).WorkerMailer") {
		t.Errorf("binding does not name the accessor once it exists:\n%s", got)
	}
	if p := PendingComponentAccessor(root, "app", "workers", "mailer"); p != "" {
		t.Errorf("PendingComponentAccessor(mailer) = %q after the accessor was added, want empty", p)
	}

	// The subcommand stops using the binding: the stale one is retired.
	writeAccessorFixture(t, root, map[string]string{
		"cmd/app/cmd/workers/mailer.go": "package workers\n",
	})
	if err := GenerateComponentAccessors(root, "app", nil); err != nil {
		t.Fatalf("GenerateComponentAccessors (binding unused): %v", err)
	}
	if _, ok := readAccessor(t, root, "workers", "mailer"); ok {
		t.Error("a binding nothing references was kept; it would be an unused var")
	}
}

// TestScaffoldedComponentCommandsCompileOnAnOwnedComposition compiles what
// `forge scaffold worker|operator` writes — the real subcommand templates plus
// their bindings — in a module whose internal/app declares neither accessor,
// then again once both are declared. Both states must build: the first is the
// one that used to fail every `forge generate`.
func TestScaffoldedComponentCommandsCompileOnAnOwnedComposition(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: builds a fixture module with go build; runs in task test")
	}
	forgeRoot := forgeRepoRoot(t)
	sum, err := os.ReadFile(filepath.Join(forgeRoot, "go.sum"))
	if err != nil {
		t.Fatalf("read forge go.sum: %v", err)
	}
	root := t.TempDir()
	writeAccessorFixture(t, root, map[string]string{
		"go.mod": "module example.com/app\n\ngo " + pkgGoVersion(t, forgeRoot) + "\n\n" +
			"require github.com/reliant-labs/forge v0.0.0\n\n" +
			"replace github.com/reliant-labs/forge => " + filepath.ToSlash(forgeRoot) + "\n",
		"go.sum":                    string(sum),
		"internal/app/lifecycle.go": ownedCompositionApp(""),
		// The command-tree surface the subcommand templates call, reduced to
		// its signatures.
		"cmd/app/cmd/cmd.go": `package cmd

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/pkg/serverkit"

	"example.com/app/internal/app"
)

type Deps struct{ Cmd *cobra.Command }

type MountFunc func(*app.Components)

var MountNone MountFunc

type ServeSpec struct {
	Mount     MountFunc
	Workers   func(*app.Components) []serverkit.Worker
	Operators func(*app.Components) []app.OperatorEntry
}

func Serve(context.Context, Deps, ServeSpec) error { return nil }
`,
		"pkg/config/config.go": "package config\n\nimport \"github.com/spf13/cobra\"\n\nfunc RegisterFlags(*cobra.Command) {}\n",
	})
	for _, scaffold := range []func(string, string, string) (bool, error){ScaffoldWorkerCmd, ScaffoldOperatorCmd} {
		if _, err := scaffold(root, "app", "mailer_queue"); err != nil {
			t.Fatal(err)
		}
	}
	build := func(state string) {
		t.Helper()
		if err := GenerateComponentAccessors(root, "app", nil); err != nil {
			t.Fatalf("GenerateComponentAccessors (%s): %v", state, err)
		}
		cmd := exec.Command("go", "build", "./...")
		cmd.Dir = root
		cmd.Env = goexec.WithTrimpath(append(os.Environ(), "GOFLAGS=-mod=mod", "GOWORK=off"))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("scaffolded subcommands do not compile %s: %v\n%s", state, err, out)
		}
	}
	build("before the accessors exist")
	if p := PendingComponentAccessor(root, "app", "workers", "mailer_queue"); p != "WorkerMailerQueue" {
		t.Errorf("PendingComponentAccessor = %q, want WorkerMailerQueue", p)
	}

	writeAccessorFixture(t, root, map[string]string{
		"internal/app/lifecycle.go": ownedCompositionApp(`
func (c *Components) WorkerMailerQueue() serverkit.Worker { return nil }

func (c *Components) OperatorMailerQueue() OperatorEntry { return OperatorEntry{Name: "mailer_queue"} }
`),
	})
	build("once the accessors exist")
	for _, group := range []string{"workers", "operators"} {
		if p := PendingComponentAccessor(root, "app", group, "mailer_queue"); p != "" {
			t.Errorf("%s binding still pending (%s) after its accessor was added", group, p)
		}
	}
}
