package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/hostedimage"
	"github.com/reliant-labs/forge/internal/kclplugin"
	"github.com/reliant-labs/forge/internal/kclrender"
)

// controlPlaneMainK is a one-env main.k whose Bundle declares the given
// ControlPlane block.
func controlPlaneMainK(controlPlane string) string {
	return `import forge

_bundle = forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = ` + controlPlane + `
}

output = forge.render(_bundle)
`
}

// renderControlPlaneDecl renders main.k (importing the in-repo forge module)
// through the real render seam and returns the control-plane declaration
// every hosted command would read from it.
func renderControlPlaneDecl(t *testing.T, main string) *cloud.Declaration {
	t.Helper()
	kclplugin.Register()
	dir := t.TempDir()
	kclMod := "[package]\nname = \"controlplane\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n\n[dependencies]\n"
	for f, c := range map[string]string{"kcl.mod": kclMod, "main.k": main} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := kclrender.Run(dir, dir, nil)
	if err != nil {
		t.Fatalf("render:\n%s\n%v", main, err)
	}
	entities, err := parseKCLEntities(out)
	if err != nil {
		t.Fatalf("parse entities: %v\n%s", err, out)
	}
	return declarationFromEntities(entities)
}

// TestControlPlaneDefault_KCLMatchesGo pins the one fact forge holds twice:
// KCL's forge.RELIANT_CLOUD_ENDPOINT (what `forge.ControlPlane {}` renders)
// and internal/cloud.DefaultEndpoint (what `forge login` uses when nothing is
// declared). If they drift, a user who logs in outside a project holds a
// credential for a server their project's envs never talk to.
//
// It renders REAL KCL through the real render seam and reads it back through
// the same entity → declaration path every hosted command takes.
func TestControlPlaneDefault_KCLMatchesGo(t *testing.T) {
	for _, tc := range []struct {
		name, block, wantURL string
	}{
		{"empty declaration is Reliant cloud", "forge.ControlPlane {}", cloud.DefaultEndpoint},
		{"explicit endpoint wins", `forge.ControlPlane {endpoint = "http://127.0.0.1:8090"}`, "http://127.0.0.1:8090"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decl := renderControlPlaneDecl(t, controlPlaneMainK(tc.block))
			if decl == nil {
				t.Fatalf("%s must render a control-plane declaration; got none", tc.block)
			}
			ep, err := cloud.ResolveEndpoint("prod", decl)
			if err != nil {
				t.Fatal(err)
			}
			if ep.URL != tc.wantURL {
				t.Errorf("%s resolved to %q, want %q", tc.block, ep.URL, tc.wantURL)
			}
			if ep.TokenEnv != cloud.DefaultTokenEnv {
				t.Errorf("token_env default: got %q, want %q", ep.TokenEnv, cloud.DefaultTokenEnv)
			}
		})
	}
}

