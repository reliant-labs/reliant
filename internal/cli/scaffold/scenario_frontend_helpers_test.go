package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator"
)

func TestResolveScenarioFrontend(t *testing.T) {
	cfg := func(fes ...config.FrontendConfig) *config.ProjectConfig { return &config.ProjectConfig{Frontends: fes} }
	web := config.FrontendConfig{Name: "web", Type: "nextjs"}
	admin := config.FrontendConfig{Name: "admin", Type: "vite-spa"}
	mobile := config.FrontendConfig{Name: "mobile", Type: "react-native"}

	tests := []struct {
		name      string
		cfg       *config.ProjectConfig
		requested string
		want      string // frontend name; "" when an error is expected
		wantErr   string
	}{
		{"the only supported frontend is the default", cfg(web, mobile), "", "web", ""},
		{"type match ignores case", cfg(config.FrontendConfig{Name: "w", Type: "NextJS"}), "", "w", ""},
		{"a named frontend is found", cfg(web, admin), "admin", "admin", ""},
		{"no supported frontend at all", cfg(mobile), "", "", "no nextjs or vite-spa frontends found"},
		{"a mobile frontend cannot be targeted", cfg(web, mobile), "mobile", "", `frontend "mobile" not found; known frontends: web`},
		{"several frontends need --frontend", cfg(web, admin), "", "", "multiple frontends present (web, admin); pass --frontend <name>"},
		{"an unknown name lists the known ones", cfg(web, admin), "ghost", "", "known frontends: web, admin"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fe, err := resolveScenarioFrontend(tc.cfg, tc.requested)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || fe.Name != tc.want {
				t.Fatalf("got (%v, %v), want %s", fe, err, tc.want)
			}
		})
	}
}

// scenarioProject is a project with one Vite frontend whose src/gen exists.
func scenarioProject(t *testing.T, withGen bool) string {
	t.Helper()
	dir := withTempProject(t, "name: testproj\nmodule_path: example.com/testproj\n")
	writeFixtureFile(t, dir, "deploy/kcl/dev/main.k", "# dev\n")
	writeFixtureFile(t, dir, "frontends/admin/package.json", "{}")
	writeFixtureFile(t, dir, "frontends/admin/vite.config.ts", "export default {}\n")
	writeFixtureFile(t, dir, "frontends/admin/src/main.tsx", "")
	if withGen {
		writeFixtureFile(t, dir, "frontends/admin/src/gen/.keep", "")
	}
	return dir
}

