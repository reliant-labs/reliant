package cli

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// compileErrorRender is a real `forge kcl eval` failure (captured from
// `forge generate` on an env config.k setting a field config_gen.k lacks), as
// renderEnvsPure wraps it.
const compileErrorRender = "render deploy/kcl/dev/: evaluate KCL in /p/demo: exit status 1\n" +
	"Error: kpm run /p/demo/deploy/kcl/dev: \n" +
	"error[E2L23]: CompileError\n" +
	"  \x1b[1;38;5;12m-->\x1b[0m /p/demo/deploy/kcl/dev/config.k:12:5\n" +
	"\x1b[1;38;5;12m   |\x1b[0m\n" +
	"\x1b[1;38;5;12m12 |\x1b[0m     brand_new_field = \"x\"\n" +
	"\x1b[1;38;5;12m   |\x1b[0m\x1b[1;38;5;9m     ^\x1b[0m \x1b[1;38;5;9mCannot add member 'brand_new_field' to schema 'AppConfig'\x1b[0m\n" +
	"\x1b[1;38;5;12m   |\x1b[0m\n"

// The cause of a failed render is what KCL said, with where it said it — never
// only forge's "evaluate KCL in <dir>: exit status 1" wrapper (forge bug
// reports c8b7a3f6, aef6d25f, cdd03871).
func TestRenderFailureCause_SurfacesTheKCLChildsError(t *testing.T) {
	for _, tc := range []struct {
		name, err, want string
	}{
		{
			name: "a compile error names its message and location",
			err:  compileErrorRender,
			want: "Cannot add member 'brand_new_field' to schema 'AppConfig' (at /p/demo/deploy/kcl/dev/config.k:12:5)",
		},
		{
			name: "an error in no diagnostic shape is the child's first line",
			err:  "render deploy/kcl/dev/: evaluate KCL in /p: exit status 1\nError: render.k: forge.render: no workload named \"api\"\nUsage: forge kcl eval\n",
			want: `render.k: forge.render: no workload named "api"`,
		},
		{
			name: "kpm's empty header is skipped for the line under it",
			err:  "evaluate KCL in /p: exit status 1\nError: kpm run /My Projects/p/deploy/kcl/dev:\nerror[E3M38]: something unexpected\n",
			want: "error[E3M38]: something unexpected",
		},
		{
			name: "a child that printed only stdout",
			err:  "evaluate KCL in /p: exit status 2\n" + kclChildStdoutTailHeader + "\npanic: boom\n",
			want: "panic: boom",
		},
		{
			name: "a child that printed nothing says so beside its exit status",
			err:  "evaluate KCL in /p: exit status 1\n" + kclChildSilentNote + "\n",
			want: "evaluate KCL in /p: exit status 1 " + kclChildSilentNote,
		},
		{
			name: "an error with nothing past the wrapper is the wrapper",
			err:  "evaluate KCL in /p: exit status 1",
			want: "evaluate KCL in /p: exit status 1",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := renderFailureCause(errors.New(tc.err)); got != tc.want {
				t.Errorf("renderFailureCause =\n  %q\nwant\n  %q", got, tc.want)
			}
		})
	}
}

// A failed child's error carries what it printed: its stderr, else the tail
// of its stdout, else a note that it printed nothing.
func TestKCLChildFailureOutput(t *testing.T) {
	if got := kclChildFailureOutput([]byte("Error: boom\n"), []byte("{}")); got != "Error: boom\n" {
		t.Errorf("stderr is the output when there is one, got %q", got)
	}
	var stdout strings.Builder
	for i := range 30 {
		stdout.WriteString("line " + string(rune('a'+i%26)) + "\n")
	}
	stdout.WriteString("the reason\n")
	got := kclChildFailureOutput(nil, []byte(stdout.String()))
	if !strings.HasPrefix(got, kclChildStdoutTailHeader+"\n") || !strings.Contains(got, "the reason") {
		t.Errorf("empty stderr must quote stdout's tail, got %q", got)
	}
	if n := strings.Count(got, "\n"); n != kclChildFailureTailLines+1 {
		t.Errorf("stdout tail has %d lines, want %d", n-1, kclChildFailureTailLines)
	}
	if got := kclChildFailureOutput([]byte("  \n"), nil); got != kclChildSilentNote+"\n" {
		t.Errorf("a silent child must be named as silent, got %q", got)
	}
}

