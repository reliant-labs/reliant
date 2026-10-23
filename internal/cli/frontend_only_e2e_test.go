//go:build e2e

package cli

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Drives the public scaffold -> rebind -> render -> up lifecycle with no
// database or cluster. The npm boundary serves a tiny fixture instead of
// downloading Next: orchestration must launch it in the DECLARED directory,
// on the rendered port, without invoking any infrastructure CLI.
func TestE2EFrontendOnlyLifecycle(t *testing.T) {
	requireTool(t, "go", "node")
	root := t.TempDir()
	bin := filepath.Join(root, "forge")
	repo := findRepoRoot(t)
	build := exec.Command("go", "build", "-ldflags", "-X github.com/reliant-labs/forge/internal/buildinfo.DevForgeRoot="+repo, "-o", bin, "./cmd/forge")
	build.Dir = repo
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	tools := filepath.Join(root, "tools")
	if err := os.MkdirAll(tools, 0755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "forbidden")
	for _, name := range []string{"kubectl", "docker", "k3d", "postgres", "initdb", "pg_ctl", "gcloud"} {
		writeFileE2E(t, filepath.Join(tools, name), "#!/bin/sh\necho "+name+" >> '"+marker+"'\nexit 99\n")
		if err := os.Chmod(filepath.Join(tools, name), 0755); err != nil {
			t.Fatal(err)
		}
	}
	// Installs are not under test; dev executes a real HTTP process from the
	// frontend path so starting npm in the project root cannot falsely pass.
	writeFileE2E(t, filepath.Join(tools, "npm"), "#!/bin/sh\nif [ \"$1\" = install ]; then exit 0; fi\nexec node forge-dev-fixture.cjs\n")
	if err := os.Chmod(filepath.Join(tools, "npm"), 0755); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "PATH="+tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	run := func(dir string, args ...string) (string, error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Dir = dir
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	must := func(dir string, args ...string) string {
		t.Helper()
		out, err := run(dir, args...)
		if err != nil {
			t.Fatalf("forge %v: %v\n%s", args, err, out)
		}
		return out
	}
	// Fails (non-zero) if the project's own first generate does.
	must(root, "project", "new", "landing", "--mod", "example.com/landing", "--frontend", "web", "--output", "static", "--routes", "none", "--disable", "orm,migrations,codegen,observability,ci,contracts", "--skip-tools", "--link-forge")
	project := filepath.Join(root, "landing")
	for _, envName := range []string{"dev", "staging", "prod"} {
		body := readFileE2E(t, filepath.Join(project, "deploy", "kcl", envName, "main.k"))
		if !strings.Contains(body, `routes = ["none",`) || !strings.Contains(body, "(_web_frontend)\n") {
			t.Fatalf("%s lost the project-new route policy or rebindable shape:\n%s", envName, body)
		}
	}
	if !strings.Contains(readFileE2E(t, filepath.Join(project, "frontends", "web", "next.config.ts")), `output: "export"`) {
		t.Fatal("project-new static frontend is not a static export")
	}
	for _, envName := range []string{"dev", "staging", "prod"} {
		out := must(project, "env", "render", envName)
		if (envName == "dev" && !strings.Contains(out, "0 object(s)")) || strings.Contains(out, "kind: Namespace") || strings.Contains(out, "kind: Deployment") || strings.Contains(out, "kind: ManagedDatabase") || strings.Contains(out, "kind: Job") {
			t.Fatalf("%s renders cluster objects:\n%s", envName, out)
		}
	}
	// Later frontends must use the same declaration/binder shape and routes.
	// The scaffold ends with a generate run, which must be green on this
	// shape (TestE2EProjectNewDisableShapes holds every --disable shape to it).
	must(project, "scaffold", "frontend", "docs", "--routes", "none")
	must(project, "env", "new", "preview", "--from", "prod", "--bind", "docs=hosted", "--bind", "web=hosted")
	must(project, "env", "render", "preview")
	if out, err := run(project, "env", "new", "bad", "--from", "prod", "--bind", "missing=hosted"); err == nil {
		t.Fatalf("missing binding accepted:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(project, "deploy/kcl/bad")); !os.IsNotExist(err) {
		t.Fatalf("refused env was written: %v", err)
	}
	port := freePortE2E(t)
	dev := filepath.Join(project, "deploy/kcl/dev/main.k")
	body, err := os.ReadFile(dev)
	if err != nil {
		t.Fatal(err)
	}
	body = []byte(strings.Replace(string(body), `plugin.resolve_port("landing-dev-web", 3000)`, fmt.Sprint(port), 1))
	if err := os.WriteFile(dev, body, 0644); err != nil {
		t.Fatal(err)
	}
	web := filepath.Join(project, "frontends/web")
	for _, name := range []string{"web", "docs"} {
		writeFileE2E(t, filepath.Join(project, "frontends", name, "forge-dev-fixture.cjs"), `require('http').createServer((q,s)=>s.end(process.cwd())).listen(Number(process.env.PORT),'127.0.0.1');`)
	}
	// Neither build nor deploy is disabled: selection and the declaration must
	// prove this frontend needs no Go image or infrastructure phase.
	t.Cleanup(func() {
		out, err := run(project, "env", "down", "dev")
		if err != nil {
			t.Errorf("cleanup own dev process: %v\n%s", err, out)
		}
	})
	must(project, "env", "up", "dev", "--background", "--no-generate", "--no-install", "--no-seed")
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d", port))
		if err == nil {
			raw := make([]byte, 4096)
			n, _ := resp.Body.Read(raw)
			resp.Body.Close()
			if string(raw[:n]) != web {
				t.Fatalf("frontend ran in %q, want %s", raw[:n], web)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("frontend never started: %v", err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	if raw, err := os.ReadFile(marker); err == nil {
		t.Fatalf("frontend-only lifecycle touched infrastructure: %s", raw)
	}
}