func TestRunScenario_WritesATemplateAndRegeneratesTheIndex(t *testing.T) {
	dir := scenarioProject(t, true)

	var indexRoot, indexRel string
	f := testFactory()
	f.Gen.WriteScenariosIndex = func(root, rel string, _ *generator.FileChecksums) error { indexRoot, indexRel = root, rel; return nil }

	out, err := captureStdout(t, func() error { return runScenario(f, "github-connected", "", "", "") })
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "frontends", "admin", "src", "mocks", "scenarios", "github-connected.ts")
	body, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatalf("scenario not written: %v", rerr)
	}
	if !strings.Contains(string(body), `"github-connected"`) {
		t.Errorf("the scenario does not carry its own name:\n%s", body)
	}
	if indexRoot != dir || indexRel != filepath.Join("frontends", "admin", "src", "mocks", "scenarios") {
		t.Errorf("index regenerated for (%q, %q)", indexRoot, indexRel)
	}
	if !strings.Contains(out, "Created "+filepath.Join("frontends", "admin", "src", "mocks", "scenarios", "github-connected.ts")) {
		t.Errorf("output = %s", out)
	}
	if strings.Contains(out, "is missing") {
		t.Errorf("src/gen exists, so no warning is due:\n%s", out)
	}

	t.Run("a second scenario of the same name is refused", func(t *testing.T) {
		err := runScenario(f, "github-connected", "", "", "")
		if err == nil || !strings.Contains(err.Error(), `scenario "github-connected" already exists`) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("--from copies a scenario under its new name", func(t *testing.T) {
		writeFixtureFile(t, dir, "frontends/admin/src/mocks/scenarios/base.ts", `export default { name: "base", handlers: [] }`+"\n")
		if err := runScenario(f, "revoked", "admin", "base", ""); err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(filepath.Join(dir, "frontends", "admin", "src", "mocks", "scenarios", "revoked.ts"))
		if !strings.Contains(string(got), `name: "revoked"`) || strings.Contains(string(got), `"base"`) {
			t.Errorf("copy = %s", got)
		}
	})

	t.Run("--from a scenario with no literal name aborts instead of writing a mismatched copy", func(t *testing.T) {
		writeFixtureFile(t, dir, "frontends/admin/src/mocks/scenarios/exotic.ts", "export default build()\n")
		err := runScenario(f, "copy", "", "exotic", "")
		if err == nil || !strings.Contains(err.Error(), "could not find `name: \"exotic\"`") {
			t.Fatalf("err = %v", err)
		}
		if _, serr := os.Stat(filepath.Join(dir, "frontends", "admin", "src", "mocks", "scenarios", "copy.ts")); !os.IsNotExist(serr) {
			t.Error("a copy was written although its name could not be rewritten")
		}
	})

	t.Run("--from a missing scenario is reported", func(t *testing.T) {
		err := runScenario(f, "other", "", "nope", "")
		if err == nil || !strings.Contains(err.Error(), `copy scenario from "nope"`) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("an index that cannot be regenerated is reported with the manual fix", func(t *testing.T) {
		bad := testFactory()
		bad.Gen.WriteScenariosIndex = func(string, string, *generator.FileChecksums) error { return os.ErrPermission }
		err := runScenario(bad, "another", "", "", "")
		if err == nil || !strings.Contains(err.Error(), "regenerate scenarios index") || !strings.Contains(err.Error(), "forge generate") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestRunScenario_Refusals(t *testing.T) {
	t.Run("a bad name", func(t *testing.T) {
		scenarioProject(t, true)
		if err := runScenario(testFactory(), "Bad_Name", "", "", ""); err == nil || !strings.Contains(err.Error(), "invalid scenario name") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a project with no usable frontend", func(t *testing.T) {
		withTempProject(t, "name: testproj\nmodule_path: example.com/testproj\n")
		err := runScenario(testFactory(), "ok-name", "", "", "")
		if err == nil || !strings.Contains(err.Error(), "resolve target frontend") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a frontend without src/gen still gets its scenario, with a warning", func(t *testing.T) {
		scenarioProject(t, false)
		out, err := captureStdout(t, func() error { return runScenario(testFactory(), "late-gen", "", "", "") })
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, filepath.Join("frontends", "admin", "src", "gen")+" is missing — run `forge generate` first") {
			t.Errorf("output = %s", out)
		}
	})
}

func TestValidateFrontendFlags(t *testing.T) {
	tests := []struct {
		name                            string
		kind, output, basePath, authMod string
		want                            frontendFlags
		wantErr                         string
	}{
		{"defaults", "", "", "", "", frontendFlags{}, ""},
		{"values are lowercased and trimmed", " WEB ", " Static ", " /admin ", " Native ", frontendFlags{kind: "web", output: "static", basePath: "/admin", authMode: "native"}, ""},
		{"unknown kind", "desktop", "", "", "", frontendFlags{}, `invalid frontend kind "desktop"`},
		{"unknown output", "web", "edge", "", "", frontendFlags{}, `invalid --output "edge"`},
		{"--output is Next.js only", "vite-spa", "static", "", "", frontendFlags{}, "--output only applies to Next.js frontends"},
		{"--base-path is Next.js only", "mobile", "", "/x", "", frontendFlags{}, "--base-path only applies to Next.js frontends"},
		{"a malformed base path", "web", "", "admin/", "", frontendFlags{}, `invalid --base-path "admin/"`},
		{"unknown auth mode", "web", "", "", "oidc", frontendFlags{}, `invalid --auth-mode "oidc"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := validateFrontendFlags("ctx", tc.kind, tc.output, tc.basePath, tc.authMod)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got (%+v, %v), want %+v", got, err, tc.want)
			}
		})
	}
}

func TestCheckFrontendNameConflict(t *testing.T) {
	existing := []config.FrontendConfig{{Name: "foo-bar"}, {Name: "admin"}}
	if err := checkFrontendNameConflict("ctx", "web", existing); err != nil {
		t.Errorf("a fresh name: %v", err)
	}
	if err := checkFrontendNameConflict("ctx", "admin", existing); err == nil || !strings.Contains(err.Error(), `frontend "admin" already exists`) {
		t.Errorf("same name: %v", err)
	}
	err := checkFrontendNameConflict("ctx", "foo_bar", existing)
	if err == nil {
		t.Fatal("hyphen and underscore spell one frontend to forge: expected a collision")
	}
	for _, want := range []string{`"foo_bar" collides with existing frontend "foo-bar"`, `canonicalize to "foo_bar"`, "foo_bar_config.proto"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
}

func TestValidateAuthMode(t *testing.T) {
	for _, ok := range []string{"", "native", "none"} {
		if err := validateAuthMode("ctx", ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	if err := validateAuthMode("ctx", "saml"); err == nil || !strings.Contains(err.Error(), `invalid --auth-mode "saml"`) {
		t.Errorf("saml: %v", err)
	}
}

func TestFrontendKCLStanzaHint(t *testing.T) {
	got := frontendKCLStanzaHint("acme", "dev", "admin", "vite", frontendOnHost, 0, nil)
	for _, want := range []string{"Declare frontend 'admin' in deploy/kcl/dev/main.k by hand", "# inside the bundle, alongside workloads:"} {
		if !strings.Contains(got, want) {
			t.Errorf("hint lacks %q:\n%s", want, got)
		}
	}
	// A pinned port needs no port declaration of its own.
	pinned := frontendKCLStanzaHint("acme", "dev", "admin", "vite", frontendOnHost, 3001, nil)
	if len(pinned) >= len(got) {
		t.Errorf("a pinned port must not add the allocated-port declaration (%d vs %d bytes)", len(pinned), len(got))
	}
}

func TestActivityOptionHint(t *testing.T) {
	got := activityOptionHint("  ")
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "  option (forge.v1.method) = { activity:") || !strings.HasPrefix(lines[1], "  (or `activity: { none:") {
		t.Errorf("hint = %q: want two lines, each indented", got)
	}
}

func TestEnvBindsWorkload(t *testing.T) {
	root := t.TempDir()
	writeFixtureFile(t, root, "deploy/kcl/dev/main.k", "_workloads = [\n    _on_host(wl.reaper)\n    # wl.ghost is mentioned only in prose\n]\n")
	for name, want := range map[string]bool{"reaper": true, "ghost": false, "other": false} {
		if got := envBindsWorkload(root, "dev", name); got != want {
			t.Errorf("envBindsWorkload(%q) = %v, want %v", name, got, want)
		}
	}
	if envBindsWorkload(root, "staging", "reaper") {
		t.Error("an env with no main.k binds nothing")
	}
}

func TestKindFromCtxLabel(t *testing.T) {
	for label, want := range map[string]string{
		"forge scaffold worker reaper":   config.ComponentKindWorker,
		"forge scaffold operator mgr":    config.ComponentKindOperator,
		"forge scaffold binary proxy":    config.ComponentKindBinary,
		"forge scaffold service billing": config.ComponentKindServer,
		"":                               config.ComponentKindServer,
	} {
		if got := kindFromCtxLabel(label); got != want {
			t.Errorf("kindFromCtxLabel(%q) = %q, want %q", label, got, want)
		}
	}
}

func TestFrontendTypedConfigFor_NoConfigProtoDegradesToTheEnvVarForm(t *testing.T) {
	if got := frontendTypedConfigFor(t.TempDir(), "web"); got.Bound {
		t.Errorf("no config protos: got %+v, want the zero value (unbound)", got)
	}
}
