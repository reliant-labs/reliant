package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/templates"
)

// renderNoFrontendEnv renders the env main.k a project scaffolded WITHOUT
// --frontend carries — the file `forge scaffold frontend` has to extend.
func renderNoFrontendEnv(t *testing.T, tmpl, env string) string {
	t.Helper()
	data := templates.EnvTemplateData{ProjectName: "acme", EnvName: env, IngressEnabled: true, PrimaryWorkload: "acme"}
	out, err := templates.DeployTemplates().Render(tmpl, data)
	if err != nil {
		t.Fatalf("render %s: %v", tmpl, err)
	}
	return string(out)
}

// scaffoldedEnvTemplate is the template `forge project new` renders an env
// from: dev on the host runtime, every other env hosted on the control plane.
func scaffoldedEnvTemplate(env string) string {
	if env == "dev" {
		return "kcl/dev/main.k.tmpl"
	}
	return "kcl/cloud/main.k.tmpl"
}

// TestSpliceFrontendIntoEnvKCL_EveryScaffoldedEnv pins that the splice finds
// its anchors in every env template forge ships (one per runtime), for a
// project born without a frontend. A template edit that moves
// or renames an anchor must fail here, not silently degrade the scaffold
// into printing a hint nobody reads.
func TestSpliceFrontendIntoEnvKCL_EveryScaffoldedEnv(t *testing.T) {
	for _, tc := range []struct {
		tmpl string
		env  string
	}{
		{"kcl/dev/main.k.tmpl", "dev"},
		{"kcl/cloud/main.k.tmpl", "staging"},
		{"kcl/cloud/main.k.tmpl", "prod"},
	} {
		t.Run(tc.tmpl+"/"+tc.env, func(t *testing.T) {
			in := renderNoFrontendEnv(t, tc.tmpl, tc.env)
			if frontendDeclaredIn(in, "web") || strings.Contains(in, "frontends = [") {
				t.Fatalf("precondition: a no-frontend %s should declare no frontend", tc.tmpl)
			}
			dev := tc.env == "dev"
			binding := frontendBindingFor(in, dev)
			out, status := spliceFrontendIntoEnvKCL(in, "acme", tc.env, "web", "nextjs", binding, 0, nil)
			if status != frontendKCLApplied {
				t.Fatalf("splice did not apply (status %d) — an anchor moved in %s", status, tc.tmpl)
			}
			wantEntry := "_web_frontend = forge.Frontend {\n    name = \"web\"\n    path = \"frontends/web\""
			if !strings.Contains(out, wantEntry) {
				t.Errorf("frontend entry missing (want %q):\n%s", wantEntry, out)
			}
			wantPort := `_web_frontend_port = plugin.resolve_port("acme-` + tc.env + `-web", 3000)`
			if dev {
				if !strings.Contains(out, wantPort) || !strings.Contains(out, "port = _web_frontend_port") {
					t.Errorf("dev env must declare and use a resolve_port for the frontend:\n%s", out)
				}
				if strings.Index(out, wantPort) > strings.Index(out, "_web_frontend = ") {
					t.Errorf("port must be declared with the other ports, before _database_url")
				}
			} else if strings.Contains(out, "_web_frontend_port") {
				t.Errorf("%s is not a dev env; it must not get a dev-server port", tc.env)
			}
			// Every frontend binds a runtime, and none is a placeholder.
			if dev && !strings.Contains(out, "runtime = forge.OnHost {}") {
				t.Errorf("dev: the spliced frontend binds no dev server:\n%s", out)
			}
			if !dev && !strings.Contains(out, "        _hosted_frontend(_web_frontend)\n") {
				t.Errorf("%s: the hosted frontend entry is not closed by its binder call:\n%s", tc.env, out)
			}
			if strings.Contains(out, "REPLACE_ME") {
				t.Errorf("%s: a scaffolded frontend carries a placeholder:\n%s", tc.env, out)
			}

			// Idempotent: a second scaffold of the same name is a no-op.
			if again, st := spliceFrontendIntoEnvKCL(out, "acme", tc.env, "web", "nextjs", binding, 0, nil); st != frontendKCLAlreadyDeclared || again != out {
				t.Errorf("second splice must be a no-op, status %d", st)
			}
		})
	}
}