// 3d0d0a20: an env whose main.k sets a config field added to the proto in the
// same edit cannot render until per-env deploy config regenerates
// config_gen.k, which runs after the inventory is derived. Reliant's dev env
// declares `web` at frontends/web, and its demo env declares `forge-demo` at
// the same directory, so with dev failing the directory was in the inventory
// only as forge-demo, and the frontend typed config refused ReliantWebConfig
// ({frontend: "web"}) with no cause — the failure had been forgotten with its
// stale cache. Once dev renders, the inventory is derived again.
func TestRederiveFrontendInventory_AfterAFailedEnvRenders(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "frontends", "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	demo := renderedEnvFrontend{Env: "demo", Frontend: FrontendEntity{Name: "forge-demo", Type: "vite-spa", Path: "frontends/web"}}
	dev := renderedEnvFrontend{Env: "dev", Frontend: FrontendEntity{Name: "web", Type: "vite-spa", Path: "frontends/web"}}

	ctx := &pipelineContext{
		ProjectDir: root,
		AbsPath:    root,
		Cfg: &config.ProjectConfig{Name: "reliant", Frontends: []config.FrontendConfig{
			config.FrontendConfig{Name: "web", Type: "vite-spa"}.WithDir("frontends/web"),
		}},
		renderedRead: true,
		rendered: renderedEnvironments{
			frontends: []renderedEnvFrontend{demo},
			failed:    []envRenderFailure{{Env: "dev", Err: errors.New(compileErrorRender)}},
		},
	}
	if err := stepDeriveFrontendInventory(ctx); err != nil {
		t.Fatal(err)
	}
	if got := frontendNames(ctx.Cfg); !slices.Equal(got, []string{"forge-demo"}) {
		t.Fatalf("fixture: with dev failing the inventory is %v, want [forge-demo]", got)
	}

	// Still failing: nothing changes.
	ctx.rederiveFrontendInventory()
	if got := frontendNames(ctx.Cfg); !slices.Equal(got, []string{"forge-demo"}) {
		t.Errorf("an env that still fails must not change the inventory, got %v", got)
	}

	// Per-env deploy config regenerated config_gen.k and forgot the failed
	// cache; the next render has dev too.
	ctx.forgetFailedRenders()
	ctx.renderedRead = true
	ctx.rendered = renderedEnvironments{frontends: []renderedEnvFrontend{demo, dev}}
	ctx.rederiveFrontendInventory()
	if got := frontendNames(ctx.Cfg); !slices.Contains(got, "web") {
		t.Errorf("once dev renders, its frontend must be in the inventory; got %v", got)
	}
	if len(ctx.inventoryMissedEnvs) != 0 {
		t.Errorf("no env is missing any more, got %v", ctx.inventoryMissedEnvs)
	}
}

func TestRederiveFrontendInventory_KeepsDeclarationsFromNewlyFailedEnv(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"reports", "shop", "preview"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	frontend := func(env, name string) renderedEnvFrontend {
		return renderedEnvFrontend{Env: env, Frontend: FrontendEntity{Name: name, Type: "vite-spa", Path: name}}
	}
	failed := func(env string) envRenderFailure {
		return envRenderFailure{Env: env, Err: errors.New(compileErrorRender)}
	}
	ctx := &pipelineContext{
		ProjectDir: root, AbsPath: root, Cfg: &config.ProjectConfig{Name: "example"}, renderedRead: true,
		rendered: renderedEnvironments{frontends: []renderedEnvFrontend{frontend("prod", "reports")}, failed: []envRenderFailure{failed("preview"), failed("staging")}},
	}
	if err := stepDeriveFrontendInventory(ctx); err != nil {
		t.Fatal(err)
	}
	ctx.rendered = renderedEnvironments{frontends: []renderedEnvFrontend{frontend("staging", "shop")}, failed: []envRenderFailure{failed("preview"), failed("prod")}}
	ctx.rederiveFrontendInventory()
	if got := frontendNames(ctx.Cfg); !slices.Equal(got, []string{"reports", "shop"}) {
		t.Errorf("staging recovered while prod failed: got %v, want [reports shop]", got)
	}
	// With prod rendering again and reporting no frontend, do not retain
	// the previous declaration. Preview's successful render adds its app.
	ctx.rendered = renderedEnvironments{frontends: []renderedEnvFrontend{frontend("preview", "preview"), frontend("staging", "shop")}}
	ctx.rederiveFrontendInventory()
	if got := frontendNames(ctx.Cfg); !slices.Equal(got, []string{"preview", "shop"}) {
		t.Errorf("prod rendered without reports: got %v, want [preview shop]", got)
	}
}

func frontendNames(cfg *config.ProjectConfig) []string {
	var names []string
	for _, fe := range cfg.Frontends {
		names = append(names, fe.Name)
	}
	slices.Sort(names)
	return names
}
