package vulnpatch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/mod/module"
	modzip "golang.org/x/mod/zip"

	"github.com/reliant-labs/forge/internal/goexec"
	"github.com/reliant-labs/forge/internal/vulnscan"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// TestRealGoBump runs the Go patcher with the real go command against a
// module proxy on disk (GOPROXY=file://), so no network is touched: a
// vulnerable v1.0.0 of example.com/lib is bumped to its fix, v1.0.1, and
// go.mod and go.sum say so.
func TestRealGoBump(t *testing.T) {
	testkit.Slow(t, "execs go get and go mod tidy against an on-disk module proxy")
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	proxy := t.TempDir()
	for _, v := range []string{"v1.0.0", "v1.0.1", "v1.1.0"} {
		publish(t, proxy, "example.com/lib", v)
	}
	root := writeTree(t, map[string]string{
		"go.mod":  "module example.com/app\n\ngo 1.22\n\nrequire example.com/lib v1.0.0\n",
		"main.go": "package main\n\nimport \"example.com/lib\"\n\nfunc main() { lib.F() }\n",
	})
	// A module cache of its own, so example.com/lib never lands in the
	// machine's (writable, so t.TempDir can remove it).
	// file:///C:/... on Windows, file:///tmp/... elsewhere.
	proxyURL := filepath.ToSlash(proxy)
	if !strings.HasPrefix(proxyURL, "/") {
		proxyURL = "/" + proxyURL
	}
	// The verify step builds the t.TempDir() module: -trimpath keeps that
	// build out of the shared cache's per-path entries.
	env := goexec.WithTrimpath([]string{"GOPROXY=file://" + proxyURL, "GOSUMDB=off", "GOFLAGS=-mod=mod -modcacherw", "GOWORK=off", "GOTOOLCHAIN=local", "GOMODCACHE=" + t.TempDir()})
	run := func(ctx context.Context, c vulnscan.Command) error {
		c.Env = append(os.Environ(), env...)
		return vulnscan.Exec(ctx, c)
	}
	// Seed go.sum the way a real project has one.
	if err := run(context.Background(), vulnscan.Command{Path: "go", Args: []string{"mod", "tidy"}, Dir: root, Stdout: os.Stderr, Stderr: os.Stderr}); err != nil {
		t.Fatal(err)
	}
	p, err := NewPlan(root, []vulnscan.Finding{goFinding("GO-2026-0001", "example.com/lib", "v1.0.0", "v1.0.1")}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Apply(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	p.Verify(context.Background(), VerifyOptions{Command: "go build ./...", Source: VerifySourceFlag, Run: run})
	doc := p.Document("", "", false)
	if doc.ExitCode != ExitOK {
		t.Fatalf("exit %d: %+v %+v", doc.ExitCode, doc.Bumps, doc.Verify)
	}
	gomod, _ := os.ReadFile(filepath.Join(root, "go.mod"))
	gosum, _ := os.ReadFile(filepath.Join(root, "go.sum"))
	if !strings.Contains(string(gomod), "example.com/lib v1.0.1") || !strings.Contains(string(gosum), "example.com/lib v1.0.1") {
		t.Errorf("go.mod:\n%s\ngo.sum:\n%s", gomod, gosum)
	}
}

// publish writes one version of a one-file module into a GOPROXY directory.
func publish(t *testing.T, proxy, path, version string) {
	t.Helper()
	src := t.TempDir()
	gomod := "module " + path + "\n\ngo 1.22\n"
	if err := os.WriteFile(filepath.Join(src, "go.mod"), []byte(gomod), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "lib.go"), []byte("package lib\n\nfunc F() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(proxy, filepath.FromSlash(path), "@v")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	zf, err := os.Create(filepath.Join(dir, version+".zip"))
	if err != nil {
		t.Fatal(err)
	}
	if err := modzip.CreateFromDir(zf, module.Version{Path: path, Version: version}, src); err != nil {
		t.Fatal(err)
	}
	_ = zf.Close()
	for name, body := range map[string]string{
		version + ".mod":  gomod,
		version + ".info": `{"Version":"` + version + `","Time":"2026-01-01T00:00:00Z"}`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f, err := os.OpenFile(filepath.Join(dir, "list"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(version + "\n")
	_ = f.Close()
}
