package templates_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A project with a frontend is born with ONE identity declaration that every
// env imports; only where it runs differs: the dev IdP in dev, the control
// plane in staging and prod. Asserted on the render, which is what every
// forge command reads.
func TestScaffoldDeclaresManagedIdentityForEveryEnv(t *testing.T) {
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
	}
}

// No frontend, no one to sign in: no identity.k and no identity on any env,
// and the envs still render.
func TestScaffoldWithoutFrontendDeclaresNoIdentity(t *testing.T) {
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
