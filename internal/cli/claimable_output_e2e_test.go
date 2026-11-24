package cli

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// The first-time-user shape, end to end: a bare Vite app, no credentials,
// `env deploy prod --claimable`, with a docker whose login is as noisy as the
// real one. A deploy that worked must not read as one that did not, must not
// write the user's ~/.docker, and must stay small enough to be cheap to read.
func TestClaimableDeploy_OutputIsQuietAndLeavesDockerConfigAlone(t *testing.T) {
	testkit.Slow(t, "scaffolds a site project and runs the whole claimable deploy")
	requirePOSIXFake(t, "docker")
	fake, _ := claimableFixture(t)
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	redirectHTTPTo(t, srv)

	bin := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = login ]; then cat >/dev/null\n" +
		"d=\"${DOCKER_CONFIG:-$HOME/.docker}\"; mkdir -p \"$d\"; echo '{\"auths\":{\"x\":{}}}' > \"$d/config.json\"\n" +
		"echo 'Login Succeeded'; echo; echo \"WARNING! Your credentials are stored unencrypted in '$d/config.json'.\"\n" +
		"echo 'Configure a credential helper to remove this warning. See'; echo 'https://docs.docker.com/go/credential-store/'; fi\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.Unsetenv("DOCKER_CONFIG")

	dir := filepath.Join(t.TempDir(), "my-landing")
	writeAdoptFixture(t, dir, map[string]string{
		"package.json": `{"name":"landing","private":true,"scripts":{"build":"vite build"},"devDependencies":{"vite":"^5.0.0"}}`,
		"index.html":   "<h1>hi</h1>\n", ".gitignore": "node_modules\n",
	})
	t.Chdir(dir)
	t.Setenv("FORGE_SKIP_NPM_INSTALL", "1")
	fake.project, fake.env = "my-landing", "prod"

	var runErr error
	stdout, stderr := captureStdoutAndStderr(t, func() {
		cmd := newTestRootCmd(t)
		cmd.SetArgs([]string{"env", "deploy", "prod", "--claimable", "--no-wait"})
		runErr = cmd.ExecuteContext(context.Background())
	})
	if runErr != nil {
		t.Fatalf("deploy: %v\nstdout:\n%s\nstderr:\n%s", runErr, stdout, stderr)
	}
	all := stdout + stderr
	t.Logf("claimable deploy output: %d bytes, %d lines", len(all), strings.Count(all, "\n"))

	for _, bad := range []string{
		"Login Succeeded", "stored unencrypted", "credential-store", "[warn]", "no recorded config", "drift",
		"upgrade the control plane", "predates", "rollback floor", "forge login", "ask an admin", "org admin",
		"records no migrations", "records no snapshot", "forge env status", "plan digest",
	} {
		if strings.Contains(all, bad) {
			t.Errorf("a successful anonymous deploy prints %q:\n%s", bad, all)
		}
	}
	for _, want := range []string{"Adopted this app", "AGENTS.md", "deploy/", "forge.yaml", ".gitignore", "✓ Live:", "Claim:  https://app.reliant.dev/claim#rcl_9f3KQe"} {
		if !strings.Contains(all, want) {
			t.Errorf("the output lacks %q:\n%s", want, all)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".docker")); !os.IsNotExist(err) {
		t.Errorf("the deploy wrote the user's ~/.docker (stat err: %v)", err)
	}
	// 8.5 KB was the output before; it must not creep back.
	if len(all) > 4500 {
		t.Errorf("the output is %d bytes, over the 4500-byte budget:\n%s", len(all), all)
	}
}
