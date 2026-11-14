package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/pkg/release"
)

func TestCompositionProjectImageBindsCompiledGoConsumers(t *testing.T) {
	const repo = "registry.example/platform/control-plane"
	a := WorkloadEntity{Name: "admin-server", Image: repo, Build: BuildConfigEntity{Type: "go", Go: &GoBuild{Cmd: "./cmd/control-plane", OutputName: "control-plane"}}}
	b := a
	b.Name = "workspace-controller"
	entities := &KCLEntities{Workloads: []WorkloadEntity{a, b}}
	output := buildResult{kind: "docker", image: "control-plane", repository: repo, digest: "sha256:" + strings.Repeat("a", 64), goTargets: goBuildTargetsFromKCL(entities)}
	source := release.Source{Repo: "github.com/acme/platform", Commit: strings.Repeat("b", 40)}
	for _, w := range entities.Workloads {
		ref, receipt, gotSource, err := compositionPrimaryOutput(context.Background(), w, "container:"+w.Name, buildOptions{}, []buildResult{output}, source)
		if err != nil || ref != repo+"@"+output.digest || receipt != nil || gotSource == nil || *gotSource != source {
			t.Fatalf("%s did not bind this invocation's native output: ref=%s receipt=%v source=%v err=%v", w.Name, ref, receipt, gotSource, err)
		}
	}
	for _, tc := range []struct {
		name string
		edit func(*buildResult, *WorkloadEntity)
	}{
		{"missing repository", func(r *buildResult, _ *WorkloadEntity) { r.repository = "" }},
		{"missing digest", func(r *buildResult, _ *WorkloadEntity) { r.digest = "" }},
		{"other repository", func(r *buildResult, _ *WorkloadEntity) { r.repository += "-other" }},
		{"uncompiled recipe", func(r *buildResult, _ *WorkloadEntity) {
			r.goTargets = []goBuildTarget{{cmd: "./cmd/foreign", outputName: "control-plane"}}
		}},
		{"different flags", func(r *buildResult, _ *WorkloadEntity) {
			r.goTargets = []goBuildTarget{{cmd: "./cmd/control-plane", outputName: "control-plane", tags: []string{"foreign"}}}
		}},
		{"name alone is not provenance", func(r *buildResult, w *WorkloadEntity) { r.goTargets = nil; r.image = w.Name }},
		{"failed build", func(r *buildResult, _ *WorkloadEntity) { r.err = errors.New("failed") }},
		{"foreign workload", func(r *buildResult, _ *WorkloadEntity) { r.workload = "foreign" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, w := output, a
			tc.edit(&r, &w)
			if _, _, _, err := compositionPrimaryOutput(context.Background(), w, "container:"+w.Name, buildOptions{}, []buildResult{r}, source); err == nil {
				t.Fatal("accepted an output without this consumer's invocation evidence")
			}
		})
	}
	conflict := output
	conflict.digest = "sha256:" + strings.Repeat("c", 64)
	if _, _, _, err := compositionPrimaryOutput(context.Background(), a, "container:"+a.Name, buildOptions{}, []buildResult{output, conflict}, source); err == nil {
		t.Fatal("accepted ambiguous immutable outputs")
	}
}

func TestCompositionDockerConsumerUsesItsInvocationRepository(t *testing.T) {
	const repo = "registry.example/platform/console"
	w := WorkloadEntity{Name: "console", Image: repo, Build: BuildConfigEntity{Type: "docker", Docker: &DockerBuild{Dockerfile: "frontends/console/Dockerfile"}}}
	output := buildResult{kind: "docker", image: "console", repository: repo, digest: "sha256:" + strings.Repeat("d", 64)}
	ref, _, _, err := compositionPrimaryOutput(context.Background(), w, "container:console", buildOptions{}, []buildResult{output}, release.Source{})
	if err != nil || ref != repo+"@"+output.digest {
		t.Fatalf("Docker consumer lost its authoritative repository: %s %v", ref, err)
	}
	foreign := output
	foreign.repository += "-foreign"
	if _, _, _, err := compositionPrimaryOutput(context.Background(), w, "container:console", buildOptions{}, []buildResult{foreign}, release.Source{}); err == nil {
		t.Fatal("same Docker consumer name accepted a different declared repository")
	}
	w.Name = "other-consumer"
	if _, _, _, err := compositionPrimaryOutput(context.Background(), w, "container:other-consumer", buildOptions{}, []buildResult{output}, release.Source{}); err == nil {
		t.Fatal("repository equality incorrectly attributed another Docker consumer")
	}
}

func TestNativeDockerWritersRetainPushedInvocationIdentity(t *testing.T) {
	if testing.Short() {
		t.Skip("runs fake Docker subprocesses; runs in task test")
	}
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fakeDocker(t)
	fakeRegistry(t)
	cfg := &config.ProjectConfig{Name: "platform"}
	for _, project := range []bool{true, false} {
		name := "console"
		if project {
			name = cfg.Name
		}
		repo := "registry.example:5000/acme/" + name
		tags := imageTagSet(repo, "v1.2.3", true, true)
		var result buildResult
		if project {
			result = dockerBuildProject(t.Context(), cfg, tags, "", versionInfo{}, projectImageGuard{})
		} else {
			result = dockerBuild(t.Context(), cfg, name, dir, tags, "")
		}
		if result.err != nil || result.repository != repo || result.image != name || result.digest != digestOf(repo+":v1.2.3") {
			t.Fatalf("project=%v lost immutable push identity: %+v", project, result)
		}
	}
}

func TestImageTagsRetainDeclaredRepositoryForNativeWriters(t *testing.T) {
	for _, pushed := range []bool{false, true} {
		tags := imageTagSet("registry.example:5000/acme/api", "v1.2.3", pushed, true)
		if tags.repository != "registry.example:5000/acme/api" {
			t.Fatalf("native image writer lost exact declared repository: %+v", tags)
		}
	}
}
