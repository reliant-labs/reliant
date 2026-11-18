package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/reliant-labs/forge/internal/buildtarget"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/pkg/release"
)

func mockExternalArtifactBuild(t *testing.T, fail bool) (*atomic.Int32, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	oldBuild, oldDigest, oldSource, oldStorage := externalShellBuild, externalImageDigestResolver, externalSourceCapture, checkBuildStorageFn
	t.Cleanup(func() {
		externalShellBuild, externalImageDigestResolver, externalSourceCapture, checkBuildStorageFn = oldBuild, oldDigest, oldSource, oldStorage
	})
	var builds, digests, captures atomic.Int32
	checkBuildStorageFn = func(string) error { return nil }
	externalShellBuild = func(_ context.Context, spec buildtarget.Spec) buildtarget.BuildResult {
		builds.Add(1)
		res := buildtarget.BuildResult{Service: spec.Service, Tag: spec.Tag}
		if fail {
			res.Err = errors.New("mock immutable push failed")
		}
		return res
	}
	externalImageDigestResolver = func(_ context.Context, _ string) (string, []string, error) {
		digests.Add(1)
		return "sha256:shared", []string{"linux/amd64"}, nil
	}
	externalSourceCapture = func(_ context.Context, service, cwd, _ string) *buildtarget.Source {
		captures.Add(1)
		if service != "api" {
			t.Errorf("source owner = %s, want deterministic first consumer api", service)
		}
		return &buildtarget.Source{Dir: cwd, Repo: "github.com/example/shared", Commit: "shared-source-commit"}
	}
	return &builds, &digests, &captures
}

func sharedExternalConsumers() []WorkloadEntity {
	return []WorkloadEntity{
		shellSvc("worker", "registry.example/shared", "mock-build-and-push", ".", map[string]string{"A": "1"}),
		shellSvc("gateway", "registry.example/shared", "mock-build-and-push", "", map[string]string{"A": "1"}),
		shellSvc("api", "registry.example/shared", "mock-build-and-push", ".", map[string]string{"A": "1"}),
	}
}

func TestBuildExternalSharedArtifact(t *testing.T) {
	for _, parallel := range []bool{false, true} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("parallel=%v/fail=%v", parallel, fail), func(t *testing.T) {
				builds, digests, captures := mockExternalArtifactBuild(t, fail)
				project := t.TempDir()
				services := sharedExternalConsumers()
				// Exercise pointer aliasing too: serial preparation must precede workers.
				services[1].Build.Shell = services[0].Build.Shell
				opts := buildOptions{env: "prod", parallel: parallel}
				for invocation := int32(1); invocation <= 2; invocation++ {
					results := buildExternalServices(context.Background(), services, opts, "v1", project)
					if len(results) != len(services) || builds.Load() != invocation || captures.Load() != invocation {
						t.Fatalf("results=%d builds=%d captures=%d; want 3 results and one build/capture per invocation", len(results), builds.Load(), captures.Load())
					}
					for _, result := range results {
						if (result.err != nil) != fail {
							t.Fatalf("result %s error=%v, fail=%v", result.name, result.err, fail)
						}
					}
					var first *buildtarget.State
					for _, svc := range services {
						state, err := buildtarget.ReadState(project, opts.env, svc.Name)
						if err != nil {
							t.Fatal(err)
						}
						if fail {
							if state != nil {
								t.Fatal("failed artifact wrote success state")
							}
							continue
						}
						if state == nil || state.Digest != "sha256:shared" || state.Source == nil || state.Source.Commit != "shared-source-commit" || state.Tag != "v1" || state.Service != svc.Name {
							t.Fatalf("consumer %s state = %+v", svc.Name, state)
						}
						state.Service = ""
						if first == nil {
							first = state
						} else if !reflect.DeepEqual(first, state) {
							t.Fatalf("consumer states differ: %+v != %+v", first, state)
						}
					}
					if !fail {
						artifact := harvestReleaseArtifacts(project, opts.env)["registry.example/shared"]
						if artifact.BuiltFrom == nil || artifact.BuiltFrom.Commit != "shared-source-commit" || artifact.Digests[release.SharedVariant] != "sha256:shared" {
							t.Fatalf("release did not harvest shared digest/provenance: %+v", artifact)
						}
					}
					wantDigests := invocation
					if fail {
						wantDigests = 0
					}
					if digests.Load() != wantDigests {
						t.Fatalf("digest lookups=%d want=%d", digests.Load(), wantDigests)
					}
				}
			})
		}
	}
}