// TestSpliceFrontendIntoEnvKCL_ComposesWithExistingFrontend pins that a
// second frontend is added beside the first rather than replacing it.
func TestSpliceFrontendIntoEnvKCL_ComposesWithExistingFrontend(t *testing.T) {
	in := renderNoFrontendEnv(t, "kcl/dev/main.k.tmpl", "dev")
	one, _ := spliceFrontendIntoEnvKCL(in, "acme", "dev", "web", "nextjs", frontendOnHost, 0, nil)
	two, status := spliceFrontendIntoEnvKCL(one, "acme", "dev", "admin", "nextjs", frontendOnHost, 0, nil)
	if status != frontendKCLApplied {
		t.Fatalf("second frontend not applied: %d", status)
	}
	for _, want := range []string{`name = "web"`, `name = "admin"`, "_admin_frontend_port = plugin.resolve_port(\"acme-dev-admin\", 3000)"} {
		if !strings.Contains(two, want) {
			t.Errorf("missing %q after adding a second frontend", want)
		}
	}
}

// TestSpliceFrontendIntoEnvKCL_PinnedPort pins that --port lands in KCL as
// the literal, with no resolve_port that could step it elsewhere.
func TestSpliceFrontendIntoEnvKCL_PinnedPort(t *testing.T) {
	in := renderNoFrontendEnv(t, "kcl/dev/main.k.tmpl", "dev")
	out, status := spliceFrontendIntoEnvKCL(in, "acme", "dev", "web", "nextjs", frontendOnHost, 4123, nil)
	if status != frontendKCLApplied {
		t.Fatalf("status %d", status)
	}
	if !strings.Contains(out, "port = 4123") || strings.Contains(out, "_web_frontend_port") {
		t.Errorf("pinned port must be written verbatim:\n%s", out)
	}
}

// An env scaffolded before hosting was the default declares no
// `_hosted_frontend`: a new frontend there binds the author's own bucket, as
// before, behind a placeholder `forge env new --check` refuses — forge never
// binds a frontend to a control plane the env does not use.
func TestFrontendBindingFor(t *testing.T) {
	legacy := "_bundle = forge.Bundle {\n    project = \"acme\"\n    secret_provider = forge.ExternalSecrets {}\n}\n"
	if got := frontendBindingFor(legacy, false); got != frontendOnBucket {
		t.Fatalf("pre-hosting env: binding = %d, want frontendOnBucket", got)
	}
	out, status := spliceFrontendIntoEnvKCL(legacy, "acme", "prod", "web", "nextjs", frontendOnBucket, 0, nil)
	if status != frontendKCLApplied || !strings.Contains(out, `runtime = forge.OnBucket {bucket = "REPLACE_ME_BUCKET"}`) {
		t.Errorf("pre-hosting env must keep the bucket binding (status %d):\n%s", status, out)
	}
	// A commented-out example does not make an env hosted.
	if got := frontendBindingFor("# _hosted_frontend = lambda f: ...\n"+legacy, false); got != frontendOnBucket {
		t.Errorf("a commented binder counted as declared: %d", got)
	}
	if got := frontendBindingFor(renderNoFrontendEnv(t, "kcl/cloud/main.k.tmpl", "prod"), false); got != frontendHosted {
		t.Errorf("scaffolded cloud env: binding = %d, want frontendHosted", got)
	}
	if got := frontendBindingFor(renderNoFrontendEnv(t, "kcl/cloud/main.k.tmpl", "prod"), true); got != frontendOnHost {
		t.Errorf("dev always binds the dev server: %d", got)
	}
}