// TestNewEnv_DoesNotCopyTheTemplatesControlPlaneEndpoint: `forge env new`
// derives from a sibling, and a sibling's endpoint copied verbatim is a
// derived env silently talking to that sibling's control plane — a local dev
// one included. The derived env must fall back to the declaration default
// (Reliant cloud), keep every other ControlPlane field, and leave `endpoint`
// fields OUTSIDE a ControlPlane block alone.
func TestNewEnv_DoesNotCopyTheTemplatesControlPlaneEndpoint(t *testing.T) {
	for _, tc := range []struct {
		name, block       string
		wantNote          string
		wantTokenEnv      string
		wantKeptUnrelated bool
	}{
		{
			name: "multi-line block",
			block: `forge.ControlPlane {
        endpoint = "http://127.0.0.1:8090"
        token_env = "ACME_DEPLOY_TOKEN"
    }`,
			wantNote:     `names "http://127.0.0.1:8090"`,
			wantTokenEnv: "ACME_DEPLOY_TOKEN",
		},
		{
			name:         "one-line block",
			block:        `forge.ControlPlane {endpoint = "http://127.0.0.1:8090", token_env = "ACME_DEPLOY_TOKEN"}`,
			wantNote:     `names "http://127.0.0.1:8090"`,
			wantTokenEnv: "ACME_DEPLOY_TOKEN",
		},
		{
			name:         "template already spelled out Reliant cloud",
			block:        `forge.ControlPlane {endpoint = "` + cloud.DefaultEndpoint + `"}`,
			wantNote:     "Reliant cloud is forge.ControlPlane's default",
			wantTokenEnv: cloud.DefaultTokenEnv,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// An unrelated `endpoint` (here a plain dict, not a ControlPlane)
			// sits beside the block and must survive the derive untouched.
			template := controlPlaneMainK(tc.block) + "\n_otel = {\n    endpoint = \"http://otel:4317\"\n}\n"
			derived := transformEnvFile(template, "prod", "preview")

			if strings.Contains(stripComments(derived), "127.0.0.1:8090") {
				t.Fatalf("the template's endpoint must not be copied into a live line:\n%s", derived)
			}
			if !strings.Contains(derived, tc.wantNote) {
				t.Errorf("derived env should explain the omitted endpoint (%q):\n%s", tc.wantNote, derived)
			}
			if !strings.Contains(derived, `endpoint = "http://otel:4317"`) {
				t.Errorf("an endpoint outside a ControlPlane block must be left alone:\n%s", derived)
			}

			// The derived file is real KCL that renders to Reliant cloud.
			decl := renderControlPlaneDecl(t, derived)
			if decl == nil {
				t.Fatalf("derived env lost its control_plane:\n%s", derived)
			}
			ep, err := cloud.ResolveEndpoint("preview", decl)
			if err != nil {
				t.Fatal(err)
			}
			if ep.URL != cloud.DefaultEndpoint {
				t.Errorf("derived env targets %q, want Reliant cloud %q:\n%s", ep.URL, cloud.DefaultEndpoint, derived)
			}
			if ep.TokenEnv != tc.wantTokenEnv {
				t.Errorf("token_env must be kept: got %q, want %q", ep.TokenEnv, tc.wantTokenEnv)
			}
		})
	}
}

// TestForgeLogin_NothingDeclaredDefaultsToReliantCloud: with no control plane
// declared, `forge login` logs into Reliant cloud rather than refusing — the
// `go install` user's first command. It says so, and logout forgets the same
// entry.
func TestForgeLogin_NothingDeclaredDefaultsToReliantCloud(t *testing.T) {
	t.Setenv("FORGE_HOME", t.TempDir())
	withDeclaredControlPlanes(t, nil)

	out, err := runLoginCmd(t, "login", "--token", "rlat_DEFAULTCLOUD", "--no-verify")
	if err != nil {
		t.Fatalf("login with nothing declared must default to Reliant cloud: %v\n%s", err, out)
	}
	if c, err := lookupStored(t, cloud.DefaultEndpoint); err != nil || c.Token != "rlat_DEFAULTCLOUD" {
		t.Fatalf("credential must be stored under %s: %+v %v", cloud.DefaultEndpoint, c, err)
	}
	if !strings.Contains(out, "Reliant cloud") || !strings.Contains(out, cloud.DefaultEndpoint) {
		t.Errorf("the default must be announced, not silent:\n%s", out)
	}

	out, err = runLoginCmd(t, "logout")
	if err != nil || !strings.Contains(out, "Logged out of "+cloud.DefaultEndpoint) {
		t.Fatalf("logout with nothing declared must forget the Reliant-cloud entry: %v\n%s", err, out)
	}
}

// TestForgeLogin_OutsideAProjectIgnoresStrayKCL uses the REAL declaration
// walk: a directory with no forge.yaml is not a project, so a deploy/kcl/ tree
// lying around in it is not rendered (this one would fail to), and login goes
// to Reliant cloud.
func TestForgeLogin_OutsideAProjectIgnoresStrayKCL(t *testing.T) {
	t.Setenv("FORGE_HOME", t.TempDir())
	dir := t.TempDir()
	stray := filepath.Join(dir, "deploy", "kcl", "prod")
	if err := os.MkdirAll(stray, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stray, "main.k"), []byte("this is not KCL {{{\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	withCwd(t, dir, func() {
		planes, skipped, err := declaredControlPlanes(context.Background())
		if err != nil || len(planes) != 0 || len(skipped) != 0 {
			t.Fatalf("outside a project nothing is declared and nothing rendered; got planes=%+v skipped=%v err=%v", planes, skipped, err)
		}
		out, err := runLoginCmd(t, "login", "--token", "rlat_OUTSIDE", "--no-verify")
		if err != nil {
			t.Fatalf("login outside a project: %v\n%s", err, out)
		}
	})
	if c, err := lookupStored(t, cloud.DefaultEndpoint); err != nil || c.Token != "rlat_OUTSIDE" {
		t.Fatalf("login outside a project must target Reliant cloud: %+v %v", c, err)
	}
}