func TestExternalArtifactConflictsFailBeforeExecutionAndPlan(t *testing.T) {
	for _, field := range []string{"recipe", "env", "cwd", "platform", "source"} {
		t.Run(field, func(t *testing.T) {
			builds, digests, _ := mockExternalArtifactBuild(t, false)
			project := t.TempDir()
			other := filepath.Join(project, "other")
			if err := os.Mkdir(other, 0o755); err != nil {
				t.Fatal(err)
			}
			services := sharedExternalConsumers()[:2]
			switch field {
			case "recipe":
				services[1].Build.Shell.Cmd = "other recipe"
			case "env":
				services[1].Build.Shell.Env = map[string]string{"A": "2"}
			case "cwd":
				services[1].Build.Shell.Cwd = other
			case "platform":
				services[1].Runtime = RuntimeEntity{Type: RuntimeCluster, Cluster: &ClusterRuntime{Platform: "arm64"}}
			case "source":
				services[1].Build.Shell.Cwd = ""
				services[1].Build.Shell.Source = &config.GitSource{Repo: "github.com/example/repo", Ref: "v1"}
				services[1].Build.Shell.SourceDir = project
			}
			opts := buildOptions{env: "prod", parallel: true}
			results := buildExternalServices(context.Background(), services, opts, "v1", project)
			for _, result := range results {
				if result.err == nil || !strings.Contains(result.err.Error(), "conflicting ShellBuild inputs") {
					t.Fatalf("result=%+v", result)
				}
			}
			if builds.Load() != 0 || digests.Load() != 0 {
				t.Fatal("conflict executed build or registry lookup")
			}
			if _, err := os.Stat(filepath.Join(project, ".forge")); !os.IsNotExist(err) {
				t.Fatalf("conflict wrote state: %v", err)
			}
			steps := planExternalBuilds(planInputs{entities: &KCLEntities{Workloads: services}, opts: opts, resolvedTag: "v1", projectDir: project})
			if len(steps) != 1 || !strings.Contains(steps[0].problem, "conflicting ShellBuild inputs") {
				t.Fatalf("plan=%+v", steps)
			}
		})
	}
}

func TestExternalArtifactPlanAndIdentity(t *testing.T) {
	project := t.TempDir()
	services := sharedExternalConsumers()
	steps := planExternalBuilds(planInputs{entities: &KCLEntities{Workloads: services}, resolvedTag: "v1", projectDir: project})
	if len(steps) != 1 || steps[0].name != "api" || !strings.Contains(steps[0].what, "shared artifact for api, gateway, worker") {
		t.Fatalf("plan=%+v", steps)
	}
	for _, variant := range []string{"image", "tag", "imageless"} {
		t.Run(variant, func(t *testing.T) {
			svcs := sharedExternalConsumers()[:2]
			switch variant {
			case "image":
				svcs[1].Image = "registry.example/other:v1"
			case "tag":
				svcs[1].BuildImage = "registry.example/shared:v2"
			case "imageless":
				svcs[0].Image, svcs[1].Image = "", ""
			}
			artifacts, err := externalBuildArtifacts(svcs, buildOptions{}, "v1", project)
			if err != nil || len(artifacts) != 2 {
				t.Fatalf("artifacts=%+v err=%v", artifacts, err)
			}
		})
	}
}