func TestSpliceFrontendIntoEnvKCL_NoAnchorLeavesFileAlone(t *testing.T) {
	in := "output = {}\n"
	out, status := spliceFrontendIntoEnvKCL(in, "acme", "dev", "web", "nextjs", frontendOnHost, 0, nil)
	if status != frontendKCLNoAnchor || out != in {
		t.Fatalf("an unrecognised file must be left untouched, status %d", status)
	}
}

// TestRunAddFrontend_DeclaresFrontendInKCL is the end-to-end guard for the
// reported defect: `forge scaffold frontend web` in a project created without
// --frontend left deploy/kcl/dev/main.k with no frontend and no resolved port,
// so `forge env up` preflighted forge.yaml's literal 3000 and refused to start.
func TestRunAddFrontend_DeclaresFrontendInKCL(t *testing.T) {
	skipNpmInstall(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "forge.yaml"), []byte(freshServiceForgeYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module github.com/example/demo\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, env := range []string{"dev", "staging", "prod"} {
		dir := filepath.Join(root, "deploy", "kcl", env)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "main.k"), []byte(renderNoFrontendEnv(t, scaffoldedEnvTemplate(env), env)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(root)

	if err := runFrontend(t.Context(), testFactory(), frontendArgs{name: "web"}); err != nil {
		t.Fatalf("runFrontend: %v", err)
	}

	dev, err := os.ReadFile(filepath.Join(root, "deploy", "kcl", "dev", "main.k"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(dev), `plugin.resolve_port("demo-dev-web", 3000)`) || !strings.Contains(string(dev), "port = _web_frontend_port") {
		t.Errorf("dev/main.k must declare the frontend with a KCL-resolved port")
	}
	for _, env := range []string{"staging", "prod"} {
		b, _ := os.ReadFile(filepath.Join(root, "deploy", "kcl", env, "main.k"))
		if !strings.Contains(string(b), `name = "web"`) {
			t.Errorf("%s/main.k must declare the frontend", env)
		}
	}

	// forge.yaml must NOT carry a literal port the KCL did not choose.
	yml, _ := os.ReadFile(filepath.Join(root, "forge.yaml"))
	if strings.Contains(string(yml), "port: 3000") {
		t.Errorf("forge.yaml must not pin port 3000 — the dev port is KCL's:\n%s", yml)
	}
}

func TestSpliceFrontendIntoEnvKCL_CarriesRoutes(t *testing.T) {
	for _, env := range []struct {
		name    string
		binding frontendBinding
	}{{"dev", frontendOnHost}, {"prod", frontendOnBucket}} {
		entry := frontendKCLDeclaration("web", "nextjs", env.binding, 0, []string{"none"})
		if !strings.Contains(entry, `routes = ["none"]`) {
			t.Errorf("%s: the route allowlist must be declared in every env's frontend:\n%s", env.name, entry)
		}
	}
	if strings.Contains(frontendKCLDeclaration("web", "nextjs", frontendOnHost, 0, nil), "routes") {
		t.Error("no routes requested: none must be emitted")
	}
}

func TestFrontendKCLEntry_DeclaresANonDefaultType(t *testing.T) {
	cases := map[string]string{"vite-spa": `type = "vite"`, "react-native": `type = "rn"`}
	for scaffoldType, want := range cases {
		if got := frontendKCLDeclaration("spa", kclFrontendType(scaffoldType), frontendOnHost, 0, nil); !strings.Contains(got, want) {
			t.Errorf("%s: the stanza must declare %s so it is not read as the nextjs default:\n%s", scaffoldType, want, got)
		}
	}
	if got := frontendKCLDeclaration("web", kclFrontendType("nextjs"), frontendOnHost, 0, nil); strings.Contains(got, "type =") {
		t.Errorf("a Next.js frontend is the schema default and must not declare a type:\n%s", got)
	}
}
