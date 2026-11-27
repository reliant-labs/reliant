package cli

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/cloudcred"
)

func covAPlanes(t *testing.T, planes []declaredControlPlane, skipped []string, err error) {
	t.Helper()
	prev := declaredControlPlanes
	declaredControlPlanes = func(context.Context) ([]declaredControlPlane, []string, error) { return planes, skipped, err }
	t.Cleanup(func() { declaredControlPlanes = prev })
}

func covAPlane(t *testing.T, url string, envs ...string) declaredControlPlane {
	t.Helper()
	ep, err := cloud.ResolveEndpoint("", &cloud.Declaration{Endpoint: url})
	if err != nil {
		t.Fatal(err)
	}
	return declaredControlPlane{Endpoint: ep, Envs: envs}
}

func TestDeclarationFromEntitiesIgnoresABlankEndpoint(t *testing.T) {
	if d := declarationFromEntities(&KCLEntities{ControlPlane: &ControlPlaneEntity{Endpoint: "   "}}); d != nil {
		t.Errorf("a blank endpoint declares nothing, got %+v", d)
	}
}

func TestControlPlaneDeclarationRendersTheEnv(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte("name: shop\nmodule_path: example.com/shop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, `{"output":{"control_plane":{"type":"control_plane","endpoint":"https://cp.example.test","token_env":"CP_TOKEN"}}}`))
	d, err := controlPlaneDeclaration(context.Background(), "prod")
	if err != nil || d == nil || d.Endpoint != "https://cp.example.test" || d.TokenEnv != "CP_TOKEN" {
		t.Fatalf("d=%+v err=%v", d, err)
	}
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, `{}`))
	if d, err := controlPlaneDeclaration(context.Background(), "prod"); err != nil || d != nil {
		t.Errorf("an env declaring none is nil, not an error: %+v %v", d, err)
	}
}

func TestResolveCloudTarget(t *testing.T) {
	isolatedCredentials(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte("name: shop\nmodule_path: example.com/shop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, `{"output":{"control_plane":{"type":"control_plane","endpoint":"https://cp.example.test"}}}`))
	ep, cred, err := resolveCloudTarget(context.Background(), "prod", "flag-token")
	if err != nil || ep.URL != "https://cp.example.test" || cred.Token != "flag-token" || cred.Source != cloud.SourceFlag {
		t.Fatalf("ep=%+v cred=%+v err=%v: the flag beats every other source", ep, cred, err)
	}
	if ep.ProjectName != "shop" {
		t.Errorf("the endpoint carries the project it acts for: %q", ep.ProjectName)
	}
	t.Setenv(cloudcred.HelperEnv, "")
	if _, _, err := resolveCloudTarget(context.Background(), "prod", ""); err == nil {
		t.Error("with no flag, env var, login or helper there is no credential")
	}
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", "")
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, `not json`))
	if _, _, err := resolveCloudTarget(context.Background(), "prod", "t"); err == nil || !strings.Contains(err.Error(), `render KCL for env "prod"`) {
		t.Errorf("a render failure is reported with the env: %v", err)
	}
}

