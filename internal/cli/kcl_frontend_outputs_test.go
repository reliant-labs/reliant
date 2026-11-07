package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// staticRuntimeProject writes a project with one Next.js frontend `web`
// whose next.config builds the given output ("export", "standalone" or ""
// for no `output:` key = a server build), and whose `env` binds it with
// runtimeExpr. The build shape is read from that next.config: there is no
// forge.yaml declaration of it.
func staticRuntimeProject(t *testing.T, env, output, runtimeExpr string) string {
	t.Helper()
	dir := t.TempDir()
	nextConfig := "const nextConfig = {};\nexport default nextConfig;\n"
	switch output {
	case "static":
		nextConfig = "const nextConfig = {\n  output: \"export\",\n};\nexport default nextConfig;\n"
	case "standalone":
		nextConfig = "const nextConfig = {\n  output: \"standalone\",\n};\nexport default nextConfig;\n"
	}
	forgeYAML := "name: acme\nmodule_path: github.com/example/acme\n"
	files := map[string]string{
		"forge.yaml":                   forgeYAML,
		"frontends/web/next.config.ts": nextConfig,
		"frontends/web/package.json":   "{\"dependencies\":{\"next\":\"15.0.0\"}}\n",
		"deploy/kcl/kcl.mod":           "[package]\nname = \"acme_deploy\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n",
		"deploy/kcl/" + env + "/main.k": "import forge\n\n" +
			"_web = forge.Frontend {name = \"web\", path = \"frontends/web\", public_dir = \"out\"}\n\n" +
			"output = forge.render(forge.Bundle {\n" +
			"    project = \"acme\"\n" +
			"    control_plane = forge.ControlPlane {endpoint = \"https://cp.example\"}\n" +
			"    frontends = [_web | {" + runtimeExpr + "}]\n" +
			"})\n",
	}
	for rel, body := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestRenderKCL_RefusesServerFrontendOnStaticRuntime is the render-time half
// of the static-export guard, end to end: the frontend's next.config builds a
// server, the env binds it to a runtime that serves files, and the render —
// the one path every `forge env render|deploy|up` and `forge build` takes —
// refuses, naming the frontend, the env and both fixes.
//
// Fails before: with renderKCLRaw not binding frontend_outputs (or render.k
// not reading it) every case below renders cleanly, and the deploy fails
// later at publish on a public_dir nothing wrote.
func TestRenderKCL_RefusesServerFrontendOnStaticRuntime(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL; skipped in -short")
	}
	hosted := `image = "ghcr.io/acme/web", runtime = forge.OnHosted {}`
	bucket := `runtime = forge.OnBucket {bucket = "acme-web"}`
	cases := []struct {
		name, output, runtime, runtimeType string
		refused                            bool
	}{
		{"standalone on hosted", "standalone", hosted, "hosted", true},
		{"server on bucket", "", bucket, "bucket", true},
		// An unset field is the LEGACY standalone shape: every frontend
		// scaffolded before forge wrote `output:` has a server next.config.
		{"no output key is a server build", "", hosted, "hosted", true},
		{"static on hosted", "static", hosted, "hosted", false},
		{"static on bucket", "static", bucket, "bucket", false},
		{"standalone on the dev server", "standalone", `runtime = forge.OnHost {}`, "host", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := staticRuntimeProject(t, "prod", tc.output, tc.runtime)
			entities, err := RenderKCL(context.Background(), dir, "prod")
			if !tc.refused {
				if err != nil {
					t.Fatalf("render refused a frontend it must accept: %v", err)
				}
				if len(entities.Frontends) != 1 || entities.Frontends[0].Runtime.Type != tc.runtimeType {
					t.Fatalf("frontends = %+v, want web on %s", entities.Frontends, tc.runtimeType)
				}
				return
			}
			if err == nil {
				t.Fatalf("render accepted a %q build on the %s runtime", tc.output, tc.runtimeType)
			}
			msg := err.Error()
			for _, want := range []string{
				"frontend 'web' cannot run on the " + tc.runtimeType + " runtime",
				"in env 'prod'",
				"frontends/web's next.config a static export",
				"forge lint --static-export",
				"bind 'web' elsewhere",
			} {
				if !strings.Contains(msg, want) {
					t.Errorf("refusal does not say %q:\n%s", want, msg)
				}
			}
		})
	}
}

// TestFrontendOutputsBinding pins the wire shape render.k decodes: a quoted
// JSON string, every declared frontend by name, the EFFECTIVE output (an
// unset field resolved, a Vite app static), and nothing bound at all for a
// project with no frontends. (Vite's `standalone` is ignored: it has no
// server build.)
func TestFrontendOutputsBinding(t *testing.T) {
	got := frontendOutputsBinding([]config.FrontendConfig{
		{Name: "web", Type: "nextjs", Output: "Standalone"},
		{Name: "admin", Type: "nextjs", Output: "server"},
		{Name: "spa", Type: "vite-spa", Output: "standalone"},
	})
	want := `frontend_outputs="{\"admin\":\"server\",\"spa\":\"static\",\"web\":\"standalone\"}"`
	if got != want {
		t.Errorf("binding:\n got %s\nwant %s", got, want)
	}
	if got := frontendOutputsBinding(nil); got != "" {
		t.Errorf("no frontends bound %q, want nothing", got)
	}
}
