package templates_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/generator"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// A project with a frontend is born with ONE identity declaration that every
// env imports; only where it runs differs: the dev IdP in dev, the control
// plane in staging and prod. Asserted on the render, which is what every
// forge command reads.
func TestScaffoldDeclaresManagedIdentityForEveryEnv(t *testing.T) {
	testkit.Slow(t, "execs go")
	root := scaffoldForRender(t, "shop", []string{"orders"}, "web")

	src, err := os.ReadFile(filepath.Join(root, "deploy/kcl/identity.k"))
	if err != nil {
		t.Fatalf("a project with a frontend is born with deploy/kcl/identity.k: %v", err)
	}
	if !strings.Contains(string(src), "IDENTITY = forge.ManagedIdentity") {
		t.Errorf("identity.k does not declare IDENTITY:\n%s", src)
	}

	for env, runtime := range map[string]string{"dev": "host", "staging": "hosted", "prod": "hosted"} {
		id, _ := renderEnvOutput(t, root, env)["identity"].(map[string]any)
		if id == nil {
			t.Errorf("%s declares no identity", env)
			continue
		}
		if got := id["runtime"]; got != runtime {
			t.Errorf("%s identity runs on %v, want %s", env, got, runtime)
		}
		roles, _ := id["roles"].([]any)
		if len(roles) != 2 {
			t.Errorf("%s roles = %v: every env declares the SAME roles from identity.k", env, roles)
		}
		apps, _ := id["applications"].([]any)
		if len(apps) != 1 {
			t.Errorf("%s applications = %v, want the one derived for the API", env, apps)
		}
		// Dev mail goes to the scaffold's Mailpit, which the dev identity
		// finds on its own (the env's only mailpit).
		if env == "dev" && id["dev_mail"] != "mail" {
			t.Errorf("dev identity dev_mail = %v, want the scaffold's mailpit", id["dev_mail"])
		}
	}
}

// No frontend, no one to sign in: no identity.k and no identity on any env,
// and the envs still render.
func TestScaffoldWithoutFrontendDeclaresNoIdentity(t *testing.T) {
	testkit.Slow(t, "execs go")
	root := scaffoldForRender(t, "shop", []string{"orders"}, "")
	if _, err := os.Stat(filepath.Join(root, "deploy/kcl/identity.k")); err == nil {
		t.Error("a project with no frontend was handed an identity.k")
	}
	for _, env := range []string{"dev", "staging", "prod"} {
		if id := renderEnvOutput(t, root, env)["identity"]; id != nil {
			t.Errorf("%s declares an identity with nothing to sign in: %v", env, id)
		}
	}
}

// --auth-mode none: a frontend and a backend, and no users. No identity.k,
// no identity on any env, no dev IdP or mail catcher, no IdP in
// docker-compose.yml, and a public frontend with no sign-in screens — while
// every env still renders and still declares the frontend.
func TestScaffoldAuthNoneDeclaresNoIdentity(t *testing.T) {
	testkit.Slow(t, "execs go")
	root := scaffoldForRender(t, "shop", []string{"orders"}, "web", func(g *generator.ProjectGenerator) { g.AuthNone = true })
	if _, err := os.Stat(filepath.Join(root, "deploy/kcl/identity.k")); err == nil {
		t.Error("a project created with --auth-mode none was handed an identity.k")
	}
	for _, env := range []string{"dev", "staging", "prod"} {
		out := renderEnvOutput(t, root, env)
		if id := out["identity"]; id != nil {
			t.Errorf("%s declares an identity in a project with no users: %v", env, id)
		}
		if fes, _ := out["frontends"].([]any); len(fes) != 1 {
			t.Errorf("%s frontends = %v, want the one web frontend: no users is not no site", env, out["frontends"])
		}
		if env == "dev" {
			for _, infra := range asList(out["infra"]) {
				if m, _ := infra.(map[string]any); m != nil && (m["name"] == "idp" || m["name"] == "mail") {
					t.Errorf("dev runs the %v host process with nobody to sign in", m["name"])
				}
			}
		}
	}
	compose, err := os.ReadFile(filepath.Join(root, "docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(compose), "\n  idp:\n") {
		t.Error("docker-compose.yml offers an idp service in a project with no users")
	}
	web := filepath.Join(root, "frontends", "web")
	for _, rel := range []string{"src/app/auth", "src/components/auth-ui.tsx", "src/components/sign-in-methods.tsx"} {
		if _, err := os.Stat(filepath.Join(web, filepath.FromSlash(rel))); err == nil {
			t.Errorf("the public frontend was handed %s, part of a sign-in flow it has no use for", rel)
		}
	}
	providers, err := os.ReadFile(filepath.Join(web, "src", "app", "providers.tsx"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(providers), "<RouteGuard") {
		t.Error("the public frontend mounts the RouteGuard")
	}
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}
