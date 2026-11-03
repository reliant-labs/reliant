package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/buildtarget"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/doctor"
)

func TestBuildGitSourcesResolveContextsAndReportPins(t *testing.T) {
	project, repo := t.TempDir(), t.TempDir()
	dir := filepath.Join(repo, "src")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSourceOverride(t, project, "github.com/acme/lib", repo)
	pin := &config.GitSource{Repo: "github.com/acme/lib", Ref: "v1", Subdir: "src"}
	cfg := &config.ProjectConfig{Docker: config.DockerConfig{BuildContexts: map[string]config.BuildContext{"lib": {Source: pin}, "base": {Path: "docker-image://alpine:3.22"}}}}
	entities := &KCLEntities{Workloads: []WorkloadEntity{{Name: "lib", Build: BuildConfigEntity{Type: "docker", Docker: &DockerBuild{BuildContexts: map[string]config.BuildContext{"lib": {Source: pin}}}}}}}
	if err := resolveBuildSources(context.Background(), project, cfg, entities); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{appendBuildContexts(nil, cfg, project), appendNamedBuildContexts(nil, entities.Workloads[0].Build.Docker.BuildContexts, project)} {
		if !argsHave(args, "--build-context", "lib="+dir) {
			t.Fatalf("context did not resolve: %v", args)
		}
	}
	result := sourceDoctorCheck(cfg, entities)
	if result.Status != doctor.StatusPass || !strings.Contains(result.Message, "2 GitSource") || !strings.Contains(result.Evidence, "pinned github.com/acme/lib@v1//src") || !strings.Contains(result.Evidence, "not a GitSource pin") {
		t.Fatalf("source report = %+v", result)
	}
	// Declarations must remain pins after materialization.
	data, err := json.Marshal(entities.Workloads[0].Build.Docker)
	if err != nil || strings.Contains(string(data), repo) {
		t.Fatalf("resolved path leaked: %s, %v", data, err)
	}
}

func TestShellBuildGitSourceRefusesAmbiguityAndMissingOverride(t *testing.T) {
	project := t.TempDir()
	pin := &config.GitSource{Repo: "github.com/acme/lib", Ref: "v1"}
	writeSourceOverride(t, project, pin.Repo, filepath.Join(project, "missing"))
	for _, sh := range []*ShellBuild{{Cmd: "true", Cwd: "../lib", Source: pin}, {Cmd: "true", Source: pin}, {Cmd: "true", Source: &config.GitSource{Repo: pin.Repo}}} {
		if err := resolveShellBuildSource(context.Background(), project, sh); err == nil {
			t.Fatalf("accepted unresolvable/ambiguous source: %+v", sh)
		}
		if sh.SourceDir != "" {
			t.Fatal("failed resolution left a working directory")
		}
	}
}

// Issue #107: a source must survive the render boundary rather than silently
// becoming an empty cwd (the project root) or an unsupported Docker context.
func TestBuildGitSourceRenderBoundary(t *testing.T) {
	for _, raw := range []string{
		`{"type":"shell","cmd":"pwd","source":{"repo":"github.com/acme/lib","ref":"v1","subdir":"src"}}`,
		`{"type":"docker","build_contexts":{"lib":{"repo":"github.com/acme/lib","ref":"v1","subdir":"src"}}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			build, err := dispatchBuild("lib", json.RawMessage(raw))
			if err != nil {
				t.Fatal(err)
			}
			out, err := json.Marshal(build)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(out), `"repo":"github.com/acme/lib"`) {
				t.Fatalf("GitSource lost at render boundary: %s", out)
			}
		})
	}
}

func TestShellBuildGitSourceRunsInFetchedSubdir(t *testing.T) {
	project := t.TempDir()
	repo := t.TempDir()
	dir := filepath.Join(repo, "src")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSourceOverride(t, project, "github.com/acme/lib", repo)
	build, err := dispatchBuild("lib", json.RawMessage(`{"type":"shell","cmd":"pwd > built-in.txt","source":{"repo":"github.com/acme/lib","ref":"v1","subdir":"src"}}`))
	if err != nil {
		t.Fatal(err)
	}
	old := externalImageDigestResolver
	externalImageDigestResolver = func(context.Context, string) (string, []string, error) { return "", nil, nil }
	t.Cleanup(func() { externalImageDigestResolver = old })
	results := buildExternalServices(context.Background(), []WorkloadEntity{{Name: "lib", Build: build}}, buildOptions{env: "dev"}, "test", project)
	if len(results) != 1 || results[0].err != nil {
		t.Fatalf("build = %+v", results)
	}
	got, err := os.ReadFile(filepath.Join(dir, "built-in.txt"))
	if err != nil {
		t.Fatalf("source build did not run in resolved subdirectory: %v", err)
	}
	if strings.TrimSpace(string(got)) != dir {
		t.Fatalf("cwd = %q, want %q", got, dir)
	}
	if _, err := os.Stat(filepath.Join(project, "built-in.txt")); !os.IsNotExist(err) {
		t.Fatalf("source build ran in project root: %v", err)
	}
}

func TestShellBuildGitSourceReleasePin(t *testing.T) {
	commit := strings.Repeat("c", 40)
	entities := &KCLEntities{Workloads: []WorkloadEntity{{Name: "lib", Build: BuildConfigEntity{Type: "shell", Shell: &ShellBuild{Cmd: "true", Source: &config.GitSource{Repo: "github.com/acme/lib", Ref: commit}}}}}}
	check := newReleaseSourceCheck(t.TempDir(), "prod", "v1", false, entities)
	pins, err := check.gitSourcePins(context.Background(), &buildtarget.Source{Repo: "github.com/acme/lib"})
	if err != nil || len(pins) != 1 || !pins[0].admits(commit) {
		t.Fatalf("ShellBuild pin not enforced by release guard: %+v, %v", pins, err)
	}
}

func TestBuildGitSourceCommandWiring(t *testing.T) {
	// Like the existing #144 guard, a resolver-only test cannot prove the
	// command calls it. Read the inputs so this test remains cache-sound.
	for file, call := range map[string]string{"build.go": "resolveBuildSources(ctx, projectDirForKCL(), cfg, entities)", "deploy.go": "resolveNamedBuildContexts(ctx, projectDirForKCL(), cfg.Docker.BuildContexts)", "doctor.go": "runSourceDoctorChecks(ctx, cfg, projectDir, signal)"} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(src), call) {
			t.Fatalf("%s no longer wires %s", file, call)
		}
	}
}

func TestDockerGitSourceYAMLLoads(t *testing.T) {
	_, err := config.LoadProject([]byte("name: demo\nmodule_path: example.com/demo\ndocker:\n  build_contexts:\n    lib:\n      repo: github.com/acme/lib\n      ref: v1\n      subdir: src\n"), filepath.Join(t.TempDir(), "forge.yaml"))
	if err != nil {
		t.Fatal(err)
	}
}
