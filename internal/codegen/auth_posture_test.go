package codegen

import (
	"os"
	"path/filepath"
	"testing"
)

// TestProjectHasNoSignIn pins which projects forge treats as nobody-signs-in
// (`forge project new --auth-mode none`): an explicit declaration, a frontend
// AND no managed identity. Every
// doubt answers NO, because NO keeps the fail-closed default — a project with
// no frontend (its rpcs take tokens or API keys), one whose dev env still runs
// an IdP, and one whose dev env cannot be read.
func TestProjectHasNoSignIn(t *testing.T) {
	t.Parallel()
	const noIDP = AuthNoneDeclaration + "\nfrontends = []\n"
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  bool
	}{
		{name: "auth-mode none: a frontend, no identity", files: map[string]string{
			"frontends/web/next.config.ts": "", "deploy/kcl/dev/main.k": noIDP,
		}, want: true},
		{name: "a vite frontend, no identity", files: map[string]string{
			"frontends/app/vite.config.ts": "", "deploy/kcl/dev/main.k": noIDP,
		}, want: true},
		{name: "external issuer without managed identity", files: map[string]string{
			"frontends/web/next.config.ts": "", "deploy/kcl/dev/main.k": "frontends = []\n",
			"deploy/kcl/config.k": "jwt_issuer = \"https://issuer.example\"\n",
		}, want: false},
		{name: "custom validator without managed identity", files: map[string]string{
			"frontends/web/next.config.ts": "", "deploy/kcl/dev/main.k": "frontends = []\n",
			"internal/app/auth.go": "package app\n// custom validator\n",
		}, want: false},
		{name: "identity.k declared", files: map[string]string{
			"frontends/web/next.config.ts": "", "deploy/kcl/dev/main.k": noIDP, IdentityKCLRelPath: "X = 1\n",
		}, want: false},
		{name: "legacy: the dev env runs zitadel", files: map[string]string{
			"frontends/web/next.config.ts": "", "deploy/kcl/dev/main.k": "infra = [forge.HostInfra {engine = \"zitadel\"}]\n",
		}, want: false},
		{name: "no dev env to read", files: map[string]string{
			"frontends/web/next.config.ts": "",
		}, want: false},
		{name: "no frontend: an API-key or bearer-token service", files: map[string]string{
			"deploy/kcl/dev/main.k": noIDP,
		}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			for rel, body := range tc.files {
				p := filepath.Join(dir, filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := ProjectHasNoSignIn(dir); got != tc.want {
				t.Errorf("ProjectHasNoSignIn = %v, want %v", got, tc.want)
			}
		})
	}
	if ProjectHasNoSignIn("") {
		t.Error(`ProjectHasNoSignIn("") = true: with no tree to read, keep the fail-closed default`)
	}
}