// TestForgeLogin_UnrenderableEnvRefusesTheDefault: when an env fails to
// render, it may declare a control plane other than Reliant cloud, so
// defaulting would be a guess. Login refuses and names both fixes, and stores
// nothing.
func TestForgeLogin_UnrenderableEnvRefusesTheDefault(t *testing.T) {
	t.Setenv("FORGE_HOME", t.TempDir())
	prev := declaredControlPlanes
	declaredControlPlanes = func(context.Context) ([]declaredControlPlane, []string, error) {
		return nil, []string{"prod (render failed: boom)"}, nil
	}
	t.Cleanup(func() { declaredControlPlanes = prev })

	out, err := runLoginCmd(t, "login", "--token", "rlat_GUESS", "--no-verify")
	if err == nil {
		t.Fatalf("login must not fall back to Reliant cloud past an unrendered env:\n%s", out)
	}
	for _, want := range []string{"did not render", "--endpoint", "forge env render"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should contain %q; got:\n%v", want, err)
		}
	}
	if _, err := lookupStored(t, cloud.DefaultEndpoint); err == nil {
		t.Fatal("a refused login must store nothing")
	}
}

// TestRegistryDefault_KCLMatchesGo pins the SECOND fact forge holds twice:
// KCL's forge.RELIANT_REGISTRY_HOST (what `forge.ControlPlane {}` renders for
// registry_host) and internal/hostedimage.DefaultRegistryHost (the Go copy,
// for the surfaces with no KCL in front of them).
//
// Drift here is worse than drift in the endpoint, because it is silent for
// longer. The two copies would compose two different push bases, so a build
// would push to one registry and a render would judge an image against
// another — and both would look internally consistent. The failure surfaces
// as a pull of an image nobody can find.
//
// It renders REAL KCL through the real render seam, like its sibling above.
func TestRegistryDefault_KCLMatchesGo(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL; skipped in -short")
	}
	const org = "4f3c2b1a-0000-4000-8000-000000000001"
	for _, tc := range []struct {
		name, block, wantHost string
	}{
		{"an empty declaration is Reliant's registry",
			`forge.ControlPlane {}`,
			hostedimage.DefaultRegistryHost},
		{"an explicit host wins",
			`forge.ControlPlane {registry_host = "registry.example.com"}`,
			"registry.example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cp := renderControlPlaneEntity(t, controlPlaneMainK(tc.block))
			if cp == nil {
				t.Fatalf("%s must render a control-plane declaration; got none", tc.block)
			}
			if cp.RegistryHost != tc.wantHost {
				t.Errorf("registry_host = %q, want %q", cp.RegistryHost, tc.wantHost)
			}
			// And the base the whole design rests on, composed end to end
			// from what KCL actually rendered plus the credential's org.
			hostedProjectNameRestore := hostedProjectName
			hostedProjectName = func() string { return "acme" }
			t.Cleanup(func() { hostedProjectName = hostedProjectNameRestore })
			cp.setResolvedOrg(org)
			got := platformPushBase(&KCLEntities{ControlPlane: cp})
			if want := tc.wantHost + "/" + org + "/acme"; got != want {
				t.Errorf("platformPushBase = %q, want %q", got, want)
			}
		})
	}
}

// renderControlPlaneEntity is renderControlPlaneDecl's twin, returning the
// rendered ENTITY rather than the cloud declaration: the registry host is
// forge's own resolution input and deliberately absent from
// cloud.Declaration, which models only how to REACH the control plane.
func renderControlPlaneEntity(t *testing.T, main string) *ControlPlaneEntity {
	t.Helper()
	kclplugin.Register()
	dir := t.TempDir()
	kclMod := "[package]\nname = \"controlplane\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n\n[dependencies]\n"
	for f, c := range map[string]string{"kcl.mod": kclMod, "main.k": main} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := kclrender.Run(dir, dir, nil)
	if err != nil {
		t.Fatalf("render:\n%s\n%v", main, err)
	}
	entities, err := parseKCLEntities(out)
	if err != nil {
		t.Fatalf("parse entities: %v\n%s", err, out)
	}
	return entities.ControlPlane
}
