package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestReportDockerCredHelpersWarnsBeforeTheBuild pins obs 51b41070: the build
// names the missing helper, its hosts and the fix instead of failing minutes
// later with "error getting credentials".
func TestReportDockerCredHelpersWarnsBeforeTheBuild(t *testing.T) {
	t.Parallel()
	cfg := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(cfg, []byte(`{"credHelpers":{"us-docker.pkg.dev":"gcloud"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	reportDockerCredHelpers(&out, cfg, func(string) (string, error) { return "", exec.ErrNotFound })
	for _, want := range []string{"us-docker.pkg.dev", "docker-credential-gcloud", "is not on PATH", "installation.sdk_root"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("warning missing %q:\n%s", want, out.String())
		}
	}

	out.Reset()
	reportDockerCredHelpers(&out, cfg, func(string) (string, error) { return "/usr/bin/docker-credential-gcloud", nil })
	if out.Len() != 0 {
		t.Fatalf("a helper on PATH needs no warning, got:\n%s", out.String())
	}
}

// The Cloud SDK directory found beside gcloud is APPENDED to PATH: it holds
// the SDK's other tools too, and the ones already on PATH keep winning.
func TestReportDockerCredHelpersAppendsTheSDKDirectory(t *testing.T) {
	sdk := filepath.Join(t.TempDir(), "google-cloud-sdk", "bin")
	bin := filepath.Join(t.TempDir(), "gobin")
	for _, d := range []string{sdk, bin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
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
	cfg := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(cfg, []byte(`{"credHelpers":{"us-docker.pkg.dev":"gcloud"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	before := "first" + string(os.PathListSeparator) + "second"
	t.Setenv("PATH", before)
	look := func(name string) (string, error) {
		if name == "gcloud" {
			return link, nil
		}
		return "", exec.ErrNotFound
	}
	var out bytes.Buffer
	reportDockerCredHelpers(&out, cfg, look)
	got := os.Getenv("PATH")
	if !strings.HasPrefix(got, before+string(os.PathListSeparator)) || !strings.Contains(out.String(), "using the one beside gcloud") {
		t.Fatalf("PATH = %q, want %q followed by the SDK directory\n%s", got, before, out.String())
	}
}
