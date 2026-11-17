package cli

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

func stubDaemon(t *testing.T, err error) {
	t.Helper()
	prev := dockerDaemonReachable
	dockerDaemonReachable = func(context.Context) error { return err }
	t.Cleanup(func() { dockerDaemonReachable = prev })
}

func TestRequireImageDaemon(t *testing.T) {
	stubDaemon(t, nil)
	if err := requireImageDaemon(context.Background(), "service gw (gw/Dockerfile)"); err != nil {
		t.Fatalf("with a daemon: %v", err)
	}
	t.Setenv("DOCKER_HOST", "unix:///nonexistent")
	stubDaemon(t, errors.New("cannot reach the Docker daemon: dial unix /nonexistent: connect: no such file"))
	err := requireImageDaemon(context.Background(), "service gw (gw/Dockerfile)")
	if err == nil {
		t.Fatal("without a daemon this must fail before docker does")
	}
	for _, want := range []string{"no Docker daemon answered (DOCKER_HOST=unix:///nonexistent)", "forge builds images with docker", "service gw", "start Docker"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q:\n%s", want, err)
		}
	}
}

// The project image is docker's alone: with no daemon it fails with the one
// error, never an image assembled some other way.
func TestProjectImageWithoutADaemonFails(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("Dockerfile", []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stubDaemon(t, errors.New("cannot reach the Docker daemon"))
	res := dockerBuildProject(context.Background(), &config.ProjectConfig{Name: "demo"},
		imageTagSet("registry.example/acme/demo", "v1", true, true), "amd64", versionInfo{},
		projectImageGuard{outputDir: "bin", envLabel: "prod"})
	if res.err == nil || !strings.Contains(res.err.Error(), "no Docker daemon answered") {
		t.Fatalf("err = %v", res.err)
	}
}

func TestWithTrimpathDoesNotMutateTheCallersTargets(t *testing.T) {
	in := []goBuildTarget{{cmd: "./cmd/a", outputName: "a"}}
	out := withTrimpath(in)
	if !out[0].trimpath || in[0].trimpath {
		t.Errorf("out=%v in=%v", out[0].trimpath, in[0].trimpath)
	}
}
