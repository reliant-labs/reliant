package dockercred

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func writeHelperConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func lookPathIn(found map[string]string) func(string) (string, error) {
	return func(name string) (string, error) {
		if p, ok := found[name]; ok {
			return p, nil
		}
		return "", exec.ErrNotFound
	}
}

// TestMissingHelpers pins obs 51b41070's precondition: a credHelpers entry
// whose docker-credential-<name> is not on PATH, found before a build.
func TestMissingHelpers(t *testing.T) {
	t.Parallel()
	cfg := writeHelperConfig(t, `{"credsStore":"desktop","credHelpers":{"us-docker.pkg.dev":"gcloud","gcr.io":"gcloud","ghcr.io":"","quay.io":"ecr-login"}}`)
	got := MissingHelpers(cfg, lookPathIn(map[string]string{"docker-credential-ecr-login": "/bin/x"}))
	want := []MissingHelper{
		{Helper: "desktop"},
		{Helper: "gcloud", Hosts: []string{"gcr.io", "us-docker.pkg.dev"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MissingHelpers = %+v, want %+v", got, want)
	}
	if got := MissingHelpers(filepath.Join(t.TempDir(), "absent.json"), lookPathIn(nil)); got != nil {
		t.Fatalf("a missing config names no helper, got %+v", got)
	}
}

func TestHelperBesideTool(t *testing.T) {
	t.Parallel()
	sdk := filepath.Join(t.TempDir(), "google-cloud-sdk", "bin")
	bin := filepath.Join(t.TempDir(), "gobin")
	for _, d := range []string{sdk, bin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// The Cloud SDK ships the helper as docker-credential-gcloud.cmd on
	// Windows, where only a PATHEXT extension makes a file executable.
	helperFile := "docker-credential-gcloud"
	if runtime.GOOS == "windows" {
		helperFile += ".cmd"
	}
	for _, f := range []string{"gcloud", helperFile} {
		if err := os.WriteFile(filepath.Join(sdk, f), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(bin, "gcloud")
	if err := os.Symlink(filepath.Join(sdk, "gcloud"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	// The directory as EvalSymlinks names it: on Windows that expands an
	// 8.3 short name (RUNNER~1) in t.TempDir() to the long one.
	want, err := filepath.EvalSymlinks(sdk)
	if err != nil {
		t.Fatal(err)
	}
	look := lookPathIn(map[string]string{"gcloud": link})
	if got := HelperBesideTool("gcloud", look); got != want {
		t.Fatalf("HelperBesideTool = %q, want %q", got, want)
	}
	if got := HelperBesideTool("desktop", look); got != "" {
		t.Fatalf("only a known tool's helper is located, got %q", got)
	}
	if got := HelperBesideTool("gcloud", func(string) (string, error) { return "", errors.New("no") }); got != "" {
		t.Fatalf("no gcloud on PATH locates nothing, got %q", got)
	}
}
