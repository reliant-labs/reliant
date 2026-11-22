package generator

import (
	"os"
	"path/filepath"
	"testing"
)

// TestProjectDeclaresIdentity pins how the regenerate lane reads "does this
// project have users?" back off the tree. deploy/kcl/identity.k is the
// declaration; without it, a dev env that still runs an identity provider (a
// project scaffolded before identity.k existed) keeps its compose IdP, and
// only a dev env that mentions none — what `--auth-mode none` writes — loses it.
func TestProjectDeclaresIdentity(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		identity bool
		devMainK string // "" writes no dev main.k
		want     bool
	}{
		{name: "identity.k declared", identity: true, devMainK: "x = 1\n", want: true},
		{name: "legacy: dev runs the zitadel host IdP", devMainK: "infra = [forge.HostInfra {name = \"idp\", engine = \"zitadel\"}]\n", want: true},
		{name: "legacy: dev binds the compose idp service", devMainK: "deploy = forge.Compose {service = \"idp\"}\n", want: true},
		{name: "no dev env to read", want: true},
		{name: "auth-mode none: no identity anywhere", devMainK: "# the dev identity provider, the IdP\nfrontends = []\n", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			kcl := filepath.Join(dir, "deploy", "kcl")
			if err := os.MkdirAll(filepath.Join(kcl, "dev"), 0o755); err != nil {
				t.Fatal(err)
			}
			if tc.identity {
				if err := os.WriteFile(filepath.Join(kcl, "identity.k"), []byte("IDENTITY = 1\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if tc.devMainK != "" {
				if err := os.WriteFile(filepath.Join(kcl, "dev", "main.k"), []byte(tc.devMainK), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := projectDeclaresIdentity(dir); got != tc.want {
				t.Errorf("projectDeclaresIdentity = %v, want %v", got, tc.want)
			}
		})
	}
	if !projectDeclaresIdentity("") {
		t.Error(`projectDeclaresIdentity("") = false: with no tree to read, the IdP must be kept`)
	}
}

// TestDeclaresIdentityFollowsAuthNone pins the scaffold lane: a project with a
// frontend and a backend declares users unless it was created with
// --auth-mode none, and one with no frontend never does.
func TestDeclaresIdentityFollowsAuthNone(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		frontend string
		authNone bool
		want     bool
	}{
		{"frontend, native", "web", false, true},
		{"frontend, --auth-mode none", "web", true, false},
		{"no frontend", "", false, false},
	} {
		g := NewProjectGenerator("shop", t.TempDir(), "example.com/shop")
		g.ServiceName = "orders"
		g.FrontendName = tc.frontend
		g.AuthNone = tc.authNone
		if got := g.declaresIdentity(); got != tc.want {
			t.Errorf("%s: declaresIdentity = %v, want %v", tc.name, got, tc.want)
		}
	}
}
