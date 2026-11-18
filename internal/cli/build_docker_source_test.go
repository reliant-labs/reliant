package cli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/buildtarget"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/gitsource"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// The render boundary: `context` and `dockerfile` are `str | GitSource`, and
// both members survive decode and re-encode — the pin, never a local path.
func TestDockerBuildGitSourceRenderBoundary(t *testing.T) {
	raw := `{"type":"docker","dockerfile":{"repo":"github.com/acme/images","ref":"v1","subdir":"py/Dockerfile"},` +
		`"context":{"repo":"github.com/acme/billing","ref":"v2.3.1","subdir":null},"platform":"linux/arm64"}`
	build, err := dispatchBuild("billing", json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	d := build.Docker
	if d == nil || d.ContextSource == nil || d.ContextSource.Repo != "github.com/acme/billing" || d.ContextSource.Ref != "v2.3.1" || d.Context != "" {
		t.Fatalf("context pin lost: %+v", d)
	}
	if d.DockerfileSource == nil || d.DockerfileSource.Subdir != "py/Dockerfile" || d.Dockerfile != "" || d.Platform != "linux/arm64" {
		t.Fatalf("dockerfile pin lost: %+v", d)
	}
	d.ContextDir, d.DockerfilePath = "/cache/billing", "/cache/images/py/Dockerfile"
	out, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"context":{"repo":"github.com/acme/billing","ref":"v2.3.1"}`) ||
		!strings.Contains(string(out), `"dockerfile":{"repo":"github.com/acme/images"`) || strings.Contains(string(out), "/cache/") {
		t.Fatalf("re-encoded declaration = %s", out)
	}

	// The path member is unchanged — every pre-existing DockerBuild.
	plain, err := dispatchBuild("web", json.RawMessage(`{"type":"docker","dockerfile":"web/Dockerfile","context":"web"}`))
	if err != nil {
		t.Fatal(err)
	}
	if plain.Docker.Dockerfile != "web/Dockerfile" || plain.Docker.Context != "web" || plain.Docker.HasGitSource() {
		t.Fatalf("path DockerBuild = %+v", plain.Docker)
	}
	if out, _ := json.Marshal(plain.Docker); !strings.Contains(string(out), `"context":"web"`) || !strings.Contains(string(out), `"dockerfile":"web/Dockerfile"`) {
		t.Fatalf("path DockerBuild re-encoded = %s", out)
	}
	if _, err := dispatchBuild("bad", json.RawMessage(`{"type":"docker","context":{"repo":"github.com/acme/x","ref":"v1","branch":"main"}}`)); err == nil {
		t.Fatal("an unknown GitSource key must be refused, not dropped")
	}
}

// A GitSource context builds the OTHER repository's tree with its own
// Dockerfile by default: `-f <context>/Dockerfile <context>`. A GitSource
// dockerfile is that file. Resolution goes through the override file here,
// the same seam a fetch uses.
func TestDockerBuildGitSourceContextBuildsTheResolvedTree(t *testing.T) {
	project, billing, images := t.TempDir(), t.TempDir(), t.TempDir()
	writeTree(t, billing, map[string]string{"Dockerfile": "FROM scratch\n", "docker/prod.Dockerfile": "FROM scratch\n"})
	writeTree(t, images, map[string]string{"py/Dockerfile": "FROM scratch\n"})
	writeOverrides(t, project, map[string]string{"github.com/acme/billing": billing, "github.com/acme/images": images})
	cfg := &config.ProjectConfig{Name: "deploy"}
	ctx := context.Background()
	prev := captureDockerSource // the git capture is covered by the provenance test
	captureDockerSource = func(context.Context, string, string, string) *buildtarget.Source { return nil }
	t.Cleanup(func() { captureDockerSource = prev })

	t.Run("default dockerfile is the context's", func(t *testing.T) {
		d := &DockerBuild{Dockerfile: "Dockerfile", ContextSource: &config.GitSource{Repo: "github.com/acme/billing", Ref: "v2.3.1"}}
		if err := resolveDockerBuildSources(ctx, project, "billing", d); err != nil {
			t.Fatal(err)
		}
		if err := checkServiceDockerContext(project, "billing", d); err != nil {
			t.Fatalf("a resolved pinned context must pass the context check: %v", err)
		}
		args, _ := serviceDockerBuildArgs(cfg, "billing", serviceDockerfile(d), d, buildOptions{}, "", "")
		if got := args[len(args)-1]; got != billing {
			t.Fatalf("main context = %q, want the resolved checkout %q; args=%v", got, billing, args)
		}
		if !argsHave(args, "-f", filepath.Join(billing, "Dockerfile")) {
			t.Fatalf("-f must be the context's own Dockerfile; args=%v", args)
		}
	})
	t.Run("a relative dockerfile is read inside the context", func(t *testing.T) {
		d := &DockerBuild{Dockerfile: "docker/prod.Dockerfile", ContextSource: &config.GitSource{Repo: "github.com/acme/billing", Ref: "v2.3.1"}}
		if err := resolveDockerBuildSources(ctx, project, "billing", d); err != nil {
			t.Fatal(err)
		}
		if got := serviceDockerfile(d); got != filepath.Join(billing, "docker", "prod.Dockerfile") {
			t.Fatalf("dockerfile = %q", got)
		}
		bad := &DockerBuild{Dockerfile: "../escape/Dockerfile", ContextSource: d.ContextSource}
		if err := resolveDockerBuildSources(ctx, project, "billing", bad); err == nil {
			t.Fatal("a dockerfile escaping the pinned context must be refused")
		}
	})
	t.Run("a pinned dockerfile from a third repo", func(t *testing.T) {
		d := &DockerBuild{
			ContextSource:    &config.GitSource{Repo: "github.com/acme/billing", Ref: "v2.3.1"},
			DockerfileSource: &config.GitSource{Repo: "github.com/acme/images", Ref: "v1", Subdir: "py/Dockerfile"},
		}
		if err := resolveDockerBuildSources(ctx, project, "billing", d); err != nil {
			t.Fatal(err)
		}
		args, _ := serviceDockerBuildArgs(cfg, "billing", serviceDockerfile(d), d, buildOptions{}, "", "")
		if !argsHave(args, "-f", filepath.Join(images, "py", "Dockerfile")) || args[len(args)-1] != billing {
			t.Fatalf("args=%v", args)
		}
		noFile := &DockerBuild{DockerfileSource: &config.GitSource{Repo: "github.com/acme/images", Ref: "v1"}}
		if err := resolveDockerBuildSources(ctx, project, "x", noFile); err == nil || !strings.Contains(err.Error(), "set subdir to the Dockerfile's path") {
			t.Fatalf("a dockerfile pin without the file's path must say so: %v", err)
		}
	})
	t.Run("a dockerfile missing from the pinned context fails, never skips", func(t *testing.T) {
		d := &DockerBuild{Dockerfile: "docker/typo.Dockerfile", ContextSource: &config.GitSource{Repo: "github.com/acme/billing", Ref: "v2.3.1"}}
		err := resolveDockerBuildSources(ctx, project, "billing", d)
		if err == nil || !strings.Contains(err.Error(), "docker/typo.Dockerfile") || d.DockerfilePath != "" {
			t.Fatalf("a missing pinned Dockerfile must be refused naming it: %v (path %q)", err, d.DockerfilePath)
		}
		// And should a pinned build reach the build with no Dockerfile, it
		// fails rather than taking the optional project-Dockerfile skip.
		gone := &DockerBuild{ContextSource: d.ContextSource, ContextDir: billing, DockerfilePath: filepath.Join(billing, "nope.Dockerfile")}
		if res := buildServiceDocker(ctx, cfg, "billing", "billing", gone, buildOptions{}, "", ""); res.err == nil {
			t.Fatal("a pinned build with no Dockerfile was skipped as successful")
		}
	})
	t.Run("unresolved is refused, never the project root", func(t *testing.T) {
		d := &DockerBuild{ContextSource: &config.GitSource{Repo: "github.com/acme/billing", Ref: "v2.3.1"}}
		if err := checkServiceDockerContext(project, "billing", d); err == nil || !strings.Contains(err.Error(), "github.com/acme/billing@v2.3.1") {
			t.Fatalf("an unresolved pin must be refused naming it: %v", err)
		}
		missing := &DockerBuild{ContextSource: &config.GitSource{Repo: "github.com/acme/gone", Ref: "v1"}}
		writeOverrides(t, project, map[string]string{"github.com/acme/gone": filepath.Join(project, "nope")})
		if err := resolveDockerBuildSources(ctx, project, "gone", missing); err == nil || missing.ContextDir != "" {
			t.Fatalf("a failed resolution must error and leave no context: %v, %q", err, missing.ContextDir)
		}
	})
}

// Provenance: the build state of a DockerBuild with a pinned context records
// the other repository and commit, and the release ledger carries it as
// built_from. The real fetch from a local bare repository proves the commit
// is the pin's.
func TestDockerBuildGitSourceRecordsProvenance(t *testing.T) {
	testkit.Slow(t, "execs git to build and fetch a bare repository")
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir()) // the source cache, under os.UserCacheDir
	root := t.TempDir()
	work, bare := filepath.Join(root, "billing"), filepath.Join(root, "billing.git")
	writeTree(t, work, map[string]string{"Dockerfile": "FROM scratch\n", "main.py": "print('hi')\n"})
	srcGit(t, work, "init", "--quiet", "-b", "main")
	srcGit(t, work, "add", ".")
	srcGit(t, work, "commit", "--quiet", "-m", "billing")
	srcGit(t, work, "tag", "v2.3.1")
	commit := srcGit(t, work, "rev-parse", "HEAD")
	srcGit(t, root, "clone", "--quiet", "--bare", work, bare)

	project := t.TempDir()
	build, err := dispatchBuild("billing", json.RawMessage(`{"type":"docker","dockerfile":"Dockerfile","context":{"repo":"file://`+bare+`","ref":"v2.3.1"}}`))
	if err != nil {
		t.Fatal(err)
	}
	entities := &KCLEntities{Workloads: []WorkloadEntity{{Name: "billing", Build: build}}}
	if err := resolveBuildSources(context.Background(), project, nil, entities); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	d := entities.Workloads[0].Build.Docker
	if _, err := os.Stat(filepath.Join(d.ContextDir, "main.py")); err != nil {
		t.Fatalf("the context is not the fetched checkout: %v", err)
	}
	if d.Built == nil || d.Built.Commit != commit || d.Built.Dirty || !d.Built.External() {
		t.Fatalf("recorded checkout = %+v, want commit %s, clean, external", d.Built, commit)
	}

	// The build result carries it into the state file, which the release
	// harvest reads into the ledger's built_from.
	writeTree(t, project, map[string]string{"forge.yaml": "name: deploy\n"})
	t.Chdir(project) // persistImageBuildStates writes under the project of the cwd
	persistImageBuildStates(buildOptions{env: "prod"}, []buildResult{{
		kind: "docker", image: "billing", repository: "ghcr.io/acme/billing", tag: "v2.3.1",
		digest: "sha256:" + strings.Repeat("a", 64), source: d.sourceForRecord(),
	}})
	st, err := buildtarget.ReadState(project, "prod", "billing")
	if err != nil || st == nil || st.Source == nil || st.Source.Commit != commit {
		t.Fatalf("build state = %+v, %v", st, err)
	}
	art, ok := harvestReleaseArtifacts(project, "prod")["ghcr.io/acme/billing"]
	if !ok || art.BuiltFrom == nil || art.BuiltFrom.Commit != commit || art.BuiltFrom.Repo != st.Source.Repo || art.BuiltFrom.Repo == "" {
		t.Fatalf("release artifact = %+v", art)
	}

	// The release guard holds the context to its pin: the cached checkout IS
	// the pin, so it passes.
	uses, err := liveShellSources(context.Background(), project, entities)
	if err != nil || len(uses) != 1 || uses[0].src.Commit != commit {
		t.Fatalf("live sources = %+v, %v", uses, err)
	}
	if _, err := newReleaseSourceCheck(project, "prod", "v1", false, entities).judge(context.Background(), uses); err != nil {
		t.Fatalf("a cached pin must pass the release guard: %v", err)
	}
}

// writeTree is memory_golden_test.go's.

func writeOverrides(t *testing.T, project string, m map[string]string) {
	t.Helper()
	var b strings.Builder
	b.WriteString("sources:\n")
	for repo, dir := range m {
		b.WriteString("  " + repo + ": " + dir + "\n")
	}
	writeTree(t, project, map[string]string{".forge/source-overrides.yaml": b.String()})
}

func srcGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

type overrideStub map[string]bool // repo → overridden

func (o overrideStub) Resolve(_ context.Context, src gitsource.Source) (gitsource.Resolution, error) {
	return gitsource.Resolution{Dir: "/work/" + src.Repo, Overridden: o[src.Repo], Commit: strings.Repeat("a", 40)}, nil
}

// A release never freezes an image built from a working copy: a DockerBuild
// pin served by the override file refuses the cut, naming it — the rule a
// pinned frontend already follows.
func TestDockerBuildGitSourceOverrideRefusesARelease(t *testing.T) {
	entities := &KCLEntities{Workloads: []WorkloadEntity{
		{Name: "billing", Build: BuildConfigEntity{Type: "docker", Docker: &DockerBuild{ContextSource: &config.GitSource{Repo: "github.com/acme/billing", Ref: "v2"}}}},
		{Name: "web", Build: BuildConfigEntity{Type: "docker", Docker: &DockerBuild{Context: "web"}}},
	}}
	ctx := context.Background()
	if err := refuseDockerSourceOverrides(ctx, overrideStub{}, "v1", entities); err != nil {
		t.Fatalf("pins resolved from the cache must pass: %v", err)
	}
	err := refuseDockerSourceOverrides(ctx, overrideStub{"github.com/acme/billing": true}, "v1", entities)
	if err == nil || !strings.Contains(err.Error(), `workload "billing": github.com/acme/billing@v2`) || !strings.Contains(err.Error(), "source-overrides.yaml") {
		t.Fatalf("an override must refuse the cut naming the workload and pin: %v", err)
	}
}

// The same holds for shared KCL: a git-pinned [forge_projects] import read
// from a working copy refuses the cut; read from its pin, it does not matter.
func TestKCLImportOverrideRefusesARelease(t *testing.T) {
	project, infra := t.TempDir(), t.TempDir()
	writeTree(t, infra, map[string]string{"forge.yaml": "name: infra\n", "deploy/kcl/kcl.mod": "[package]\nname = \"infra\"\n"})
	writeTree(t, project, map[string]string{
		"forge.yaml":         "name: app\n",
		"deploy/kcl/kcl.mod": "[package]\nname = \"app\"\n\n[forge_projects]\ninfra = { repo = \"github.com/acme/infra\", ref = \"v1\" }\n",
	})
	ctx := context.Background()
	if err := refuseKCLImportOverrides(ctx, t.TempDir(), "v1"); err != nil {
		t.Fatalf("a project with no KCL imports must pass: %v", err)
	}
	writeOverrides(t, project, map[string]string{"github.com/acme/infra": infra})
	err := refuseKCLImportOverrides(ctx, project, "v1")
	if err == nil || !strings.Contains(err.Error(), "[forge_projects] infra: github.com/acme/infra@v1") || !strings.Contains(err.Error(), "LOCAL SOURCE OVERRIDE") {
		t.Fatalf("an overridden KCL import must refuse the cut: %v", err)
	}
}
