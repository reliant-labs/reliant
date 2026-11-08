//go:build integration

// Tagged integration because it runs the real docker: the default build's
// host-tool tripwire (hermetic_host_test.go) refuses that. The images are
// this test's own (forge-sde-test:a/b) and are removed when it ends.

package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/config"
)

// TestServiceDockerBuild_ReproducibleImageID builds one tiny image twice with
// --no-cache, a second apart, through the argv forge really assembles, and
// requires the same image ID (the config digest, which names every layer).
// Without SOURCE_DATE_EPOCH the two configs carry different `created` times
// and the IDs differ: this is the otelcol-reliant regression in miniature.
func TestServiceDockerBuild_ReproducibleImageID(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not on PATH")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("no docker daemon")
	}
	dir := t.TempDir()
	// No Dockerfile default: the epoch must come from forge's argv. (File
	// mtimes inside layers are the Dockerfile's to clamp on the classic image
	// store; the input file here is written once, so its mtime is stable.)
	dockerfile := "FROM scratch\nCOPY in.txt /in.txt\n"
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {
		t.Fatal(err)
	}
	in := filepath.Join(dir, "in.txt")
	if err := os.WriteFile(in, []byte("same bytes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.ProjectConfig{Name: "repro"}
	build := func(tag string) string {
		t.Helper()
		args, _ := serviceDockerBuildArgs(cfg, "forge-sde-test", "Dockerfile", &DockerBuild{Context: "."}, buildOptions{}, "", tag)
		args = appendSourceDateEpoch(args, "1700000000")
		args = append(args[:1], append([]string{"--no-cache", "-q"}, args[1:]...)...)
		cmd := exec.CommandContext(context.Background(), "docker", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("docker %v: %v\n%s", args, err, out)
		}
		id, err := exec.Command("docker", "image", "inspect", "--format", "{{.Id}}", "forge-sde-test:"+tag).Output()
		if err != nil {
			t.Fatalf("inspect: %v", err)
		}
		t.Cleanup(func() { _ = exec.Command("docker", "image", "rm", "-f", "forge-sde-test:"+tag).Run() })
		return strings.TrimSpace(string(id))
	}
	first := build("a")
	time.Sleep(1100 * time.Millisecond)
	second := build("b")
	if first != second {
		t.Fatalf("two builds of identical inputs gave different images:\n  %s\n  %s", first, second)
	}
}