func TestLoginTargets(t *testing.T) {
	ctx := context.Background()
	a, b := covAPlane(t, "https://a.example.test", "prod"), covAPlane(t, "https://b.example.test", "staging")

	t.Run("--endpoint beats the declarations", func(t *testing.T) {
		covAPlanes(t, []declaredControlPlane{a, b}, nil, nil)
		got, err := loginTargets(ctx, "  https://c.example.test/ ", &bytes.Buffer{})
		if err != nil || len(got) != 1 || got[0].Endpoint.URL != "https://c.example.test" || len(got[0].Envs) != 0 {
			t.Fatalf("got=%+v err=%v", got, err)
		}
	})
	t.Run("a malformed --endpoint", func(t *testing.T) {
		if _, err := loginTargets(ctx, "not a url", &bytes.Buffer{}); err == nil {
			t.Fatal("want an error")
		}
	})
	t.Run("every declared control plane, and a note for each skipped env", func(t *testing.T) {
		covAPlanes(t, []declaredControlPlane{a, b}, []string{"dev (render failed: kcl)"}, nil)
		var errOut bytes.Buffer
		got, err := loginTargets(ctx, "", &errOut)
		if err != nil || len(got) != 2 {
			t.Fatalf("got=%+v err=%v", got, err)
		}
		if !strings.Contains(errOut.String(), "note: skipped env dev (render failed: kcl)") {
			t.Errorf("stderr = %q", errOut.String())
		}
	})
	t.Run("nothing declared and an env failed to render: refuse to guess", func(t *testing.T) {
		covAPlanes(t, nil, []string{"dev (render failed: kcl)", "qa (render failed: kcl)"}, nil)
		_, err := loginTargets(ctx, "", &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "no env that rendered declares a control plane, and 2 did not render") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("nothing declared and nothing skipped: Reliant cloud, with a note", func(t *testing.T) {
		covAPlanes(t, nil, nil, nil)
		var errOut bytes.Buffer
		got, err := loginTargets(ctx, "", &errOut)
		if err != nil || len(got) != 1 || got[0].Endpoint.URL != strings.TrimRight(cloud.DefaultEndpoint, "/") {
			t.Fatalf("got=%+v err=%v", got, err)
		}
		if !strings.Contains(errOut.String(), "using Reliant cloud") {
			t.Errorf("stderr = %q", errOut.String())
		}
	})
	t.Run("a failure reading the declarations", func(t *testing.T) {
		covAPlanes(t, nil, nil, errors.New("unreadable"))
		if _, err := loginTargets(ctx, "", &bytes.Buffer{}); err == nil || err.Error() != "unreadable" {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestDistinctControlPlanesGroupsByNormalisedEndpoint(t *testing.T) {
	got := distinctControlPlanes([]envDeclaration{
		{Env: "staging", Decl: &cloud.Declaration{Endpoint: "https://B.example.test/"}},
		{Env: "none"},
		{Env: "prod", Decl: &cloud.Declaration{Endpoint: "https://b.example.test"}},
		{Env: "dev", Decl: &cloud.Declaration{Endpoint: "https://a.example.test"}},
	})
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	if got[0].Endpoint.URL != "https://a.example.test" || strings.Join(got[0].Envs, ",") != "dev" {
		t.Errorf("first = %+v", got[0])
	}
	if strings.Join(got[1].Envs, ",") != "prod,staging" {
		t.Errorf("two spellings of one endpoint are one login, with every env named (sorted): %+v", got[1])
	}
}

func TestForgeLoginWithAPastedToken(t *testing.T) {
	t.Setenv("FORGE_HOME", t.TempDir())
	t.Setenv(cloudcred.HelperEnv, "/some/helper")
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if auth != "Bearer rlat_pasted" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":"unauthenticated","message":"bad token"}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	t.Run("a good token is verified, stored and the helper is mentioned", func(t *testing.T) {
		covAPlanes(t, nil, nil, nil)
		out, err := runLoginCmd(t, "login", "--endpoint", srv.URL, "--token", "rlat_pasted")
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		for _, want := range []string{"Logged in to " + srv.URL, "note: $" + cloudcred.HelperEnv + " is set", "In CI, set"} {
			if !strings.Contains(out, want) {
				t.Errorf("output lacks %q:\n%s", want, out)
			}
		}
		if strings.Contains(out, "rlat_pasted") {
			t.Errorf("the token itself must never be printed:\n%s", out)
		}
		if c, err := lookupStored(t, srv.URL); err != nil || c.Token != "rlat_pasted" {
			t.Errorf("stored = %+v err %v", c, err)
		}
	})
	t.Run("a rejected token is not stored, unless --no-verify", func(t *testing.T) {
		out, err := runLoginCmd(t, "login", "--endpoint", srv.URL, "--token", "rlat_bad")
		if err == nil || !strings.Contains(err.Error(), "the token was not accepted by "+srv.URL) || !strings.Contains(err.Error(), "--no-verify") {
			t.Fatalf("err = %v\n%s", err, out)
		}
		if c, _ := lookupStored(t, srv.URL); c.Token == "rlat_bad" {
			t.Error("a rejected token was stored")
		}
		if _, err := runLoginCmd(t, "login", "--endpoint", srv.URL, "--token", "rlat_bad", "--no-verify"); err != nil {
			t.Fatal(err)
		}
		if c, _ := lookupStored(t, srv.URL); c.Token != "rlat_bad" {
			t.Errorf("--no-verify must store it: %+v", c)
		}
	})
	t.Run("one token cannot be stored under several control planes", func(t *testing.T) {
		covAPlanes(t, []declaredControlPlane{covAPlane(t, "https://a.example.test", "prod"), covAPlane(t, "https://b.example.test", "dev")}, nil, nil)
		_, err := runLoginCmd(t, "login", "--token", "t")
		if err == nil || !strings.Contains(err.Error(), "--token is for one control plane, and this project declares 2: https://a.example.test, https://b.example.test") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("several declared planes are each logged in, with the envs that declare them", func(t *testing.T) {
		covAPlanes(t, []declaredControlPlane{covAPlane(t, srv.URL, "prod", "staging")}, nil, nil)
		out, err := runLoginCmd(t, "login", "--token", "rlat_pasted")
		if err != nil || !strings.Contains(out, "Declared by: prod, staging") {
			t.Fatalf("out=%q err=%v", out, err)
		}
	})
}

func TestForgeLogoutVariants(t *testing.T) {
	t.Setenv("FORGE_HOME", t.TempDir())
	t.Run("not logged in is said, not an error", func(t *testing.T) {
		out, err := runLoginCmd(t, "logout", "--endpoint", "https://never.example.test")
		if err != nil || !strings.Contains(out, "Not logged in to https://never.example.test") {
			t.Fatalf("out=%q err=%v", out, err)
		}
	})
	t.Run("a bad endpoint", func(t *testing.T) {
		if _, err := runLoginCmd(t, "logout", "--endpoint", "not a url"); err == nil {
			t.Fatal("want an error")
		}
	})
	t.Run("a pasted token is a delegated capability that logout leaves alone", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{}`)) }))
		t.Cleanup(srv.Close)
		covAPlanes(t, nil, nil, nil)
		if _, err := runLoginCmd(t, "login", "--endpoint", srv.URL, "--token", "rlat_keep"); err != nil {
			t.Fatal(err)
		}
		out, err := runLoginCmd(t, "logout", "--endpoint", srv.URL)
		if err != nil || !strings.Contains(out, "Not logged in to "+srv.URL) {
			t.Fatalf("out=%q err=%v", out, err)
		}
		if c, _ := lookupStored(t, srv.URL); c.Token != "rlat_keep" {
			t.Error("logout must not remove a delegated capability")
		}
	})
}

func TestCloudStatusCmd(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte("name: shop\nmodule_path: example.com/shop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	isolatedCredentials(t)
	t.Setenv(cloudcred.HelperEnv, "")
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, `{"output":{"control_plane":{"type":"control_plane","endpoint":"https://cp.example.test","token_env":"CP_TOKEN"}}}`))
	t.Setenv("CP_TOKEN", "")

	run := func(args ...string) (string, error) {
		cmd := newCloudCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(append([]string{"status"}, args...))
		err := cmd.ExecuteContext(context.Background())
		return out.String(), err
	}

	out, err := run("prod", "--token", "rlat_secret_value_1234")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{`env "prod"`, "endpoint:  https://cp.example.test   (declared in deploy/kcl/prod/main.k)", "token env: CP_TOKEN", "credential: ", "--token", "the secret is never printed"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "rlat_secret_value_1234") {
		t.Errorf("the bearer credential must never be printed:\n%s", out)
	}

	out, err = run("prod")
	if err == nil || !strings.Contains(out, "credential: NONE") {
		t.Fatalf("no credential: out=%q err=%v", out, err)
	}

	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, `not json`))
	if _, err := run("prod"); err == nil || !strings.Contains(err.Error(), `render KCL for env "prod"`) {
		t.Errorf("err = %v", err)
	}
}

func TestCredentialsPathForHelpNamesTheFile(t *testing.T) {
	t.Setenv("FORGE_HOME", t.TempDir())
	if got := credentialsPathForHelp(); !strings.Contains(got, "credentials") {
		t.Errorf("path = %q", got)
	}
}
