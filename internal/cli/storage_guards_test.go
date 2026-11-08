package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/storage"
)

func TestStorageReserveBlocksDirectBuildLanes(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	if err := os.WriteFile("Dockerfile", []byte("FROM scratch\n"), 0600); err != nil {
		t.Fatal(err)
	}
	want := errors.New("host free space below reserve")
	old := checkBuildStorageFn
	checkBuildStorageFn = func(string) error { return want }
	t.Cleanup(func() { checkBuildStorageFn = old })
	cfg := &config.ProjectConfig{Name: "test"}
	ctx := context.Background()
	for name, run := range map[string]func() buildResult{
		"go": func() buildResult {
			return buildGoTarget(ctx, goBuildTarget{cmd: "./cmd/test", outputName: "test"}, "bin", false, "", versionInfo{}, buildMemoryCaps{})
		},
		"variant":  func() buildResult { return buildVariant(ctx, "test", "./cmd/test", BuildVariant{Name: "debug"}, "bin") },
		"frontend": func() buildResult { return buildFrontend(ctx, config.FrontendConfig{Name: "web"}, buildMemoryCaps{}) },
		"project Docker": func() buildResult {
			return dockerBuildProject(ctx, cfg, dockerImageTags{}, "", versionInfo{}, projectImageGuard{})
		},
		"frontend Docker": func() buildResult { return dockerBuild(ctx, cfg, "web", ".", dockerImageTags{}, "") },
		"service Docker":  func() buildResult { return buildServiceDocker(ctx, cfg, "api", "api", nil, buildOptions{}, "", "") },
	} {
		t.Run(name, func(t *testing.T) {
			if got := run(); !errors.Is(got.err, want) {
				t.Fatalf("build was not blocked by reserve: %+v", got)
			}
		})
	}
	results := buildExternalServices(ctx, []WorkloadEntity{{Name: "shell", Build: BuildConfigEntity{Type: "shell", Shell: &ShellBuild{Cmd: "touch should-not-exist"}}}}, buildOptions{}, "dev", root)
	if len(results) != 1 || !errors.Is(results[0].err, want) {
		t.Fatalf("shell build was not blocked: %+v", results)
	}
	if _, err := os.Stat("should-not-exist"); !os.IsNotExist(err) {
		t.Fatalf("shell build ran: %v", err)
	}
}

func TestStorageCheckRejectsReserveWithoutCreatingOutputs(t *testing.T) {
	root := t.TempDir()
	policy := storage.DefaultPolicy()
	policy.HostReserveGiB = 1 << 30
	path := filepath.Join(root, "storage.json")
	if err := storage.Save(path, policy); err != nil {
		t.Fatal(err)
	}
	cmd := newStorageCmd()
	cmd.SetArgs([]string{"--policy", path, "check", filepath.Join(root, "new", "bin")})
	if err := cmd.ExecuteContext(context.Background()); err == nil {
		t.Fatal("space check accepted impossible reserve")
	}
	if _, err := os.Stat(filepath.Join(root, "new")); !os.IsNotExist(err) {
		t.Fatalf("space check created output: %v", err)
	}
}

func TestStorageReserveBlocksEnvUpBeforeSideEffects(t *testing.T) {
	root := newTestProject(t)
	t.Chdir(root)
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		t.Setenv(name, "")
	}
	want := errors.New("host free space below reserve")
	old := checkBuildStorageFn
	checkBuildStorageFn = func(path string) error {
		if path != root {
			t.Fatalf("checked %q instead of project %q", path, root)
		}
		return want
	}
	t.Cleanup(func() { checkBuildStorageFn = old })
	if err := runUp(context.Background(), upOptions{env: "dev", noBuild: true}); !errors.Is(err, want) {
		t.Fatalf("env up was not blocked by reserve: %v", err)
	}
}
